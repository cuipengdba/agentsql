package businessdb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestTimescaleOptionsAndPolicy(t *testing.T) {
	ds := model.Datasource{DBType: "timescaledb", Host: "localhost", Database: "metrics", Username: "user", TLSMode: "disable"}
	cfg, _, err := timescaleConfig(ds, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "localhost" || cfg.Port != 5432 || cfg.Database != "metrics" || cfg.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("config %+v", cfg)
	}
	if _, _, err := timescaleConfig(model.Datasource{DBType: "timescaledb", Host: "localhost", Username: "user"}, "secret", true); err == nil {
		t.Fatal("missing database accepted")
	}
	for _, tc := range []struct{ cmd, stmt string }{
		{"SELECT", "SELECT * FROM metrics LIMIT 10"}, {"SELECT", "SELECT count(*) FROM metrics"},
		{"SHOW", "SHOW server_version"}, {"EXPLAIN", "EXPLAIN SELECT * FROM metrics LIMIT 10"},
		{"WITH", "WITH recent AS (SELECT * FROM metrics LIMIT 10) SELECT * FROM recent LIMIT 10"},
	} {
		if err := timescaleStatementAllowed(tc.cmd, tc.stmt, true); err != nil {
			t.Errorf("%s: %v", tc.stmt, err)
		}
	}
	for _, tc := range []struct{ cmd, stmt string }{
		{"INSERT", "INSERT INTO metrics (ts, value) VALUES (1, 2)"},
		{"UPDATE", "UPDATE metrics SET value=2 WHERE ts=1"},
		{"DELETE", "DELETE FROM metrics WHERE ts=1"},
		{"CREATE", "CREATE INDEX idx ON metrics (ts)"},
		{"DROP", "DROP INDEX idx"},
		{"ALTER", "ALTER TABLE metrics ADD COLUMN extra TEXT"},
	} {
		if timescaleStatementAllowed(tc.cmd, tc.stmt, true) == nil {
			t.Errorf("read-only accepted %s", tc.stmt)
		}
		if err := timescaleStatementAllowed(tc.cmd, tc.stmt, false); err != nil {
			t.Errorf("write %s: %v", tc.stmt, err)
		}
	}
	for _, tc := range []struct{ cmd, stmt string }{
		{"SELECT", "SELECT * FROM metrics"}, {"DELETE", "DELETE FROM metrics"},
		{"DROP", "DROP TABLE metrics"}, {"TRUNCATE", "TRUNCATE metrics"},
		{"CREATE", "CREATE TABLE x (id int)"}, {"CREATE", "CREATE EXTENSION timescaledb"},
		{"SELECT", "SELECT * INTO x FROM metrics LIMIT 1"}, {"COPY", "COPY metrics TO '/tmp/x'"},
		{"SELECT", "SELECT * FROM metrics LIMIT 1; DROP TABLE metrics"},
	} {
		if timescaleStatementAllowed(tc.cmd, tc.stmt, false) == nil {
			t.Errorf("accepted %s", tc.stmt)
		}
	}
	if v := boundedNativeString(strings.Repeat("a", nativeMaxValueBytes+1)); len(v) != nativeMaxValueBytes {
		t.Fatal("value not bounded")
	}
}

func TestTimescaleContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"}, Env: map[string]string{"POSTGRES_PASSWORD": "secret", "POSTGRES_USER": "agentsql", "POSTGRES_DB": "metrics"}, WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{DBType: "timescaledb", Host: host, Port: port.Int(), Database: "metrics", Username: "agentsql", TLSMode: "disable", StmtTimeoutMS: 10000}
	var e *TimescaleExecutor
	for i := 0; i < 20; i++ {
		e, err = NewTimescaleExecutor(ds, "secret", false)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	setup, err := pgx.Connect(ctx, "postgres://agentsql:secret@"+host+":"+port.Port()+"/metrics?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close(context.Background())
	if _, err = setup.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, "CREATE TABLE b55_metrics (ts TIMESTAMPTZ NOT NULL, value INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, "SELECT create_hypertable('b55_metrics','ts')"); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || !strings.Contains(v, "TimescaleDB ") {
		t.Fatalf("version %s: %v", v, err)
	}
	t.Logf("timescale/timescaledb:latest-pg16 version %s", v)
	cat, err := e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range cat.Namespaces {
		if n.Kind == "hypertable" && n.Name == "b55_metrics" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hypertable absent: %+v", cat)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b55_metrics (ts, value) VALUES ('2026-10-07', 1)"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "UPDATE", Args: []string{"UPDATE b55_metrics SET value=2 WHERE value=1"}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT value FROM b55_metrics LIMIT 10"}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("select %+v %v", r, err)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"DELETE FROM b55_metrics WHERE value=2"}}); err != nil {
		t.Fatal(err)
	}
}
