package businessdb

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestValkeySentinelControlledLive(t *testing.T) {
	if os.Getenv("AGENTSQL_V370_VALKEY_SENTINEL_LIVE") != "1" {
		t.Skip("explicit Valkey Sentinel fixture required")
	}
	password, err := os.ReadFile("/fixtures/reader-password.txt")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{
		ID: "valkey-sentinel", DBType: "valkey", Host: "valkey-v370", Port: 6379,
		Database: "0", Username: "agent_reader", TLSMode: "strict", TLSCAFile: "/fixtures/certs/ca.crt",
		StmtTimeoutMS: 5000, ConnLimit: 3,
		ReadControlJSON: `{"redis_sentinel":{"master_name":"mymaster","sentinels":["vsentinel1:26379","vsentinel2:26379","vsentinel3:26379"],"allowed_masters":["valkey-v370:6379","valkey2:6379"],"sentinel_username":"sentinel_reader"}}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executor, err := NewRedisExecutorWithContext(ctx, ds, strings.TrimSpace(string(password)), true)
	if os.Getenv("AGENTSQL_V370_VALKEY_SENTINEL_EXPECT_DENY") == "1" {
		if err != nil {
			return
		}
		defer executor.Close()
		if result, readErr := executor.ReadHashControlled(ctx, RedisHashReadRequest{Key: "customers:sentinel", Fields: []string{"phone"}}); readErr == nil {
			t.Fatalf("revoked or untrusted Valkey Sentinel path returned data: %+v", result)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	hash, err := executor.ReadHashControlled(ctx, RedisHashReadRequest{Key: "customers:sentinel", Fields: []string{"phone", "name"}})
	if err != nil || hash.RowCount != 1 || hash.Rows[0][0] != "13800138000" {
		t.Fatalf("controlled Valkey Hash after Sentinel discovery: %+v %v", hash, err)
	}
	if _, err := executor.ReadHashControlled(ctx, RedisHashReadRequest{Key: "customers:sentinel", Fields: []string{"missing"}}); err == nil {
		t.Fatal("missing Valkey Hash field was returned")
	}
	wrongCA := ds
	wrongCA.TLSCAFile = "/fixtures/mongo/wrongca.pem"
	if candidate, err := NewRedisExecutorWithContext(ctx, wrongCA, strings.TrimSpace(string(password)), true); err == nil {
		_ = candidate.Close()
		t.Fatal("untrusted Valkey Sentinel CA connected")
	}
}
