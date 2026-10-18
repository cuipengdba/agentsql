package businessdb

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

// TestHighGoV9010Integration requires the isolated official Enterprise PG-mode
// instance and a SELECT-only role. A generic PostgreSQL fixture is insufficient.
func TestHighGoV9010Integration(t *testing.T) {
	password := os.Getenv("HIGHGO_RO_PASSWORD")
	if password == "" {
		t.Skip("set HIGHGO_RO_PASSWORD for the isolated HighGo 9.0.10 integration test")
	}
	host := os.Getenv("AGENTSQL_TEST_HIGHGO_HOST")
	if host == "" {
		host = "agentsql-v170-highgo"
	}
	port := 5866
	if raw := os.Getenv("AGENTSQL_TEST_HIGHGO_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		port = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	datasource := model.Datasource{
		ID: "highgo-v9010-integration", DBType: "highgo", Host: host, Port: port, Database: "highgo",
		Username: "agentsql_v170_ro", ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 10,
	}
	executor, err := NewHighGoExecutor(ctx, datasource, password)
	if err != nil {
		t.Fatalf("connect to HighGo 9.0.10 PG mode: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	columns, err := ListSchema(ctx, executor, "highgo", []SchemaTable{{Schema: "public", Table: "agentsql_v170_verify"}})
	if err != nil {
		t.Fatalf("read HighGo metadata: %v", err)
	}
	if len(columns) != 3 {
		t.Fatalf("expected three synthetic columns, got %+v", columns)
	}
	query := "SELECT id, full_name, phone FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1"
	result, err := executor.Query(ctx, query, 10)
	if err != nil {
		t.Fatalf("bounded HighGo query: %v", err)
	}
	if result.RowCount != 1 || len(result.Rows) != 1 || result.Rows[0][0] != "1" {
		t.Fatalf("unexpected HighGo query result: %+v", result)
	}
	if _, err := executor.Explain(ctx, query); err != nil {
		t.Fatalf("HighGo JSON plan: %v", err)
	}
	for _, sql := range []string{
		"UPDATE public.agentsql_v170_verify SET full_name = 'changed' WHERE id = 1",
		"SELECT id FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1 FOR UPDATE",
		"SELECT id FROM public.agentsql_v170_verify WHERE id = 1 LIMIT 1; DELETE FROM public.agentsql_v170_verify",
	} {
		if _, err := executor.Query(ctx, sql, 10); err == nil {
			t.Fatalf("unsafe HighGo SQL was accepted: %q", sql)
		}
	}
	if _, err := executor.Execute(ctx, "DELETE FROM public.agentsql_v170_verify WHERE id = 1"); err == nil {
		t.Fatal("HighGo write entry point was accepted")
	}
	if _, err := executor.BeginWriteTx(ctx); err == nil {
		t.Fatal("HighGo transaction entry point was accepted")
	}
}

func TestHighGoRejectsOrdinaryPostgres(t *testing.T) {
	host := os.Getenv("AGENTSQL_TEST_PLAIN_PG_HOST")
	password := os.Getenv("AGENTSQL_TEST_PLAIN_PG_PASSWORD")
	if host == "" || password == "" {
		t.Skip("set AGENTSQL_TEST_PLAIN_PG_HOST and AGENTSQL_TEST_PLAIN_PG_PASSWORD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	datasource := model.Datasource{ID: "plain-pg-not-highgo", DBType: "highgo",
		Host: host, Port: 5432, Database: "postgres", Username: "postgres",
		ConnLimit: 2, StmtTimeoutMS: 5000, RowLimit: 10}
	executor, err := NewHighGoExecutor(ctx, datasource, password)
	if err == nil {
		_ = executor.Close()
		t.Fatal("ordinary PostgreSQL was accepted as HighGo")
	}
}
