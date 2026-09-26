//go:build ignore

package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const labelKey = "agentsql.b5.s0"

type result struct {
	Case            string `json:"case"`
	ServerVersion   string `json:"server_version,omitempty"`
	ElapsedMS       int64  `json:"elapsed_ms"`
	DriverError     string `json:"driver_error,omitempty"`
	Truth           string `json:"truth"`
	FaultPhase      string `json:"fault_phase,omitempty"`
	OldConnectionID int64  `json:"old_connection_id,omitempty"`
	NewConnectionID int64  `json:"new_connection_id,omitempty"`
	RawAfterFault   string `json:"raw_after_fault,omitempty"`
	TypedOutcome    string `json:"typed_outcome,omitempty"`
}

type faultMode int

const (
	pass faultMode = iota
	beforeSend
	afterSend
)

type faultConn struct {
	net.Conn
	mu         sync.Mutex
	mode       faultMode
	verb       string
	triggered  chan string
	closed     chan struct{}
	closeOnce  sync.Once
	freezeRead atomic.Bool
}

func (c *faultConn) arm(mode faultMode, verb string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode, c.verb = mode, strings.ToUpper(verb)
}

func (c *faultConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	mode, verb := c.mode, c.verb
	c.mu.Unlock()
	if mode != pass && bytes.Contains(bytes.ToUpper(p), []byte(verb)) {
		if mode == beforeSend {
			select {
			case c.triggered <- "before_send":
			default:
			}
			<-c.closed
			return 0, errors.New("b5 injected close before terminal packet")
		}
		n, err := c.Conn.Write(p)
		c.freezeRead.Store(true)
		select {
		case c.triggered <- "after_send_before_ack":
		default:
		}
		return n, err
	}
	return c.Conn.Write(p)
}

func (c *faultConn) Read(p []byte) (int, error) {
	if c.freezeRead.Load() {
		<-c.closed
		return 0, errors.New("b5 injected response loss after terminal packet")
	}
	return c.Conn.Read(p)
}

func (c *faultConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("agentsql-b5-s0-mysql-%d", time.Now().UnixNano())
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Name: name, Image: "mysql:8", Labels: map[string]string{labelKey: "true"},
			Env:          map[string]string{"MYSQL_ROOT_PASSWORD": "rootpw", "MYSQL_DATABASE": "b5"},
			ExposedPorts: []string{"3306/tcp"},
			WaitingFor:   wait.ForAll(wait.ForListeningPort("3306/tcp"), wait.ForLog("ready for connections")).WithDeadline(90 * time.Second),
		}, Started: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		clean, cc := context.WithTimeout(context.Background(), 30*time.Second)
		defer cc()
		if err := testcontainers.TerminateContainer(c, testcontainers.StopContext(clean)); err != nil {
			log.Printf("cleanup: %v", err)
		}
	}()
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "3306/tcp")
	addr := net.JoinHostPort(host, port.Port())
	observer := openDB("tcp", addr)
	defer observer.Close()
	waitPing(ctx, observer)
	var version string
	if err := observer.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		log.Fatal(err)
	}
	printJSON(result{Case: "server", ServerVersion: version, Truth: "reachable"})
	if !strings.HasPrefix(version, "8.4.") {
		log.Fatalf("expected MySQL 8.4, got %s", version)
	}
	if _, err := observer.ExecContext(ctx, `CREATE TABLE evidence(id INT PRIMARY KEY, note VARCHAR(64)) ENGINE=InnoDB`); err != nil {
		log.Fatal(err)
	}

	for i, tc := range []struct {
		name, verb string
		mode       faultMode
		commit     bool
	}{
		{"commit_before_send", "COMMIT", beforeSend, true},
		{"commit_after_send_before_ack", "COMMIT", afterSend, true},
		{"rollback_before_send", "ROLLBACK", beforeSend, false},
		{"rollback_after_send_before_ack", "ROLLBACK", afterSend, false},
	} {
		runTerminal(ctx, observer, addr, i+1, tc.name, tc.verb, tc.mode, tc.commit)
	}
	runStatementDeadline(ctx, addr)
	runBaseline(ctx, observer)
}

func openDB(network, addr string) *sql.DB {
	cfg := mysql.NewConfig()
	cfg.User = "root"
	cfg.Passwd = "rootpw"
	cfg.Net = network
	cfg.Addr = addr
	cfg.DBName = "b5"
	cfg.Timeout = 2 * time.Second
	cfg.ReadTimeout = 0
	cfg.WriteTimeout = 0
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return db
}

