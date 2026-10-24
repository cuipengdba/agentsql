package model

import (
	"strings"
	"testing"
)

func TestParseNativeReadControl(t *testing.T) {
	valid := `{"mongo":{"replica_set":"rs0","members":["mongo1.test:27017","mongo2.test:27017","mongo3.test:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}`
	control, err := ParseNativeReadControl(valid, "mongodb")
	if err != nil || control.Mongo == nil || control.Mongo.Indexes[0].Name != "customer_id_1" {
		t.Fatalf("valid control rejected: %+v %v", control, err)
	}
	for _, raw := range []string{
		`null`, `[]`, `{"mongo":{"replica_set":"rs0","members":[],"indexes":[]}}`,
		`{"mongo":{"replica_set":"rs0","members":["mongo1.test:27017","mongo1.test:27017","mongo3.test:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}`,
		`{"mongo":{"replica_set":"rs0","members":["mongo1.test:27017","mongo2.test:27017","mongo3.test:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}],"password":"secret"}}`,
	} {
		if _, err := ParseNativeReadControl(raw, "mongodb"); err == nil {
			t.Errorf("unsafe control accepted: %s", raw)
		}
	}
	if _, err := ParseNativeReadControl(valid, "redis"); err == nil {
		t.Fatal("MongoDB control accepted for Redis")
	}
}

func TestParseRedisSentinelReadControl(t *testing.T) {
	valid := `{"redis_sentinel":{"master_name":"mymaster","sentinels":["s1.test:26379","s2.test:26379","s3.test:26379"],"allowed_masters":["r1.test:6379","r2.test:6379"],"sentinel_username":"sentinel_reader"}}`
	control, err := ParseNativeReadControl(valid, "redis")
	if err != nil || control.Redis == nil || control.Redis.MasterName != "mymaster" {
		t.Fatalf("valid Sentinel control rejected: %+v %v", control, err)
	}
	for _, dbType := range []string{"mongodb", "postgres"} {
		if _, err := ParseNativeReadControl(valid, dbType); err == nil {
			t.Fatalf("Sentinel control accepted for %s", dbType)
		}
	}
	for _, raw := range []string{
		strings.Replace(valid, `"sentinel_username":"sentinel_reader"`, `"sentinel_username":"sentinel_reader","password":"secret"`, 1),
		strings.Replace(valid, `"sentinel_username":"sentinel_reader"`, `"sentinel_username":"sentinel_reader","sentinel_password_env":"OTHER_SECRET"`, 1),
		strings.Replace(valid, `"allowed_masters":["r1.test:6379","r2.test:6379"]`, `"allowed_masters":["r1.test:6379","r1.test:6379"]`, 1),
	} {
		if _, err := ParseNativeReadControl(raw, "redis"); err == nil {
			t.Errorf("unsafe Sentinel control accepted: %s", raw)
		}
	}
	if SentinelSecretEnvName("tenant-a", "ds") == SentinelSecretEnvName("tenant-b", "ds") ||
		SentinelSecretEnvName("tenant-a", "ds") == SentinelSecretEnvName("tenant-a", "other") {
		t.Fatal("Sentinel secret names must bind to tenant and datasource")
	}
}

func TestParseClickHouseAndMilvusReadControl(t *testing.T) {
	clickhouse := `{"clickhouse":{"max_rows_to_read":1000,"max_bytes_to_read":262144,"max_memory_usage":33554432}}`
	profile, err := ParseNativeReadControl(clickhouse, "clickhouse")
	if err != nil || profile.ClickHouse == nil || profile.ClickHouse.MaxRowsToRead != 1000 {
		t.Fatalf("valid ClickHouse budget rejected: %+v %v", profile, err)
	}
	milvus := `{"milvus":{"tenant_field":"tenant_id","filter_fields":["status","region"]}}`
	profile, err = ParseNativeReadControl(milvus, "milvus")
	if err != nil || profile.Milvus == nil || profile.Milvus.TenantField != "tenant_id" {
		t.Fatalf("valid Milvus profile rejected: %+v %v", profile, err)
	}
	for _, test := range []struct{ raw, dbType string }{
		{clickhouse, "milvus"},
		{milvus, "clickhouse"},
		{`{"clickhouse":{"max_rows_to_read":10001,"max_bytes_to_read":262144,"max_memory_usage":33554432}}`, "clickhouse"},
		{`{"clickhouse":{"max_rows_to_read":1000,"max_bytes_to_read":1048577,"max_memory_usage":33554432}}`, "clickhouse"},
		{`{"milvus":{"tenant_field":"tenant_id","filter_fields":["tenant_id"]}}`, "milvus"},
		{`{"milvus":{"tenant_field":"tenant_id","filter_fields":["status","status"]}}`, "milvus"},
		{`{"milvus":{"tenant_field":"tenant_id","filter_fields":[],"password":"secret"}}`, "milvus"},
		{`{"milvus":{"tenant_field":"tenant_id","filter_fields":[]},"mongo":{"replica_set":"rs0"}}`, "milvus"},
	} {
		if _, err := ParseNativeReadControl(test.raw, test.dbType); err == nil {
			t.Errorf("unsafe %s profile accepted: %s", test.dbType, test.raw)
		}
	}
}

func TestParseOraclePlanReadControl(t *testing.T) {
	profile, err := ParseNativeReadControl(`{"oracle":{"explain":true}}`, "oracle")
	if err != nil || profile.Oracle == nil || !profile.Oracle.Explain {
		t.Fatalf("Oracle plan profile rejected: %+v %v", profile, err)
	}
	for _, raw := range []string{
		`{"oracle":{}}`,
		`{"oracle":{"explain":false}}`,
		`{"oracle":{"explain":true,"plan_table":"SYSTEM.PLAN_TABLE"}}`,
		`{"oracle":{"explain":true},"milvus":{"tenant_field":"tenant_id"}}`,
	} {
		if _, err := ParseNativeReadControl(raw, "oracle"); err == nil {
			t.Errorf("unsafe Oracle profile accepted: %s", raw)
		}
	}
	if _, err := ParseNativeReadControl(`{"oracle":{"explain":true}}`, "sqlserver"); err == nil {
		t.Fatal("Oracle plan profile accepted for SQL Server")
	}
}
