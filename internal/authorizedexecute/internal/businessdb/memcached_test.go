package businessdb

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestMemcachedCommandsAndStats(t *testing.T) {
	for _, name := range []string{"GET", "MGET", "STATS", "STATS ITEMS", "STATS SLABS", "VERSION"} {
		if err := memcachedCommandAllowed(name, true); err != nil {
			t.Error(err)
		}
	}
	for _, name := range []string{"SET", "ADD", "DELETE", "INCR", "DECR", "APPEND", "PREPEND"} {
		if memcachedCommandAllowed(name, true) == nil || memcachedCommandAllowed(name, false) != nil {
			t.Errorf("write policy %s", name)
		}
	}
	for _, name := range []string{"FLUSH_ALL", "STATS SETTINGS", "SLABS REASSIGN"} {
		if memcachedCommandAllowed(name, false) == nil {
			t.Errorf("dangerous command %s accepted", name)
		}
	}
	stats := parseMemcachedStats([]string{"STAT items:1:number 4", "STAT 1:chunk_size 96"})
	if stats["1"]["number"] != "4" || stats["1"]["chunk_size"] != "96" {
		t.Fatalf("stats %+v", stats)
	}
}

func TestMemcachedContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "memcached:1.6-alpine", ExposedPorts: []string{"11211/tcp"}, WaitingFor: wait.ForListeningPort("11211/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "11211/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "memcached", DBType: "memcached", Host: host, Port: port.Int()}
	writer, err := NewMemcachedExecutor(ds, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "SET", Args: []string{"b51key", "value"}}); err != nil {
		t.Fatal(err)
	}
	reader, err := NewMemcachedExecutor(ds, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := reader.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %q %v", version, err)
	}
	t.Logf("memcached:1.6-alpine %s", version)
	catalog, err := reader.Discover(ctx)
	if err != nil || len(catalog.Namespaces) == 0 {
		t.Fatalf("catalog %+v %v", catalog, err)
	}
	for _, command := range []string{"GET", "STATS", "STATS ITEMS", "STATS SLABS"} {
		args := []string(nil)
		if command == "GET" {
			args = []string{"b51key"}
		}
		_, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: command, Args: args})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
	}
	if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "SET", Args: []string{"x", "y"}}); err == nil {
		t.Fatal("read-only write accepted")
	}
	if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "DELETE", Args: []string{"b51key"}}); err != nil {
		t.Fatal(err)
	}
}
