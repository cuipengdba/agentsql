package businessdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	dm8 "github.com/godoes/gorm-dameng/dm8"
)

// DMExecutor supports connection lifecycle, typed metadata, narrowly validated
// read-only SELECTs and normalized EXPLAIN FOR output. Transactions and writes
// remain unavailable until their authorization contracts are implemented and
// verified.
type DMExecutor struct{ *limitedSQLExecutor }

// NewDMExecutor opens and verifies a native DM8 datasource.
func NewDMExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*DMExecutor, error) {
	dsn, err := buildDMDSN(datasource, password)
	if err != nil {
		return nil, err
	}
	executor, err := newLimitedSQLExecutorWithDialect(
		ctx, datasource, "dm", dsn, "dm", password, readOnly, classifyDMError, explainDMSelect,
	)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT USER"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &DMExecutor{limitedSQLExecutor: executor}, nil
}

const (
	dmExplainRowLimit   = 4_096
	dmExplainTextLimit  = 1 << 20
	dmExplainDepthLimit = 256
)

var dmExplainColumns = []string{
	"PLAN_ID", "PLAN_NAME", "CREATE_TIME", "LEVEL_ID", "OPERATION",
	"TAB_NAME", "IDX_NAME", "SCAN_TYPE", "SCAN_RANGE", "ROW_NUMS",
	"BYTES", "COST", "CPU_COST", "IO_COST", "FILTER", "JOIN_COND",
	"ADVICE_INFO", "PSTART", "PSTOP",
}

// This deliberately small set is backed by the DM SQL manual examples used by
// the offline fixtures. A new operator must be documented and covered before it
// can participate in authorization decisions.
var dmExplainOperators = map[string]struct{}{
	"NSET2":                 {},
	"PRJT2":                 {},
	"SLCT2":                 {},
	"CSCN":                  {},
	"CSCN2":                 {},
	"CSEK":                  {},
	"CSEK2":                 {},
	"SSCN":                  {},
	"SSCN2":                 {},
	"SSEK":                  {},
	"SSEK2":                 {},
	"BLKUP":                 {},
	"BLKUP2":                {},
	"HASH LEFT SEMI JOIN2":  {},
	"INDEX JOIN SEMI JOIN2": {},
}

type dmPlanNode struct {
	id        int64
	level     int64
	operation string
	rows      int64
	cost      float64
}

func explainDMSelect(
	ctx context.Context,
	runner limitedDialectRunner,
	sqlText string,
) (model.ExplainInfo, error) {
	// The DM Go driver does not expose plain EXPLAIN's protocol text through
	// database/sql. DM documents EXPLAIN FOR as the result-set form.
	rows, err := runner.QueryContext(ctx, "EXPLAIN FOR "+sqlText)
	if err != nil {
		return model.ExplainInfo{}, dmDatabaseError(ctx, DBStageExplain, err)
	}
	result, err := collectRows(&mysqlRowSource{rows: rows}, dmExplainRowLimit)
	if err != nil {
		return model.ExplainInfo{}, dmDatabaseError(ctx, DBStageExplain, err)
	}
	info, err := parseDMExplainFor(result)
	if err != nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	return info, nil
}

