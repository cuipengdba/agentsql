package businessdb

import (
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestStarRocksConfigAndPolicy(t *testing.T) {
	ds := model.Datasource{DBType: "starrocks", Host: "localhost", Database: "analytics", Username: "root"}
	cfg, err := olapMySQLConfig(ds, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "localhost:9030" || cfg.DBName != "analytics" || cfg.MultiStatements || !strings.Contains(olapMySQLDSN(cfg), "multiStatements=false") || strings.Contains(strings.ToLower(olapMySQLDSN(cfg)), "multistatements=true") {
		t.Fatalf("config %+v", cfg)
	}
	for _, tc := range []struct {
		command, statement string
		readOnly, allowed  bool
	}{
		{"SELECT", "SELECT id FROM events LIMIT 1", true, true},
		{"SHOW", "SHOW TABLES", true, true},
		{"INSERT", "INSERT INTO events (id) VALUES (1)", false, true},
		{"UPDATE", "UPDATE events SET id=2 WHERE id=1", false, true},
		{"DELETE", "DELETE FROM events WHERE id=1", false, true},
		{"SELECT", "SELECT id FROM events", true, false},
		{"UPDATE", "UPDATE events SET id=2", false, false},
		{"DELETE", "DELETE FROM events", false, false},
		{"INSERT", "INSERT INTO events (id) VALUES (1)", true, false},
		{"SET", "SET GLOBAL x=1", false, false},
		{"SELECT", "SELECT id FROM events LIMIT 1; SELECT 2", true, false},
	} {
		err := olapMySQLStatementAllowed(tc.command, tc.statement, tc.readOnly)
		if (err == nil) != tc.allowed {
			t.Errorf("%s: %v", tc.statement, err)
		}
	}
}

func TestStarRocksContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration in short mode")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "starrocks/allin1-ubuntu:3.5.21", ExposedPorts: []string{"9030/tcp"}, WaitingFor: wait.ForListeningPort("9030/tcp")}, Started: true})
	if err != nil {
		t.Skipf("StarRocks image unavailable: %v", err)
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
	ds := model.Datasource{DBType: "starrocks", Host: host, Port: port.Int(), Username: "root"}
	e, err := NewStarRocksExecutor(ds, "", false)
	if err != nil {
		t.Skipf("StarRocks FE unavailable after container start: %v", err)
	}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := e.ServerVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("starrocks/allin1-ubuntu:3.5.21 version %q", version)
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("catalog %+v: %v", cat, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SHOW", Args: []string{"SHOW DATABASES"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, "CREATE DATABASE b56_starrocks"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		_, err = e.db.ExecContext(ctx, `CREATE TABLE b56_starrocks.events (id INT NOT NULL, name VARCHAR(32)) PRIMARY KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ("replication_num"="1")`)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "Cluster has no available capacity") {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Skipf("StarRocks BE unavailable after FE start: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Skipf("StarRocks BE startup timed out: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	cat, err = e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ns := range cat.Namespaces {
		if ns.Kind == "table" && ns.Name == "events" && ns.Metadata["database"] == "b56_starrocks" {
			found = true
		}
	}
	if !found {
		t.Fatalf("table missing: %+v", cat.Namespaces)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_starrocks.events (id, name) VALUES (1, 'ok')"}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT name FROM b56_starrocks.events LIMIT 1"}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("select %+v: %v", r, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "UPDATE", Args: []string{"UPDATE b56_starrocks.events SET name='changed' WHERE id=1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"DELETE FROM b56_starrocks.events WHERE id=1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "UPDATE", Args: []string{"UPDATE events SET id=2"}}); err == nil {
		t.Fatal("unbounded UPDATE accepted")
	}
	reader, err := NewStarRocksExecutor(ds, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO b56_starrocks.events (id) VALUES (2)"}}); err == nil {
		t.Fatal("read-only INSERT accepted")
	}
}
