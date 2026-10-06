package businessdb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestNebulaOptionsAndPolicy(t *testing.T) {
	if v, err := new(NebulaExecutor).ServerVersion(context.Background()); err != nil || v != "" {
		t.Fatalf("unexpected graphd version %q: %v", v, err)
	}
	o, err := nebulaOptions(model.Datasource{DBType: "nebula", Host: "localhost", Database: "space1", Username: "root"}, "pass")
	if err != nil || o.port != 9669 || o.username != "root" || o.conf.MaxConnPoolSize < 1 {
		t.Fatalf("options %+v: %v", o, err)
	}
	tlsOpts, err := nebulaOptions(model.Datasource{DBType: "nebula", Host: "localhost", TLSMode: "require", TLSServerName: "graph.example"}, "")
	if err != nil || tlsOpts.tls == nil || tlsOpts.tls.ServerName != "graph.example" {
		t.Fatalf("Nebula TLS options: %v", err)
	}
	for _, stmt := range []string{"SHOW SPACES", "SHOW TAGS", "MATCH (v) RETURN v LIMIT 10", "GO 1 STEPS FROM 'id' OVER knows YIELD dst(edge) LIMIT 10", "LOOKUP ON person YIELD id(vertex) LIMIT 10", "DESCRIBE TAG person"} {
		command := strings.Fields(stmt)[0]
		if err := nebulaStatementAllowed(command, stmt, true); err != nil {
			t.Errorf("read %s: %v", stmt, err)
		}
	}
	for _, stmt := range []string{"INSERT VERTEX person(name) VALUES 'id':('x')", "INSERT EDGE knows() VALUES 'a'->'b':()", "UPDATE VERTEX ON person 'id' SET name='x'", "DELETE VERTEX 'id'"} {
		command := strings.Fields(stmt)[0]
		if nebulaStatementAllowed(command, stmt, true) == nil || nebulaStatementAllowed(command, stmt, false) != nil {
			t.Errorf("write policy %s", stmt)
		}
	}
	for _, stmt := range []string{"MATCH (v) RETURN v", "GO FROM 'id' OVER knows", "CREATE SPACE db", "DROP SPACE db", "SUBMIT JOB STATS", "GRANT ROLE admin", "SIGN IN", "DELETE VERTEX *", "MATCH (v) RETURN v LIMIT 101", "MATCH (v) RETURN v -- LIMIT 10"} {
		command := strings.Fields(stmt)[0]
		if nebulaStatementAllowed(command, stmt, false) == nil {
			t.Errorf("dangerous nGQL accepted: %s", stmt)
		}
	}
	ns := nebulaCatalog([]string{"space1"}, map[string][]string{"space1": {"person"}}, map[string][]string{"space1": {"knows"}})
	if len(ns) != 3 || ns[0].Metadata["tag_count"] != "1" || ns[2].Kind != "edge_type" {
		t.Fatalf("catalog %+v", ns)
	}
}

func TestNebulaGraphdContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "vesoft/nebula-graphd:v3.8.0", ExposedPorts: []string{"9669/tcp"}, WaitingFor: wait.ForListeningPort("9669/tcp")}, Started: true})
	if err != nil {
		t.Skipf("graphd image or standalone startup unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9669/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b54-nebula", DBType: "nebula", Host: host, Port: port.Int(), Username: "root", StmtTimeoutMS: 10000}
	var e *NebulaExecutor
	for i := 0; i < 10; i++ {
		e, err = NewNebulaExecutor(ds, "nebula", false)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		t.Skipf("standalone graphd has no ready metadata/storage service: %v", err)
	}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("graphd spaces: %d", len(cat.Namespaces))
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SHOW", Args: []string{"SHOW SPACES"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "GO", Args: []string{"GO FROM 'id' OVER edge"}}); err == nil {
		t.Fatal("unbounded GO accepted")
	}
}
