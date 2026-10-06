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
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/gocql/gocql"
)

type CassandraExecutor struct {
	session                *gocql.Session
	dialect                model.DBDialect
	datasourceID, keyspace string
	readOnly               bool
}

func cassandraCluster(ds model.Datasource, password string) (*gocql.ClusterConfig, error) {
	if ds.DBType != "cassandra" && ds.DBType != "scylla" {
		return nil, fmt.Errorf("invalid Cassandra dialect")
	}
	if strings.TrimSpace(ds.Host) == "" {
		return nil, fmt.Errorf("cassandra host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 9042
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("cassandra port is invalid")
	}
	if ds.Username == "" && password != "" {
		return nil, fmt.Errorf("cassandra password requires username")
	}
	if ds.Database != "" && !regexp.MustCompile(`^[a-zA-Z_][a-zA-Z_0-9]*$`).MatchString(ds.Database) {
		return nil, fmt.Errorf("invalid Cassandra keyspace")
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	limit, err := normalizedConnectionLimit(ds.ConnLimit)
	if err != nil {
		return nil, err
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified Cassandra TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return nil, fmt.Errorf("invalid Cassandra TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return nil, fmt.Errorf("Cassandra TLS settings require tls_mode")
	}
	cluster := gocql.NewCluster(ds.Host)
	cluster.Port = port
	cluster.Keyspace = ds.Database
	if cluster.Keyspace == "" {
		cluster.Keyspace = "system"
	}
	cluster.Timeout = time.Duration(timeout) * time.Millisecond
	cluster.ConnectTimeout = cluster.Timeout
	cluster.NumConns = limit
	cluster.Consistency = gocql.One
	if ds.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{Username: ds.Username, Password: password}
	}
	if ds.TLSMode != "" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, err := os.ReadFile(ds.TLSCAFile)
			if err != nil {
				return nil, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid Cassandra TLS CA")
			}
			cfg.RootCAs = roots
		}
		cluster.SslOpts = &gocql.SslOptions{Config: cfg, EnableHostVerification: true}
	}
	return cluster, nil
}

func NewCassandraExecutor(ds model.Datasource, password string, readOnly bool) (*CassandraExecutor, error) {
	cluster, err := cassandraCluster(ds, password)
	if err != nil {
		return nil, err
	}
	session, err := cluster.CreateSession()
	if err != nil {
		return nil, err
	}
	e := &CassandraExecutor{session: session, dialect: model.DBDialect(ds.DBType), datasourceID: ds.ID, keyspace: cluster.Keyspace, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), cluster.Timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		session.Close()
		return nil, err
	}
	return e, nil
}

func (e *CassandraExecutor) Category() model.DatasourceCategory { return model.CategoryWideColumn }
func (e *CassandraExecutor) Dialect() model.DBDialect           { return e.dialect }
func (e *CassandraExecutor) Close() error                       { e.session.Close(); return nil }
func (e *CassandraExecutor) Ping(ctx context.Context) error {
	var version string
	return e.session.Query("SELECT release_version FROM system.local").WithContext(ctx).Scan(&version)
}
func (e *CassandraExecutor) ServerVersion(ctx context.Context) (string, error) {
	var version string
	err := e.session.Query("SELECT release_version FROM system.local").WithContext(ctx).Scan(&version)
	return boundedNativeString(version), err
}

type cassandraKeyspace struct {
	name    string
	durable bool
}
type cassandraTable struct{ keyspace, name string }

