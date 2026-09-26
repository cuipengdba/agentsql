//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type walRecord struct {
	EventUUID, TxID, Kind, DBOutcome string
	At                               time.Time
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	biz, bizDSN := startPG(ctx, "business")
	defer terminateAudit(biz)
	audit, auditDSN := startPG(ctx, "audit")
	defer terminateAudit(audit)
	bizConn := connect(ctx, bizDSN)
	defer bizConn.Close(ctx)
	auditConn := connect(ctx, auditDSN)
	_, err := bizConn.Exec(ctx, `CREATE TABLE business_evidence(id int primary key,note text)`)
	mustAudit(err)
	_, err = auditConn.Exec(ctx, `CREATE TABLE audit_events(event_uuid text primary key,tx_id text,kind text,db_outcome text,created_at timestamptz default now())`)
	mustAudit(err)
	tmp, err := os.MkdirTemp("", "agentsql-b5-s0-audit-")
	mustAudit(err)
	defer os.RemoveAll(tmp)
	walPath := filepath.Join(tmp, "emergency.wal")

	// Outage before durable intent: fail closed. The business rollback is real,
	// but neither intent nor rollback can be promised durable in the primary audit store.
	tx, err := bizConn.Begin(ctx)
	mustAudit(err)
	_, err = tx.Exec(ctx, `INSERT INTO business_evidence VALUES(1,'intent outage')`)
	mustAudit(err)
	stop(ctx, audit)
	auditErr := appendAudit(ctx, auditConn, "intent-outage-intent", "tx-intent-outage", "commit_intent", "")
	rollbackErr := tx.Rollback(ctx)
	rollbackAuditErr := appendAudit(ctx, auditConn, "intent-outage-rollback", "tx-intent-outage", "rollback", "not_committed")
	walLatency := appendWAL(walPath, walRecord{"intent-outage-emergency", "tx-intent-outage", "rollback_pending", "not_committed", time.Now()})
	var count int
	mustAudit(bizConn.QueryRow(ctx, `SELECT count(*) FROM business_evidence WHERE id=1`).Scan(&count))
	printAudit(map[string]any{"case": "audit_outage_before_intent", "intent_error": errStringAudit(auditErr), "rollback_error": errStringAudit(rollbackErr), "rollback_audit_error": errStringAudit(rollbackAuditErr), "business_rows": count, "truth": "not_committed", "emergency_wal_fsync_us": walLatency.Microseconds()})
	auditConn.Close(context.Background())
	auditDSN = startAgain(ctx, audit)
	auditConn = connect(ctx, auditDSN)
	reconcile(ctx, auditConn, "intent-outage-emergency", "tx-intent-outage", "reconciliation", "not_committed")
	reconcile(ctx, auditConn, "intent-outage-emergency", "tx-intent-outage", "reconciliation", "not_committed")

	// Durable intent exists, business commit succeeds, then outcome audit is down.
	tx, err = bizConn.Begin(ctx)
	mustAudit(err)
	_, err = tx.Exec(ctx, `INSERT INTO business_evidence VALUES(2,'outcome outage')`)
	mustAudit(err)
	mustAudit(appendAudit(ctx, auditConn, "outcome-intent", "tx-outcome-outage", "commit_intent", "pending"))
	stop(ctx, audit)
	commitErr := tx.Commit(ctx)
	outcomeAuditErr := appendAudit(ctx, auditConn, "outcome-final", "tx-outcome-outage", "commit_outcome", "committed")
	walLatency = appendWAL(walPath, walRecord{"outcome-emergency", "tx-outcome-outage", "committed_audit_pending", "committed", time.Now()})
	mustAudit(bizConn.QueryRow(ctx, `SELECT count(*) FROM business_evidence WHERE id=2`).Scan(&count))
	printAudit(map[string]any{"case": "audit_outage_after_business_commit", "commit_error": errStringAudit(commitErr), "outcome_audit_error": errStringAudit(outcomeAuditErr), "business_rows": count, "truth": "committed/audit_pending", "emergency_wal_fsync_us": walLatency.Microseconds()})
	auditConn.Close(context.Background())
	auditDSN = startAgain(ctx, audit)
	auditConn = connect(ctx, auditDSN)
	defer auditConn.Close(ctx)
	reconcile(ctx, auditConn, "outcome-emergency", "tx-outcome-outage", "reconciliation", "committed")
	reconcile(ctx, auditConn, "outcome-emergency", "tx-outcome-outage", "reconciliation", "committed")
	var intentCount, reconcileCount int
	mustAudit(auditConn.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE event_uuid='outcome-intent'`).Scan(&intentCount))
	mustAudit(auditConn.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE event_uuid IN ('intent-outage-emergency','outcome-emergency')`).Scan(&reconcileCount))
	printAudit(map[string]any{"case": "reconciliation", "durable_intent_survived_restart": intentCount == 1, "idempotent_reconciliation_rows": reconcileCount, "expected_rows": 2})

	startBench := time.Now()
	const n = 100
	for i := 0; i < n; i++ {
		appendWAL(walPath, walRecord{fmt.Sprintf("bench-%d", i), "bench", "pending", "unknown", time.Now()})
	}
	info, _ := os.Stat(walPath)
	printAudit(map[string]any{"case": "emergency_wal_cost", "records": n, "avg_fsync_us": time.Since(startBench).Microseconds() / n, "bytes_total": info.Size(), "bytes_per_record": info.Size() / int64(n+2)})
}

