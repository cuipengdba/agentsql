package businessdb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestCouchbaseURLsAndPolicy(t *testing.T) {
	m, q, err := couchbaseURLs(model.Datasource{Host: "localhost", Port: 11210})
	if err != nil || m != "http://localhost:8091" || q != "http://localhost:8093" {
		t.Fatalf("%s %s %v", m, q, err)
	}
	m, q, err = couchbaseURLs(model.Datasource{Host: "localhost", Port: 11210, TLSMode: "strict"})
	if err != nil || m != "https://localhost:18091" || q != "https://localhost:18093" {
		t.Fatalf("TLS %s %s %v", m, q, err)
	}
	if _, err := parseCouchbaseVersion([]byte(`{"implementationVersion":"7.6.1"}`)); err != nil {
		t.Fatal(err)
	}
	b, err := parseCouchbaseBuckets([]byte(`[{"name":"a","flushEnabled":true,"ramQuotaMB":128}]`))
	if err != nil || len(b) != 1 || b[0].RamQuotaMB == nil || *b[0].RamQuotaMB != 128 {
		t.Fatalf("buckets %+v %v", b, err)
	}
	b, err = parseCouchbaseBuckets([]byte(`[{"name":"real","quota":{"ram":134217728}}]`))
	if err != nil || len(b) != 1 || b[0].Quota.Ram != 134217728 {
		t.Fatalf("quota %+v %v", b, err)
	}
	s, err := couchbaseStatementAllowed("SELECT", "SELECT * FROM `a`", true)
	if err != nil || !strings.HasSuffix(s, "LIMIT 1000") {
		t.Fatalf("select %q %v", s, err)
	}
	for _, c := range []string{"INSERT", "UPSERT"} {
		if _, err := couchbaseStatementAllowed(c, c+` INTO a (KEY, VALUE) VALUES ("k", {"x":1})`, false); err != nil {
			t.Fatal(c, err)
		}
	}
	for _, s := range []string{`UPDATE a USE KEYS "k" SET x=1`, `DELETE FROM a USE KEYS "k"`} {
		if _, err := couchbaseStatementAllowed(strings.Fields(s)[0], s, false); err != nil {
			t.Fatal(s, err)
		}
	}
	for _, s := range []string{`CREATE INDEX x ON a(x)`, `DROP BUCKET a`, `SELECT * FROM a; DELETE FROM a`, `UPDATE a SET x=1`} {
		if _, err := couchbaseStatementAllowed(strings.Fields(s)[0], s, false); err == nil {
			t.Fatal("accepted", s)
		}
	}
	for _, s := range []string{`SELECT remote_udf(x) FROM a`, `INSERT INTO a (KEY, VALUE) SELECT x,y FROM b`} {
		if _, err := couchbaseStatementAllowed(strings.Fields(s)[0], s, false); err == nil {
			t.Fatal("complex query accepted", s)
		}
	}
	if _, err := couchbaseStatementAllowed("DELETE", `DELETE FROM a USE KEYS "k"`, true); err == nil {
		t.Fatal("read-only write accepted")
	}
}

func TestCouchbaseContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "couchbase:community", ExposedPorts: []string{"8091/tcp", "8093/tcp"}, WaitingFor: wait.ForHTTP("/pools").WithPort("8091/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	managementPort, err := c.MappedPort(ctx, "8091/tcp")
	if err != nil {
		t.Fatal(err)
	}
	queryPort, err := c.MappedPort(ctx, "8093/tcp")
	if err != nil {
		t.Fatal(err)
	}
	management := "http://" + host + ":" + managementPort.Port()
	query := "http://" + host + ":" + queryPort.Port()
	init := url.Values{"username": {"Administrator"}, "password": {"password123"}, "services": {"kv,index,n1ql"}, "memoryQuota": {"256"}, "indexMemoryQuota": {"256"}, "port": {"SAME"}}
	response, err := http.PostForm(management+"/clusterInit", init)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("cluster init status %d: %s", response.StatusCode, body)
	}
	e := &CouchbaseExecutor{client: &http.Client{Timeout: 10 * time.Second}, management: management, query: query, username: "Administrator", password: "password123", datasourceID: "couchbase-test"}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %q %v", version, err)
	}
	t.Logf("couchbase:community version %s", version)
	deadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT 1 AS ready"}}); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("query service not ready: %v", err)
		}
		time.Sleep(time.Second)
	}
	form := url.Values{"name": {"b52"}, "ramQuota": {"128"}, "bucketType": {"couchbase"}}
	if _, err := e.request(ctx, "POST", management+"/pools/default/buckets", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range cat.Namespaces {
		if n.Name == "b52" && n.Kind == "bucket" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bucket missing: %+v", cat)
	}
	e.readOnly = false
	for _, statement := range []string{`INSERT INTO b52 (KEY, VALUE) VALUES ("item", {"x":1})`, `UPDATE b52 USE KEYS "item" SET x=2`} {
		command := strings.Fields(statement)[0]
		deadline := time.Now().Add(45 * time.Second)
		for {
			if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: command, Args: []string{statement}}); err == nil {
				break
			} else if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
		}
	}
	e.readOnly = true
	result, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{`SELECT x FROM b52 USE KEYS "item"`}})
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("SELECT %+v %v", result, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{`DELETE FROM b52 USE KEYS "item"`}}); err == nil {
		t.Fatal("read-only write accepted")
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "CREATE", Args: []string{`CREATE PRIMARY INDEX ON b52`}}); err == nil {
		t.Fatal("CREATE accepted")
	}
	e.readOnly = false
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{`DELETE FROM b52 USE KEYS "item"`}}); err != nil {
		t.Fatal(err)
	}
}

func TestCouchbaseREST(t *testing.T) {
	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pools":
			fmt.Fprint(w, `{"pools":[]}`)
		case "/pools/default":
			fmt.Fprint(w, `{"implementationVersion":"7.6.1"}`)
		case "/pools/default/buckets":
			fmt.Fprint(w, `[{"name":"test","flushEnabled":false,"ramQuotaMB":128}]`)
		case "/pools/default/buckets/test/scopes":
			fmt.Fprint(w, `{"scopes":[{"name":"_default","collections":[{"name":"_default"}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer management.Close()
	query := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query/service" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		statement := r.Form.Get("statement")
		if strings.HasPrefix(statement, "SELECT") && r.Form.Get("readonly") != "true" || strings.HasPrefix(statement, "INSERT") && r.Form.Get("readonly") != "" {
			t.Errorf("unsafe form: %+v", r.Form)
		}
		fmt.Fprint(w, `{"status":"success","results":[{"x":1}]}`)
	}))
	defer query.Close()
	e := &CouchbaseExecutor{client: management.Client(), management: management.URL, query: query.URL, readOnly: true}
	ctx := context.Background()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || v != "7.6.1" {
		t.Fatalf("version %s %v", v, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 3 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	res, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SELECT", Args: []string{"SELECT 1 AS x"}})
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("query %+v %v", res, err)
	}
	e.readOnly = false
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "INSERT", Args: []string{`INSERT INTO test (KEY, VALUE) VALUES ("x", {"x":1})`}}); err != nil {
		t.Fatal(err)
	}
}

func TestCouchbaseVersionFallback(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pools/default" {
			fmt.Fprint(w, `{"nodes":[]}`)
		} else if r.URL.Path == "/pools" {
			fmt.Fprint(w, `{"implementationVersion":"8.0.0"}`)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	e := &CouchbaseExecutor{client: s.Client(), management: s.URL}
	v, err := e.ServerVersion(context.Background())
	if err != nil || v != "8.0.0" {
		t.Fatalf("fallback %q %v", v, err)
	}
}
