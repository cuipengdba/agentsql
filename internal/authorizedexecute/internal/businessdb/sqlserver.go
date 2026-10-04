package businessdb

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	mssql "github.com/microsoft/go-mssqldb"
)

const (
	sqlServer2025Major     = 17
	sqlServerShowplanLimit = 1 << 20
	sqlServerShowplanNodes = 4_096
	sqlServerShowplanDepth = 256
)

// SQLServerExecutor is the SQL Server 2025 read-only capability. It uses the
// pure-Go TDS driver and deliberately inherits limitedSQLExecutor: arbitrary
// DML, procedures, batches and transaction control remain unavailable.
type SQLServerExecutor struct{ *limitedSQLExecutor }

// NewSQLServerExecutor opens a SQL Server 2025 datasource with client-enforced
// TLS. Strict TDS 8.0 is the default; verify-full exists for deployments that
// need the prelogin-compatible TLS mode or a controlled development certificate.
func NewSQLServerExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*SQLServerExecutor, error) {
	dsn, err := buildSQLServerDSN(datasource, password)
	if err != nil {
		return nil, err
	}
	executor, err := newLimitedSQLExecutorWithDialect(
		ctx, datasource, "sqlserver", dsn, string(model.DialectSQLServer), password,
		readOnly, classifySQLServerError, explainSQLServerSelect,
	)
	if err != nil {
		return nil, err
	}
	if err := probeSQLServer2025(ctx, executor); err != nil {
		_ = executor.Close()
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT COALESCE(SCHEMA_NAME(), N'dbo')"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &SQLServerExecutor{limitedSQLExecutor: executor}, nil
}

func buildSQLServerDSN(datasource model.Datasource, password string) (string, error) {
	if err := validateDatasource(datasource); err != nil {
		return "", fmt.Errorf("build SQL Server connection configuration: %w", err)
	}
	if password == "" || containsControl(datasource.Username) || containsControl(password) ||
		containsControl(datasource.TLSServerName) || containsControl(datasource.TLSCAFile) {
		return "", fmt.Errorf("build SQL Server connection configuration: invalid credential or TLS value")
	}
	tlsMode := strings.TrimSpace(datasource.TLSMode)
	if tlsMode == "" {
		tlsMode = "strict"
	}
	if tlsMode != "strict" && tlsMode != "verify-full" {
		return "", fmt.Errorf("build SQL Server connection configuration: unsupported TLS mode")
	}
	if tlsMode == "strict" && datasource.TrustServerCertificate {
		return "", fmt.Errorf("build SQL Server connection configuration: strict TLS requires certificate verification")
	}

	connection := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(datasource.Username, password),
		Host:   net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)),
	}
	query := connection.Query()
	query.Set("database", datasource.Database)
	query.Set("app name", "AgentSQL")
	query.Set("tlsmin", "1.2")
	if tlsMode == "strict" {
		query.Set("encrypt", "strict")
	} else {
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", strconv.FormatBool(datasource.TrustServerCertificate))
	}
	if datasource.TLSServerName != "" {
		query.Set("hostnameincertificate", datasource.TLSServerName)
	}
	if datasource.TLSCAFile != "" {
		query.Set("certificate", datasource.TLSCAFile)
	}
	connection.RawQuery = query.Encode()
	return connection.String(), nil
}

func probeSQLServer2025(ctx context.Context, executor *limitedSQLExecutor) error {
	if executor == nil || executor.database == nil || ctx == nil {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, DBStageMetadata, "", ErrDatasourceUnreachable)
	}
	timed, cancel := executor.timeoutContext(ctx)
	defer cancel()
	var major int
	if err := executor.database.QueryRowContext(
		timed, "SELECT CONVERT(int, SERVERPROPERTY('ProductMajorVersion'))",
	).Scan(&major); err != nil {
		return executor.databaseError(timed, DBStageMetadata, err)
	}
	if major != sqlServer2025Major {
		return newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageMetadata, strconv.Itoa(major), nil)
	}
	return nil
}

func classifySQLServerError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified, true
	}
	var serverError mssql.Error
	if errors.As(cause, &serverError) {
		driverCode := strconv.FormatInt(int64(serverError.Number), 10)
		kind, code, compat := sqlServerErrorCode(serverError.Number)
		return newDBError(kind, code, stage, driverCode, compat), true
	}
	if isNetworkConnectionError(cause) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, "", ErrDatasourceUnreachable), true
	}
	return nil, false
}

func sqlServerErrorCode(number int32) (DBErrorKind, DBErrorCode, error) {
	switch number {
	case 18456, 18487, 18488:
		return DBErrorKindAuthentication, DBErrorCodeAuthentication, nil
	case 4060:
		return DBErrorKindDatabaseNotFound, DBErrorCodeDatabaseNotFound, ErrDatasourceUnreachable
	case 229, 230, 297:
		return DBErrorKindPermission, DBErrorCodePermission, ErrPermissionDenied
	case 207:
		return DBErrorKindColumnNotFound, DBErrorCodeColumnNotFound, nil
	case 208, 3701:
		return DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound, nil
	case 102, 105, 156, 170:
		return DBErrorKindSyntax, DBErrorCodeSyntax, nil
	case 2601, 2627:
		return DBErrorKindConstraint, DBErrorCodeConstraint, nil
	case 515, 547:
		return DBErrorKindConstraint, DBErrorCodeConstraint, nil
	case 1205, 1222:
		return DBErrorKindRetryable, DBErrorCodeRetryable, nil
	case 701, 802, 8645, 8651:
		return DBErrorKindResource, DBErrorCodeResource, nil
	case 233, 10053, 10054, 10060:
		return DBErrorKindConnection, DBErrorCodeConnection, ErrDatasourceUnreachable
	default:
		return DBErrorKindExecution, DBErrorCodeExecution, nil
	}
}

