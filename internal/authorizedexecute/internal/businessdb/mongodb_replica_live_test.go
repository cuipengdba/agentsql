package businessdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestMongoReplicaControlledLive(t *testing.T) {
	if os.Getenv("AGENTSQL_V370_MONGO_LIVE") != "1" {
		t.Skip("explicit MongoDB replica fixture required")
	}
	password, err := os.ReadFile("/fixtures/reader.pass")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{
		ID: "mongo-replica", DBType: "mongodb", Host: "mongo1", Port: 27017,
		Database: "agv370", Username: "reader", TLSMode: "strict", TLSCAFile: "/fixtures/ca.pem",
		StmtTimeoutMS: 5000, ConnLimit: 3,
		ReadControlJSON: `{"mongo":{"replica_set":"rs0","members":["mongo1:27017","mongo2:27017","mongo3:27017"],"indexes":[{"collection":"customers","name":"customer_id_1","fields":["customer_id"]}]}}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executor, err := NewMongoExecutorWithContext(ctx, ds, string(password), true)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	version, err := executor.ServerVersion(ctx)
	if err != nil || version != "7.0.45" {
		t.Fatalf("pinned MongoDB version: %s %v", version, err)
	}
	request := MongoFindRequest{Collection: "customers", Fields: []string{"customer_id", "phone"}, Filter: map[string]string{"customer_id": "C7"}, Limit: 10}
	result, err := executor.FindControlled(ctx, request)
	if os.Getenv("AGENTSQL_V370_MONGO_EXPECT_MISSING_INDEX") == "1" {
		if err == nil {
			t.Fatalf("missing index returned data: %+v", result)
		}
		return
	}
	if err != nil || result.RowCount != 1 || result.Rows[0][0] != "C7" {
		t.Fatalf("controlled replica find: %+v %v", result, err)
	}
	request.Filter = map[string]string{"status": "active"}
	if _, err := executor.FindControlled(ctx, request); err == nil {
		t.Fatal("unapproved scan succeeded")
	}
	wrongCA := ds
	wrongCA.TLSCAFile = "/fixtures/wrongca.pem"
	if candidate, err := NewMongoExecutorWithContext(ctx, wrongCA, string(password), true); err == nil {
		_ = candidate.Close()
		t.Fatal("untrusted CA connected")
	}
}
