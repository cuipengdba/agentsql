package businessdb

import (
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestCassandraClusterAndPolicy(t *testing.T) {
	for _, dialect := range []string{"cassandra", "scylla"} {
		cfg, err := cassandraCluster(model.Datasource{DBType: dialect, Host: "localhost", Username: "u", TLSMode: "verify-full", TLSServerName: "db.example"}, "p")
		if err != nil || cfg.Port != 9042 || cfg.Keyspace != "system" || cfg.SslOpts == nil || cfg.SslOpts.Config.ServerName != "db.example" || cfg.Authenticator == nil {
			t.Fatalf("%s config: %+v %v", dialect, cfg, err)
		}
	}
	for _, ds := range []model.Datasource{{DBType: "cassandra", Host: "h", TrustServerCertificate: true}, {DBType: "cassandra", Host: "h", TLSMode: "bogus"}, {DBType: "cassandra", Host: "h", Port: -1}} {
		if _, err := cassandraCluster(ds, ""); err == nil {
			t.Errorf("unsafe config accepted: %+v", ds)
		}
	}
	if _, err := cassandraCluster(model.Datasource{DBType: "cassandra", Host: "h", Database: "system;DROP"}, ""); err == nil {
		t.Fatal("unsafe keyspace accepted")
	}
	for _, command := range []string{"SELECT", "LIST TABLES", "LIST KEYSPACES"} {
		if err := cassandraCommandAllowed(command, true); err != nil {
			t.Error(err)
		}
	}
	for _, command := range []string{"INSERT", "UPDATE", "DELETE"} {
		if cassandraCommandAllowed(command, true) == nil || cassandraCommandAllowed(command, false) != nil {
			t.Errorf("write policy: %s", command)
		}
	}
	for _, command := range []string{"CREATE", "DROP", "ALTER", "TRUNCATE", "BATCH", "USE", "GRANT"} {
		if cassandraCommandAllowed(command, false) == nil {
			t.Errorf("dangerous command accepted: %s", command)
		}
	}
	command, _, err := normalizedNativeCommand(NativeQueryRequest{Command: " list tables "})
	if err != nil || command != "LIST TABLES" {
		t.Fatalf("normalization: %q %v", command, err)
	}
}

func TestCassandraAndScyllaContainers(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	for _, tc := range []struct{ name, image string }{{"cassandra", "cassandra:4.1"}, {"scylla", "scylladb/scylla:5"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := dockerTestContext(t)
			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: tc.image, ExposedPorts: []string{"9042/tcp"}, WaitingFor: wait.ForListeningPort("9042/tcp")}, Started: true})
			if err != nil {
				t.Skipf("container image unavailable: %v", err)
			}
			testcontainers.CleanupContainer(t, container)
			host, err := container.Host(ctx)
			if err != nil {
				t.Fatal(err)
			}
			port, err := container.MappedPort(ctx, "9042/tcp")
			if err != nil {
				t.Fatal(err)
			}
			ds := model.Datasource{ID: tc.name, DBType: tc.name, Host: host, Port: port.Int(), StmtTimeoutMS: 20000}
			var writer *CassandraExecutor
			for attempt := 0; attempt < 20; attempt++ {
				writer, err = NewCassandraExecutor(ds, "", false)
				if err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(3 * time.Second):
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if writer != nil {
					_ = writer.Close()
				}
			}()
			if err := writer.session.Query("CREATE KEYSPACE b53 WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}").WithContext(ctx).Exec(); err != nil {
				t.Fatal(err)
			}
			ds.Database = "b53"
			reader, err := NewCassandraExecutor(ds, "", true)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if err := writer.session.Query("CREATE TABLE b53.items (id text PRIMARY KEY, value text)").WithContext(ctx).Exec(); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			writer = nil
			writer, err = NewCassandraExecutor(ds, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Ping(ctx); err != nil {
				t.Fatal(err)
			}
			version, err := reader.ServerVersion(ctx)
			if err != nil || version == "" {
				t.Fatalf("version %q: %v", version, err)
			}
			t.Logf("%s version %s", tc.image, version)
			catalog, err := reader.Discover(ctx)
			if err != nil || len(catalog.Namespaces) == 0 {
				t.Fatalf("discover: %+v %v", catalog, err)
			}
			found := false
			for _, ns := range catalog.Namespaces {
				if ns.Kind == "table" && ns.Name == "items" && ns.Metadata["keyspace"] == "b53" {
					found = true
				}
			}
			if !found {
				t.Fatalf("table missing from discovery: %+v", catalog.Namespaces)
			}
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO items (id, value) VALUES (?, ?)", "k", "v"}}); err != nil {
				t.Fatal(err)
			}
			rows, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT value FROM items WHERE id = ? LIMIT 1", "k"}})
			if err != nil || len(rows.Rows) != 1 {
				t.Fatalf("select: %+v %v", rows, err)
			}
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "UPDATE", Args: []string{"UPDATE items SET value = ? WHERE id = ?", "v2", "k"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"DELETE FROM items WHERE id = ?", "k"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{"INSERT INTO items (id, value) VALUES (?, ?)", "x", "y"}}); err == nil {
				t.Fatal("read-only write accepted")
			}
			for _, command := range []string{"CREATE", "DROP", "TRUNCATE", "BATCH"} {
				if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: command}); err == nil {
					t.Fatalf("%s accepted", command)
				}
			}
			if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT * FROM items"}}); err == nil {
				t.Fatal("unbounded SELECT accepted")
			}
		})
	}
}
func TestCassandraStatementBoundsAndDiscovery(t *testing.T) {
	for _, test := range []struct {
		command, statement string
		binds              int
		allowed            bool
	}{
		{"SELECT", "SELECT id FROM things WHERE id = ? LIMIT 10", 1, true},
		{"SELECT", "SELECT * FROM things", 0, false},
		{"SELECT", "SELECT * FROM things WHERE id = ? ALLOW FILTERING LIMIT 10", 1, false},
		{"SELECT", "SELECT * FROM things WHERE id = ? LIMIT 1001", 1, false},
		{"INSERT", "INSERT INTO things (id, value) VALUES (?, ?)", 2, true},
		{"UPDATE", "UPDATE things SET value = ? WHERE id = ?", 2, true},
		{"DELETE", "DELETE FROM things WHERE id = ?", 1, true},
		{"DELETE", "DELETE FROM things", 0, false},
		{"INSERT", "INSERT INTO things (id) VALUES ('x')", 0, false},
	} {
		_, _, _, err := cassandraValidate(test.command, test.statement, test.binds)
		if (err == nil) != test.allowed {
			t.Errorf("%s: %v", test.statement, err)
		}
	}
	ns := cassandraNamespaces([]cassandraKeyspace{{"ks", true}}, []cassandraTable{{"ks", "tbl"}})
	if len(ns) != 2 || ns[0].Metadata["durable_writes"] != "true" || ns[1].Metadata["keyspace"] != "ks" || ns[1].ItemCount != 0 {
		t.Fatalf("namespaces: %+v", ns)
	}
	rows := boundedNativeRows([]string{"value"}, [][]string{{strings.Repeat("x", nativeMaxValueBytes+1)}})
	if len(rows.Rows[0][0]) != nativeMaxValueBytes {
		t.Fatal("unbounded value")
	}
}