func explainSQLServerSelect(
	ctx context.Context,
	runner limitedDialectRunner,
	sqlText string,
) (info model.ExplainInfo, returnedErr error) {
	if _, err := runner.ExecContext(ctx, "SET SHOWPLAN_XML ON"); err != nil {
		return model.ExplainInfo{}, sqlServerDatabaseError(ctx, DBStageExplain, err)
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if _, err := runner.ExecContext(cleanupContext, "SET SHOWPLAN_XML OFF"); err != nil && returnedErr == nil {
			returnedErr = sqlServerDatabaseError(ctx, DBStageExplain, err)
		}
	}()

	rows, err := runner.QueryContext(ctx, sqlText)
	if err != nil {
		return model.ExplainInfo{}, sqlServerDatabaseError(ctx, DBStageExplain, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) != 1 || !rows.Next() {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	var raw string
	if err := rows.Scan(&raw); err != nil {
		return model.ExplainInfo{}, sqlServerDatabaseError(ctx, DBStageExplain, err)
	}
	if rows.Next() || rows.Err() != nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	parsed, err := parseSQLServerShowplan([]byte(raw))
	if err != nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	return parsed, nil
}

func sqlServerDatabaseError(ctx context.Context, stage DBStage, cause error) error {
	if classified, ok := classifySQLServerError(ctx, stage, cause); ok {
		return classified
	}
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func parseSQLServerShowplan(raw []byte) (model.ExplainInfo, error) {
	if len(raw) == 0 || len(raw) > sqlServerShowplanLimit {
		return model.ExplainInfo{}, fmt.Errorf("invalid SQL Server SHOWPLAN size")
	}
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	depth, nodes := 0, 0
	foundRoot, foundStatement, foundRelOp := false, false, false
	var estimatedRows int64
	var estimatedCost float64
	var usesIndex, scans bool
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return model.ExplainInfo{}, err
		}
		switch typed := token.(type) {
		case xml.Directive:
			return model.ExplainInfo{}, fmt.Errorf("XML directives are unsupported")
		case xml.StartElement:
			depth++
			if depth > sqlServerShowplanDepth {
				return model.ExplainInfo{}, fmt.Errorf("SQL Server SHOWPLAN nesting exceeds limit")
			}
			switch typed.Name.Local {
			case "ShowPlanXML":
				foundRoot = true
			case "StmtSimple":
				foundStatement = true
				if value, ok := xmlAttribute(typed.Attr, "StatementSubTreeCost"); ok {
					cost, parseErr := parseNonNegativeFloat(value)
					if parseErr != nil {
						return model.ExplainInfo{}, parseErr
					}
					estimatedCost = math.Max(estimatedCost, cost)
				}
				if value, ok := xmlAttribute(typed.Attr, "StatementEstRows"); ok {
					rows, parseErr := parseSQLServerRows(value)
					if parseErr != nil {
						return model.ExplainInfo{}, parseErr
					}
					estimatedRows = max(estimatedRows, rows)
				}
			case "RelOp":
				foundRelOp = true
				nodes++
				if nodes > sqlServerShowplanNodes {
					return model.ExplainInfo{}, fmt.Errorf("SQL Server SHOWPLAN node count exceeds limit")
				}
				if value, ok := xmlAttribute(typed.Attr, "EstimateRows"); ok {
					rows, parseErr := parseSQLServerRows(value)
					if parseErr != nil {
						return model.ExplainInfo{}, parseErr
					}
					estimatedRows = max(estimatedRows, rows)
				}
				if value, ok := xmlAttribute(typed.Attr, "EstimatedTotalSubtreeCost"); ok {
					cost, parseErr := parseNonNegativeFloat(value)
					if parseErr != nil {
						return model.ExplainInfo{}, parseErr
					}
					estimatedCost = math.Max(estimatedCost, cost)
				}
				if physical, ok := xmlAttribute(typed.Attr, "PhysicalOp"); ok {
					upper := strings.ToUpper(physical)
					usesIndex = usesIndex || strings.Contains(upper, "INDEX") || strings.Contains(upper, "SEEK")
					scans = scans || strings.Contains(upper, "SCAN")
				}
			}
		case xml.EndElement:
			depth--
			if depth < 0 {
				return model.ExplainInfo{}, fmt.Errorf("invalid SQL Server SHOWPLAN nesting")
			}
		}
	}
	if depth != 0 || !foundRoot || !foundStatement || !foundRelOp || nodes == 0 {
		return model.ExplainInfo{}, fmt.Errorf("incomplete SQL Server SHOWPLAN")
	}
	info := model.ExplainInfo{EstScanRows: estimatedRows, EstCost: estimatedCost, UsesIndex: usesIndex, SeqScan: scans}
	info.Raw = fmt.Sprintf("nodes=%d rows=%d cost=%s index=%t scan=%t", nodes, estimatedRows,
		strconv.FormatFloat(estimatedCost, 'g', -1, 64), usesIndex, scans)
	return info, nil
}

func xmlAttribute(attributes []xml.Attr, name string) (string, bool) {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			return attribute.Value, true
		}
	}
	return "", false
}

func parseSQLServerRows(value string) (int64, error) {
	parsed, err := parseNonNegativeFloat(value)
	if err != nil || parsed > float64(math.MaxInt64) {
		return 0, fmt.Errorf("invalid SQL Server row estimate")
	}
	return int64(math.Ceil(parsed)), nil
}

func parseNonNegativeFloat(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("invalid non-negative number")
	}
	return parsed, nil
}

var _ Executor = (*SQLServerExecutor)(nil)
