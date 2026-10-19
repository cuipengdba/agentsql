package businessdb

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
)

// This test is gated on a locally seeded, official openGauss 7.0.0 LTS
// instance with verified server TLS and a SELECT-only account.
func TestOpenGauss700LTSIntegration(t *testing.T) {
	password := os.Getenv("AGENTSQL_TEST_OPENGAUSS_RO_PASSWORD")
	ca := os.Getenv("AGENTSQL_TEST_OPENGAUSS_CA")
	if password == "" || ca == "" {
		t.Skip("set AGENTSQL_TEST_OPENGAUSS_RO_PASSWORD and AGENTSQL_TEST_OPENGAUSS_CA")
	}
	host := os.Getenv("AGENTSQL_TEST_OPENGAUSS_HOST")
	if host == "" {
		host = "host.docker.internal"
	}
	port := 15434
	if raw := os.Getenv("AGENTSQL_TEST_OPENGAUSS_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		port = parsed
	}
	serverName := os.Getenv("AGENTSQL_TEST_OPENGAUSS_SERVER_NAME")
	if serverName == "" {
		serverName = "og-lts.agentsql.test"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	datasource := model.Datasource{
		ID: "opengauss-700-lts-integration", DBType: "opengauss", Host: host, Port: port,
		Database: "postgres", Username: "agentsql_v240_ro", ConnLimit: 2,
		StmtTimeoutMS: 5000, RowLimit: 10, TLSMode: "verify-full",
		TLSCAFile: ca, TLSServerName: serverName,
	}
	executor, err := NewOpenGaussExecutor(ctx, datasource, password)
	if err != nil {
		t.Fatalf("open verified LTS read-only connection: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	columns, err := ListSchema(ctx, executor, "postgres", []SchemaTable{{Schema: "public", Table: "agentsql_v240_verify"}})
	if err != nil || len(columns) != 3 {
		t.Fatalf("LTS metadata failed: columns=%+v err=%v", columns, err)
	}
	query := "SELECT id, full_name, phone FROM ONLY public.agentsql_v240_verify WHERE id = 1 LIMIT 1"
	result, err := executor.Query(ctx, query, 10)
	if err != nil || result.RowCount != 1 || len(result.Rows) != 1 || result.Rows[0][0] != "1" {
		t.Fatalf("LTS bounded query failed: result=%+v err=%v", result, err)
	}
	if _, err := executor.Explain(ctx, query); err != nil {
		t.Fatalf("LTS JSON explain failed: %v", err)
	}
	if lockerPassword := os.Getenv("AGENTSQL_TEST_OPENGAUSS_LOCKER_PASSWORD"); lockerPassword != "" {
		adminURL := url.URL{
			Scheme: "postgres", User: url.UserPassword("agentsql_v240_locker", lockerPassword),
			Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/postgres",
		}
		adminOptions := adminURL.Query()
		adminOptions.Set("sslmode", "verify-full")
		adminOptions.Set("sslrootcert", ca)
		adminURL.RawQuery = adminOptions.Encode()
		adminConfig, err := pgx.ParseConfig(adminURL.String())
		if err != nil || adminConfig.TLSConfig == nil {
			t.Fatalf("configure synthetic lock holder: %v", err)
		}
		adminConfig.TLSConfig.ServerName = serverName
		adminConnection, err := pgx.ConnectConfig(ctx, adminConfig)
		if err != nil {
			t.Fatalf("connect synthetic lock holder: %v", err)
		}
		defer adminConnection.Close(context.Background())
		transaction, err := adminConnection.Begin(ctx)
		if err != nil {
			t.Fatalf("start synthetic lock holder: %v", err)
		}
		defer transaction.Rollback(context.Background())
		if _, err := transaction.Exec(ctx, "LOCK TABLE public.agentsql_v240_verify IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatalf("lock synthetic table: %v", err)
		}
		timedContext, stopTimeout := context.WithTimeout(ctx, 250*time.Millisecond)
		blocked, blockedErr := executor.Query(timedContext, query, 10)
		stopTimeout()
		if blockedErr == nil || len(blocked.Rows) != 0 {
			t.Fatalf("lock wait timeout delivered rows: %+v err=%v", blocked, blockedErr)
		}
		cancelledContext, stopCancel := context.WithCancel(ctx)
		go func() {
			time.Sleep(150 * time.Millisecond)
			stopCancel()
		}()
		blocked, blockedErr = executor.Query(cancelledContext, query, 10)
		if blockedErr == nil || len(blocked.Rows) != 0 {
			t.Fatalf("cancelled lock wait delivered rows: %+v err=%v", blocked, blockedErr)
		}
		if err := transaction.Rollback(ctx); err != nil {
			t.Fatalf("release synthetic lock: %v", err)
		}
		result, err = executor.Query(ctx, query, 10)
		if err != nil || result.RowCount != 1 {
			t.Fatalf("LTS read did not recover after lock release: %+v err=%v", result, err)
		}
		t.Log("openGauss lock wait timeout, cancellation, and recovery passed")
	}
	if _, err := executor.Query(ctx,
		"SELECT secret FROM ONLY public.agentsql_v240_view WHERE id = 1 LIMIT 1", 10); err == nil {
		t.Fatal("a view over an ungranted table was accepted as a physical table")
	}
	for _, sql := range []string{
		"SELECT id FROM public.agentsql_v240_verify WHERE id = 1 LIMIT 1",
		"SELECT id FROM ONLY public.agentsql_v240_verify WHERE id = 1 LIMIT ALL",
		"SELECT id FROM ONLY public.agentsql_v240_verify WHERE id = 1 LIMIT 1 FOR UPDATE",
		"DELETE FROM public.agentsql_v240_verify WHERE id = 1",
	} {
		if _, err := executor.Query(ctx, sql, 10); err == nil {
			t.Fatalf("unsafe SQL was accepted: %q", sql)
		}
	}
	if _, err := executor.Execute(ctx, "DELETE FROM public.agentsql_v240_verify WHERE id = 1"); err == nil {
		t.Fatal("write entry point was accepted")
	}
	if _, err := executor.BeginWriteTx(ctx); err == nil {
		t.Fatal("write transaction was accepted")
	}
	if badCA := os.Getenv("AGENTSQL_TEST_OPENGAUSS_BAD_CA"); badCA != "" {
		invalid := datasource
		invalid.TLSCAFile = badCA
		if untrusted, err := NewOpenGaussExecutor(ctx, invalid, password); err == nil {
			_ = untrusted.Close()
			t.Fatal("wrong CA was accepted")
		}
	}
	invalid := datasource
	invalid.TLSServerName = "wrong-host.agentsql.test"
	if misnamed, err := NewOpenGaussExecutor(ctx, invalid, password); err == nil {
		_ = misnamed.Close()
		t.Fatal("wrong certificate hostname was accepted")
	}
	plainURL := url.URL{
		Scheme: "postgres", User: url.UserPassword(datasource.Username, password),
		Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/" + datasource.Database,
	}
	plainOptions := plainURL.Query()
	plainOptions.Set("sslmode", "disable")
	plainURL.RawQuery = plainOptions.Encode()
	if plain, err := pgx.Connect(ctx, plainURL.String()); err == nil {
		_ = plain.Close(ctx)
		t.Fatal("server accepted a plaintext connection for the read-only role")
	}
}
