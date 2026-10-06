package businessdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestCouchDBEndpointsAndPolicy(t *testing.T) {
	u, err := couchDBURL(model.Datasource{Host: "localhost"})
	if err != nil || u != "http://localhost:5984" {
		t.Fatalf("%s %v", u, err)
	}
	u, err = couchDBURL(model.Datasource{Host: "localhost", TLSMode: "strict"})
	if err != nil || u != "https://localhost:5984" {
		t.Fatalf("TLS %s %v", u, err)
	}
	v, err := parseCouchDBVersion([]byte(`{"couchdb":"Welcome","version":"3.4.2"}`))
	if err != nil || v != "3.4.2" {
		t.Fatalf("%s %v", v, err)
	}
	for _, c := range []string{"GET", "ALL_DOCS", "FIND", "VIEW"} {
		if err := couchDBCommandAllowed(c, true); err != nil {
			t.Fatal(c, err)
		}
	}
	for _, c := range []string{"PUT", "DELETE", "BULK_DOCS"} {
		if couchDBCommandAllowed(c, true) == nil || couchDBCommandAllowed(c, false) != nil {
			t.Fatal(c)
		}
	}
	for _, c := range []string{"_REPLICATE", "_COMPACT", "_PURGE", "_SECURITY", "_RESTART", "DROP"} {
		if couchDBCommandAllowed(c, false) == nil {
			t.Fatal(c)
		}
	}
	if couchDBID("_security") == nil {
		t.Fatal("management ID allowed")
	}
	if couchDBDatabase("_users") == nil {
		t.Fatal("system database allowed")
	}
	if _, err := couchDBDocument([]byte(`{"_deleted":true}`)); err == nil {
		t.Fatal("deletion marker allowed")
	}
}

func TestCouchDBContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "couchdb:3", ExposedPorts: []string{"5984/tcp"}, Env: map[string]string{"COUCHDB_USER": "admin", "COUCHDB_PASSWORD": "secret"}, WaitingFor: wait.ForHTTP("/").WithPort("5984/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5984/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "couchdb-test", DBType: "couchdb", Host: host, Port: port.Int(), Username: "admin"}
	w, err := NewCouchDBExecutor(ds, "secret", false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := NewCouchDBExecutor(ds, "secret", true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	version, err := r.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %q %v", version, err)
	}
	t.Logf("couchdb:3 version %s", version)
	if _, err := w.request(ctx, "PUT", "/b52", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.NativeQuery(ctx, NativeQueryRequest{Namespace: "b52", Command: "PUT", Args: []string{"item", `{"name":"b52"}`}}); err != nil {
		t.Fatal(err)
	}
	cat, err := r.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range cat.Namespaces {
		if n.Name == "b52" && n.ItemCount == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("database missing: %+v", cat)
	}
	for _, req := range []NativeQueryRequest{{Namespace: "b52", Command: "ALL_DOCS"}, {Namespace: "b52", Command: "FIND", Args: []string{`{"selector":{"name":"b52"}}`}}} {
		res, err := r.NativeQuery(ctx, req)
		if err != nil || len(res.Rows) == 0 {
			t.Fatalf("%s %+v %v", req.Command, res, err)
		}
	}
	get, err := r.NativeQuery(ctx, NativeQueryRequest{Namespace: "b52", Command: "GET", Args: []string{"item"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rev string `json:"_rev"`
	}
	if err := json.Unmarshal([]byte(get.Raw), &doc); err != nil || doc.Rev == "" {
		t.Fatalf("document %s %v", get.Raw, err)
	}
	if _, err := r.NativeQuery(ctx, NativeQueryRequest{Namespace: "b52", Command: "PUT", Args: []string{"bad", `{"x":1}`}}); err == nil {
		t.Fatal("read-only write accepted")
	}
	if _, err := w.NativeQuery(ctx, NativeQueryRequest{Namespace: "b52", Command: "_PURGE"}); err == nil {
		t.Fatal("purge accepted")
	}
	if _, err := w.NativeQuery(ctx, NativeQueryRequest{Namespace: "b52", Command: "DELETE", Args: []string{"item", doc.Rev}}); err != nil {
		t.Fatal(err)
	}
}

func TestCouchDBREST(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `{"version":"3.4.2"}`)
		case "/_all_dbs":
			fmt.Fprint(w, `["test"]`)
		case "/test":
			fmt.Fprint(w, `{"doc_count":2}`)
		case "/test/_all_docs":
			if r.URL.Query().Get("limit") != "1000" {
				t.Error("limit")
			}
			fmt.Fprint(w, `{"rows":[{"id":"x"}]}`)
		case "/test/_find":
			if r.Method != "POST" {
				t.Error("method")
			}
			fmt.Fprint(w, `{"docs":[{"_id":"x"}]}`)
		case "/test/item":
			if r.Method == "PUT" || r.Method == "DELETE" {
				fmt.Fprint(w, `{"ok":true}`)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	e := &CouchDBExecutor{client: s.Client(), base: s.URL, readOnly: true}
	ctx := context.Background()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || v != "3.4.2" {
		t.Fatalf("version %s %v", v, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].ItemCount != 2 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	for _, req := range []NativeQueryRequest{{Namespace: "test", Command: "ALL_DOCS"}, {Namespace: "test", Command: "FIND", Args: []string{`{"selector":{"_id":"x"}}`}}} {
		res, err := e.NativeQuery(ctx, req)
		if err != nil || len(res.Rows) != 1 {
			t.Fatalf("%s %+v %v", req.Command, res, err)
		}
	}
	e.readOnly = false
	for _, req := range []NativeQueryRequest{{Namespace: "test", Command: "PUT", Args: []string{"item", `{"x":1}`}}, {Namespace: "test", Command: "DELETE", Args: []string{"item", "1-abc"}}} {
		if _, err := e.NativeQuery(ctx, req); err != nil {
			t.Fatal(req.Command, err)
		}
	}
}