func dmDatabaseError(ctx context.Context, stage DBStage, cause error) error {
	var resource *ResourceError
	if errors.As(cause, &resource) {
		return resource
	}
	if classified, ok := classifyDMError(ctx, stage, cause); ok {
		return classified
	}
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func parseDMExplainFor(result model.QueryResult) (model.ExplainInfo, error) {
	if result.Truncated || len(result.Rows) == 0 || len(result.Rows) > dmExplainRowLimit {
		return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN FOR result size")
	}
	if len(result.Columns) != len(dmExplainColumns) {
		return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN FOR column count")
	}
	for index, expected := range dmExplainColumns {
		if !strings.EqualFold(strings.TrimSpace(result.Columns[index]), expected) {
			return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN FOR columns")
		}
	}

	nodes := make([]dmPlanNode, 0, len(result.Rows))
	planID := ""
	for index, row := range result.Rows {
		if len(row) != len(dmExplainColumns) {
			return model.ExplainInfo{}, fmt.Errorf("DM EXPLAIN FOR row width mismatch")
		}
		for _, field := range row {
			if strings.IndexFunc(field, unicode.IsControl) >= 0 {
				return model.ExplainInfo{}, fmt.Errorf("control character in DM EXPLAIN FOR field")
			}
		}
		currentPlanID := strings.TrimSpace(row[0])
		parsedPlanID, err := strconv.ParseInt(currentPlanID, 10, 64)
		if err != nil || parsedPlanID < 0 || (planID != "" && currentPlanID != planID) {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN FOR plan ID")
		}
		planID = currentPlanID
		node, err := newDMPlanNode(
			int64(index+1), row[3], row[4], row[9], row[11],
		)
		if err != nil {
			return model.ExplainInfo{}, err
		}
		nodes = append(nodes, node)
	}
	return normalizeDMPlan(nodes)
}

// parseDMExplainText parses the textual tree printed by DM's plain EXPLAIN.
// It exists for deterministic offline verification against the official manual;
// live database/sql execution uses the documented EXPLAIN FOR result set above.
func parseDMExplainText(raw string) (model.ExplainInfo, error) {
	if raw == "" || len(raw) > dmExplainTextLimit || !utf8.ValidString(raw) {
		return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN text size or encoding")
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	for _, character := range raw {
		if unicode.IsControl(character) && character != '\n' {
			return model.ExplainInfo{}, fmt.Errorf("control character in DM EXPLAIN text")
		}
	}

	lines := strings.Split(raw, "\n")
	nodes := make([]dmPlanNode, 0, min(len(lines), dmExplainRowLimit))
	rootHashColumn := -1
	inPredicateSection := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Predicate Information") {
			if len(nodes) == 0 {
				return model.ExplainInfo{}, fmt.Errorf("DM EXPLAIN predicate section without plan")
			}
			inPredicateSection = true
			continue
		}
		if inPredicateSection {
			continue
		}
		if len(nodes) == dmExplainRowLimit {
			return model.ExplainInfo{}, fmt.Errorf("too many DM EXPLAIN nodes")
		}

		hashColumn := strings.IndexByte(line, '#')
		if hashColumn < 0 {
			return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN line")
		}
		idText := strings.TrimSpace(line[:hashColumn])
		if idText == "" || strings.ContainsAny(idText, " \n") {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN node ID")
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || id != int64(len(nodes)+1) {
			return model.ExplainInfo{}, fmt.Errorf("non-contiguous DM EXPLAIN node ID")
		}
		if rootHashColumn < 0 {
			rootHashColumn = hashColumn
		}
		columnDelta := hashColumn - rootHashColumn
		if columnDelta < 0 || columnDelta%2 != 0 {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN indentation")
		}
		level := int64(columnDelta / 2)

		body := line[hashColumn+1:]
		colon := strings.IndexByte(body, ':')
		if colon <= 0 {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN operator")
		}
		operation := strings.TrimSpace(body[:colon])
		tupleAndDetail := strings.TrimSpace(body[colon+1:])
		if !strings.HasPrefix(tupleAndDetail, "[") {
			return model.ExplainInfo{}, fmt.Errorf("missing DM EXPLAIN cost tuple")
		}
		closeBracket := strings.IndexByte(tupleAndDetail, ']')
		if closeBracket < 0 {
			return model.ExplainInfo{}, fmt.Errorf("unterminated DM EXPLAIN cost tuple")
		}
		tuple := strings.Split(tupleAndDetail[1:closeBracket], ",")
		if len(tuple) != 3 {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN cost tuple")
		}
		if detail := strings.TrimSpace(tupleAndDetail[closeBracket+1:]); detail != "" && !strings.HasPrefix(detail, ";") {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN node detail")
		}
		node, err := newDMPlanNode(id, strconv.FormatInt(level, 10), operation, tuple[1], tuple[0])
		if err != nil {
			return model.ExplainInfo{}, err
		}
		bytesValue, err := strconv.ParseInt(strings.TrimSpace(tuple[2]), 10, 64)
		if err != nil || bytesValue < 0 {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN byte estimate")
		}
		nodes = append(nodes, node)
	}
	return normalizeDMPlan(nodes)
}

func newDMPlanNode(id int64, levelText, operation, rowsText, costText string) (dmPlanNode, error) {
	level, err := strconv.ParseInt(strings.TrimSpace(levelText), 10, 64)
	if err != nil || level < 0 || level > dmExplainDepthLimit {
		return dmPlanNode{}, fmt.Errorf("invalid DM EXPLAIN level")
	}
	operation = strings.ToUpper(strings.TrimSpace(operation))
	if _, known := dmExplainOperators[operation]; !known {
		return dmPlanNode{}, fmt.Errorf("unknown DM EXPLAIN operator")
	}
	rows, err := strconv.ParseInt(strings.TrimSpace(rowsText), 10, 64)
	if err != nil || rows < 0 {
		return dmPlanNode{}, fmt.Errorf("invalid DM EXPLAIN row estimate")
	}
	cost, err := strconv.ParseFloat(strings.TrimSpace(costText), 64)
	if err != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return dmPlanNode{}, fmt.Errorf("invalid DM EXPLAIN cost")
	}
	return dmPlanNode{id: id, level: level, operation: operation, rows: rows, cost: cost}, nil
}

