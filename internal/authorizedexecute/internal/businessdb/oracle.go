package businessdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/model"
	goora "github.com/sijms/go-ora/v2"
	gooranetwork "github.com/sijms/go-ora/v2/network"
)

// OracleExecutor supports connection lifecycle, typed metadata, narrowly
// validated read-only SELECTs and normalized PLAN_TABLE EXPLAIN output.
type OracleExecutor struct{ *limitedSQLExecutor }

// NewOracleExecutor opens and verifies an Oracle service datasource.
func NewOracleExecutor(
	ctx context.Context,
	datasource model.Datasource,
	password string,
	readOnly bool,
) (*OracleExecutor, error) {
	dsn := buildOracleDSN(datasource, password)
	executor, err := newLimitedSQLExecutorWithDialect(
		ctx, datasource, "oracle", dsn, "oracle", password, readOnly,
		classifyOracleError, explainOracleSelect,
	)
	if err != nil {
		return nil, err
	}
	if err := executor.probeCurrentSchema(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL"); err != nil {
		_ = executor.Close()
		return nil, err
	}
	return &OracleExecutor{limitedSQLExecutor: executor}, nil
}

func classifyOracleError(ctx context.Context, stage DBStage, cause error) (error, bool) {
	if classified, ok := classifyContextError(ctx, stage, cause); ok {
		return classified, true
	}
	var driverError *gooranetwork.OracleError
	if errors.As(cause, &driverError) {
		driverCode := strconv.Itoa(driverError.ErrCode)
		switch code := driverError.ErrCode; {
		case code == 1017:
			return newDBError(DBErrorKindAuthentication, DBErrorCodeAuthentication, stage, driverCode, nil), true
		case code == 1031:
			return newDBError(DBErrorKindPermission, DBErrorCodePermission, stage, driverCode, ErrPermissionDenied), true
		case code == 900 || code == 901 || code == 905 || code == 906 || code == 907 ||
			code == 911 || code == 917 || code == 923 || code == 933 || code == 936:
			return newDBError(DBErrorKindSyntax, DBErrorCodeSyntax, stage, driverCode, nil), true
		case code == 904:
			return newDBError(DBErrorKindColumnNotFound, DBErrorCodeColumnNotFound, stage, driverCode, nil), true
		case code == 942 || code == 4043:
			return newDBError(DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound, stage, driverCode, nil), true
		case code == 1:
			return newDBError(DBErrorKindConstraint, DBErrorCodeConstraint, stage, driverCode, nil), true
		case code == 54 || code == 60:
			return newDBError(DBErrorKindRetryable, DBErrorCodeRetryable, stage, driverCode, nil), true
		case code == 1652 || code == 4031:
			return newDBError(DBErrorKindResource, DBErrorCodeResource, stage, driverCode, nil), true
		case code == 1013:
			return newDBError(DBErrorKindInterrupted, DBErrorCodeInterrupted, stage, driverCode, ErrQueryTimeout), true
		case code == 28 || code == 1012 || code == 1033 || code == 1034 || code == 1089 ||
			code == 3113 || code == 3114 || code == 3135 || (code >= 12100 && code <= 12699):
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

var oracleExplainSequence atomic.Uint64

const oracleExplainRowLimit = 4_096

func explainOracleSelect(
	ctx context.Context,
	runner limitedDialectRunner,
	sqlText string,
) (model.ExplainInfo, error) {
	statementID := fmt.Sprintf(
		"ASQL_%X_%X",
		uint64(time.Now().UnixNano()),
		oracleExplainSequence.Add(1),
	)
	if len(statementID) > 30 {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	tx, err := runner.BeginTx(ctx, nil)
	if err != nil {
		return model.ExplainInfo{}, oracleDatabaseError(ctx, DBStageExplain, err)
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(
		ctx,
		"EXPLAIN PLAN SET STATEMENT_ID='"+statementID+"' FOR "+sqlText,
	); err != nil {
		return model.ExplainInfo{}, oracleDatabaseError(ctx, DBStageExplain, err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT ID,PARENT_ID,OPERATION,OPTIONS,CARDINALITY,COST
FROM PLAN_TABLE WHERE STATEMENT_ID=:1 ORDER BY ID`, statementID)
	if err != nil {
		return model.ExplainInfo{}, oracleDatabaseError(ctx, DBStageExplain, err)
	}
	result, err := collectRows(&mysqlRowSource{rows: rows}, oracleExplainRowLimit)
	if err != nil {
		return model.ExplainInfo{}, oracleDatabaseError(ctx, DBStageExplain, err)
	}
	if result.Truncated {
		return model.ExplainInfo{}, newDBError(DBErrorKindResource, DBErrorCodeResource, DBStageExplain, "", nil)
	}
	info, err := parseOraclePlanTable(result)
	if err != nil {
		return model.ExplainInfo{}, newDBError(DBErrorKindExecution, DBErrorCodeExecution, DBStageExplain, "", nil)
	}
	if err := tx.Rollback(); err != nil {
		return model.ExplainInfo{}, oracleDatabaseError(ctx, DBStageExplain, err)
	}
	rolledBack = true
	return info, nil
}

func oracleDatabaseError(ctx context.Context, stage DBStage, cause error) error {
	var resource *ResourceError
	if errors.As(cause, &resource) {
		return resource
	}
	if classified, ok := classifyOracleError(ctx, stage, cause); ok {
		return classified
	}
	return newDBError(DBErrorKindExecution, DBErrorCodeExecution, stage, "", nil)
}

func parseOraclePlanTable(result model.QueryResult) (model.ExplainInfo, error) {
	expectedColumns := []string{"ID", "PARENT_ID", "OPERATION", "OPTIONS", "CARDINALITY", "COST"}
	if len(result.Columns) != len(expectedColumns) || len(result.Rows) == 0 || result.Truncated {
		return model.ExplainInfo{}, fmt.Errorf("unsupported Oracle PLAN_TABLE shape")
	}
	for index := range expectedColumns {
		if !strings.EqualFold(strings.TrimSpace(result.Columns[index]), expectedColumns[index]) {
			return model.ExplainInfo{}, fmt.Errorf("unsupported Oracle PLAN_TABLE columns")
		}
	}
	type planNode struct {
		id          int64
		parent      int64
		hasParent   bool
		operation   string
		options     string
		cardinality string
		cost        string
	}
	nodes := make([]planNode, 0, len(result.Rows))
	nodesByID := make(map[int64]planNode, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) != len(expectedColumns) {
			return model.ExplainInfo{}, fmt.Errorf("Oracle PLAN_TABLE row width mismatch")
		}
		id, err := strconv.ParseInt(strings.TrimSpace(row[0]), 10, 64)
		if err != nil || id < 0 {
			return model.ExplainInfo{}, fmt.Errorf("invalid Oracle plan node ID")
		}
		if _, duplicate := nodesByID[id]; duplicate {
			return model.ExplainInfo{}, fmt.Errorf("duplicate Oracle plan node ID")
		}
		node := planNode{
			id: id, operation: strings.ToUpper(strings.TrimSpace(row[2])),
			options:     strings.ToUpper(strings.TrimSpace(row[3])),
			cardinality: strings.TrimSpace(row[4]), cost: strings.TrimSpace(row[5]),
		}
		if node.operation == "" {
			return model.ExplainInfo{}, fmt.Errorf("empty Oracle plan operation")
		}
		for _, field := range []string{node.operation, node.options, node.cardinality, node.cost} {
			if strings.IndexFunc(field, unicode.IsControl) >= 0 {
				return model.ExplainInfo{}, fmt.Errorf("control character in Oracle plan field")
			}
		}
		if node.cardinality != "" {
			cardinality, parseErr := strconv.ParseInt(node.cardinality, 10, 64)
			if parseErr != nil || cardinality < 0 {
				return model.ExplainInfo{}, fmt.Errorf("invalid Oracle cardinality")
			}
		}
		if node.cost != "" {
			cost, parseErr := strconv.ParseFloat(node.cost, 64)
			if parseErr != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
				return model.ExplainInfo{}, fmt.Errorf("invalid Oracle cost")
			}
		}
		if parentText := strings.TrimSpace(row[1]); parentText != "" {
			node.parent, err = strconv.ParseInt(parentText, 10, 64)
			if err != nil || node.parent < 0 || node.parent == node.id {
				return model.ExplainInfo{}, fmt.Errorf("invalid Oracle plan parent ID")
			}
			node.hasParent = true
		}
		nodes = append(nodes, node)
		nodesByID[id] = node
	}
	root := nodes[0]
	if root.id != 0 || root.hasParent || root.operation != "SELECT STATEMENT" || root.cardinality == "" || root.cost == "" {
		return model.ExplainInfo{}, fmt.Errorf("unsupported Oracle plan root")
	}
	estimatedRows, err := strconv.ParseInt(root.cardinality, 10, 64)
	if err != nil || estimatedRows < 0 {
		return model.ExplainInfo{}, fmt.Errorf("invalid Oracle cardinality")
	}
	estimatedCost, err := strconv.ParseFloat(root.cost, 64)
	if err != nil || estimatedCost < 0 || math.IsNaN(estimatedCost) || math.IsInf(estimatedCost, 0) {
		return model.ExplainInfo{}, fmt.Errorf("invalid Oracle cost")
	}
	for _, node := range nodes[1:] {
		if !node.hasParent {
			return model.ExplainInfo{}, fmt.Errorf("multiple Oracle plan roots")
		}
		seen := map[int64]struct{}{node.id: {}}
		current := node
		for current.hasParent {
			if _, duplicate := seen[current.parent]; duplicate {
				return model.ExplainInfo{}, fmt.Errorf("cycle in Oracle plan tree")
			}
			seen[current.parent] = struct{}{}
			parent, exists := nodesByID[current.parent]
			if !exists {
				return model.ExplainInfo{}, fmt.Errorf("missing Oracle plan parent")
			}
			current = parent
		}
		if current.id != root.id {
			return model.ExplainInfo{}, fmt.Errorf("disconnected Oracle plan tree")
		}
	}
	info := model.ExplainInfo{EstScanRows: estimatedRows, EstCost: estimatedCost}
	var raw strings.Builder
	for _, node := range nodes {
		if node.operation == "INDEX" || node.operation == "BITMAP INDEX" || node.operation == "DOMAIN INDEX" {
			info.UsesIndex = true
		}
		if node.operation == "TABLE ACCESS" && node.options == "FULL" {
			info.SeqScan = true
		}
		fmt.Fprintf(&raw, "%d|%s|%s|%s|%s\n", node.id, node.operation, node.options, node.cardinality, node.cost)
	}
	info.Raw = strings.TrimSuffix(raw.String(), "\n")
	return info, nil
}

func buildOracleDSN(datasource model.Datasource, password string) string {
	options := map[string]string{}
	if datasource.StmtTimeoutMS > 0 {
		seconds := (datasource.StmtTimeoutMS + 999) / 1_000
		options["CONNECTION TIMEOUT"] = strconv.Itoa(seconds)
		options["TIMEOUT"] = strconv.Itoa(seconds)
	}
	return goora.BuildUrl(
		datasource.Host,
		datasource.Port,
		datasource.Database,
		datasource.Username,
		password,
		options,
	)
}

var _ Executor = (*OracleExecutor)(nil)
