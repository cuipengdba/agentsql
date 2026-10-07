package businessdb

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/qdrant/go-client/qdrant"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type qdrantTestClient struct {
	qdrantAPI
	query  *qdrant.QueryPoints
	scroll *qdrant.ScrollPoints
	upsert *qdrant.UpsertPoints
}

func (*qdrantTestClient) HealthCheck(context.Context) (*qdrant.HealthCheckReply, error) {
	return &qdrant.HealthCheckReply{Version: "1.19-test"}, nil
}
func (*qdrantTestClient) ListCollections(context.Context) ([]string, error) {
	return []string{"Things", ""}, nil
}
func (*qdrantTestClient) GetCollectionInfo(context.Context, string) (*qdrant.CollectionInfo, error) {
	count := uint64(3)
	return &qdrant.CollectionInfo{PointsCount: &count, Config: &qdrant.CollectionConfig{Params: &qdrant.CollectionParams{VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: 3})}}}, nil
}
func (c *qdrantTestClient) Query(_ context.Context, req *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error) {
	c.query = req
	return []*qdrant.ScoredPoint{{Id: qdrant.NewIDNum(1), Score: 0.75}}, nil
}
func (c *qdrantTestClient) ScrollAndOffset(_ context.Context, req *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error) {
	c.scroll = req
	return []*qdrant.RetrievedPoint{{Id: qdrant.NewIDNum(1)}}, qdrant.NewIDNum(2), nil
}
func (c *qdrantTestClient) Upsert(_ context.Context, req *qdrant.UpsertPoints) (*qdrant.UpdateResult, error) {
	c.upsert = req
	return &qdrant.UpdateResult{}, nil
}
func (*qdrantTestClient) Close() error { return nil }

func TestQdrantConfigAndMetadata(t *testing.T) {
	cfg, err := qdrantConfig(model.Datasource{DBType: "qdrant", Host: "localhost"}, "secret")
	if err != nil || cfg.Host != "localhost" || cfg.Port != 6334 || cfg.APIKey != "secret" || cfg.PoolSize != 1 {
		t.Fatalf("config %+v %v", cfg, err)
	}
	if _, err := qdrantConfig(model.Datasource{DBType: "qdrant"}, ""); err == nil {
		t.Fatal("missing host accepted")
	}
	if _, err := qdrantConfig(model.Datasource{DBType: "qdrant", Host: "localhost", Port: 65536}, ""); err == nil {
		t.Fatal("invalid port accepted")
	}
	c := &qdrantTestClient{}
	e := &QdrantExecutor{client: c, readOnly: true}
	ctx := context.Background()
	if err := e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || version != "1.19-test" {
		t.Fatalf("version %q %v", version, err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Metadata["vector_dimensions"] != "3" || cat.Namespaces[0].ItemCount != 3 {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	if _, err := qdrantNamespace("Things", nil); err == nil {
		t.Fatal("missing collection info accepted")
	}
	longName := strings.Repeat("a", 5000)
	n, err := qdrantNamespace(longName, &qdrant.CollectionInfo{})
	if err != nil || len(n.Name) != nativeMaxValueBytes || n.Metadata["points_count"] != "unavailable" {
		t.Fatalf("bounded namespace %+v %v", n, err)
	}
}

func TestQdrantPolicyAndQueryBounds(t *testing.T) {
	for _, command := range []string{"SEARCH", "GET", "DESCRIBE", "SCROLL", "STATUS", "HEALTH"} {
		if err := qdrantCommandAllowed(command, true); err != nil {
			t.Errorf("read %s: %v", command, err)
		}
	}
	if qdrantCommandAllowed("UPSERT", true) == nil || qdrantCommandAllowed("UPSERT", false) != nil {
		t.Fatal("upsert policy")
	}
	for _, command := range []string{"DELETE_COLLECTION", "CREATE_COLLECTION", "DELETE_POINTS", "CLEAR", "SNAPSHOT", "RECOVERY", "UNKNOWN"} {
		if qdrantCommandAllowed(command, false) == nil {
			t.Errorf("dangerous command %s accepted", command)
		}
	}
	c := &qdrantTestClient{}
	e := &QdrantExecutor{client: c, readOnly: false}
	ctx := context.Background()
	for _, req := range []NativeQueryRequest{
		{Command: " health "},
		{Namespace: "Things", Command: "get"},
		{Namespace: "Things", Command: "describe"},
		{Namespace: "Things", Command: " search ", Args: []string{"[1,0,0]", "100"}},
		{Namespace: "Things", Command: "scroll", Args: []string{"100", "START"}},
		{Namespace: "Things", Command: "upsert", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}},
	} {
		result, err := e.NativeQuery(ctx, req)
		if err != nil || len(result.Rows) != 1 {
			t.Errorf("bounded query %+v: %+v %v", req, result, err)
		}
	}
	if c.query == nil || c.query.GetLimit() != 100 || c.query.GetQuery() == nil {
		t.Fatalf("search request %+v", c.query)
	}
	if c.scroll == nil || c.scroll.GetLimit() != 100 {
		t.Fatalf("scroll request %+v", c.scroll)
	}
	if c.upsert == nil || len(c.upsert.GetPoints()) != 1 || !c.upsert.GetWait() {
		t.Fatalf("upsert request %+v", c.upsert)
	}
	if result, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Things", Command: "SCROLL", Args: []string{"1", "1"}}); err != nil || result.Info["next_cursor"] != "2" || c.scroll.GetOffset().GetNum() != 1 {
		t.Fatalf("scroll cursor %+v %v", result, err)
	}
	for _, req := range []NativeQueryRequest{
		{Namespace: "Things", Command: "SEARCH", Args: []string{"[1,0,0]"}},
		{Namespace: "Things", Command: "SEARCH", Args: []string{"[1,0,0]", "101"}},
		{Namespace: "Things", Command: "SEARCH", Args: []string{"[null,0,0]", "1"}},
		{Namespace: "Things", Command: "SCROLL", Args: []string{"100"}},
		{Namespace: "Things", Command: "SCROLL", Args: []string{"101", "START"}},
		{Namespace: "Things", Command: "SCROLL", Args: []string{"1", "bad;cursor"}},
		{Namespace: "Things", Command: "DELETE_COLLECTION"},
		{Namespace: "Things", Command: "CREATE_COLLECTION"},
		{Namespace: "Things", Command: "DELETE_POINTS"},
		{Namespace: "Things", Command: "SNAPSHOT"},
		{Namespace: "Things", Command: "SEARCH; DELETE_COLLECTION"},
		{Namespace: "Things", Command: "SEARCH SCROLL", Args: []string{"[1]", "1"}},
		{Namespace: "Things", Command: "UPSERT", Args: []string{`[{"id":1,"vector":[1,0,0],"payload":{}}]`}},
		{Namespace: "Things", Command: "UPSERT", Args: []string{`[{"id":1,"vector":[null]}]`}},
	} {
		if _, err := e.NativeQuery(ctx, req); err == nil {
			t.Errorf("accepted %+v", req)
		}
	}
	e.readOnly = true
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Things", Command: "UPSERT", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}}); err == nil {
		t.Fatal("read-only upsert accepted")
	}
	if _, err := qdrantWritePoints("[" + strings.Repeat(`{"id":1,"vector":[1]}`+",", 100) + `{"id":2,"vector":[1]}]`); err == nil {
		t.Fatal("oversized upsert batch accepted")
	}
}

func TestQdrantContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	if unavailable, probeErr := probeDockerAvailable(); unavailable || probeErr != nil {
		t.Skipf("docker daemon unavailable: %v", probeErr)
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "qdrant/qdrant:v1.19.1", ExposedPorts: []string{"6334/tcp"}, WaitingFor: wait.ForLog("Qdrant gRPC listening on 6334").WithStartupTimeout(3 * time.Minute)}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "6334/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "b57-qdrant", DBType: "qdrant", Host: host, Port: port.Int(), StmtTimeoutMS: 10000}
	var e *QdrantExecutor
	for i := 0; i < 20; i++ {
		e, err = NewQdrantExecutor(ds, "", false)
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
	version, err := e.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %q %v", version, err)
	}
	t.Logf("qdrant/qdrant:v1.19.1 server %s", version)
	client := e.client.(*qdrant.Client)
	if err := client.CreateCollection(ctx, &qdrant.CreateCollection{CollectionName: "Batch57", VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: 3, Distance: qdrant.Distance_Cosine})}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Batch57", Command: "UPSERT", Args: []string{`[{"id":1,"vector":[1,0,0]}]`}}); err != nil {
		t.Fatal(err)
	}
	cat, err := e.Discover(ctx)
	if err != nil || len(cat.Namespaces) != 1 || cat.Namespaces[0].Metadata["vector_dimensions"] != "3" {
		t.Fatalf("catalog %+v %v", cat, err)
	}
	result, err := e.NativeQuery(ctx, NativeQueryRequest{Namespace: "Batch57", Command: "SEARCH", Args: []string{"[1,0,0]", "10"}})
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("search %+v %v", result, err)
	}
	t.Log(fmt.Sprintf("collection=%s points=%d search_rows=%d", cat.Namespaces[0].Name, cat.Namespaces[0].ItemCount, len(result.Rows)))
}
