package businessdb

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// TestVastbaseBuild9Integration runs only against a real G100 Build 9 PG-mode
// instance seeded with agentsql_v060.customers. It is intentionally separate
// from PostgreSQL protocol tests, which cannot establish vendor compatibility.
func TestVastbaseBuild9Integration(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_G100_HOST")
	if host == "" {
		t.Skip("set AGENTSQL_TEST_G100_HOST to run against a seeded G100 Build 9 instance")
	}
	port := 5432
	if raw := os.Getenv("AGENTSQL_TEST_G100_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		port = parsed
	}
	password := os.Getenv("AGENTSQL_TEST_G100_PASSWORD")
	if password == "" {
		t.Fatal("AGENTSQL_TEST_G100_PASSWORD is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	datasource := model.Datasource{
		ID: "g100-build9-integration", DBType: "vastbase", Host: host, Port: port, Database: "vastbase",
		Username: "agentsql_ro", ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 10,
	}
	executor, err := NewVastbaseExecutor(ctx, datasource, password)
	if err != nil {
		t.Fatalf("connect to real G100: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })

	columns, err := ListSchema(ctx, executor, "vastbase", []SchemaTable{{Schema: "agentsql_v060", Table: "customers"}})
	if err != nil {
		t.Fatalf("read G100 metadata with bound identifiers: %v", err)
	}
	if len(columns) != 3 {
		t.Fatalf("expected 3 seeded columns, got %+v", columns)
	}
	query := "SELECT id, label FROM agentsql_v060.customers WHERE id = 1 LIMIT 1"
	result, err := executor.Query(ctx, query, 10)
	if err != nil {
		t.Fatalf("bounded G100 query: %v", err)
	}
	if result.RowCount != 1 || len(result.Rows) != 1 || result.Rows[0][0] != "1" || result.Rows[0][1] != "alpha" {
		t.Fatalf("unexpected G100 query result: %+v", result)
	}
	if _, err := executor.Explain(ctx, query); err != nil {
		t.Fatalf("G100 JSON plan: %v", err)
	}
	for _, sql := range []string{
		"UPDATE agentsql_v060.customers SET label = 'changed' WHERE id = 1",
		"SELECT id FROM agentsql_v060.customers WHERE id = 1 LIMIT 1 FOR UPDATE",
		"SELECT id FROM agentsql_v060.customers WHERE id = 1 LIMIT 1; DELETE FROM agentsql_v060.customers",
	} {
		if _, err := executor.Query(ctx, sql, 10); err == nil {
			t.Fatalf("unsafe G100 SQL was accepted: %q", sql)
		}
	}
	if _, err := executor.Execute(ctx, "DELETE FROM agentsql_v060.customers WHERE id = 1"); err == nil {
		t.Fatal("G100 write entry point was accepted")
	}
	if _, err := executor.BeginWriteTx(ctx); err == nil {
		t.Fatal("G100 transaction entry point was accepted")
	}
}
