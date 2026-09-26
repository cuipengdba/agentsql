//go:build ignore

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		child()
		return
	}
	parent()
}

func parent() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c, dsn := quotaPG(ctx)
	defer quotaTerminate(c)
	conn, err := pgx.Connect(ctx, dsn)
	quotaMust(err)
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE TABLE quota_counter(kind text primary key,used bigint not null,limit_value bigint not null);
INSERT INTO quota_counter VALUES('sessions',0,10),('plan_bytes',0,8388608),('db_connections',0,6);
CREATE TABLE connection_claims(pid int primary key,instance_id text);
CREATE TABLE session_directory(session_id text primary key,instance_id text not null)`)
	quotaMust(err)
	exe, err := os.Executable()
	quotaMust(err)
	local := runChildren(exe, dsn, "local", 4, 10)
	printQuota(map[string]any{"case": "process_local_quota_fanout", "processes": 4, "per_process_limit": 10, "requested_each": 10, "admitted_total": local, "intended_cluster_limit": 10, "bypass_factor": float64(local) / 10})
	cluster := runChildren(exe, dsn, "cluster", 4, 10)
	var used int
	quotaMust(conn.QueryRow(ctx, `SELECT used FROM quota_counter WHERE kind='sessions'`).Scan(&used))
	printQuota(map[string]any{"case": "postgres_cluster_admission", "processes": 4, "admitted_total": cluster, "counter_used": used, "cluster_limit": 10, "bounded": used == 10})
	bytesAdmitted := runChildren(exe, dsn, "bytes", 4, 4)
	var usedBytes int64
	quotaMust(conn.QueryRow(ctx, `SELECT used FROM quota_counter WHERE kind='plan_bytes'`).Scan(&usedBytes))
	printQuota(map[string]any{"case": "cluster_plan_byte_budget", "requests": 16, "request_bytes": 1 << 20, "admitted_requests": bytesAdmitted, "used_bytes": usedBytes, "limit_bytes": 8 << 20, "bounded": usedBytes == 8<<20})
	connections := runChildren(exe, dsn, "connections", 4, 3)
	var uniquePIDs int
	quotaMust(conn.QueryRow(ctx, `SELECT count(*) FROM connection_claims`).Scan(&uniquePIDs))
	printQuota(map[string]any{"case": "cluster_db_connection_budget", "requested_connections": 12, "admitted_connections": connections, "unique_backend_pids": uniquePIDs, "global_limit": 6, "bounded": connections == 6 && uniquePIDs == 6})
	routeReads := runChildren(exe, dsn, "route", 4, 20)
	var routes, conflicts int
	quotaMust(conn.QueryRow(ctx, `SELECT count(*),count(*)-count(distinct session_id) FROM session_directory`).Scan(&routes, &conflicts))
	printQuota(map[string]any{"case": "shared_session_directory_routing", "processes": 4, "same_session_ids_per_process": 20, "successful_owner_reads": routeReads, "directory_rows": routes, "duplicate_session_rows": conflicts, "single_owner_per_session": routes == 20 && conflicts == 0})
	tombstones()
}

func child() {
	mode := os.Args[2]
	n, _ := strconv.Atoi(os.Args[3])
	dsn := os.Getenv("B5_S0_DSN")
	if mode == "local" {
		fmt.Println(n)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	quotaMust(err)
	defer c.Close(ctx)
	instance := "unknown"
	if len(os.Args) > 4 {
		instance = os.Args[4]
	}
	if mode == "route" {
		ok := 0
		for i := 0; i < n; i++ {
			sid := fmt.Sprintf("shared-session-%d", i)
			_, err = c.Exec(ctx, `INSERT INTO session_directory(session_id,instance_id) VALUES($1,$2) ON CONFLICT(session_id) DO NOTHING`, sid, instance)
			quotaMust(err)
			var owner string
			quotaMust(c.QueryRow(ctx, `SELECT instance_id FROM session_directory WHERE session_id=$1`, sid).Scan(&owner))
			if owner != "" {
				ok++
			}
		}
		fmt.Println(ok)
		return
	}
	admitted := 0
	var held []*pgx.Conn
	for i := 0; i < n; i++ {
		kind := "sessions"
		delta := int64(1)
		if mode == "bytes" {
			kind = "plan_bytes"
			delta = 1 << 20
		}
		if mode == "connections" {
			kind = "db_connections"
		}
		var used int64
		err = c.QueryRow(ctx, `UPDATE quota_counter SET used=used+$2 WHERE kind=$1 AND used+$2<=limit_value RETURNING used`, kind, delta).Scan(&used)
		if err == nil {
			admitted++
			if mode == "connections" {
				physical, e := pgx.Connect(ctx, dsn)
				quotaMust(e)
				var pid int
				quotaMust(physical.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
				_, e = physical.Exec(ctx, `INSERT INTO connection_claims(pid,instance_id) VALUES($1,$2)`, pid, instance)
				quotaMust(e)
				held = append(held, physical)
			}
		}
	}
	if mode == "connections" {
		time.Sleep(300 * time.Millisecond)
		for _, physical := range held {
			physical.Close(ctx)
		}
	}
	fmt.Println(admitted)
}

func runChildren(exe, dsn, mode string, processes, requests int) int {
	var wg sync.WaitGroup
	out := make(chan int, processes)
	errs := make(chan error, processes)
	for i := 0; i < processes; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe, "child", mode, strconv.Itoa(requests), fmt.Sprintf("instance-%d", i))
			cmd.Env = append(os.Environ(), "B5_S0_DSN="+dsn)
			b, err := cmd.Output()
			if err != nil {
				errs <- fmt.Errorf("%s: %w", string(b), err)
				return
			}
			v, e := strconv.Atoi(strings.TrimSpace(string(b)))
			if e != nil {
				errs <- e
				return
			}
			out <- v
		}()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		quotaMust(e)
	}
	total := 0
	for v := range out {
		total += v
	}
	return total
}

type fatTombstone struct {
	Until   time.Time
	Outcome string
	Plan    []byte
}
type compactTombstone struct {
	Until      time.Time
	Outcome    uint8
	PlanDigest [32]byte
}

func tombstones() {
	runtime.GC()
	base := heap()
	fat := make(map[string]fatTombstone, 64)
	for i := 0; i < 64; i++ {
		p := make([]byte, 1<<20)
		for j := 0; j < len(p); j += 4096 {
			p[j] = byte(i)
		}
		fat[fmt.Sprintf("fat-%d", i)] = fatTombstone{time.Now().Add(15 * time.Minute), "closed", p}
	}
	runtime.GC()
	fatHeap := heap() - base
	// A tombstone needs only terminal metadata and a digest, never retained plan bytes.
	compact := make(map[string]compactTombstone, 50000)
	digest := sha256.Sum256([]byte("plan"))
	for i := 0; i < 50000; i++ {
		compact[fmt.Sprintf("session-%08d", i)] = compactTombstone{time.Now().Add(15 * time.Minute), 1, digest}
	}
	runtime.GC()
	both := heap() - base
	compactHeap := both - fatHeap
	printQuota(map[string]any{"case": "tombstone_retained_bytes", "fat_entries": len(fat), "plan_bytes_each": 1 << 20, "fat_heap_delta_bytes": fatHeap, "compact_entries": len(compact), "compact_heap_increment_bytes": compactHeap, "compact_approx_bytes_each": compactHeap / 50000, "ttl_minutes": 15, "growth_rule": "arrival_rate * TTL * retained_bytes"})
	runtime.KeepAlive(fat)
	runtime.KeepAlive(compact)
}

func heap() uint64 { var m runtime.MemStats; runtime.ReadMemStats(&m); return m.HeapAlloc }
func quotaPG(ctx context.Context) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-quota-%d", time.Now().UnixNano())
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: "postgres:14", Labels: map[string]string{"agentsql.b5.s0": "true"}, Env: map[string]string{"POSTGRES_DB": "b5", "POSTGRES_USER": "b5", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	quotaMust(err)
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", h, p.Port())
}
func quotaTerminate(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); err != nil {
		log.Printf("cleanup: %v", err)
	}
}
func quotaMust(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func printQuota(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
