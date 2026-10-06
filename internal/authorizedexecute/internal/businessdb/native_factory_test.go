package businessdb

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestNativeFactoryFailsClosed(t *testing.T) {
	for _, name := range []string{"redis", "valkey", "memcached", "mongodb", "couchbase", "couchdb", "cassandra", "hbase", "scylla", "neo4j", "janusgraph", "nebula", "influxdb", "prometheus", "timescaledb"} {
		_, err := openNativeExecutor(model.Datasource{DBType: name}, "", true)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host is required") {
			t.Errorf("%s did not reach its adapter: %v", name, err)
		}
	}
	for _, name := range []string{"clickhouse", "doris", "starrocks", "milvus", "qdrant", "weaviate"} {
		_, err := openNativeExecutor(model.Datasource{DBType: name}, "", true)
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"postgres", "unknown"} {
		if _, err := openNativeExecutor(model.Datasource{DBType: name}, "", true); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
