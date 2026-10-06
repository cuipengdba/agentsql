package businessdb

import (
	"context"
	"fmt"
	"net"
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

func TestInfluxPolicy(t *testing.T) {
	for _, tc := range []struct{ cmd, stmt string }{
		{"SHOW", "SHOW DATABASES"}, {"SHOW", "SHOW MEASUREMENTS"},
		{"SELECT", "SELECT value FROM cpu LIMIT 10"},
		{"FLUX", `from(bucket: "metrics") |> range(start: -1h) |> limit(n: 10)`},
	} {
		if err := influxStatementAllowed(tc.cmd, tc.stmt, true); err != nil {
			t.Errorf("%s: %v", tc.stmt, err)
		}
	}
	for _, tc := range []struct{ cmd, stmt string }{
		{"SELECT", "SELECT value FROM cpu"}, {"FLUX", `from(bucket: "metrics") |> range(start: -1h)`},
		{"FLUX", `from(bucket: "metrics") |> range(start: -1h) |> limit(n: 101)`},
		{"FLUX", `from(bucket: "metrics") |> range(start: -8d) |> limit(n: 10)`},
		{"DROP", "DROP DATABASE metrics"}, {"CREATE", "CREATE DATABASE metrics"},
		{"SHOW", "SHOW USERS"}, {"SELECT", "SELECT value FROM cpu LIMIT 1; DROP DATABASE x"},
	} {
		if influxStatementAllowed(tc.cmd, tc.stmt, false) == nil {
			t.Errorf("accepted %s", tc.stmt)
		}
	}
	if influxStatementAllowed("WRITE", "WRITE cpu,host=a value=1 123", true) == nil {
		t.Fatal("read-only write accepted")
	}
	if err := influxStatementAllowed("WRITE", "WRITE cpu,host=a value=1 123", false); err != nil {
		t.Fatal(err)
	}
	deleteStmt := "DELETE FROM cpu WHERE time >= '2026-10-01T00:00:00Z' AND time < '2026-10-02T00:00:00Z'"
	if influxStatementAllowed("DELETE", deleteStmt, true) == nil || influxStatementAllowed("DELETE", deleteStmt, false) != nil {
		t.Fatal("bounded DELETE policy")
	}
	if influxStatementAllowed("DELETE", "DELETE FROM cpu WHERE time >= '2026-10-01T00:00:00Z' AND time < '2026-10-10T00:00:00Z'", false) == nil {
		t.Fatal("wide DELETE accepted")
	}
	if _, _, err := influxOptions(model.Datasource{DBType: "influxdb", Host: "localhost", Database: "org"}, "token"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := influxOptions(model.Datasource{DBType: "influxdb", Host: "localhost", Database: "org", Username: "old"}, "token"); err == nil {
		t.Fatal("v1 auth accepted")
	}
	result, err := influxRows([]byte(`{"results":[{"series":[{"columns":["name"],"values":[["cpu"]]}]}]}`))
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "cpu" {
		t.Fatalf("rows %+v %v", result, err)
	}
	large := `{"results":[{"series":[{"columns":["value"],"values":[` + strings.Repeat(`[1],`, nativeMaxRows) + `[1]]}]}]}`
	result, err = influxRows([]byte(large))
	if err != nil || len(result.Rows) != nativeMaxRows || result.Info["truncated"] != "true" {
		t.Fatalf("unbounded result %+v %v", result.Info, err)
	}
	csvResult, err := influxCSV("#datatype,string,long\n,result,value\n,,1\n")
	if err != nil || len(csvResult.Columns) != 3 || len(csvResult.Rows) != 1 || csvResult.Rows[0][2] != "1" {
		t.Fatalf("Flux CSV %+v %v", csvResult, err)
	}
}

func TestInfluxContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "influxdb:2", ExposedPorts: []string{"8086/tcp"}, Env: map[string]string{"DOCKER_INFLUXDB_INIT_MODE": "setup", "DOCKER_INFLUXDB_INIT_USERNAME": "agentsql", "DOCKER_INFLUXDB_INIT_PASSWORD": "password123", "DOCKER_INFLUXDB_INIT_ORG": "agentsql", "DOCKER_INFLUXDB_INIT_BUCKET": "metrics", "DOCKER_INFLUXDB_INIT_ADMIN_TOKEN": "b55-token"}, WaitingFor: wait.ForHTTP("/health").WithPort("8086/tcp").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "8086/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{DBType: "influxdb", Host: host, Port: port.Int(), Database: "agentsql", StmtTimeoutMS: 10000}
	var e *InfluxDBExecutor
	for i := 0; i < 20; i++ {
		e, err = NewInfluxDBExecutor(ds, "b55-token", false)
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
	v, err := e.ServerVersion(ctx)
	if err != nil || v == "" {
		t.Fatalf("version %s: %v", v, err)
	}
	t.Logf("influxdb:2 version %s", v)
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	line := fmt.Sprintf("b55_cpu,host=test value=1 %d", time.Now().UnixNano())
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Namespace: "metrics", Command: "WRITE", Args: []string{"WRITE " + line}}); err != nil {
		t.Fatal(err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "FLUX", Args: []string{`from(bucket: "metrics") |> range(start: -1h) |> limit(n: 10)`}})
	if err != nil || len(r.Rows) == 0 {
		t.Fatalf("Flux %+v %v", r, err)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "FLUX", Args: []string{`from(bucket: "metrics") |> range(start: -1h)`}}); err == nil {
		t.Fatal("unbounded Flux accepted")
	}
}
func TestInfluxHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" && r.Header.Get("Authorization") != "Token token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/ping":
			w.Header().Set("X-Influxdb-Version", "2.test")
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/buckets":
			fmt.Fprint(w, `{"buckets":[{"name":"metrics"}]}`)
		case "/query":
			if strings.EqualFold(r.URL.Query().Get("q"), "SHOW DATABASES") {
				fmt.Fprint(w, `{"results":[{"series":[{"columns":["name"],"values":[["metrics"]]}]}]}`)
			} else {
				fmt.Fprint(w, `{"results":[{"series":[{"columns":["name"],"values":[["cpu"]]}]}]}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, portText, _ := net.SplitHostPort(u.Host)
	port, _ := net.LookupPort("tcp", portText)
	ds := model.Datasource{DBType: "influxdb", Host: host, Port: port, Database: "org"}
	base, _, err := influxOptions(ds, "token")
	if err != nil || base != server.URL {
		t.Fatalf("options %s %v", base, err)
	}
	e, err := NewInfluxDBExecutor(ds, "token", true)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	v, err := e.ServerVersion(ctx)
	if err != nil || v != "2.test" {
		t.Fatalf("version %s %v", v, err)
	}
	c, err := e.Discover(ctx)
	if err != nil || len(c.Namespaces) != 2 || c.Namespaces[1].Name != "cpu" {
		t.Fatalf("catalog %+v %v", c, err)
	}
	result, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "SHOW", Args: []string{"SHOW DATABASES"}})
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("query %+v %v", result, err)
	}
}
