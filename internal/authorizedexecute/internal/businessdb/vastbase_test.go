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

func TestVerifiedG100VersionIsExactBuild9V3(t *testing.T) {
	for _, version := range []string{
		"(Vastbase G100 V3.0 (Build 9) Release) compiled at 2026-01-22 10:41:11 commit 30156 last mr product name:Vastbase G100 version:V3.0 (Build 9) Release patch:0 commit:30156 openGauss version:6.0.0",
	} {
		if !verifiedG100Version.MatchString(version) {
			t.Fatalf("expected accepted vendor version %q", version)
		}
	}
	for _, version := range []string{
		"PostgreSQL 9.2.4", "Vastbase G100 V3.0 Build 90 commit:30156", "Vastbase G100 V4.0 Build 9 commit:30156",
		"Vastbase G100 V3.0 Build 9 commit:30157", "Vastbase G100 V3.0 Build 9",
	} {
		if verifiedG100Version.MatchString(version) {
			t.Fatalf("unexpected accepted vendor version %q", version)
		}
	}
}

func TestVastbaseExplainCountsPhysicalScanUnderLimit(t *testing.T) {
	raw := []byte(`[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":1,"Plans":[{"Node Type":"Seq Scan","Plan Rows":2505,"Total Cost":180}]}}]`)
	info, err := parseVastbaseExplainJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if info.EstScanRows != 2505 || info.EstCost != 180 || !info.SeqScan {
		t.Fatalf("limit hid physical scan: %+v", info)
	}
}

func TestVastbaseExplainRejectsUnknownOrInvalidPlans(t *testing.T) {
	for _, raw := range []string{
		`[{"Plan":{"Node Type":"Vendor Remote Scan","Plan Rows":1,"Total Cost":1}}]`,
		`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":-1,"Total Cost":1}}]`,
		`[{"Plan":{"Node Type":"Seq Scan","Plan Rows":1e30,"Total Cost":1}}]`,
		`[{"Plan":{"Node Type":"Result","Plan Rows":1,"Total Cost":1}}]`,
		`[]`,
	} {
		if _, err := parseVastbaseExplainJSON([]byte(raw)); err == nil {
			t.Fatalf("unsafe plan was accepted: %s", raw)
		}
	}
}
