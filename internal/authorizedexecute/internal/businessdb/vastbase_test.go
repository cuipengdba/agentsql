package businessdb

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestVastbaseExecutorRejectsWriteAndUnsafeReadWithoutPool(t *testing.T) {
	executor := &VastbaseExecutor{}
	if executor.Dialect() != "vastbase" {
		t.Fatal("incorrect dialect")
	}
	if _, err := executor.Execute(context.Background(), `DELETE FROM public.accounts`); !errors.Is(err, ErrReadOnlyViolated) {
		t.Fatalf("write was not rejected: %v", err)
	}
	if _, err := executor.BeginWriteTx(context.Background()); !errors.Is(err, ErrReadOnlyViolated) {
		t.Fatalf("transaction was not rejected: %v", err)
	}
	if _, err := executor.OpenSession(context.Background(), "s"); !errors.Is(err, ErrReadOnlyViolated) {
		t.Fatalf("session bypass was not rejected: %v", err)
	}
	if _, err := executor.Query(context.Background(), `SELECT * FROM public.accounts`, 10); err == nil {
		t.Fatal("unsafe query was not rejected")
	}
	if _, err := executor.Explain(context.Background(), `EXPLAIN ANALYZE DELETE FROM public.accounts`); err == nil {
		t.Fatal("unsafe explain was not rejected")
	}
	if _, err := executor.Query(context.Background(), `SELECT id FROM public.accounts WHERE id=1 LIMIT 10`, 10); err == nil {
		t.Fatal("missing pool was not rejected")
	}
}

func TestVastbaseConstructorRejectsWrongType(t *testing.T) {
	if _, err := NewVastbaseExecutor(context.Background(), model.Datasource{DBType: "postgres"}, ""); err == nil {
		t.Fatal("wrong datasource type was accepted")
	}
}
