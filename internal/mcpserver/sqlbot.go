package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/parser"
	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
	pgquery "github.com/pganalyze/pg_query_go/v5"
	"vitess.io/vitess/go/vt/sqlparser"
)

type sqlbotScopeContextKey struct{}

type SQLBotDelegationVerifier interface {
	VerifySQLBot(context.Context, string, string) (rbac.Principal, store.SQLBotBinding, error)
}

type sqlbotHTTPGate struct {
	verifier SQLBotDelegationVerifier
	runtime  *bootstrap.Runtime
}

func WithSQLBotDelegation(verifier SQLBotDelegationVerifier) HTTPOption {
	return func(options *httpHandlerOptions) { options.sqlbot = verifier }
}

func sqlbotScope(ctx context.Context) (store.SQLBotBinding, bool) {
	b, ok := ctx.Value(sqlbotScopeContextKey{}).(store.SQLBotBinding)
	return b, ok && b.ID != ""
}

// serve handles only the independently signed SQLBot credential namespace.
// Other human credentials retain the original deployment-level contract.
func (gate sqlbotHTTPGate) serve(writer http.ResponseWriter, request *http.Request, next http.Handler) bool {
	values := request.Header.Values(humanDelegationHeader)
	if len(values) != 1 {
		return false
	}
	token, ok := bearerKey(values[0])
	if !ok || !strings.HasPrefix(token, "SQB1.") {
		return false
	}
	identity, ok := identityFromContext(request.Context())
	if !ok || gate.verifier == nil {
		writeHTTPError(writer, http.StatusUnauthorized, "unauthorized")
		return true
	}
	principal, b, err := gate.verifier.VerifySQLBot(request.Context(), token, identity.agent.ID)
	if err != nil || principal.UserID == "" || principal.TenantID != identity.agent.TenantID || b.TenantID != principal.TenantID ||
		b.AgentSQLUserID != principal.UserID || b.AgentID != identity.agent.ID || identity.agent.Level != "readonly" || identity.agent.TaskGrantID != "" {
		writeHTTPError(writer, http.StatusUnauthorized, "unauthorized")
		return true
	}
	ctx := context.WithValue(request.Context(), delegatedHumanContextKey{}, delegatedHuman{userID: principal.UserID, tenantID: principal.TenantID})
	ctx = context.WithValue(ctx, sqlbotScopeContextKey{}, b)
	request = request.WithContext(ctx)
	if !gate.allowed(request, b) {
		gate.deny(writer, request, b)
		return true
	}
	next.ServeHTTP(writer, request)
	return true
}

