package businessdb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	milvus "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type milvusTestClient struct{ milvus.Client }

func (milvusTestClient) CheckHealth(context.Context) (*entity.MilvusState, error) {
	return &entity.MilvusState{IsHealthy: true}, nil
}
func (milvusTestClient) GetVersion(context.Context) (string, error) { return "2.4-test", nil }
func (milvusTestClient) ListCollections(context.Context, ...milvus.ListCollectionOption) ([]*entity.Collection, error) {
	return []*entity.Collection{{Name: "Things"}, {Name: ""}}, nil
}
func (milvusTestClient) DescribeCollection(_ context.Context, name string) (*entity.Collection, error) {
	return &entity.Collection{Name: name, Schema: entity.NewSchema().WithField(entity.NewField().WithName("id").WithDataType(entity.FieldTypeInt64).WithIsPrimaryKey(true)).WithField(entity.NewField().WithName("vector").WithDataType(entity.FieldTypeFloatVector).WithDim(3))}, nil
}
func (milvusTestClient) GetCollectionStatistics(context.Context, string) (map[string]string, error) {
	return map[string]string{"row_count": "1"}, nil
}
func (milvusTestClient) DescribeIndex(context.Context, string, string, ...milvus.IndexOption) ([]entity.Index, error) {
	return []entity.Index{entity.NewGenericIndex("", "", map[string]string{"metric_type": "IP"})}, nil
}
func (milvusTestClient) Query(context.Context, string, []string, string, []string, ...milvus.SearchQueryOptionFunc) (milvus.ResultSet, error) {
	return milvus.ResultSet{entity.NewColumnInt64("id", []int64{1})}, nil
}
func (milvusTestClient) Search(context.Context, string, []string, string, []string, []entity.Vector, string, entity.MetricType, int, entity.SearchParam, ...milvus.SearchQueryOptionFunc) ([]milvus.SearchResult, error) {
	return []milvus.SearchResult{{ResultCount: 1, IDs: entity.NewColumnInt64("id", []int64{1}), Scores: []float32{1}}}, nil
}
func (milvusTestClient) Insert(context.Context, string, string, ...entity.Column) (entity.Column, error) {
	return entity.NewColumnInt64("id", []int64{1}), nil
}
func (milvusTestClient) Upsert(context.Context, string, string, ...entity.Column) (entity.Column, error) {
	return entity.NewColumnInt64("id", []int64{1}), nil
}
func TestMilvusOptionsAndMetadata(t *testing.T) {
	cfg, err := milvusConfig(model.Datasource{DBType: "milvus", Host: "localhost"}, "key")
	if err != nil || cfg.Address != "localhost:19530" || cfg.APIKey != "key" {
		t.Fatalf("API key config: %+v %v", cfg, err)
	}
	cfg, err = milvusConfig(model.Datasource{DBType: "milvus", Host: "localhost", Username: "user"}, "pass")
	if err != nil || cfg.Username != "user" || cfg.Password != "pass" || cfg.APIKey != "" {
		t.Fatalf("user config: %+v %v", cfg, err)
	}
	e := &MilvusExecutor{client: milvusTestClient{}, readOnly: true}
	ctx := context.Background()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.ServerVersion(ctx)
	if err != nil || v != "2.4-test" {
		t.Fatalf("version %s %v", v, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Metadata["vector_dimensions"] != "vector:3" {
		t.Fatalf("catalog %+v %v", cat, err)
	}
}
func TestMilvusPolicyAndBounds(t *testing.T) {
	for _, c := range []string{"SEARCH", "QUERY", "GET", "DESCRIBE", "STATUS", "HEALTH"} {
		if err := milvusCommandAllowed(c, true); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	for _, c := range []string{"INSERT", "UPSERT"} {
		if milvusCommandAllowed(c, true) == nil || milvusCommandAllowed(c, false) != nil {
			t.Errorf("write policy %s", c)
		}
	}
	for _, c := range []string{"DROP_COLLECTION", "CREATE_COLLECTION", "DELETE", "FLUSH", "COMPACT", "LOAD", "RELEASE", "IMPORT", "EXPORT"} {
		if milvusCommandAllowed(c, false) == nil {
			t.Errorf("dangerous %s accepted", c)
		}
	}
	for _, s := range []string{"0", "101", "bad", ""} {
		if _, err := vectorLimit(s); err == nil {
			t.Errorf("limit %q accepted", s)
		}
	}
	if _, err := vectorLimit("100"); err != nil {
		t.Fatal(err)
	}
	e := &MilvusExecutor{client: milvusTestClient{}, readOnly: false}
	ctx := context.Background()
	for _, req := range []NativeQueryRequest{{Namespace: "Things", Command: "SEARCH", Args: []string{"vector", "[1,2,3]", "10"}}, {Namespace: "Things", Command: "QUERY", Args: []string{"id > 0", "10"}}, {Namespace: "Things", Command: "GET"}, {Namespace: "Things", Command: "INSERT", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}}, {Namespace: "Things", Command: "UPSERT", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}}} {
		r, err := e.NativeQuery(ctx, req)
		if err != nil || len(r.Rows) != 1 {
			t.Errorf("bounded query %+v: %+v %v", req, r, err)
		}
	}
	for _, req := range []NativeQueryRequest{{Namespace: "Things", Command: "SEARCH", Args: []string{"vector", "[1,2,3]"}}, {Namespace: "Things", Command: "QUERY", Args: []string{"id > 0"}}, {Namespace: "Things", Command: "FLUSH"}, {Namespace: "Things", Command: "CREATE_COLLECTION"}, {Namespace: "Things", Command: "DROP_COLLECTION"}, {Namespace: "Things", Command: "SEARCH; DROP_COLLECTION"}} {
		if _, err := e.NativeQuery(ctx, req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	if c, _, err := normalizedNativeCommand(NativeQueryRequest{Command: " search "}); err != nil || c != "SEARCH" {
		t.Fatalf("normalize %s %v", c, err)
	}
	if got := boundedNativeString(strings.Repeat("a", 5000)); len(got) != nativeMaxValueBytes {
		t.Fatalf("bounded %d", len(got))
	}
}
func TestMilvusContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	if unavailable, probeErr := probeDockerAvailable(); unavailable || probeErr != nil {
		t.Skipf("docker daemon unavailable: %v", probeErr)
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "milvusdb/milvus:v2.4.13", ExposedPorts: []string{"19530/tcp"}, Env: map[string]string{"ETCD_USE_EMBED": "true", "COMMON_STORAGETYPE": "local"}, Cmd: []string{"milvus", "run", "standalone"}, WaitingFor: wait.ForListeningPort("19530/tcp").WithStartupTimeout(4 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "19530/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b57-milvus", DBType: "milvus", Host: host, Port: port.Int(), StmtTimeoutMS: 10000}
	var e *MilvusExecutor
	for i := 0; i < 20; i++ {
		e, err = NewMilvusExecutor(ds, "", false)
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
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
	t.Logf("milvusdb/milvus:v2.4.13 server %s", v)
	if err := e.client.NewCollection(ctx, "Batch57", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Batch57", Command: "INSERT", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}}); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) == 0 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Batch57", Command: "SEARCH", Args: []string{"vector", "[1,0,0]", "10"}}); err != nil {
		t.Fatal(err)
	}
}
