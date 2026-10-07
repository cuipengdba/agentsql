package businessdb

import (
	"context"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestClickHouseOptionsAndPolicy(t *testing.T) {
	ds := model.Datasource{DBType: "clickhouse", Host: "localhost", Database: "analytics", Username: "reader", TLSMode: "strict"}
	opts, err := clickHouseOptions(ds, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Addr[0] != "localhost:9000" || opts.Auth.Database != "analytics" || opts.Auth.Username != "reader" || opts.Auth.Password != "secret" || opts.TLS == nil || opts.TLS.InsecureSkipVerify {
		t.Fatalf("options %+v", opts)
	}
	for _, tc := range []struct {
		command, statement string
		readOnly, allowed  bool
	}{
		{"SELECT", "SELECT name FROM events LIMIT 10", true, true},
		{"SELECT", "SELECT count(*) FROM events", true, true},
		{"SHOW", "SHOW DATABASES", true, true},
		{"DESCRIBE", "DESCRIBE events", true, true},
		{"EXISTS", "EXISTS TABLE events", true, true},
		{"INSERT", "INSERT INTO events (id, name) VALUES (1, 'x')", false, true},
		{"INSERT", "INSERT INTO events (id) SELECT id FROM source LIMIT 10", false, true},
		{"ALTER", "ALTER TABLE events ADD COLUMN score UInt32", false, true},
		{"ALTER", "ALTER TABLE events MODIFY COMMENT 'updated'", false, true},
		{"ALTER", "ALTER TABLE events DROP PARTITION '2026-10'", false, true},
		{"OPTIMIZE", "OPTIMIZE TABLE events PARTITION '2026-10'", false, true},
		{"INSERT", "INSERT INTO events (id) VALUES (1)", true, false},
		{"SELECT", "SELECT name FROM events", true, false},
		{"SELECT", "SELECT name FROM events LIMIT 1001", true, false},
		{"SELECT", "SELECT name FROM events LIMIT 1; DROP TABLE events", true, false},
		{"ALTER", "ALTER TABLE events UPDATE name='x' WHERE id=1", false, false},
		{"ALTER", "ALTER TABLE events DROP COLUMN id", false, false},
		{"ALTER", "ALTER TABLE events DROP PARTITION", false, false},
		{"SYSTEM", "SYSTEM STOP MERGES", false, false},
		{"CREATE", "CREATE DATABASE x", false, false},
		{"DROP", "DROP TABLE events", false, false},
		{"TRUNCATE", "TRUNCATE TABLE events", false, false},
		{"GRANT", "GRANT ALL ON *.* TO x", false, false},
	} {
		err := clickHouseStatementAllowed(tc.command, tc.statement, tc.readOnly)
		if (err == nil) != tc.allowed {
			t.Errorf("%s: %v", tc.statement, err)
		}
	}
	if _, _, err := normalizedNativeCommand(NativeQueryRequest{Command: "select", Args: []string{"SELECT 1"}}); err != nil {
		t.Fatal(err)
	}
	if len(boundedNativeRows([]string{"v"}, [][]string{{strings.Repeat("x", nativeMaxValueBytes+1)}}).Rows[0][0]) != nativeMaxValueBytes {
		t.Fatal("value not bounded")
	}
}

func TestClickHouseContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration in short mode")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9000/tcp"}, Env: map[string]string{"CLICKHOUSE_USER": "b56", "CLICKHOUSE_PASSWORD": "secret", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1"}, WaitingFor: wait.ForListeningPort("9000/tcp")}, Started: true})
	if err != nil {
		t.Skipf("ClickHouse image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{DBType: "clickhouse", Host: host, Port: port.Int(), Username: "b56"}
	e, err := NewClickHouseExecutor(ds, "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %q: %v", version, err)
	}
	t.Logf("clickhouse/clickhouse-server:24.8 version %s", version)
	if _, err := e.db.ExecContext(ctx, "CREATE TABLE b56_events (id UInt32, name String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ns := range cat.Namespaces {
		if ns.Kind == "table" && ns.Name == "b56_events" {
			found = true
		}
	}
	if !found {
		t.Fatalf("table missing: %+v", cat.Namespaces)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_events (id, name) VALUES (1, 'ok')"}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT name FROM b56_events LIMIT 1"}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("select %+v: %v", r, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "ALTER", Args: []string{"ALTER TABLE b56_events ADD COLUMN score UInt32"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DROP", Args: []string{"DROP TABLE b56_events"}}); err == nil {
		t.Fatal("DROP accepted")
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT * FROM b56_events"}}); err == nil {
		t.Fatal("unbounded SELECT accepted")
	}
	reader, err := NewClickHouseExecutor(ds, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.NativeQuery(context.Background(), NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_events (id) VALUES (2)"}}); err == nil {
		t.Fatal("read-only write accepted")
	}
}
