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
