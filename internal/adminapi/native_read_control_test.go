package adminapi

import "testing"

func TestDatasourceNativeReadControlValidation(t *testing.T) {
	mongo := datasourceInput{
		ID: "mongo-ha", Name: "Mongo HA", DBType: "mongodb", Host: "mongo1.test", Port: 27017,
		Database: "app", Username: "reader", TLSMode: "strict", TLSCAFile: "/etc/agentsql/ca.pem",
		ReadControlJSON: `{"mongo":{"replica_set":"rs0","members":["mongo1.test:27017","mongo2.test:27017","mongo3.test:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}`,
	}
	if _, err := datasourceFromInput(mongo); err != nil {
		t.Fatalf("valid MongoDB replica source: %v", err)
	}
	mongo.Host = "unapproved.test"
	if _, err := datasourceFromInput(mongo); err == nil {
		t.Fatal("MongoDB unapproved seed accepted")
	}
	mongo.Host = "mongo1.test"
	mongo.TLSServerName = "mongo1.test"
	if _, err := datasourceFromInput(mongo); err == nil {
		t.Fatal("MongoDB fixed server name accepted for multiple members")
	}

	redis := datasourceInput{
		ID: "redis-ha", Name: "Redis HA", DBType: "redis", Host: "redis1.test", Port: 6379,
		Database: "0", Username: "reader", TLSMode: "strict", TLSCAFile: "/etc/agentsql/ca.pem",
		ReadControlJSON: `{"redis_sentinel":{"master_name":"mymaster","sentinels":["s1.test:26379","s2.test:26379","s3.test:26379"],"allowed_masters":["redis1.test:6379","redis2.test:6379"],"sentinel_username":"sentinel_reader"}}`,
	}
	if _, err := datasourceFromInput(redis); err != nil {
		t.Fatalf("valid Redis Sentinel source: %v", err)
	}
	redis.Username = "sentinel_reader"
	if _, err := datasourceFromInput(redis); err == nil {
		t.Fatal("shared Redis and Sentinel ACL identity accepted")
	}
}