func startPG(ctx context.Context, suffix string) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-audit-%s-%d", suffix, time.Now().UnixNano())
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: "postgres:14", Labels: map[string]string{"agentsql.b5.s0": "true"}, Env: map[string]string{"POSTGRES_DB": "b5", "POSTGRES_USER": "b5", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	mustAudit(err)
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", h, p.Port())
}

func connect(ctx context.Context, dsn string) *pgx.Conn {
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, e := pgx.Connect(ctx, dsn)
		if e == nil {
			if e = c.Ping(ctx); e == nil {
				return c
			}
			c.Close(ctx)
		}
		if time.Now().After(deadline) {
			log.Fatal(e)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func appendAudit(parent context.Context, c *pgx.Conn, event, txid, kind, outcome string) error {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	_, err := c.Exec(ctx, `INSERT INTO audit_events(event_uuid,tx_id,kind,db_outcome) VALUES($1,$2,$3,$4)`, event, txid, kind, outcome)
	return err
}
func reconcile(ctx context.Context, c *pgx.Conn, event, txid, kind, outcome string) {
	_, err := c.Exec(ctx, `INSERT INTO audit_events(event_uuid,tx_id,kind,db_outcome) VALUES($1,$2,$3,$4) ON CONFLICT(event_uuid) DO NOTHING`, event, txid, kind, outcome)
	mustAudit(err)
}
func appendWAL(path string, r walRecord) time.Duration {
	b, _ := json.Marshal(r)
	start := time.Now()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	mustAudit(err)
	_, err = f.Write(append(b, '\n'))
	mustAudit(err)
	mustAudit(f.Sync())
	mustAudit(f.Close())
	return time.Since(start)
}
func stop(ctx context.Context, c testcontainers.Container) {
	t := 2 * time.Second
	mustAudit(c.Stop(ctx, &t))
}
func startAgain(ctx context.Context, c testcontainers.Container) string {
	mustAudit(c.Start(ctx))
	deadline := time.Now().Add(30 * time.Second)
	for !c.IsRunning() {
		if time.Now().After(deadline) {
			log.Fatal("container failed to restart")
		}
		time.Sleep(100 * time.Millisecond)
	}
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", h, p.Port())
}
func terminateAudit(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); err != nil {
		log.Printf("cleanup: %v", err)
	}
}
func mustAudit(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func errStringAudit(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
func printAudit(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
