package businessdb

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestJanusGraphOptionsAndPolicy(t *testing.T) {
	if v, err := new(JanusGraphExecutor).ServerVersion(context.Background()); err != nil || v != "" {
		t.Fatalf("unexpected Gremlin version %q: %v", v, err)
	}
	o, err := janusGraphOptions(model.Datasource{DBType: "janusgraph", Host: "localhost", Username: "user", TLSMode: "require"}, "pass")
	if err != nil || o.uri != "wss://localhost:8182/gremlin" || o.auth == nil || o.tls == nil {
		t.Fatalf("options %+v: %v", o, err)
	}
	for _, stmt := range []string{"g.V().limit(10)", "g.E().label().dedup().limit(50)", "g.V().has('name','a').limit(1).valueMap()", "g.V().limit(1).count()"} {
		if err := gremlinAllowed(stmt, true); err != nil {
			t.Errorf("read %s: %v", stmt, err)
		}
	}
	for _, stmt := range []string{"g.addV('Thing')", "g.V('id').property('name','x')", "g.V('id').as('source').addV('Thing')", "g.V('a').addE('knows').to(g.V('b'))", "g.V('id').drop()"} {
		if gremlinAllowed(stmt, true) == nil || gremlinAllowed(stmt, false) != nil {
			t.Errorf("write policy %s", stmt)
		}
	}
	for _, stmt := range []string{"g.V()", "g.E()", "g.V().count().limit(1)", "g.V().order().limit(1)", "g.V().drop()", "g.E().drop()", "g.V('id').has('x','y').drop()", "g.V('id').property('x',java.lang.Runtime.getRuntime())", "g.tx().commit()", "graph.open()", "g.V().limit(1);g.V().drop()", "g.V().limit(1)\ng.E()", "g.V().sideEffect(System.exit(0)).limit(1)", "g.V('a').addE('knows')"} {
		if gremlinAllowed(stmt, false) == nil {
			t.Errorf("dangerous Gremlin accepted: %s", stmt)
		}
	}
	ns := janusGraphCatalog([]string{"Person"}, []string{"knows"})
	if len(ns) != 3 || ns[0].Kind != "graph" || ns[0].ItemCount != 1 || ns[1].Kind != "vertex_label" || ns[2].Kind != "edge_label" {
		t.Fatalf("catalog %+v", ns)
	}
}

func TestJanusGraphContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "janusgraph/janusgraph:1", ExposedPorts: []string{"8182/tcp"}, WaitingFor: wait.ForListeningPort("8182/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8182/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b54-janus", DBType: "janusgraph", Host: host, Port: port.Int(), StmtTimeoutMS: 10000}
	var e *JanusGraphExecutor
	for i := 0; i < 20; i++ {
		e, err = NewJanusGraphExecutor(ds, "", false)
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
	if err != nil || version != "" {
		t.Fatalf("version %q: %v", version, err)
	}
	t.Log("Gremlin Server version unavailable through standard endpoint")
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{"g.addV('B54Thing')"}}); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"a", "b"} {
		if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{"g.addV('B54Thing').property('b54', '" + marker + "')"}}); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]string, 0, 2)
	for _, marker := range []string{"a", "b"} {
		r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{"g.V().has('b54', '" + marker + "').limit(1).id()"}})
		if err != nil || len(r.Rows) != 1 {
			t.Fatalf("id %+v: %v", r, err)
		}
		id := r.Rows[0][0]
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			id = strconv.Quote(id)
		}
		ids = append(ids, id)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{fmt.Sprintf("g.V(%s).addE('B54Edge').to(g.V(%s))", ids[0], ids[1])}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{"g.V().label().limit(10)"}})
	if err != nil || len(r.Rows) == 0 {
		t.Fatalf("query %+v: %v", r, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("discover %+v: %v", cat, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "g", Args: []string{"g.V().drop()"}}); err == nil {
		t.Fatal("full graph drop accepted")
	}
}
