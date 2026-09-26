//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, dsn := lockPG(ctx)
	defer lockTerminate(c)
	setup := lockConnect(ctx, dsn)
	_, err := setup.Exec(ctx, `CREATE TABLE control_lock(id int primary key,v int);CREATE TABLE business_lock(id int primary key,v int);INSERT INTO control_lock VALUES(1,0);INSERT INTO business_lock VALUES(1,0)`)
	lockMust(err)
	setup.Close(ctx)
	deadlockCase(ctx, dsn)
	orderedCase(ctx, dsn)
}

func deadlockCase(ctx context.Context, dsn string) {
	c1, c2 := lockConnect(ctx, dsn), lockConnect(ctx, dsn)
	defer c1.Close(ctx)
	defer c2.Close(ctx)
	t1, e := c1.Begin(ctx)
	lockMust(e)
	t2, e := c2.Begin(ctx)
	lockMust(e)
	_, e = t1.Exec(ctx, `UPDATE business_lock SET v=v+1 WHERE id=1`)
	lockMust(e)
	_, e = t2.Exec(ctx, `UPDATE control_lock SET v=v+1 WHERE id=1`)
	lockMust(e)
	ch := make(chan string, 2)
	start := time.Now()
	go func() { _, e := t1.Exec(ctx, `UPDATE control_lock SET v=v+1 WHERE id=1`); ch <- lockErr(e) }()
	time.Sleep(50 * time.Millisecond)
	go func() { _, e := t2.Exec(ctx, `UPDATE business_lock SET v=v+1 WHERE id=1`); ch <- lockErr(e) }()
	e1, e2 := <-ch, <-ch
	_ = t1.Rollback(ctx)
	_ = t2.Rollback(ctx)
	printLock(map[string]any{"case": "business_control_reverse_edge", "edge_1": "business -> control", "edge_2": "control -> business", "result_1": e1, "result_2": e2, "elapsed_ms": time.Since(start).Milliseconds(), "deadlock_detected": containsCode(e1, "40P01") || containsCode(e2, "40P01")})
}

func orderedCase(ctx context.Context, dsn string) {
	c1, c2 := lockConnect(ctx, dsn), lockConnect(ctx, dsn)
	defer c1.Close(ctx)
	defer c2.Close(ctx)
	t1, e := c1.Begin(ctx)
	lockMust(e)
	_, e = t1.Exec(ctx, `UPDATE control_lock SET v=v+1 WHERE id=1`)
	lockMust(e)
	_, e = t1.Exec(ctx, `UPDATE business_lock SET v=v+1 WHERE id=1`)
	lockMust(e)
	t2, e := c2.Begin(ctx)
	lockMust(e)
	_, e = t2.Exec(ctx, `SET LOCAL lock_timeout='250ms'`)
	lockMust(e)
	start := time.Now()
	_, waitErr := t2.Exec(ctx, `UPDATE control_lock SET v=v+1 WHERE id=1`)
	elapsed := time.Since(start)
	_ = t2.Rollback(ctx)
	_ = t1.Rollback(ctx)
	printLock(map[string]any{"case": "control_then_business_order", "edge": "control -> business", "second_wait_error": lockErr(waitErr), "elapsed_ms": elapsed.Milliseconds(), "deadlock": containsCode(lockErr(waitErr), "40P01"), "bounded_by_lock_timeout": containsCode(lockErr(waitErr), "55P03")})
}

func lockPG(ctx context.Context) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-lock-%d", time.Now().UnixNano())
	c, e := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: "postgres:14", Labels: map[string]string{"agentsql.b5.s0": "true"}, Env: map[string]string{"POSTGRES_DB": "b5", "POSTGRES_USER": "b5", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	lockMust(e)
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", h, p.Port())
}
func lockConnect(ctx context.Context, dsn string) *pgx.Conn {
	c, e := pgx.Connect(ctx, dsn)
	lockMust(e)
	return c
}
func lockTerminate(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); e != nil {
		log.Printf("cleanup: %v", e)
	}
}
func lockMust(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
func lockErr(e error) string {
	if e == nil {
		return "<nil>"
	}
	return e.Error()
}
func containsCode(s, code string) bool {
	for i := 0; i+len(code) <= len(s); i++ {
		if s[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
func printLock(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