func normalizeDMPlan(nodes []dmPlanNode) (model.ExplainInfo, error) {
	if len(nodes) == 0 || len(nodes) > dmExplainRowLimit {
		return model.ExplainInfo{}, fmt.Errorf("empty or oversized DM EXPLAIN plan")
	}
	if nodes[0].id != 1 || nodes[0].level != 0 || nodes[0].operation != "NSET2" {
		return model.ExplainInfo{}, fmt.Errorf("unsupported DM EXPLAIN root")
	}
	for index, node := range nodes[1:] {
		previous := nodes[index]
		if node.id != int64(index+2) || node.level == 0 || node.level > previous.level+1 {
			return model.ExplainInfo{}, fmt.Errorf("invalid DM EXPLAIN tree")
		}
	}

	info := model.ExplainInfo{EstScanRows: nodes[0].rows, EstCost: nodes[0].cost}
	var normalized strings.Builder
	for _, node := range nodes {
		switch node.operation {
		case "CSEK", "CSEK2", "SSEK", "SSEK2", "SSCN", "SSCN2", "BLKUP", "BLKUP2":
			info.UsesIndex = true
		case "CSCN", "CSCN2":
			info.SeqScan = true
		}
		fmt.Fprintf(
			&normalized, "%d|%d|%s|%d|%s\n",
			node.id, node.level, node.operation, node.rows,
			strconv.FormatFloat(node.cost, 'g', -1, 64),
		)
	}
	info.Raw = strings.TrimSuffix(normalized.String(), "\n")
	return info, nil
}

func classifyDMError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified, true
	}
	var driverError *dm8.DmError
	if errors.As(cause, &driverError) {
		driverCode := strconv.FormatInt(int64(driverError.ErrCode), 10)
		switch code := driverError.ErrCode; {
		case code == -2501:
			return newDBError(DBErrorKindAuthentication, DBErrorCodeAuthentication, stage, driverCode, nil), true
		case code == -2007:
			return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, stage, driverCode, nil), true
		case code == -2103 || code == -2106:
			return newDBError(DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound, stage, driverCode, nil), true
		case code == -6407:
			return newDBError(DBErrorKindRetryable, DBErrorCodeRetryable, stage, driverCode, nil), true
		case code == -6602:
			return newDBError(DBErrorKindConstraint, DBErrorCodeConstraint, stage, driverCode, nil), true
		case code <= -5501 && code > -6000:
			return newDBError(DBErrorKindPermission, DBErrorCodePermission, stage, driverCode, ErrPermissionDenied), true
		case code == 6001 || code == 6060 || code == 9007 || code == 9008 || code == 20001:
			return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, driverCode, ErrDatasourceUnreachable), true
		default:
			return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, driverCode, nil), true
		}
	}
	if isNetworkConnectionError(cause) {
		return newDBError(DBErrorKindConnection, DBErrorCodeConnection, stage, "", ErrDatasourceUnreachable), true
	}
	return nil, false
}

func buildDMDSN(datasource model.Datasource, password string) (string, error) {
	// dm8 v8.1.4.80 splits this DSN itself and does not URL-decode userinfo.
	// An '@' in the password is safe because the driver uses LastIndex("@"),
	// while '?' and control characters cannot be represented unambiguously.
	if strings.Contains(datasource.Username, ":") ||
		strings.ContainsAny(datasource.Username, "?") ||
		strings.ContainsAny(password, "?") ||
		strings.ContainsAny(datasource.Database, "?&=") ||
		containsControl(datasource.Username) || containsControl(password) || containsControl(datasource.Database) {
		return "", fmt.Errorf("build DM connection configuration: unsupported credential or schema character")
	}
	query := "schema=" + datasource.Database
	if datasource.StmtTimeoutMS > 0 {
		query += "&connectTimeout=" + strconv.Itoa(datasource.StmtTimeoutMS)
	}
	return "dm://" + datasource.Username + ":" + password + "@" +
		net.JoinHostPort(datasource.Host, strconv.Itoa(datasource.Port)) + "?" + query, nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

var _ Executor = (*DMExecutor)(nil)
