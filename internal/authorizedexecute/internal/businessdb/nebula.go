package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	nebula "github.com/vesoft-inc/nebula-go/v3"
)

type NebulaExecutor struct {
	pool                   *nebula.ConnectionPool
	session                *nebula.Session
	mu                     sync.Mutex
	datasourceID, database string
	readOnly               bool
}
type nebulaConnection struct {
	host               string
	port               int
	username, password string
	tls                *tls.Config
	conf               nebula.PoolConfig
	timeout            time.Duration
}

func nebulaOptions(ds model.Datasource, password string) (nebulaConnection, error) {
	if ds.DBType != "nebula" {
		return nebulaConnection{}, fmt.Errorf("invalid Nebula dialect")
	}
	if strings.TrimSpace(ds.Host) == "" {
		return nebulaConnection{}, fmt.Errorf("nebula host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 9669
	}
	if port < 1 || port > 65535 {
		return nebulaConnection{}, fmt.Errorf("nebula port is invalid")
	}
	if ds.Database != "" && !graphIdentifier.MatchString(ds.Database) {
		return nebulaConnection{}, fmt.Errorf("invalid Nebula space")
	}
	if ds.Username == "" && password != "" {
		return nebulaConnection{}, fmt.Errorf("Nebula password requires username")
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nebulaConnection{}, err
	}
	limit, err := normalizedConnectionLimit(ds.ConnLimit)
	if err != nil {
		return nebulaConnection{}, err
	}
	if ds.TrustServerCertificate {
		return nebulaConnection{}, fmt.Errorf("unverified Nebula TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" && ds.TLSMode != "require" {
		return nebulaConnection{}, fmt.Errorf("invalid Nebula TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return nebulaConnection{}, fmt.Errorf("Nebula TLS settings require tls_mode")
	}
	var cfg *tls.Config
	if ds.TLSMode != "" {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, err := os.ReadFile(ds.TLSCAFile)
			if err != nil {
				return nebulaConnection{}, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nebulaConnection{}, fmt.Errorf("invalid Nebula TLS CA")
			}
			cfg.RootCAs = roots
		}
	}
	conf := nebula.GetDefaultConf()
	conf.TimeOut = time.Duration(ms) * time.Millisecond
	conf.MaxConnPoolSize = limit
	conf.MinConnPoolSize = 1
	return nebulaConnection{host: ds.Host, port: port, username: ds.Username, password: password, tls: cfg, conf: conf, timeout: time.Duration(ms) * time.Millisecond}, nil
}
func NewNebulaExecutor(ds model.Datasource, password string, readOnly bool) (*NebulaExecutor, error) {
	opts, err := nebulaOptions(ds, password)
	if err != nil {
		return nil, err
	}
	pool, err := nebula.NewSslConnectionPool([]nebula.HostAddress{{Host: opts.host, Port: opts.port}}, opts.conf, opts.tls, nebula.DefaultLogger{})
	if err != nil {
		return nil, err
	}
	session, err := pool.GetSession(opts.username, opts.password)
	if err != nil {
		pool.Close()
		return nil, err
	}
	e := &NebulaExecutor{pool: pool, session: session, datasourceID: ds.ID, database: ds.Database, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *NebulaExecutor) Category() model.DatasourceCategory { return model.CategoryGraph }
func (e *NebulaExecutor) Dialect() model.DBDialect           { return "nebula" }
func (e *NebulaExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session.Release()
	e.pool.Close()
	return nil
}
func nebulaResult(rs *nebula.ResultSet) NativeQueryResult {
	rows := make([][]string, 0, min(rs.GetRowSize(), nativeMaxRows))
	for i := 0; i < rs.GetRowSize() && i < nativeMaxRows; i++ {
		r, err := rs.GetRowValuesByIndex(i)
		if err != nil {
			break
		}
		row := make([]string, len(rs.GetColNames()))
		for j := range row {
			v, err := r.GetValueByIndex(j)
			if err == nil {
				row[j] = boundedNativeString(v.String())
			}
		}
		rows = append(rows, row)
	}
	out := boundedNativeRows(rs.GetColNames(), rows)
	if rs.GetRowSize() > nativeMaxRows {
		out.Info = map[string]string{"truncated": "true"}
	}
	return out
}
func (e *NebulaExecutor) executeLocked(ctx context.Context, stmt string) (NativeQueryResult, error) {
	if err := ctx.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	rs, err := e.session.ExecuteAndCheck(stmt)
	if err != nil {
		return NativeQueryResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	return nebulaResult(rs), nil
}
func (e *NebulaExecutor) useLocked(ctx context.Context, space string) error {
	if space == "" {
		return nil
	}
	if !graphIdentifier.MatchString(space) {
		return fmt.Errorf("invalid Nebula space")
	}
	_, err := e.executeLocked(ctx, "USE "+space)
	return err
}
func (e *NebulaExecutor) Ping(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.executeLocked(ctx, "SHOW SPACES")
	return err
}

// graphd has no stable version endpoint exposed through nebula-go.
func (e *NebulaExecutor) ServerVersion(ctx context.Context) (string, error) { return "", ctx.Err() }
func nebulaCatalog(spaces []string, tags, edges map[string][]string) []NativeNamespace {
	out := make([]NativeNamespace, 0)
	for _, space := range spaces {
		if len(out) >= nativeMaxRows {
			break
		}
		out = append(out, NativeNamespace{Name: space, Kind: "space", Metadata: map[string]string{"tag_count": strconv.Itoa(len(tags[space])), "edge_type_count": strconv.Itoa(len(edges[space])), "counts": "schema entries only; graph element counts unavailable", "server_version": "graphd has no stable version endpoint in nebula-go"}})
		for _, group := range []struct {
			kind  string
			names []string
		}{{"tag", tags[space]}, {"edge_type", edges[space]}} {
			for _, name := range group.names {
				if len(out) >= nativeMaxRows {
					break
				}
				out = append(out, NativeNamespace{Name: name, Kind: group.kind, Metadata: map[string]string{"space": space}})
			}
		}
	}
	return out
}
func (e *NebulaExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.executeLocked(ctx, "SHOW SPACES")
	if err != nil {
		return NativeCatalog{}, err
	}
	spaces := firstColumn(r)
	if len(spaces) > 50 {
		spaces = spaces[:50]
	}
	tags := map[string][]string{}
	edges := map[string][]string{}
	for _, space := range spaces {
		if !graphIdentifier.MatchString(space) {
			continue
		}
		if err := e.useLocked(ctx, space); err != nil {
			continue
		}
		if r, err := e.executeLocked(ctx, "SHOW TAGS"); err == nil {
			tags[space] = firstColumn(r)
		}
		if r, err := e.executeLocked(ctx, "SHOW EDGES"); err == nil {
			edges[space] = firstColumn(r)
		}
	}
	return NativeCatalog{Category: model.CategoryGraph, Namespaces: nebulaCatalog(spaces, tags, edges)}, nil
}

var nebulaFetch = regexp.MustCompile(`^FETCH PROP ON [A-Z_][A-Z_0-9]*`)
var nebulaDeleteVertex = regexp.MustCompile(`(?i)^DELETE VERTEX (('[^']+'|"[^"]+"|[0-9]+))(\s*,\s*('[^']+'|"[^"]+"|[0-9]+))*$`)
var nebulaDeleteEdge = regexp.MustCompile(`(?i)^DELETE EDGE [A-Z_][A-Z_0-9]*\s+('[^']+'|"[^"]+"|[0-9]+)\s*->\s*('[^']+'|"[^"]+"|[0-9]+)(\s*@\s*[0-9]+)?$`)
var nebulaUpdateVertex = regexp.MustCompile(`(?i)^(UPDATE|UPSERT) VERTEX ON [A-Z_][A-Z_0-9]*\s+('[^']+'|"[^"]+"|[0-9]+)\s+SET\s+.+$`)
var nebulaUpdateEdge = regexp.MustCompile(`(?i)^(UPDATE|UPSERT) EDGE ON [A-Z_][A-Z_0-9]*\s+('[^']+'|"[^"]+"|[0-9]+)\s*->\s*('[^']+'|"[^"]+"|[0-9]+)(\s*@\s*[0-9]+)?\s+SET\s+.+$`)
var nebulaInsertID = regexp.MustCompile(`(?i)\bVALUES\s+('[^']+'|"[^"]+"|[0-9]+)\s*(:|->)`)

func nebulaStatementAllowed(command, stmt string, readOnly bool) error {
	mask, err := graphStatementMask(stmt)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(mask, command+" ") && mask != command {
		return fmt.Errorf("nGQL command mismatch")
	}
	for _, bad := range []string{" CREATE ", " DROP ", " ALTER ", " GRANT ", " REVOKE ", " SUBMIT JOB ", " STOP JOB ", " INGEST ", " SIGN IN ", " SIGN OUT ", " USE ", " PIPE ", " | "} {
		if strings.Contains(" "+mask+" ", bad) {
			return fmt.Errorf("nGQL clause is forbidden")
		}
	}
	for _, bad := range []string{" INSERT ", " UPDATE ", " UPSERT ", " DELETE "} {
		if strings.Contains(" "+mask+" ", bad) && strings.TrimSpace(bad) != command {
			return fmt.Errorf("nGQL mixed clauses are forbidden")
		}
	}
	read := false
	write := false
	switch command {
	case "SHOW":
		read = regexp.MustCompile(`^SHOW (SPACES|TAGS|EDGES|HOSTS( GRAPH)?)$`).MatchString(mask)
	case "DESCRIBE":
		read = regexp.MustCompile(`^DESCRIBE (SPACE|TAG|EDGE) [A-Z_][A-Z_0-9]*$`).MatchString(mask)
	case "MATCH", "GO", "LOOKUP", "GET":
		read = boundedGraphLimit(mask) && ((command != "GET") || strings.HasPrefix(mask, "GET SUBGRAPH ")) && ((command != "LOOKUP") || strings.HasPrefix(mask, "LOOKUP ON "))
		if command == "MATCH" && strings.Contains(mask, "*") {
			read = false
		}
		if command == "GO" {
			if m := regexp.MustCompile(`^GO ([0-9]+)( TO ([0-9]+))? STEPS `).FindStringSubmatch(mask); m != nil {
				n, _ := strconv.Atoi(m[1])
				upper := n
				if m[3] != "" {
					upper, _ = strconv.Atoi(m[3])
				}
				read = read && n >= 1 && upper <= 3 && upper >= n
			}
		}
		if command == "GET" {
			m := regexp.MustCompile(`^GET SUBGRAPH ([0-9]+) STEPS `).FindStringSubmatch(mask)
			if m == nil {
				read = false
			} else {
				n, _ := strconv.Atoi(m[1])
				read = read && n >= 1 && n <= 3
			}
		}
	case "FETCH":
		read = nebulaFetch.MatchString(mask) && strings.ContainsAny(stmt, "'\"0123456789")
	case "INSERT":
		write = regexp.MustCompile(`^INSERT (VERTEX|EDGE) [A-Z_][A-Z_0-9]*(\(| )`).MatchString(mask) && nebulaInsertID.MatchString(stmt) && strings.Count(stmt, ",") <= 200
	case "UPDATE", "UPSERT":
		write = nebulaUpdateVertex.MatchString(stmt) || nebulaUpdateEdge.MatchString(stmt)
	case "DELETE":
		write = (nebulaDeleteVertex.MatchString(stmt) || nebulaDeleteEdge.MatchString(stmt)) && strings.Count(stmt, ",") <= 99
	}
	if !read && !write {
		return fmt.Errorf("nGQL statement is not allowed or is unbounded")
	}
	if write && readOnly {
		return fmt.Errorf("nGQL write is forbidden in read-only mode")
	}
	return nil
}
func (e *NebulaExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("nebula", e.datasourceID, command, e.readOnly, err) }()
	command, _, err = normalizedNativeCommand(NativeQueryRequest{Command: request.Command})
	if err != nil {
		return result, err
	}
	if len(request.Args) != 1 {
		return result, fmt.Errorf("one nGQL statement is required")
	}
	if request.Namespace != "" && request.Namespace != e.database {
		return result, fmt.Errorf("native namespace differs from connected space")
	}
	if err = nebulaStatementAllowed(command, request.Args[0], e.readOnly); err != nil {
		return result, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.database != "" && !strings.EqualFold(request.Args[0], "SHOW SPACES") && !strings.HasPrefix(strings.ToUpper(request.Args[0]), "SHOW HOSTS") {
		if err = e.useLocked(ctx, e.database); err != nil {
			return result, err
		}
	}
	return e.executeLocked(ctx, request.Args[0])
}