func waitPing(ctx context.Context, db *sql.DB) {
	for {
		if err := db.PingContext(ctx); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			log.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func runTerminal(ctx context.Context, observer *sql.DB, addr string, id int, name, verb string, mode faultMode, commit bool) {
	var tracked atomic.Pointer[faultConn]
	network := fmt.Sprintf("b5fault%d", id)
	mysql.RegisterDialContext(network, func(dctx context.Context, target string) (net.Conn, error) {
		nc, err := (&net.Dialer{}).DialContext(dctx, "tcp", target)
		if err != nil {
			return nil, err
		}
		fc := &faultConn{Conn: nc, triggered: make(chan string, 1), closed: make(chan struct{})}
		tracked.Store(fc)
		return fc, nil
	})
	db := openDB(network, addr)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	var oldID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&oldID); err != nil {
		log.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO evidence(id,note) VALUES (?,?)", id, name); err != nil {
		log.Fatal(err)
	}
	fc := tracked.Load()
	if fc == nil {
		log.Fatal("tracked physical connection missing")
	}
	fc.arm(mode, verb)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		if commit {
			done <- tx.Commit()
		} else {
			done <- tx.Rollback()
		}
	}()
	phase := "not_triggered"
	select {
	case phase = <-fc.triggered:
	case <-time.After(2 * time.Second):
		log.Fatalf("%s terminal packet not observed", name)
	}
	// This is the independent terminal watchdog: network deadline first, then
	// hard close/discard of the retained physical socket.
	time.Sleep(150 * time.Millisecond)
	_ = fc.SetDeadline(time.Now())
	_ = fc.Close()
	var terminalErr error
	select {
	case terminalErr = <-done:
	case <-time.After(700 * time.Millisecond):
		terminalErr = errors.New("terminal call exceeded watchdog bound")
	}
	elapsed := time.Since(started)
	// Returning driver.ErrBadConn from Raw is database/sql's documented signal
	// that the retained physical connection must never return to the pool.
	rawErr := conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
	var newID int64
	_ = db.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&newID)
	deadline := time.Now().Add(3 * time.Second)
	count := -1
	for time.Now().Before(deadline) {
		if err := observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM evidence WHERE id=?", id).Scan(&count); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	truth := "not_committed"
	if count == 1 {
		truth = "committed"
	}
	if count < 0 {
		truth = "unknown_to_observer"
	}
	typed := "NOT_COMMITTED"
	if commit && phase == "after_send_before_ack" {
		typed = "UNKNOWN"
	}
	printJSON(result{Case: name, ElapsedMS: elapsed.Milliseconds(), DriverError: errString(terminalErr), Truth: truth, FaultPhase: phase, OldConnectionID: oldID, NewConnectionID: newID, RawAfterFault: errString(rawErr), TypedOutcome: typed})
}

func runStatementDeadline(ctx context.Context, addr string) {
	db := openDB("tcp", addr)
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := db.ExecContext(cctx, "SELECT SLEEP(5)")
	printJSON(result{Case: "statement_context_deadline", ElapsedMS: time.Since(start).Milliseconds(), DriverError: errString(err), Truth: "statement_canceled"})
}

func runBaseline(ctx context.Context, db *sql.DB) {
	const n = 100
	measure := func(name string, fn func() error) {
		start := time.Now()
		for range n {
			if err := fn(); err != nil {
				log.Fatal(err)
			}
		}
		printJSON(result{Case: name, ElapsedMS: time.Since(start).Microseconds() / int64(n) / 1000, Truth: fmt.Sprintf("avg_us=%d", time.Since(start).Microseconds()/int64(n))})
	}
	measure("baseline_begin_rollback", func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		return tx.Rollback()
	})
	measure("baseline_begin_insert_rollback", func() error {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO evidence(id,note) VALUES (?,?) ON DUPLICATE KEY UPDATE note=VALUES(note)", 1000, "x"); e != nil {
			return e
		}
		return tx.Rollback()
	})
	var seq atomic.Int64
	seq.Store(2000)
	measure("baseline_begin_insert_commit", func() error {
		id := seq.Add(1)
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO evidence(id,note) VALUES (?,?)", id, "x"); e != nil {
			return e
		}
		return tx.Commit()
	})
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	if errors.Is(err, driver.ErrBadConn) {
		return err.Error() + " [ErrBadConn]"
	}
	return err.Error()
}
func printJSON(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
