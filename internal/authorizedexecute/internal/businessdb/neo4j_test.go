package businessdb

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestNeo4jOptionsAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		ds    model.Datasource
		want  string
		valid bool
	}{
		{model.Datasource{DBType: "neo4j", Host: "localhost"}, "bolt://localhost:7687", true},
		{model.Datasource{DBType: "neo4j", Host: "localhost", TLSMode: "require", TLSServerName: "localhost"}, "bolt+s://localhost:7687", true},
		{model.Datasource{DBType: "neo4j", Host: "localhost", TLSMode: "require", TLSServerName: "other"}, "", false},
		{model.Datasource{DBType: "neo4j", Host: "localhost", TrustServerCertificate: true}, "", false},
	} {
		opts, err := neo4jOptions(tc.ds, "")
		if (err == nil) != tc.valid {
			t.Errorf("options %+v: %v", tc.ds, err)
		}
		if tc.valid {
			if opts.uri != tc.want {
				t.Errorf("URI %q", opts.uri)
			}
			c := &neo4j.Config{}
			opts.config(c)
			if (c.TlsConfig != nil) != (tc.ds.TLSMode != "") {
				t.Errorf("TLS config %+v", c)
			}
		}
	}
	authOpts, err := neo4jOptions(model.Datasource{DBType: "neo4j", Host: "localhost", Username: "neo4j"}, "password")
	if err != nil || authOpts.auth.Tokens["principal"] != "neo4j" || authOpts.auth.Tokens["credentials"] != "password" {
		t.Fatalf("Neo4j basic auth was not configured: %v", err)
	}
	for _, stmt := range []string{"MATCH (n) RETURN n LIMIT 10", "RETURN 1", "SHOW LABELS", "CALL db.labels()"} {
		command := strings.ToUpper(strings.Fields(stmt)[0])
		if err := neo4jStatementAllowed(command, stmt, true); err != nil {
			t.Errorf("read %s: %v", stmt, err)
		}
	}
	for _, stmt := range []string{"CREATE (n:Thing {id:'b54'}) RETURN n", "MERGE (n:Thing {id:'b54'}) RETURN n", "CREATE INDEX idx IF NOT EXISTS FOR (n:Thing) ON (n.id)", "DROP INDEX idx"} {
		command := strings.ToUpper(strings.Fields(stmt)[0])
		if neo4jStatementAllowed(command, stmt, true) == nil || neo4jStatementAllowed(command, stmt, false) != nil {
			t.Errorf("write policy %s", stmt)
		}
	}
	for _, tc := range []struct{ command, stmt string }{{"SET", "MATCH (n) WHERE elementId(n) = 'id' SET n.name = 'x' RETURN n LIMIT 1"}, {"REMOVE", "MATCH (n) WHERE elementId(n) = 'id' REMOVE n.name"}, {"DELETE", "MATCH (n) WHERE elementId(n) = 'id' DELETE n"}} {
		if neo4jStatementAllowed(tc.command, tc.stmt, true) == nil || neo4jStatementAllowed(tc.command, tc.stmt, false) != nil {
			t.Errorf("targeted write policy: %s", tc.stmt)
		}
	}
	for _, stmt := range []string{"MATCH (n) RETURN n", "MATCH (n) DETACH DELETE n LIMIT 10", "CALL dbms.shutdown()", "CALL apoc.help('x')", "GRANT ROLE admin", "LOAD CSV FROM 'file:///x' AS row RETURN row", "CREATE DATABASE x", "CREATE (n) WITH n DELETE n", "MATCH (n) RETURN n LIMIT 101", "MATCH (n) RETURN n LIMIT 1; DROP DATABASE x"} {
		command := strings.ToUpper(strings.Fields(stmt)[0])
		if neo4jStatementAllowed(command, stmt, false) == nil {
			t.Errorf("dangerous Cypher accepted: %s", stmt)
		}
	}
	if neo4jStatementAllowed("DELETE", "MATCH (n) WHERE n.name = 'x' DELETE n", false) == nil {
		t.Fatal("unanchored DELETE accepted")
	}
	if neo4jStatementAllowed("DELETE", "MATCH (n) WHERE elementId(n) = 'id' DETACH DELETE n", false) == nil {
		t.Fatal("DETACH DELETE accepted")
	}
	ns := neo4jCatalog([]string{"neo4j"}, map[string][]string{"neo4j": {"Person"}}, map[string][]string{"neo4j": {"KNOWS"}}, map[string][]string{"neo4j": {"idx"}})
	if len(ns) != 4 || ns[0].Metadata["label_count"] != "1" || ns[2].Kind != "relationship_type" {
		t.Fatalf("catalog %+v", ns)
	}
}

func TestNeo4jContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "neo4j:5", ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/password123"}, WaitingFor: wait.ForListeningPort("7687/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "7687/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b54-neo4j", DBType: "neo4j", Host: host, Port: port.Int(), Username: "neo4j", StmtTimeoutMS: 10000}
	var e *Neo4jExecutor
	for i := 0; i < 20; i++ {
		e, err = NewNeo4jExecutor(ds, "password123", false)
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
	t.Logf("neo4j:5 server %s", version)
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "CREATE", Args: []string{"CREATE (n:B54Thing {id:'one'}) RETURN n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "MERGE", Args: []string{"MERGE (n:B54Thing {id:'two'}) RETURN n"}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "MATCH", Args: []string{"MATCH (n:B54Thing) RETURN n.id LIMIT 10"}})
	if err != nil || len(r.Rows) == 0 {
		t.Fatalf("match %+v: %v", r, err)
	}
	idResult, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "MATCH", Args: []string{"MATCH (n:B54Thing {id:'one'}) RETURN elementId(n) LIMIT 1"}})
	if err != nil || len(idResult.Rows) != 1 || len(idResult.Rows[0]) != 1 {
		t.Fatalf("elementId %+v: %v", idResult, err)
	}
	id := idResult.Rows[0][0]
	if strings.ContainsAny(id, "';\n\r") {
		t.Fatal("unexpected elementId")
	}
	for _, tc := range []struct{ command, stmt string }{{"SET", fmt.Sprintf("MATCH (n) WHERE elementId(n) = '%s' SET n.checked = 'yes' RETURN n LIMIT 1", id)}, {"REMOVE", fmt.Sprintf("MATCH (n) WHERE elementId(n) = '%s' REMOVE n.checked", id)}, {"DELETE", fmt.Sprintf("MATCH (n) WHERE elementId(n) = '%s' DELETE n", id)}} {
		if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: tc.command, Args: []string{tc.stmt}}); err != nil {
			t.Fatalf("%s: %v", tc.command, err)
		}
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("discover %+v: %v", cat, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "MATCH", Args: []string{"MATCH (n) RETURN n"}}); err == nil {
		t.Fatal("unbounded MATCH accepted")
	}
}