func cassandraNamespaces(keys []cassandraKeyspace, tables []cassandraTable) []NativeNamespace {
	out := make([]NativeNamespace, 0, min(nativeMaxRows, len(keys)+len(tables)))
	for _, k := range keys {
		if len(out) >= nativeMaxRows {
			break
		}
		out = append(out, NativeNamespace{Name: k.name, Kind: "keyspace", Metadata: map[string]string{"durable_writes": strconv.FormatBool(k.durable)}})
	}
	for _, t := range tables {
		if len(out) >= nativeMaxRows {
			break
		}
		out = append(out, NativeNamespace{Name: t.name, Kind: "table", Metadata: map[string]string{"keyspace": t.keyspace}})
	}
	return out // ItemCount is 0: COUNT(*) can scan an unbounded distributed table.
}
func (e *CassandraExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	var keys []cassandraKeyspace
	iter := e.session.Query("SELECT keyspace_name, durable_writes FROM system_schema.keyspaces").WithContext(ctx).Iter()
	var name string
	var durable bool
	for iter.Scan(&name, &durable) {
		if len(keys) < nativeMaxRows {
			keys = append(keys, cassandraKeyspace{name, durable})
		}
	}
	if err := iter.Close(); err != nil {
		return NativeCatalog{}, err
	}
	var tables []cassandraTable
	for _, key := range keys {
		iter = e.session.Query("SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?", key.name).WithContext(ctx).Iter()
		for iter.Scan(&name) {
			if len(tables) < nativeMaxRows {
				tables = append(tables, cassandraTable{key.name, name})
			}
		}
		if err := iter.Close(); err != nil {
			return NativeCatalog{}, err
		}
	}
	return NativeCatalog{Category: model.CategoryWideColumn, ServerVersion: version, Namespaces: cassandraNamespaces(keys, tables)}, nil
}

var cqlIdent = `[a-zA-Z_][a-zA-Z_0-9]*`
var cqlSelect = regexp.MustCompile(`(?i)^SELECT (\*|[a-zA-Z_][a-zA-Z_0-9]*(?:\s*,\s*[a-zA-Z_][a-zA-Z_0-9]*)*) FROM (` + cqlIdent + `) WHERE (` + cqlIdent + `)\s*=\s*\? LIMIT ([0-9]+)$`)
var cqlInsert = regexp.MustCompile(`(?i)^INSERT INTO (` + cqlIdent + `) \((` + cqlIdent + `(?:\s*,\s*` + cqlIdent + `)*)\) VALUES \((\?(?:\s*,\s*\?)*)\)$`)
var cqlUpdate = regexp.MustCompile(`(?i)^UPDATE (` + cqlIdent + `) SET (` + cqlIdent + `)\s*=\s*\? WHERE (` + cqlIdent + `)\s*=\s*\?$`)
var cqlDelete = regexp.MustCompile(`(?i)^DELETE FROM (` + cqlIdent + `) WHERE (` + cqlIdent + `)\s*=\s*\?$`)

func cassandraCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "SELECT", "LIST TABLES", "LIST KEYSPACES", "DESCRIBE":
		return nil
	case "INSERT", "UPDATE", "DELETE":
		if readOnly {
			return fmt.Errorf("native write command %s is forbidden in read-only mode", command)
		}
		return nil
	}
	return fmt.Errorf("native command %s is not allowed", command)
}
func cassandraValidate(command, stmt string, count int) (string, string, int, error) {
	stmt = strings.TrimSpace(stmt)
	var table, key string
	limit := 0
	switch command {
	case "SELECT":
		m := cqlSelect.FindStringSubmatch(stmt)
		if m == nil || count != 1 {
			return "", "", 0, fmt.Errorf("SELECT requires a simple partition-key equality and LIMIT")
		}
		table, key = m[2], m[3]
		limit, _ = strconv.Atoi(m[4])
		if limit < 1 || limit > nativeMaxRows {
			return "", "", 0, fmt.Errorf("SELECT LIMIT must be 1 to %d", nativeMaxRows)
		}
	case "INSERT":
		m := cqlInsert.FindStringSubmatch(stmt)
		if m == nil {
			return "", "", 0, fmt.Errorf("INSERT must bind a single row")
		}
		table = m[1]
		if count != strings.Count(m[3], "?") || count != len(strings.Split(m[2], ",")) {
			return "", "", 0, fmt.Errorf("INSERT binding count mismatch")
		}
	case "UPDATE":
		m := cqlUpdate.FindStringSubmatch(stmt)
		if m == nil || count != 2 {
			return "", "", 0, fmt.Errorf("UPDATE requires bound values and row key")
		}
		table, key = m[1], m[3]
	case "DELETE":
		m := cqlDelete.FindStringSubmatch(stmt)
		if m == nil || count != 1 {
			return "", "", 0, fmt.Errorf("DELETE requires bound row key")
		}
		table, key = m[1], m[2]
	default:
		return "", "", 0, fmt.Errorf("unsupported CQL statement")
	}
	return table, key, limit, nil
}
func (e *CassandraExecutor) partitionKey(ctx context.Context, table string) (string, error) {
	var key string
	iter := e.session.Query("SELECT column_name, kind FROM system_schema.columns WHERE keyspace_name = ? AND table_name = ?", e.keyspace, table).WithContext(ctx).Iter()
	var name, kind string
	count := 0
	for iter.Scan(&name, &kind) {
		if kind == "partition_key" {
			key = name
			count++
		}
	}
	if err := iter.Close(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", fmt.Errorf("only single-column partition keys are supported")
	}
	return key, nil
}
func cassandraRows(iter *gocql.Iter) (NativeQueryResult, error) {
	columns := iter.Columns()
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	rows := make([][]string, 0)
	values := map[string]interface{}{}
	for len(rows) < nativeMaxRows && iter.MapScan(values) {
		row := make([]string, len(names))
		for i, name := range names {
			row[i] = boundedNativeString(fmt.Sprint(values[name]))
		}
		rows = append(rows, row)
		values = map[string]interface{}{}
	}
	err := iter.Close()
	return boundedNativeRows(names, rows), err
}
func (e *CassandraExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.dialect, e.datasourceID, command, e.readOnly, err) }()
	var args []string
	command, args, err = normalizedNativeCommand(request)
	if err != nil {
		return result, err
	}
	if err = cassandraCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if request.Namespace != "" && request.Namespace != e.keyspace {
		return result, fmt.Errorf("native namespace differs from connected keyspace")
	}
	if command == "LIST KEYSPACES" {
		if len(args) != 0 {
			return result, fmt.Errorf("LIST KEYSPACES takes no arguments")
		}
		return cassandraRows(e.session.Query("SELECT keyspace_name, durable_writes FROM system_schema.keyspaces").WithContext(ctx).Iter())
	}
	if command == "LIST TABLES" {
		if len(args) != 0 {
			return result, fmt.Errorf("LIST TABLES takes no arguments")
		}
		return cassandraRows(e.session.Query("SELECT table_name FROM system_schema.tables WHERE keyspace_name = ?", e.keyspace).WithContext(ctx).Iter())
	}
	if command == "DESCRIBE" {
		return result, fmt.Errorf("DESCRIBE is unavailable through the CQL binary protocol")
	}
	if len(args) < 1 {
		return result, fmt.Errorf("CQL statement is required")
	}
	stmt := strings.TrimSpace(args[0])
	table, key, _, err := cassandraValidate(command, stmt, len(args)-1)
	if err != nil {
		return result, err
	}
	if !strings.EqualFold(strings.Fields(stmt)[0], command) {
		return result, fmt.Errorf("CQL command mismatch")
	}
	if key != "" {
		var pk string
		pk, err = e.partitionKey(ctx, table)
		if err != nil {
			return result, err
		}
		if !strings.EqualFold(key, pk) {
			return result, fmt.Errorf("partition-key equality is required")
		}
	}
	bindings := make([]interface{}, len(args)-1)
	for i, value := range args[1:] {
		bindings[i] = value
	}
	q := e.session.Query(stmt, bindings...).WithContext(ctx)
	if command == "SELECT" {
		return cassandraRows(q.Iter())
	}
	err = q.Exec()
	if err != nil {
		return result, err
	}
	return NativeQueryResult{Raw: "OK"}, nil
}
