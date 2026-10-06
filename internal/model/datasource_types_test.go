package model

import "testing"

func TestDatasourceRegistry(t *testing.T) {
	if len(datasourceTypes) != 27 {
		t.Fatalf("registered %d types, want 27", len(datasourceTypes))
	}
	for name, spec := range datasourceTypes {
		if spec.DefaultPort < 1 || spec.DefaultPort > 65535 || spec.DisplayName == "" {
			t.Errorf("invalid spec for %s: %+v", name, spec)
		}
		if !SupportedDatasourceType(name) {
			t.Errorf("unsupported registered type %s", name)
		}
		category, ok := CategoryOf(name)
		if !ok || category != spec.Category {
			t.Errorf("incorrect category for %s", name)
		}
		if IsNative(name) != (category != CategoryRelational) {
			t.Errorf("incorrect native classification for %s", name)
		}
	}
	for _, name := range []string{"postgres", "mysql", "dm", "oracle", "yashan", "sqlserver"} {
		spec, ok := TypeSpecFor(name)
		if !ok || spec.Category != CategoryRelational || !spec.RequiresDatabase || !spec.RequiresUsername || !spec.RequiresPassword {
			t.Errorf("relational spec changed: %s", name)
		}
	}
	for name, port := range map[string]int{"redis": 6379, "valkey": 6379, "memcached": 11211, "mongodb": 27017, "couchbase": 11210, "couchdb": 5984, "cassandra": 9042, "hbase": 16020, "scylla": 9042, "neo4j": 7687, "janusgraph": 8182, "nebula": 9669, "influxdb": 8086, "prometheus": 9090, "timescaledb": 5432, "clickhouse": 9000, "doris": 9030, "starrocks": 9030, "milvus": 19530, "qdrant": 6334, "weaviate": 8080} {
		spec, ok := TypeSpecFor(name)
		if !ok || spec.DefaultPort != port {
			t.Errorf("missing or wrong port for %s", name)
		}
	}
	for _, name := range []string{"mongodb", "couchbase", "couchdb"} {
		spec, _ := TypeSpecFor(name)
		if spec.Category != CategoryDocument || spec.RequiresDatabase || spec.RequiresUsername || spec.RequiresPassword {
			t.Errorf("document connection fields should be optional for %s: %+v", name, spec)
		}
	}
	for _, name := range []string{"cassandra", "hbase", "scylla"} {
		spec, _ := TypeSpecFor(name)
		if spec.Category != CategoryWideColumn || spec.RequiresDatabase || spec.RequiresUsername || spec.RequiresPassword {
			t.Errorf("wide-column connection fields should be optional for %s: %+v", name, spec)
		}
	}
	if SupportedDatasourceType("unknown") || IsNative("unknown") {
		t.Fatal("unknown type accepted")
	}
}
