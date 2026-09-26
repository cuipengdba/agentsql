//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	for _, major := range []string{"14", "18"} {
		benchPG(ctx, major)
	}
	benchWatchdog()
}

func benchPG(ctx context.Context, major string) {
	c, dsn := latPG(ctx, major)
	defer latTerminate(c)
	db, e := pgx.Connect(ctx, dsn)
	latMust(e)
	defer db.Close(ctx)
	_, e = db.Exec(ctx, `CREATE TABLE latency(id bigint primary key,v bigint);INSERT INTO latency VALUES(1,0)`)
	latMust(e)
	const n = 200
	measure := func(op string, fn func(int) error) {
		values := make([]int64, 0, n)
		for i := 0; i < n; i++ {
			s := time.Now()
			latMust(fn(i))
			values = append(values, time.Since(s).Microseconds())
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		sum := int64(0)
		for _, v := range values {
			sum += v
		}
		printLat(map[string]any{"case": "latency", "db": "postgres" + major, "operation": op, "n": n, "avg_us": sum / n, "p50_us": values[n/2], "p95_us": values[n*95/100]})
	}
	measure("begin_rollback", func(int) error {
		tx, e := db.Begin(ctx)
		if e != nil {
			return e
		}
		return tx.Rollback(ctx)
	})
	measure("begin_insert_rollback", func(i int) error {
		tx, e := db.Begin(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO latency VALUES($1,$2)`, int64(10000+i), i); e != nil {
			return e
		}
		return tx.Rollback(ctx)
	})
	measure("begin_insert_commit", func(i int) error {
		tx, e := db.Begin(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO latency VALUES($1,$2)`, int64(20000+i), i); e != nil {
			return e
		}
		return tx.Commit(ctx)
	})
	tx, e := db.Begin(ctx)
	latMust(e)
	measure("statement_update_in_tx", func(i int) error { _, e := tx.Exec(ctx, `UPDATE latency SET v=$1 WHERE id=1`, i); return e })
	latMust(tx.Rollback(ctx))
}

func benchWatchdog() {
	const n = 100000
	vals := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		s := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(time.Hour, cancel)
		_ = ctx
		timer.Stop()
		cancel()
		vals = append(vals, time.Since(s).Nanoseconds())
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	sum := int64(0)
	for _, v := range vals {
		sum += v
	}
	printLat(map[string]any{"case": "latency", "operation": "independent_watchdog_create_stop", "n": n, "avg_ns": sum / n, "p50_ns": vals[n/2], "p95_ns": vals[n*95/100]})
}
func latPG(ctx context.Context, major string) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-lat-pg%s-%d", major, time.Now().UnixNano())
	c, e := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: "postgres:" + major, Labels: map[string]string{"agentsql.b5.s0": "true"}, Env: map[string]string{"POSTGRES_DB": "b5", "POSTGRES_USER": "b5", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	latMust(e)
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", h, p.Port())
}
func latTerminate(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); e != nil {
		log.Printf("cleanup: %v", e)
	}
}
func latMust(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
func printLat(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
