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

func TestPrometheusPolicy(t *testing.T) {
	for _, q := range []string{"up", "vector(1)", "topk(10, http_requests_total)"} {
		if err := prometheusQueryAllowed(q); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
	for _, q := range []string{"{__name__=~\".*\"}", "topk(10, {__name__ =~ \".*\"})", "sum(up)", "up + topk(10, up)", "topk(10,up) + up", "topk(101, up)", "up; delete_series"} {
		if prometheusQueryAllowed(q) == nil {
			t.Errorf("accepted %q", q)
		}
	}
	if err := prometheusRangeAllowed("0", "604800", "605"); err != nil {
		t.Fatal(err)
	}
	if prometheusRangeAllowed("0", "604801", "605") == nil || prometheusRangeAllowed("0", "1000", "0.1") == nil {
		t.Fatal("unbounded range accepted")
	}
	if prometheusRangeAllowed("NaN", "10", "1") == nil {
		t.Fatal("NaN range accepted")
	}
	if prometheusSeriesWindowAllowed("0", "604800") != nil || prometheusSeriesWindowAllowed("0", "604801") == nil {
		t.Fatal("series window limit")
	}
	if _, _, err := prometheusOptions(model.Datasource{DBType: "prometheus", Host: "localhost", Username: "user"}, "pw"); err != nil {
		t.Fatal(err)
	}
	large := `{"result":[` + strings.Repeat(`null,`, nativeMaxRows) + `null]}`
	r, err := prometheusResult([]byte(large))
	if err != nil || len(r.Rows) != nativeMaxRows || r.Info["truncated"] != "true" {
		t.Fatalf("unbounded result %+v %v", r.Info, err)
	}
}

func TestPrometheusContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "prom/prometheus:latest", ExposedPorts: []string{"9090/tcp"}, WaitingFor: wait.ForHTTP("/-/ready").WithPort("9090/tcp").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "9090/tcp")
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewPrometheusExecutor(model.Datasource{DBType: "prometheus", Host: host, Port: port.Int(), StmtTimeoutMS: 10000}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	v, err := e.ServerVersion(ctx)
	if err != nil || v == "" {
		t.Fatalf("version %s: %v", v, err)
	}
	t.Logf("prom/prometheus:latest version %s", v)
	if _, err = e.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "QUERY", Args: []string{"vector(1)"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.NativeQuery(ctx, NativeQueryRequest{Command: "WRITE", Args: []string{"x"}}); err == nil {
		t.Fatal("write accepted")
	}
}

func TestPrometheusHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status/buildinfo":
			fmt.Fprint(w, `{"status":"success","data":{"version":"2.test"}}`)
		case "/api/v1/labels":
			fmt.Fprint(w, `{"status":"success","data":["job"]}`)
		case "/api/v1/label/job/values":
			fmt.Fprint(w, `{"status":"success","data":["api"]}`)
		case "/api/v1/query":
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up"},"value":[1,"1"]}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, portText, _ := net.SplitHostPort(u.Host)
	port, _ := net.LookupPort("tcp", portText)
	ds := model.Datasource{DBType: "prometheus", Host: host, Port: port, Username: "alice"}
	base, _, err := prometheusOptions(ds, "secret")
	if err != nil || base != server.URL {
		t.Fatalf("options %s %v", base, err)
	}
	e, err := NewPrometheusExecutor(ds, "secret", false)
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
	if err != nil || len(c.Namespaces) != 2 || c.Namespaces[1].Name != "api" {
		t.Fatalf("catalog %+v %v", c, err)
	}
	r, err := e.NativeQuery(ctx, NativeQueryRequest{Command: "query", Args: []string{"up"}})
	if err != nil || len(r.Rows) != 1 {
		t.Fatalf("query %+v %v", r, err)
	}
	for _, command := range []string{"WRITE", "/api/v1/admin/snapshot", "QUERY_EXEMPLARS"} {
		_, err := e.NativeQuery(ctx, NativeQueryRequest{Command: command})
		if err == nil {
			t.Errorf("accepted %s", command)
		}
		if command == "WRITE" && !strings.Contains(err.Error(), "只读数据源") {
			t.Errorf("write error %v", err)
		}
	}
}
