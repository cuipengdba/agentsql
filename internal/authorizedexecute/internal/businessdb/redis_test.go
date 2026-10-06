package businessdb

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestRedisCommandsAndKeyspace(t *testing.T) {
	for _, name := range []string{"GET", "MGET", "SCAN", "OBJECT ENCODING", "PING"} {
		if err := redisCommandAllowed(name, true); err != nil {
			t.Errorf("read %s: %v", name, err)
		}
	}
	for _, name := range []string{"SET", "DEL", "HSET"} {
		if redisCommandAllowed(name, true) == nil || redisCommandAllowed(name, false) != nil {
			t.Errorf("write policy for %s", name)
		}
	}
	for _, name := range []string{"FLUSHALL", "FLUSHDB", "CONFIG", "EVAL", "KEYS", "SCRIPT", "SHUTDOWN", "SLOWLOG GET"} {
		if redisCommandAllowed(name, false) == nil {
			t.Errorf("dangerous command %s accepted", name)
		}
	}
	command, _, err := normalizedNativeCommand(NativeQueryRequest{Command: " object encoding "})
	if err != nil || command != "OBJECT ENCODING" {
		t.Fatalf("normalization: %q %v", command, err)
	}
	namespaces := parseRedisKeyspace("# Keyspace\ndb0:keys=3,expires=1,avg_ttl=10\ndb1:keys=8,expires=0\n")
	if len(namespaces) != 2 || namespaces[0].ItemCount != 3 || namespaces[1].ItemCount != 8 {
		t.Fatalf("keyspace: %+v", namespaces)
	}
}

func TestRedisAndValkeyContainers(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	for _, test := range []struct{ name, image string }{{"redis", "redis:7.2-alpine"}, {"valkey", "valkey/valkey:8-alpine"}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := dockerTestContext(t)
			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: test.image, ExposedPorts: []string{"6379/tcp"}, WaitingFor: wait.ForListeningPort("6379/tcp")}, Started: true})
			if err != nil {
				t.Skipf("container image unavailable: %v", err)
			}
			testcontainers.CleanupContainer(t, container)
			host, err := container.Host(ctx)
			if err != nil {
				t.Fatal(err)
			}
			port, err := container.MappedPort(ctx, "6379/tcp")
			if err != nil {
				t.Fatal(err)
			}
			ds := model.Datasource{ID: test.name, DBType: test.name, Host: host, Port: port.Int()}
			writer, err := NewRedisExecutor(ds, "", false)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "SET", Args: []string{"b51:key", "value"}}); err != nil {
				t.Fatal(err)
			}
			reader, err := NewRedisExecutor(ds, "", true)
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
			t.Logf("%s %s", test.image, version)
			catalog, err := reader.Discover(ctx)
			if err != nil || len(catalog.Namespaces) == 0 || catalog.Namespaces[0].ItemCount == 0 {
				t.Fatalf("catalog %+v %v", catalog, err)
			}
			for _, command := range []string{"GET", "EXISTS", "TYPE", "INFO"} {
				args := []string{"b51:key"}
				if command == "INFO" {
					args = []string{"server"}
				}
				result, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: command, Args: args})
				if err != nil || len(result.Rows) == 0 {
					t.Fatalf("%s: %+v %v", command, result, err)
				}
			}
			if _, err := reader.NativeQuery(ctx, NativeQueryRequest{Command: "SET", Args: []string{"x", "y"}}); err == nil {
				t.Fatal("read-only write accepted")
			}
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "FLUSHALL"}); err == nil {
				t.Fatal("dangerous command accepted")
			}
			if _, err := writer.NativeQuery(ctx, NativeQueryRequest{Command: "DEL", Args: []string{"b51:key"}}); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(version, "7.") && test.name == "redis" {
				t.Logf("unexpected but detected Redis version %s", version)
			}
		})
	}
}