func (gate sqlbotHTTPGate) allowed(request *http.Request, b store.SQLBotBinding) bool {
	if request.Method == http.MethodDelete {
		return true
	}
	// The managed adapter uses complete JSON responses. Deny SSE replay so a
	// revoked identity cannot keep an authenticated long-lived event stream.
	if request.Method != http.MethodPost || request.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxMCPRequestBodyBytes+1))
	if err != nil || len(raw) > maxMCPRequestBodyBytes {
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))
	var rpc struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments struct {
				DatasourceID string `json:"datasource_id"`
				SQL          string `json:"sql"`
				ApprovalID   string `json:"approval_id"`
				Table        string `json:"table"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &rpc) != nil {
		return false
	}
	switch rpc.Method {
	case "initialize", "notifications/initialized", "ping", "tools/list":
		return true
	case "tools/call":
	default:
		return false
	}
	a := rpc.Params.Arguments
	switch rpc.Params.Name {
	case "list_datasources":
		return true
	case "list_schema":
		if a.DatasourceID != b.DatasourceID {
			return false
		}
		parts := strings.Split(a.Table, ".")
		return len(parts) == 1 || len(parts) == 2 && parts[0] == b.SchemaName
	case "query", "explain_query", "request_approval":
		return a.DatasourceID == b.DatasourceID && sqlbotReadOnlyForDatasource(a.SQL, b.DatasourceDBType)
	case "get_approval_result", "execute_approved":
		if gate.runtime == nil || gate.runtime.Store == nil {
			return false
		}
		approval, err := gate.runtime.Store.Approvals().Get(request.Context(), a.ApprovalID)
		return err == nil && approval.TenantID == b.TenantID && approval.AgentID != nil && *approval.AgentID == b.AgentID &&
			approval.RequesterUserID != nil && *approval.RequesterUserID == b.AgentSQLUserID && approval.DatasourceID != nil &&
			*approval.DatasourceID == b.DatasourceID && approval.SQLRaw != nil && sqlbotReadOnlyForDatasource(*approval.SQLRaw, b.DatasourceDBType)
	default:
		return false
	}
}

func sqlbotReadOnlyForDatasource(sql, dbType string) bool {
	if dbType == "mysql" {
		return sqlbotMySQLReadOnly(sql)
	}
	if dbType != "postgres" {
		return false
	}
	return sqlbotReadOnly(sql)
}

func sqlbotMySQLReadOnly(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	if len(sql) > 65536 || strings.ContainsAny(sql, "@#`") || strings.Contains(sql, "--") ||
		strings.Contains(sql, "/*") || strings.Contains(sql, ":=") ||
		len(trimmed) <= len("SELECT") || !strings.EqualFold(trimmed[:len("SELECT")], "SELECT") ||
		!strings.ContainsAny(trimmed[len("SELECT"):len("SELECT")+1], " \t\r\n") {
		return false
	}
	p, err := parser.NewParser(model.DBDialect("mysql"))
	if err != nil {
		return false
	}
	ast, err := p.Parse(sql)
	if err != nil || ast == nil || ast.IsMulti || ast.StmtType != model.StmtType("SELECT") {
		return false
	}
	for _, op := range ast.Operations {
		if op == "SQL_COMMENT" || op == "INTO OUTFILE" || op == "INTO DUMPFILE" ||
			strings.HasPrefix(op, "EXPLAIN") {
			return false
		}
	}
	for _, function := range ast.Functions {
		if !sqlbotMySQLFunctionAllowed(function) {
			return false
		}
	}
	for _, projection := range ast.ProjectionLineages {
		for _, arm := range projection.Arms {
			if arm.Status != model.LineageResolved && arm.Status != model.LineageSourceFree {
				return false
			}
		}
	}
	vitessParser, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.0.30"})
	if err != nil {
		return false
	}
	parsed, err := vitessParser.Parse(sql)
	if err != nil {
		return false
	}
	if _, ok := parsed.(*sqlparser.Select); !ok {
		return false
	}
	err = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch expression := node.(type) {
		case *sqlparser.Select:
			if expression.With != nil || expression.Into != nil || expression.Lock != 0 {
				return false, errors.New("SQLBot MySQL SELECT has unsupported side effects")
			}
		case *sqlparser.FuncExpr:
			if !expression.Qualifier.IsEmpty() || !sqlbotMySQLFunctionAllowed(expression.Name.String()) {
				return false, errors.New("SQLBot MySQL function is unsupported")
			}
		case sqlparser.AggrFunc:
			if !sqlbotMySQLFunctionAllowed(expression.AggrName()) {
				return false, errors.New("SQLBot MySQL aggregate is unsupported")
			}
		default:
			// Vitess models several functions outside FuncExpr. Unknown function
			// node types cannot inherit the short allowlist by accident.
			if strings.Contains(strings.ToLower(reflect.TypeOf(node).String()), "func") {
				return false, errors.New("SQLBot MySQL function node is unsupported")
			}
		}
		return true, nil
	}, parsed)
	if err != nil {
		return false
	}
	return true
}

func sqlbotMySQLFunctionAllowed(name string) bool {
	switch strings.ToLower(name) {
	case "count", "sum", "avg", "min", "max", "abs", "round", "ceil", "ceiling", "floor", "lower", "upper", "length":
		return true
	default:
		return false
	}
}

