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

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type Neo4jExecutor struct {
	driver                 neo4j.DriverWithContext
	database, datasourceID string
	readOnly               bool
}

type neo4jConnection struct {
	uri     string
	auth    neo4j.AuthToken
	config  func(*neo4j.Config)
	timeout time.Duration
}

func neo4jOptions(ds model.Datasource, password string) (neo4jConnection, error) {
	if ds.DBType != "neo4j" {
		return neo4jConnection{}, fmt.Errorf("invalid Neo4j dialect")
	}
	if strings.TrimSpace(ds.Host) == "" {
		return neo4jConnection{}, fmt.Errorf("neo4j host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 7687
	}
	if port < 1 || port > 65535 {
		return neo4jConnection{}, fmt.Errorf("neo4j port is invalid")
	}
	if ds.Username == "" && password != "" {
		return neo4jConnection{}, fmt.Errorf("neo4j password requires username")
	}
	if ds.Database != "" && !graphIdentifier.MatchString(ds.Database) {
		return neo4jConnection{}, fmt.Errorf("invalid Neo4j database")
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return neo4jConnection{}, err
	}
	limit, err := normalizedConnectionLimit(ds.ConnLimit)
	if err != nil {
		return neo4jConnection{}, err
	}
	if ds.TrustServerCertificate {
		return neo4jConnection{}, fmt.Errorf("unverified Neo4j TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" && ds.TLSMode != "require" {
		return neo4jConnection{}, fmt.Errorf("invalid Neo4j TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return neo4jConnection{}, fmt.Errorf("Neo4j TLS settings require tls_mode")
	}
	if ds.TLSServerName != "" && !strings.EqualFold(ds.TLSServerName, ds.Host) {
		return neo4jConnection{}, fmt.Errorf("Neo4j driver cannot override TLS server name independently of URI host")
	}
	scheme := "bolt"
	if ds.TLSMode != "" {
		scheme = "bolt+s"
	}
	var roots *x509.CertPool
	if ds.TLSCAFile != "" {
		pem, err := os.ReadFile(ds.TLSCAFile)
		if err != nil {
			return neo4jConnection{}, err
		}
		roots, err = x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return neo4jConnection{}, fmt.Errorf("invalid Neo4j TLS CA")
		}
	}
	auth := neo4j.NoAuth()
	if ds.Username != "" {
		auth = neo4j.BasicAuth(ds.Username, password, "")
	}
	return neo4jConnection{uri: scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(port)), auth: auth, timeout: time.Duration(ms) * time.Millisecond, config: func(c *neo4j.Config) {
		c.MaxConnectionPoolSize = limit
		c.SocketConnectTimeout = time.Duration(ms) * time.Millisecond
		if ds.TLSMode != "" {
			c.TlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		}
	}}, nil
}

func NewNeo4jExecutor(ds model.Datasource, password string, readOnly bool) (*Neo4jExecutor, error) {
	opts, err := neo4jOptions(ds, password)
	if err != nil {
		return nil, err
	}
	driver, err := neo4j.NewDriverWithContext(opts.uri, opts.auth, opts.config)
	if err != nil {
		return nil, err
	}
	e := &Neo4jExecutor{driver: driver, database: ds.Database, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *Neo4jExecutor) Category() model.DatasourceCategory { return model.CategoryGraph }
func (e *Neo4jExecutor) Dialect() model.DBDialect           { return "neo4j" }
func (e *Neo4jExecutor) Close() error                       { return e.driver.Close(context.Background()) }
func (e *Neo4jExecutor) run(ctx context.Context, database, stmt string, mode neo4j.AccessMode) (NativeQueryResult, error) {
	s := e.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: database, AccessMode: mode})
	defer s.Close(ctx)
	r, err := s.Run(ctx, stmt, nil)
	if err != nil {
		return NativeQueryResult{}, err
	}
	rows := make([][]string, 0)
	for r.Next(ctx) {
		if len(rows) >= nativeMaxRows {
			break
		}
		row := make([]string, len(r.Record().Values))
		for i, v := range r.Record().Values {
			row[i] = boundedNativeString(fmt.Sprint(v))
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	keys, err := r.Keys()
	if err != nil {
		return NativeQueryResult{}, err
	}
	if _, err := r.Consume(ctx); err != nil {
		return NativeQueryResult{}, err
	}
	return boundedNativeRows(keys, rows), nil
}
func (e *Neo4jExecutor) Ping(ctx context.Context) error {
	_, err := e.run(ctx, e.database, "RETURN 1", neo4j.AccessModeRead)
	return err
}
func (e *Neo4jExecutor) ServerVersion(ctx context.Context) (string, error) {
	info, err := e.driver.GetServerInfo(ctx)
	if err != nil {
		return "", err
	}
	return boundedNativeString(info.Agent()), nil
}
func neo4jCatalog(databases []string, labels, rels, indexes map[string][]string) []NativeNamespace {
	out := make([]NativeNamespace, 0)
	for _, db := range databases {
		if len(out) >= nativeMaxRows {
			break
		}
		out = append(out, NativeNamespace{Name: db, Kind: "database", Metadata: map[string]string{"label_count": strconv.Itoa(len(labels[db])), "relationship_type_count": strconv.Itoa(len(rels[db])), "index_count": strconv.Itoa(len(indexes[db])), "counts": "schema entries only; graph element counts unavailable"}})
		for _, group := range []struct {
			kind   string
			values []string
		}{{"label", labels[db]}, {"relationship_type", rels[db]}, {"index", indexes[db]}} {
			for _, name := range group.values {
				if len(out) >= nativeMaxRows {
					break
				}
				out = append(out, NativeNamespace{Name: name, Kind: group.kind, Metadata: map[string]string{"database": db}})
			}
		}
	}
	return out
}
func firstColumn(r NativeQueryResult) []string {
	out := make([]string, 0, len(r.Rows))
	for _, row := range r.Rows {
		if len(row) > 0 {
			out = append(out, row[0])
		}
	}
	return out
}
func (e *Neo4jExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, _ := e.ServerVersion(ctx)
	dbsResult, err := e.run(ctx, "system", "SHOW DATABASES YIELD name", neo4j.AccessModeRead)
	dbs := firstColumn(dbsResult)
	if err != nil || len(dbs) == 0 {
		if e.database != "" {
			dbs = []string{e.database}
		} else {
			dbs = []string{"neo4j"}
		}
	}
	if len(dbs) > 50 {
		dbs = dbs[:50]
	}
	labels := map[string][]string{}
	rels := map[string][]string{}
	indexes := map[string][]string{}
	for _, db := range dbs {
		if r, err := e.run(ctx, db, "SHOW LABELS", neo4j.AccessModeRead); err == nil {
			labels[db] = firstColumn(r)
		}
		if r, err := e.run(ctx, db, "SHOW RELATIONSHIP TYPES", neo4j.AccessModeRead); err == nil {
			rels[db] = firstColumn(r)
		}
		if r, err := e.run(ctx, db, "SHOW INDEXES YIELD name", neo4j.AccessModeRead); err == nil {
			indexes[db] = firstColumn(r)
		}
	}
	return NativeCatalog{Category: model.CategoryGraph, ServerVersion: v, Namespaces: neo4jCatalog(dbs, labels, rels, indexes)}, nil
}

var graphIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*$`)
var graphLimit = regexp.MustCompile(`(?i)\bLIMIT\s+([0-9]+)\s*$`)

// graphStatementMask rejects multi-statements/comments and blanks quoted data
// before keyword checks. It deliberately does not attempt a full graph grammar.
func graphStatementMask(stmt string) (string, error) {
	if len(stmt) == 0 || len(stmt) > nativeMaxValueBytes {
		return "", fmt.Errorf("graph statement is empty or exceeds size limit")
	}
	if strings.Contains(stmt, "--") || strings.Contains(stmt, "#") {
		return "", fmt.Errorf("graph comments are forbidden")
	}
	var b strings.Builder
	quote := byte(0)
	escaped := false
	for i := 0; i < len(stmt); i++ {
		c := stmt[i]
		if c == '\n' || c == '\r' || c == ';' {
			return "", fmt.Errorf("multi-statement graph script is forbidden")
		}
		if quote != 0 {
			b.WriteByte(' ')
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			b.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(stmt) && (stmt[i+1] == '/' || stmt[i+1] == '*') {
			return "", fmt.Errorf("graph comments are forbidden")
		}
		b.WriteByte(c)
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated graph string")
	}
	return strings.ToUpper(strings.Join(strings.Fields(b.String()), " ")), nil
}
func boundedGraphLimit(mask string) bool {
	m := graphLimit.FindStringSubmatch(mask)
	if m == nil {
		return false
	}
	n, e := strconv.Atoi(m[1])
	return e == nil && n >= 1 && n <= 100
}

var neo4jSetByID = regexp.MustCompile(`(?i)^MATCH \(n\) WHERE elementId\(n\)\s*=\s*'[^']+' SET n\.[a-z_][a-z_0-9]*\s*=\s*('[^']*'|[0-9]+|true|false)( RETURN n LIMIT ([0-9]+))?$`)
var neo4jRemoveByID = regexp.MustCompile(`(?i)^MATCH \(n\) WHERE elementId\(n\)\s*=\s*'[^']+' REMOVE n\.[a-z_][a-z_0-9]*( RETURN n LIMIT ([0-9]+))?$`)
var neo4jDeleteByID = regexp.MustCompile(`(?i)^MATCH \(n\) WHERE elementId\(n\)\s*=\s*'[^']+' DELETE n$`)

func neo4jTargetWrite(command, stmt string) bool {
	stmt = strings.TrimSpace(stmt)
	var m []string
	switch command {
	case "SET":
		m = neo4jSetByID.FindStringSubmatch(stmt)
	case "REMOVE":
		m = neo4jRemoveByID.FindStringSubmatch(stmt)
	case "DELETE":
		return neo4jDeleteByID.MatchString(stmt)
	}
	if m == nil {
		return false
	}
	if m[len(m)-1] == "" {
		return true
	}
	n, err := strconv.Atoi(m[len(m)-1])
	return err == nil && n >= 1 && n <= 100
}
func neo4jStatementAllowed(command, stmt string, readOnly bool) error {
	mask, err := graphStatementMask(stmt)
	if err != nil {
		return err
	}
	targetWrite := command == "SET" || command == "REMOVE" || command == "DELETE"
	if !strings.HasPrefix(mask, command+" ") && mask != command && !(targetWrite && strings.HasPrefix(mask, "MATCH (")) {
		return fmt.Errorf("Cypher command mismatch")
	}
	for _, bad := range []string{" DBMS.", " APOC.", " GRANT ", " REVOKE ", " LOAD CSV ", " FOREACH ", " CREATE DATABASE ", " DROP DATABASE ", " START DATABASE ", " STOP DATABASE ", " ALTER ", " UNION ", " USE ", " WITH "} {
		if strings.Contains(" "+mask+" ", bad) {
			return fmt.Errorf("Cypher clause is not allowed")
		}
	}
	if command != "CALL" && strings.Contains(" "+mask+" ", " CALL ") {
		return fmt.Errorf("Cypher procedure is not allowed")
	}
	for _, bad := range []string{" DETACH ", " DELETE ", " REMOVE ", " SET ", " MERGE "} {
		if strings.Contains(" "+mask+" ", bad) && !(bad == " MERGE " && command == "MERGE") && !(targetWrite && strings.TrimSpace(bad) == command) {
			return fmt.Errorf("Cypher clause is not allowed")
		}
	}
	read := false
	write := false
	switch command {
	case "MATCH":
		read = boundedGraphLimit(mask) && strings.Contains(mask, " RETURN ") && !strings.Contains(mask, " CREATE ") && !strings.Contains(mask, " SET ") && !strings.Contains(mask, " DELETE ") && !strings.Contains(mask, " MERGE ")
	case "RETURN":
		read = regexp.MustCompile(`^RETURN ([0-9]+|[A-Z_][A-Z_0-9]*)( AS [A-Z_][A-Z_0-9]*)?$`).MatchString(mask)
	case "UNWIND":
		read = boundedGraphLimit(mask) && strings.Contains(mask, " RETURN ") && !strings.Contains(mask, " CREATE ") && !strings.Contains(mask, " SET ")
	case "SHOW":
		read = regexp.MustCompile(`^SHOW (DATABASES|LABELS|RELATIONSHIP TYPES|INDEXES)( YIELD NAME)?$`).MatchString(mask)
	case "CALL":
		read = regexp.MustCompile(`^CALL DB\.(LABELS|RELATIONSHIPTYPES|INDEXES)\(\)$`).MatchString(mask)
	case "CREATE":
		write = regexp.MustCompile(`^CREATE (INDEX|CONSTRAINT)\b`).MatchString(mask) || (strings.HasPrefix(mask, "CREATE (") && strings.Count(mask, "(") == 1 && !strings.Contains(mask, " MATCH ") && !strings.Contains(mask, " UNWIND "))
	case "MERGE":
		write = strings.HasPrefix(mask, "MERGE (") && strings.Count(mask, "(") == 1 && strings.Contains(stmt, "{") && !strings.Contains(mask, " MATCH ") && !strings.Contains(mask, " UNWIND ")
	case "DROP":
		write = regexp.MustCompile(`^DROP (INDEX|CONSTRAINT)\b`).MatchString(mask)
	case "SET", "REMOVE", "DELETE":
		write = neo4jTargetWrite(command, stmt)
	}
	if !read && !write {
		return fmt.Errorf("Cypher statement is not allowed or is unbounded")
	}
	if write && readOnly {
		return fmt.Errorf("Cypher write is forbidden in read-only mode")
	}
	return nil
}
func (e *Neo4jExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("neo4j", e.datasourceID, command, e.readOnly, err) }()
	command, _, err = normalizedNativeCommand(NativeQueryRequest{Command: request.Command})
	if err != nil {
		return result, err
	}
	if len(request.Args) != 1 {
		return result, fmt.Errorf("one Cypher statement is required")
	}
	if request.Namespace != "" && request.Namespace != e.database {
		return result, fmt.Errorf("native namespace differs from connected database")
	}
	if err = neo4jStatementAllowed(command, request.Args[0], e.readOnly); err != nil {
		return result, err
	}
	mode := neo4j.AccessModeRead
	if command == "CREATE" || command == "MERGE" || command == "DROP" || command == "SET" || command == "REMOVE" || command == "DELETE" {
		mode = neo4j.AccessModeWrite
	}
	return e.run(ctx, e.database, request.Args[0], mode)
}
