//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type watchdogArgs struct{}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, dsn := watchPG(ctx)
	defer watchTerminate(c)
	db, e := pgx.Connect(ctx, dsn)
	watchMust(e)
	defer db.Close(ctx)
	_, e = db.Exec(ctx, `CREATE TABLE evidence(id int primary key)`)
	watchMust(e)
	started := make(chan struct{})
	rolled := make(chan map[string]any, 1)
	s := mcp.NewServer(&mcp.Implementation{Name: "watchdog", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "tx"}, func(reqCtx context.Context, _ *mcp.CallToolRequest, _ watchdogArgs) (*mcp.CallToolResult, watchdogArgs, error) {
		tx, e := db.Begin(context.Background())
		if e != nil {
			return nil, watchdogArgs{}, e
		}
		if _, e = tx.Exec(context.Background(), `INSERT INTO evidence VALUES(1)`); e != nil {
			return nil, watchdogArgs{}, e
		}
		close(started)
		wall := time.NewTimer(300 * time.Millisecond)
		defer wall.Stop()
		requestCanceled := false
		select {
		case <-reqCtx.Done():
			requestCanceled = true
		case <-wall.C:
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer rcancel()
		begin := time.Now()
		e = tx.Rollback(rctx)
		rolled <- map[string]any{"request_context_canceled": requestCanceled, "rollback_error": watchErr(e), "rollback_ms": time.Since(begin).Milliseconds()}
		return &mcp.CallToolResult{}, watchdogArgs{}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})
	ts := httptest.NewServer(h)
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	nc, e := net.Dial("tcp", u.Host)
	watchMust(e)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tx","arguments":{}}}`
	req := fmt.Sprintf("POST / HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nMCP-Protocol-Version: 2025-06-18\r\nContent-Length: %d\r\n\r\n%s", u.Host, len(body), body)
	_, e = io.WriteString(nc, req)
	watchMust(e)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		log.Fatal("handler did not start")
	}
	disconnected := time.Now()
	_ = nc.Close()
	var obs map[string]any
	select {
	case obs = <-rolled:
	case <-time.After(time.Second):
		log.Fatal("watchdog did not roll back")
	}
	var rows int
	watchMust(db.QueryRow(ctx, `SELECT count(*) FROM evidence`).Scan(&rows))
	obs["case"] = "mcp_2025_06_18_independent_watchdog"
	obs["wall_to_rollback_ms"] = time.Since(disconnected).Milliseconds()
	obs["business_rows"] = rows
	obs["truth"] = "not_committed"
	b, _ := json.Marshal(obs)
	fmt.Println(string(b))
}

func watchPG(ctx context.Context) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-mcpwatch-%d", time.Now().UnixNano())
	c, e := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: "postgres:14", Labels: map[string]string{"agentsql.b5.s0": "true"}, Env: map[string]string{"POSTGRES_DB": "b5", "POSTGRES_USER": "b5", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}, Started: true})
	watchMust(e)
	host, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://b5:pw@%s:%s/b5?sslmode=disable", host, p.Port())
}
func watchTerminate(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); e != nil {
		log.Printf("cleanup: %v", e)
	}
}
func watchMust(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
func watchErr(e error) string {
	if e == nil {
		return "<nil>"
	}
	return e.Error()
}