func sqlbotReadOnly(sql string) bool {
	p, err := parser.NewParser(model.DBDialect("postgres"))
	if err != nil {
		return false
	}
	ast, err := p.Parse(sql)
	if err != nil || ast == nil || ast.IsMulti || ast.StmtType != model.StmtType("SELECT") {
		return false
	}
	for _, op := range ast.Operations {
		if op == "SELECT INTO" || strings.HasPrefix(op, "EXPLAIN") {
			return false
		}
	}
	// Use the raw tree as well: the shared statement category intentionally
	// classifies EXPLAIN and SELECT INTO with their underlying SELECT.
	raw, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return false
	}
	var document struct {
		Statements []struct {
			Statement map[string]any `json:"stmt"`
		} `json:"stmts"`
	}
	if json.Unmarshal([]byte(raw), &document) != nil || len(document.Statements) != 1 || document.Statements[0].Statement["SelectStmt"] == nil {
		return false
	}
	nodes := 0
	return sqlbotReadTree(document.Statements[0].Statement, 0, &nodes)
}

// This is an additional integration restriction, not a replacement for
// PostgreSQL object binding, database privileges or the original policy chain.
func sqlbotReadTree(value any, depth int, nodes *int) bool {
	*nodes++
	if depth > 128 || *nodes > 20000 {
		return false
	}
	switch node := value.(type) {
	case []any:
		for _, child := range node {
			if !sqlbotReadTree(child, depth+1, nodes) {
				return false
			}
		}
	case map[string]any:
		for key, child := range node {
			if strings.HasSuffix(key, "Stmt") && key != "SelectStmt" {
				return false
			}
			if key == "intoClause" || key == "lockingClause" || key == "RangeFunction" {
				return false
			}
			if key == "FuncCall" {
				function, ok := child.(map[string]any)
				if !ok {
					return false
				}
				parts, ok := function["funcname"].([]any)
				if !ok || len(parts) < 1 || len(parts) > 2 {
					return false
				}
				names := make([]string, 0, len(parts))
				for _, part := range parts {
					item, ok := part.(map[string]any)
					if !ok {
						return false
					}
					str, ok := item["String"].(map[string]any)
					if !ok {
						return false
					}
					name, ok := str["sval"].(string)
					if !ok {
						return false
					}
					names = append(names, name)
				}
				if len(names) == 2 && names[0] != "pg_catalog" {
					return false
				}
				switch names[len(names)-1] {
				case "count", "sum", "avg", "min", "max", "abs", "round", "ceil", "ceiling", "floor", "lower", "upper", "length", "date_trunc", "date_part", "to_char":
				default:
					return false
				}
			}
			if !sqlbotReadTree(child, depth+1, nodes) {
				return false
			}
		}
	}
	return true
}

func (gate sqlbotHTTPGate) deny(writer http.ResponseWriter, request *http.Request, b store.SQLBotBinding) {
	if gate.runtime == nil || gate.runtime.ManagementAudit == nil {
		writeHTTPError(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	action, actor, detail := "sqlbot_scope_denied", "user", `{"binding_id":`+strconvJSON(b.ID)+`}`
	recorded, err := gate.runtime.ManagementAudit.Record(request.Context(), model.AuditLog{AgentID: &b.AgentID, DatasourceID: &b.DatasourceID,
		Decision: string(model.DecisionDeny), Action: &action, ActorType: &actor, ActorID: &b.AgentSQLUserID, DetailsJSON: &detail})
	if err != nil {
		writeHTTPError(writer, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	gate.runtime.PublishManagementAudit(recorded)
	writeHTTPError(writer, http.StatusForbidden, "operation is outside the SQLBot binding")
}

func strconvJSON(value string) string { raw, _ := json.Marshal(value); return string(raw) }
