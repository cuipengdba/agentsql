package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	gremlingo "github.com/apache/tinkerpop/gremlin-go/v3/driver"
	"github.com/cuipengdba/agentsql/internal/model"
)

type JanusGraphExecutor struct {
	client       *gremlingo.Client
	datasourceID string
	readOnly     bool
	timeout      time.Duration
}
type janusGraphConnection struct {
	uri     string
	auth    gremlingo.AuthInfoProvider
	tls     *tls.Config
	timeout time.Duration
	limit   int
}

func janusGraphOptions(ds model.Datasource, password string) (janusGraphConnection, error) {
	if ds.DBType != "janusgraph" {
		return janusGraphConnection{}, fmt.Errorf("invalid JanusGraph dialect")
	}
	if strings.TrimSpace(ds.Host) == "" {
		return janusGraphConnection{}, fmt.Errorf("janusgraph host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 8182
	}
	if port < 1 || port > 65535 {
		return janusGraphConnection{}, fmt.Errorf("janusgraph port is invalid")
	}
	if ds.Database != "" {
		return janusGraphConnection{}, fmt.Errorf("JanusGraph database field is unsupported by Gremlin Server")
	}
	if ds.Username == "" && password != "" {
		return janusGraphConnection{}, fmt.Errorf("JanusGraph password requires username")
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return janusGraphConnection{}, err
	}
	limit, err := normalizedConnectionLimit(ds.ConnLimit)
	if err != nil {
		return janusGraphConnection{}, err
	}
	if ds.TrustServerCertificate {
		return janusGraphConnection{}, fmt.Errorf("unverified JanusGraph TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" && ds.TLSMode != "require" {
		return janusGraphConnection{}, fmt.Errorf("invalid JanusGraph TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return janusGraphConnection{}, fmt.Errorf("JanusGraph TLS settings require tls_mode")
	}
	scheme := "ws"
	var cfg *tls.Config
	if ds.TLSMode != "" {
		scheme = "wss"
		cfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, err := os.ReadFile(ds.TLSCAFile)
			if err != nil {
				return janusGraphConnection{}, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return janusGraphConnection{}, fmt.Errorf("invalid JanusGraph TLS CA")
			}
			cfg.RootCAs = roots
		}
	}
	var auth gremlingo.AuthInfoProvider
	if ds.Username != "" {
		auth = gremlingo.BasicAuthInfo(ds.Username, password)
	}
	return janusGraphConnection{uri: scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(port)) + "/gremlin", auth: auth, tls: cfg, timeout: time.Duration(ms) * time.Millisecond, limit: limit}, nil
}
func NewJanusGraphExecutor(ds model.Datasource, password string, readOnly bool) (*JanusGraphExecutor, error) {
	opts, err := janusGraphOptions(ds, password)
	if err != nil {
		return nil, err
	}
	client, err := gremlingo.NewClient(opts.uri, func(s *gremlingo.ClientSettings) {
		if opts.auth != nil {
			s.AuthInfo = opts.auth
		}
		if opts.tls != nil {
			s.TlsConfig = opts.tls
		}
		s.ConnectionTimeout = opts.timeout
		s.WriteDeadline = opts.timeout
		s.MaximumConcurrentConnections = opts.limit
	})
	if err != nil {
		return nil, err
	}
	e := &JanusGraphExecutor{client: client, datasourceID: ds.ID, readOnly: readOnly, timeout: opts.timeout}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *JanusGraphExecutor) Category() model.DatasourceCategory { return model.CategoryGraph }
func (e *JanusGraphExecutor) Dialect() model.DBDialect           { return "janusgraph" }
func (e *JanusGraphExecutor) Close() error                       { e.client.Close(); return nil }
func (e *JanusGraphExecutor) submit(ctx context.Context, script string) (NativeQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	ms := int(e.timeout / time.Millisecond)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := int(time.Until(deadline) / time.Millisecond)
		if remaining < 1 {
			return NativeQueryResult{}, ctx.Err()
		}
		if remaining < ms {
			ms = remaining
		}
	}
	opts := new(gremlingo.RequestOptionsBuilder).SetEvaluationTimeout(ms).SetBatchSize(100).Create()
	rs, err := e.client.SubmitWithOptions(script, opts)
	if err != nil {
		return NativeQueryResult{}, err
	}
	values, err := rs.All()
	if err != nil {
		return NativeQueryResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	rows := make([][]string, 0, min(len(values), nativeMaxRows))
	for i, v := range values {
		if i >= nativeMaxRows {
			break
		}
		rows = append(rows, []string{boundedNativeString(v.GetString())})
	}
	result := boundedNativeRows([]string{"value"}, rows)
	if len(values) > nativeMaxRows {
		result.Info = map[string]string{"truncated": "true"}
	}
	return result, nil
}
func (e *JanusGraphExecutor) Ping(ctx context.Context) error {
	_, err := e.submit(ctx, "g.V().limit(1).count()")
	return err
}

// Gremlin Server has no standard server-version endpoint. An empty value is intentional.
func (e *JanusGraphExecutor) ServerVersion(ctx context.Context) (string, error) { return "", ctx.Err() }
func janusGraphCatalog(vertices, edges []string) []NativeNamespace {
	out := make([]NativeNamespace, 0, min(len(vertices)+len(edges)+1, nativeMaxRows))
	out = append(out, NativeNamespace{Name: "graph", Kind: "graph", ItemCount: int64(len(vertices)), Metadata: map[string]string{"count_scope": "distinct vertex labels in bounded traversal; may be incomplete", "server_version": "Gremlin Server has no standard version endpoint"}})
	for _, group := range []struct {
		kind  string
		names []string
	}{{"vertex_label", vertices}, {"edge_label", edges}} {
		for _, name := range group.names {
			if len(out) >= nativeMaxRows {
				break
			}
			out = append(out, NativeNamespace{Name: boundedNativeString(name), Kind: group.kind, Metadata: map[string]string{"count_scope": "distinct labels in bounded traversal; may be incomplete"}})
		}
	}
	return out
}
func (e *JanusGraphExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.submit(ctx, "g.V().label().dedup().limit(50)")
	if err != nil {
		return NativeCatalog{}, err
	}
	r, err := e.submit(ctx, "g.E().label().dedup().limit(50)")
	if err != nil {
		return NativeCatalog{}, err
	}
	return NativeCatalog{Category: model.CategoryGraph, Namespaces: janusGraphCatalog(firstColumn(v), firstColumn(r))}, nil
}

type gremlinStep struct{ name, args string }

// gremlinSteps accepts only a literal g.<step>(...) chain. Nested traversal
// expressions, interpolation, Groovy operators and arbitrary methods fail closed.
func gremlinSteps(script string) ([]gremlinStep, error) {
	if len(script) < 5 || len(script) > nativeMaxValueBytes || !strings.HasPrefix(script, "g.") || strings.ContainsAny(script, ";\r\n{}[]=+*/") {
		return nil, fmt.Errorf("Gremlin script is not a safe traversal")
	}
	if strings.Contains(strings.ToLower(script), "java.lang") || strings.Contains(strings.ToLower(script), "graph.") || strings.Contains(strings.ToLower(script), "system") || strings.Contains(script, "//") {
		return nil, fmt.Errorf("Gremlin system access is forbidden")
	}
	var out []gremlinStep
	for p := 1; p < len(script); {
		if script[p] != '.' {
			return nil, fmt.Errorf("Gremlin step separator is invalid")
		}
		p++
		start := p
		for p < len(script) && ((script[p] >= 'a' && script[p] <= 'z') || (script[p] >= 'A' && script[p] <= 'Z')) {
			p++
		}
		if start == p || p >= len(script) || script[p] != '(' {
			return nil, fmt.Errorf("Gremlin step is invalid")
		}
		name := script[start:p]
		p++
		start = p
		quote := byte(0)
		escaped := false
		for p < len(script) {
			c := script[p]
			if quote != 0 {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == quote {
					quote = 0
				}
				p++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				p++
				continue
			}
			if c == ')' {
				break
			}
			if c == '(' || c == '`' || c == '$' {
				return nil, fmt.Errorf("nested or dynamic Gremlin expression is forbidden")
			}
			p++
		}
		if p >= len(script) || quote != 0 {
			return nil, fmt.Errorf("Gremlin arguments are unbalanced")
		}
		out = append(out, gremlinStep{name: name, args: strings.TrimSpace(script[start:p])})
		p++
		if len(out) > 20 {
			return nil, fmt.Errorf("Gremlin traversal has too many steps")
		}
	}
	return out, nil
}
func gremlinLiteral(s string) bool {
	if s == "" {
		return false
	}
	if len(s) >= 2 && ((s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"')) {
		return !strings.ContainsAny(s[1:len(s)-1], "'\"\\")
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

var gremlinEndpoint = regexp.MustCompile(`\.to\(g\.V\((('[^'"\\]+')|("[^'"\\]+")|([0-9]+))\)\)`)

func gremlinLiterals(args string, min, max int) bool {
	if args == "" {
		return min == 0
	}
	parts := strings.Split(args, ",")
	if len(parts) < min || len(parts) > max {
		return false
	}
	for _, part := range parts {
		if !gremlinLiteral(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}
func gremlinAllowed(script string, readOnly bool) error {
	parsed := gremlinEndpoint.ReplaceAllString(script, `.to($1)`)
	steps, err := gremlinSteps(parsed)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return fmt.Errorf("Gremlin traversal is empty")
	}
	first := steps[0]
	if first.name != "V" && first.name != "E" && first.name != "addV" {
		return fmt.Errorf("Gremlin source step is not allowed")
	}
	anchored := (first.name == "V" || first.name == "E") && gremlinLiteral(first.args)
	write := first.name == "addV"
	bounded := false
	addEdge := false
	endpoint := false
	if first.name == "addV" && !gremlinLiteral(first.args) {
		return fmt.Errorf("addV requires a literal label")
	}
	for i, step := range steps {
		switch step.name {
		case "V", "E":
			if i != 0 {
				return fmt.Errorf("nested Gremlin source step is forbidden")
			}
			if step.args != "" && !gremlinLiteral(step.args) {
				return fmt.Errorf("Gremlin id must be literal")
			}
		case "limit":
			n, e := strconv.Atoi(step.args)
			if e != nil || n < 1 || n > 100 {
				return fmt.Errorf("Gremlin limit must be 1 to 100")
			}
			bounded = true
		case "has", "hasId", "values", "properties", "valueMap", "label", "dedup", "order", "count", "id", "as":
			if !gremlinLiterals(step.args, 0, 2) {
				return fmt.Errorf("dynamic Gremlin step argument is forbidden")
			}
			if step.name == "as" && !gremlinLiterals(step.args, 1, 1) {
				return fmt.Errorf("Gremlin alias must be literal")
			}
			if (step.name == "order" || step.name == "count") && !bounded {
				return fmt.Errorf("Gremlin aggregation requires a preceding limit")
			}
			if step.name == "hasId" && first.name == "V" && gremlinLiterals(step.args, 1, 1) {
				anchored = true
			}
		case "addV", "addE", "property", "update", "remove":
			write = true
			if step.name == "addE" {
				if first.name != "V" || !anchored || !gremlinLiteral(step.args) {
					return fmt.Errorf("addE requires an explicit source id and label")
				}
				addEdge = true
			}
			if step.name == "remove" && step.args != "" {
				return fmt.Errorf("remove takes no arguments")
			}
			if step.name == "addV" && !gremlinLiteral(step.args) {
				return fmt.Errorf("addV requires a literal label")
			}
			if (step.name == "property" || step.name == "update") && !gremlinLiterals(step.args, 2, 2) {
				return fmt.Errorf("Gremlin write arguments must be literal")
			}
		case "drop":
			if !anchored || step.args != "" || i != len(steps)-1 || i > 2 || first.name == "addV" {
				return fmt.Errorf("drop requires a single explicit element id")
			}
			if i == 2 && steps[1].name != "hasId" {
				return fmt.Errorf("drop requires a single explicit element id")
			}
			write = true
		case "to":
			if !addEdge || !gremlinLiteral(step.args) || !strings.Contains(script, ".to(g.V(") {
				return fmt.Errorf("edge endpoint must be an explicit vertex id")
			}
			endpoint = true
		default:
			return fmt.Errorf("Gremlin step %s is not allowed", step.name)
		}
	}
	if addEdge && !endpoint {
		return fmt.Errorf("addE requires an explicit destination id")
	}
	if first.name == "V" || first.name == "E" {
		if write && !anchored {
			return fmt.Errorf("Gremlin write requires an explicit element id")
		}
		if !write && !bounded {
			return fmt.Errorf("unbounded Gremlin traversal is forbidden")
		}
	}
	if write && readOnly {
		return fmt.Errorf("Gremlin write is forbidden in read-only mode")
	}
	return nil
}
func (e *JanusGraphExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("janusgraph", e.datasourceID, command, e.readOnly, err) }()
	if request.Namespace != "" {
		return result, fmt.Errorf("JanusGraph namespace is unsupported")
	}
	if len(request.Args) != 1 {
		return result, fmt.Errorf("one Gremlin traversal is required")
	}
	command = strings.ToUpper(strings.TrimSpace(request.Command))
	if command != "G" {
		return result, fmt.Errorf("Gremlin command must be g")
	}
	if err = gremlinAllowed(request.Args[0], e.readOnly); err != nil {
		return result, err
	}
	return e.submit(ctx, request.Args[0])
}
