package businessdb

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/auth"
	"github.com/weaviate/weaviate/entities/models"
)

func TestWeaviateOptionsAndPolicy(t *testing.T) {
	cfg, err := weaviateConfig(model.Datasource{DBType: "weaviate", Host: "localhost"}, "key")
	if err != nil || cfg.Host != "localhost:8080" || cfg.Scheme != "http" {
		t.Fatalf("config %+v %v", cfg, err)
	}
	if a, ok := cfg.AuthConfig.(auth.ApiKey); !ok || a.Value != "key" {
		t.Fatalf("API key config: %+v", cfg.AuthConfig)
	}
	for _, c := range []string{"SEARCH", "GET", "DESCRIBE", "STATUS", "HEALTH"} {
		if err := weaviateCommandAllowed(c, true); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{"INSERT", "UPDATE"} {
		if weaviateCommandAllowed(c, true) == nil || weaviateCommandAllowed(c, false) != nil {
			t.Errorf("write policy %s", c)
		}
	}
	for _, c := range []string{"DELETE_CLASS", "CREATE_CLASS", "DELETE", "SCHEMA_DROP", "BACKUP", "RESTORE"} {
		if weaviateCommandAllowed(c, false) == nil {
			t.Errorf("dangerous %s accepted", c)
		}
	}
}
func TestWeaviateHTTPAdapter(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/.well-known/live", "/v1/.well-known/ready":
			fmt.Fprint(w, "{}")
			return
		case "/v1/meta":
			fmt.Fprint(w, `{"version":"1.27-test"}`)
			return
		case "/v1/schema":
			fmt.Fprint(w, `{"classes":[{"class":"Things","vectorizer":"none","properties":[{"name":"name","dataType":["text"]}]}]}`)
			return
		case "/v1/schema/Things":
			fmt.Fprint(w, `{"class":"Things","vectorizer":"none","properties":[{"name":"name","dataType":["text"]}]}`)
			return
		case "/v1/graphql":
			fmt.Fprint(w, `{"data":{"Get":{"Things":[{"name":"one","_additional":{"id":"a"}}]}}}`)
			return
		case "/v1/objects":
			if r.Method == http.MethodPost {
				fmt.Fprint(w, `{"id":"4c79928e-31c4-4c45-a143-65f895b7417b"}`)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/v1/objects/") && r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer s.Close()
	addr := strings.TrimPrefix(s.URL, "http://")
	host, port, err := splitTestHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewWeaviateExecutor(model.Datasource{DBType: "weaviate", Host: host, Port: port}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || v != "1.27-test" {
		t.Fatalf("version %q %v", v, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Metadata["properties"] != "name" {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	for _, req := range []NativeQueryRequest{{Namespace: "Things", Command: "SEARCH", Args: []string{"nearVector", "[1,2,3]", "2"}}, {Namespace: "Things", Command: "GET", Args: []string{"name", "one", "2"}}} {
		r, err := e.NativeQuery(ctx, req)
		if err != nil || len(r.Rows) != 1 {
			t.Fatalf("query %+v %+v %v", req, r, err)
		}
	}
	for _, req := range []NativeQueryRequest{{Namespace: "Things", Command: "SEARCH", Args: []string{"nearVector", "[1]"}}, {Namespace: "Things", Command: "GET", Args: []string{"name", "one", "101"}}, {Namespace: "Things", Command: "DELETE_CLASS"}, {Namespace: "Things", Command: "BACKUP"}, {Namespace: "Things", Command: "GET; DELETE_CLASS"}} {
		if _, err := e.NativeQuery(ctx, req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	e.readOnly = false
	for _, req := range []NativeQueryRequest{{Namespace: "Things", Command: "INSERT", Args: []string{`[{"name":"one"}]`}}, {Namespace: "Things", Command: "UPDATE", Args: []string{"4c79928e-31c4-4c45-a143-65f895b7417b", `{"name":"two"}`}}} {
		if _, err := e.NativeQuery(ctx, req); err != nil {
			t.Errorf("write %+v: %v", req, err)
		}
	}
}
func splitTestHostPort(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(p)
	return h, n, err
}

func TestWeaviateContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	if unavailable, probeErr := probeDockerAvailable(); unavailable || probeErr != nil {
		t.Skipf("docker daemon unavailable: %v", probeErr)
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "semitechnologies/weaviate:1.27.0", ExposedPorts: []string{"8080/tcp"}, Env: map[string]string{"AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED": "true", "PERSISTENCE_DATA_PATH": "/var/lib/weaviate", "DEFAULT_VECTORIZER_MODULE": "none", "QUERY_DEFAULTS_LIMIT": "100"}, WaitingFor: wait.ForHTTP("/v1/.well-known/live").WithPort("8080/tcp").WithStartupTimeout(3 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b57-weaviate", DBType: "weaviate", Host: host, Port: port.Int(), StmtTimeoutMS: 10000}
	var e *WeaviateExecutor
	for i := 0; i < 20; i++ {
		e, err = NewWeaviateExecutor(ds, "", false)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || v == "" {
		t.Fatalf("version %q %v", v, err)
	}
	t.Logf("semitechnologies/weaviate:1.27.0 server %s", v)
	if err := e.client.Schema().ClassCreator().WithClass(&models.Class{Class: "Batch57", Vectorizer: "none", Properties: []*models.Property{{Name: "name", DataType: []string{"text"}}}}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.Data().Creator().WithClassName("Batch57").WithProperties(map[string]any{"name": "one"}).WithVector([]float32{1, 0, 0}).Do(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Batch57", Command: "SEARCH", Args: []string{"nearVector", "[1,0,0]", "10"}})
	if err != nil || len(r.Rows) == 0 {
		t.Fatalf("search %+v %v", r, err)
	}
}
