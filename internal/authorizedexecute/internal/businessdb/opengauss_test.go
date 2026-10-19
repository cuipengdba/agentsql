package businessdb

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestOpenGaussExecutorRejectsUnsafeEntryPoints(t *testing.T) {
	executor := &OpenGaussExecutor{}
	if executor.Dialect() != "opengauss" {
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
	if _, err := executor.Query(context.Background(), `SELECT id FROM ONLY public.accounts WHERE id=1 LIMIT 10`, 10); err == nil {
		t.Fatal("missing pool was not rejected")
	}
}

func TestOpenGaussConstructorRequiresVerifiedTLS(t *testing.T) {
	if _, err := NewOpenGaussExecutor(context.Background(), model.Datasource{DBType: "postgres"}, ""); err == nil {
		t.Fatal("wrong datasource type was accepted")
	}
	for _, datasource := range []model.Datasource{
		{DBType: "opengauss"},
		{DBType: "opengauss", TLSMode: "disable"},
		{DBType: "opengauss", TLSMode: "verify-full", TLSCAFile: "ca.pem", TrustServerCertificate: true},
	} {
		if _, err := NewOpenGaussExecutor(context.Background(), datasource, ""); err == nil {
			t.Fatalf("unverified TLS was accepted: %+v", datasource)
		}
	}
}

func TestOpenGaussIdentityRejectsRCAndOtherServers(t *testing.T) {
	for _, item := range []struct {
		version string
		allowed bool
	}{
		{"(openGauss 7.0.0 build 2397a86f) compiled at 2026-09-23 00:09:53 commit 1003", true},
		{"(openGauss 7.0.0 build 123) compiled at x", false},
		{"openGauss 7.0.0-RC3 build 01b7e318", false},
		{"openGauss 7.0.1 build 123", false},
		{"openGauss 6.0.0 build 123", false},
		{"PostgreSQL 17.0", false},
	} {
		if actual := openGaussIdentityAllowed(item.version); actual != item.allowed {
			t.Fatalf("unexpected openGauss identity result for %q", item.version)
		}
	}
}
