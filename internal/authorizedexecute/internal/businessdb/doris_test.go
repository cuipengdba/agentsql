package businessdb

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestDorisConfigAndPolicy(t *testing.T) {
	if olapReportedVersion("5.7.99") != "" || olapReportedVersion("4.1.3") != "4.1.3" {
		t.Fatal("compatibility version was reported as FE version")
	}
	ds := model.Datasource{DBType: "doris", Host: "localhost", Database: "analytics", Username: "root", TLSMode: "strict"}
	cfg, err := olapMySQLConfig(ds, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "localhost:9030" || cfg.DBName != "analytics" || cfg.User != "root" || cfg.Passwd != "secret" || cfg.MultiStatements || cfg.TLS == nil || cfg.TLS.InsecureSkipVerify {
		t.Fatalf("config %+v", cfg)
	}
	for _, tc := range []struct {
		command, statement string
		readOnly, allowed  bool
	}{
		{"SELECT", "SELECT name FROM events LIMIT 10", true, true},
		{"SELECT", "SELECT count(*) FROM events", true, true},
		{"SHOW", "SHOW DATABASES", true, true},
		{"DESCRIBE", "DESCRIBE events", true, true},
		{"EXPLAIN", "EXPLAIN SELECT name FROM events LIMIT 1", true, true},
		{"INSERT", "INSERT INTO events (id, name) VALUES (1, 'x')", false, true},
		{"INSERT", "INSERT INTO events (id) SELECT id FROM source LIMIT 10", false, true},
		{"UPDATE", "UPDATE events SET name='x' WHERE id=1", false, true},
		{"DELETE", "DELETE FROM events WHERE id=1", false, true},
		{"ALTER", "ALTER TABLE events ADD COLUMN score INT", false, true},
		{"INSERT", "INSERT INTO events (id) VALUES (1)", true, false},
		{"SELECT", "SELECT name FROM events", true, false},
		{"SELECT", "SELECT name FROM events LIMIT 1001", true, false},
		{"UPDATE", "UPDATE events SET name='x'", false, false},
		{"DELETE", "DELETE FROM events", false, false},
		{"ALTER", "ALTER TABLE events DROP COLUMN id", false, false},
		{"SELECT", "SELECT name FROM events LIMIT 1; DROP TABLE events", true, false},
		{"DROP", "DROP TABLE events", false, false},
		{"CREATE", "CREATE DATABASE x", false, false},
		{"TRUNCATE", "TRUNCATE TABLE events", false, false},
		{"SET", "SET GLOBAL x=1", false, false},
		{"GRANT", "GRANT ALL ON *.* TO x", false, false},
	} {
		err := olapMySQLStatementAllowed(tc.command, tc.statement, tc.readOnly)
		if (err == nil) != tc.allowed {
			t.Errorf("%s: %v", tc.statement, err)
		}
	}
	if !strings.Contains(olapMySQLDSN(cfg), "multiStatements=false") || strings.Contains(strings.ToLower(olapMySQLDSN(cfg)), "multistatements=true") {
		t.Fatal("multiStatements enabled")
	}
}

func TestDorisContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration in short mode")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "apache/doris:all-in-one-4.1.3", ExposedPorts: []string{"9030/tcp"}, WaitingFor: wait.ForHealthCheck()}, Started: true})
	if err != nil {
		t.Skipf("Doris image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9030/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{DBType: "doris", Host: host, Port: port.Int(), Username: "root"}
	e, err := NewDorisExecutor(ds, "", false)
	if err != nil {
		t.Skipf("Doris FE unavailable after container start: %v", err)
	}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := e.ServerVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("apache/doris:all-in-one-4.1.3 version %q", version)
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("catalog %+v: %v", cat, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SHOW", Args: []string{"SHOW DATABASES"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, "CREATE DATABASE b56_doris"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, `CREATE TABLE b56_doris.events (id INT NOT NULL, name VARCHAR(32)) UNIQUE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num' = '1')`); err != nil {
		t.Fatal(err)
	}
	cat, err = e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ns := range cat.Namespaces {
		if ns.Kind == "table" && ns.Name == "events" && ns.Metadata["database"] == "b56_doris" {
			found = true
		}
	}
	if !found {
		t.Fatalf("table missing: %+v", cat.Namespaces)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_doris.events (id, name) VALUES (1, 'ok')"}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT name FROM b56_doris.events LIMIT 1"}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("select %+v: %v", r, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "UPDATE", Args: []string{"UPDATE b56_doris.events SET name='changed' WHERE id=1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"DELETE FROM b56_doris.events WHERE id=1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"DELETE FROM events"}}); err == nil {
		t.Fatal("unbounded DELETE accepted")
	}
	reader, err := NewDorisExecutor(ds, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_doris.events (id) VALUES (2)"}}); err == nil {
		t.Fatal("read-only INSERT accepted")
	}
}
