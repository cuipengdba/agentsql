//go:build ignore

// Command spike runs the B2 S0 empirical checks against disposable real
// PostgreSQL/MySQL containers. Run it from the repository root, for example:
//
//	go run ./.design/s0/spike.go -experiment versions
//
// The build tag keeps this evidence harness outside the product build.
package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type runningDB struct {
	container testcontainers.Container
	db        *sql.DB
	image     string
	dsn       string
}

func (r *runningDB) close(ctx context.Context) {
	if r.db != nil {
		_ = r.db.Close()
	}
	if r.container != nil {
		if err := r.container.Terminate(ctx); err != nil {
			log.Printf("cleanup %s: %v", r.image, err)
		}
	}
}

func startPostgres(ctx context.Context, tag string) (*runningDB, error) {
	image := "postgres:" + tag
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image,
			Env: map[string]string{
				"POSTGRES_PASSWORD": "spike",
				"POSTGRES_USER":     "spike",
				"POSTGRES_DB":       "spike",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithDeadline(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return nil, err
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	dsn := fmt.Sprintf("postgres://spike:spike@%s:%s/spike?sslmode=disable", host, port.Port())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	if err := pingUntil(ctx, db); err != nil {
		_ = db.Close()
		_ = c.Terminate(ctx)
		return nil, err
	}
	return &runningDB{container: c, db: db, image: image, dsn: dsn}, nil
}

func startMySQL(ctx context.Context, extraArgs ...string) (*runningDB, error) {
	image := "mysql:8"
	args := []string{
		"--gtid-mode=ON",
		"--enforce-gtid-consistency=ON",
		"--log-bin=mysql-bin",
		"--binlog-format=ROW",
		"--server-id=6204",
		"--event-scheduler=ON",
	}
	args = append(args, extraArgs...)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: image,
			Env: map[string]string{
				"MYSQL_ROOT_PASSWORD": "spike",
				"MYSQL_DATABASE":      "spike",
			},
			Cmd:          args,
			ExposedPorts: []string{"3306/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForListeningPort("3306/tcp"),
				wait.ForLog("ready for connections").WithOccurrence(1),
			).WithDeadline(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return nil, err
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	port, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	dsn := fmt.Sprintf("root:spike@tcp(%s:%s)/spike?multiStatements=true&parseTime=true", host, port.Port())
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	if err := pingUntil(ctx, db); err != nil {
		_ = db.Close()
		_ = c.Terminate(ctx)
		return nil, err
	}
	return &runningDB{container: c, db: db, image: image, dsn: dsn}, nil
}

func pingUntil(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if err := db.PingContext(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("database did not become ready: %w", last)
}

func scalar(ctx context.Context, db *sql.DB, query string) string {
	var v any
	if err := db.QueryRowContext(ctx, query).Scan(&v); err != nil {
		return "ERROR: " + err.Error()
	}
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

func mustExec(ctx context.Context, db *sql.DB, query string) error {
	if _, err := db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("exec %q: %w", query, err)
	}
	return nil
}

func printQuery(ctx context.Context, db *sql.DB, label, query string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Printf("[%s]\n%s\n", label, strings.Join(cols, "\t"))
	count := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				parts[i] = "<NULL>"
			} else if b, ok := v.([]byte); ok {
				parts[i] = string(b)
			} else {
				parts[i] = fmt.Sprint(v)
			}
		}
		fmt.Println(strings.Join(parts, "\t"))
		count++
	}
	fmt.Printf("rows=%d\n", count)
	return rows.Err()
}

func experimentFK(ctx context.Context) error {
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return err
		}
		fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SHOW server_version"))
		setup := `CREATE SCHEMA s0_fk;
CREATE TABLE s0_fk.parent(id integer PRIMARY KEY, payload text);
CREATE TABLE s0_fk.child(id integer PRIMARY KEY, parent_id integer REFERENCES s0_fk.parent(id) ON UPDATE CASCADE ON DELETE RESTRICT);`
		if err := mustExec(ctx, r.db, setup); err != nil {
			r.close(ctx)
			return err
		}
		if err := printQuery(ctx, r.db, "fk_trigger_rows", `
SELECT t.tgname, t.tgrelid::regclass::text AS trigger_relation,
       t.tgisinternal, c.conrelid::regclass::text AS conrelid,
       c.confrelid::regclass::text AS confrelid
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_constraint c ON c.oid=t.tgconstraint
WHERE c.conname='child_parent_id_fkey'
ORDER BY t.tgrelid,t.tgname`); err != nil {
			r.close(ctx)
			return err
		}
		if err := printQuery(ctx, r.db, "wrong_parent_conrelid_only", `
SELECT c.conname FROM pg_catalog.pg_constraint c
WHERE c.contype='f' AND c.conrelid='s0_fk.parent'::regclass`); err != nil {
			r.close(ctx)
			return err
		}
		if err := printQuery(ctx, r.db, "correct_parent_bidirectional", `
WITH target(relid) AS (VALUES ('s0_fk.parent'::regclass))
SELECT c.conname,
       CASE WHEN c.conrelid=t.relid AND c.confrelid=t.relid THEN 'self'
            WHEN c.conrelid=t.relid THEN 'outgoing' ELSE 'incoming' END AS direction
FROM pg_catalog.pg_constraint c JOIN target t
  ON c.conrelid=t.relid OR c.confrelid=t.relid
WHERE c.contype='f'`); err != nil {
			r.close(ctx)
			return err
		}
		if err := mustExec(ctx, r.db, `INSERT INTO s0_fk.parent VALUES(1,'p'); INSERT INTO s0_fk.child VALUES(1,1); UPDATE s0_fk.parent SET id=2 WHERE id=1`); err != nil {
			r.close(ctx)
			return err
		}
		fmt.Printf("cascade_child_parent_id=%s\n", scalar(ctx, r.db, "SELECT parent_id FROM s0_fk.child WHERE id=1"))
		_, delErr := r.db.ExecContext(ctx, "DELETE FROM s0_fk.parent WHERE id=2")
		fmt.Printf("parent_delete_error=%v\n", delErr)
		r.close(ctx)
	}

	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	if err := mustExec(ctx, r.db, `CREATE DATABASE s0_fk;
CREATE TABLE s0_fk.parent(id integer PRIMARY KEY, payload varchar(30));
CREATE TABLE s0_fk.child(id integer PRIMARY KEY, parent_id integer,
CONSTRAINT child_parent_fk FOREIGN KEY(parent_id) REFERENCES s0_fk.parent(id) ON UPDATE CASCADE ON DELETE RESTRICT)`); err != nil {
		return err
	}
	return printQuery(ctx, r.db, "mysql_parent_bidirectional", `
WITH roots(schema_name,table_name) AS (SELECT 's0_fk','parent')
SELECT k.CONSTRAINT_NAME,k.TABLE_SCHEMA,k.TABLE_NAME,
       k.REFERENCED_TABLE_SCHEMA,k.REFERENCED_TABLE_NAME,
       CASE WHEN k.TABLE_SCHEMA=r.schema_name AND k.TABLE_NAME=r.table_name THEN 'outgoing' ELSE 'incoming' END AS direction
FROM information_schema.KEY_COLUMN_USAGE k JOIN roots r
 ON (k.TABLE_SCHEMA=r.schema_name AND k.TABLE_NAME=r.table_name)
 OR (k.REFERENCED_TABLE_SCHEMA=r.schema_name AND k.REFERENCED_TABLE_NAME=r.table_name)
WHERE k.REFERENCED_TABLE_NAME IS NOT NULL`)
}

type relOID struct {
	oid  int64
	name string
	kind string
}

func relationOIDs(ctx context.Context, db *sql.DB) ([]relOID, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.oid::bigint, format('%I.%I',n.nspname,c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='s0_lock' AND c.relname IN ('base_a','base_b','v_ab') ORDER BY c.oid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []relOID
	for rows.Next() {
		var r relOID
		if err := rows.Scan(&r.oid, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func printPGLocks(ctx context.Context, db *sql.DB, label string, pid int64) error {
	return printQuery(ctx, db, label, fmt.Sprintf(`
SELECT c.oid::bigint,c.relname,c.relkind,l.mode,l.granted,l.fastpath
FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE l.pid=%d AND l.locktype='relation' AND n.nspname='s0_lock'
ORDER BY c.oid,l.mode`, pid))
}

func waitForWaitingRelation(ctx context.Context, db *sql.DB, pid int64, rel string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var got string
		err := db.QueryRowContext(ctx, `SELECT c.relname FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
WHERE l.pid=$1 AND l.locktype='relation' AND NOT l.granted LIMIT 1`, pid).Scan(&got)
		if err == nil && got == rel {
			return nil
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("pid %d did not wait on %s", pid, rel)
}

func experimentPGLocks(ctx context.Context) error {
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return err
		}
		fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SHOW server_version"))
		setup := `CREATE SCHEMA s0_lock;
CREATE TABLE s0_lock.base_a(id integer PRIMARY KEY, secret text);
CREATE TABLE s0_lock.base_b(id integer PRIMARY KEY, note text);
CREATE VIEW s0_lock.v_ab AS SELECT a.id,a.secret,b.note FROM s0_lock.base_a a JOIN s0_lock.base_b b USING(id)`
		if err := mustExec(ctx, r.db, setup); err != nil {
			r.close(ctx)
			return err
		}
		oids, err := relationOIDs(ctx, r.db)
		if err != nil {
			r.close(ctx)
			return err
		}
		fmt.Printf("oid_sorted_candidates=%v\n", oids)

		// Two independent DDL blockers make the server's acquisition order visible.
		ba, _ := r.db.Conn(ctx)
		bb, _ := r.db.Conn(ctx)
		binder, _ := r.db.Conn(ctx)
		txa, _ := ba.BeginTx(ctx, nil)
		txb, _ := bb.BeginTx(ctx, nil)
		if _, err = txa.ExecContext(ctx, "LOCK TABLE s0_lock.base_a IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		if _, err = txb.ExecContext(ctx, "LOCK TABLE s0_lock.base_b IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		txbind, _ := binder.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		var binderPID int64
		_ = txbind.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&binderPID)
		prepDone := make(chan error, 1)
		go func() {
			_, e := txbind.ExecContext(ctx, "PREPARE s0_order(integer) AS SELECT secret FROM s0_lock.v_ab WHERE id=$1")
			prepDone <- e
		}()
		if err = waitForWaitingRelation(ctx, r.db, binderPID, "base_a"); err != nil {
			return err
		}
		fmt.Println("automatic_order_step1=granted:v_ab waiting:base_a base_b:not_yet_requested")
		if err = printPGLocks(ctx, r.db, "automatic_order_step1_locks", binderPID); err != nil {
			return err
		}
		_ = txa.Rollback()
		if err = waitForWaitingRelation(ctx, r.db, binderPID, "base_b"); err != nil {
			return err
		}
		fmt.Println("automatic_order_step2=after_base_a_release waiting:base_b")
		if err = printPGLocks(ctx, r.db, "automatic_order_step2_locks", binderPID); err != nil {
			return err
		}
		_ = txb.Rollback()
		if err = <-prepDone; err != nil {
			return err
		}
		if err = printPGLocks(ctx, r.db, "automatic_prepare_held_closure", binderPID); err != nil {
			return err
		}
		_ = txbind.Rollback()
		_ = ba.Close()
		_ = bb.Close()
		_ = binder.Close()

		// New execution transaction: explicit LOCK list is built in numeric OID order.
		sort.Slice(oids, func(i, j int) bool { return oids[i].oid < oids[j].oid })
		qnames := make([]string, len(oids))
		for i, o := range oids {
			qnames[i] = o.name
		}
		execConn, _ := r.db.Conn(ctx)
		tx, _ := execConn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		var execPID int64
		_ = tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&execPID)
		lockSQL := "LOCK TABLE " + strings.Join(qnames, ",") + " IN ACCESS SHARE MODE"
		fmt.Printf("explicit_lock_sql=%s\n", lockSQL)
		if _, err = tx.ExecContext(ctx, lockSQL); err != nil {
			return err
		}
		if err = printPGLocks(ctx, r.db, "immediate_explicit_locks", execPID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "PREPARE s0_exec(integer) AS SELECT secret FROM s0_lock.v_ab WHERE id=$1"); err != nil {
			return err
		}
		if err = printPGLocks(ctx, r.db, "post_rewrite_held_closure", execPID); err != nil {
			return err
		}
		_ = tx.Rollback()
		_ = execConn.Close()

		// Deliberately omit base_b from the candidate set and show fail-closed delta.
		var aOID, vOID int64
		for _, o := range oids {
			if strings.HasSuffix(o.name, "base_a") {
				aOID = o.oid
			}
			if strings.HasSuffix(o.name, "v_ab") {
				vOID = o.oid
			}
		}
		badConn, _ := r.db.Conn(ctx)
		badTx, _ := badConn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		var badPID int64
		_ = badTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&badPID)
		if _, err = badTx.ExecContext(ctx, "PREPARE s0_bad(integer) AS SELECT secret FROM s0_lock.v_ab WHERE id=$1"); err != nil {
			return err
		}
		outside := fmt.Sprintf(`SELECT c.relname FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=%d AND l.locktype='relation' AND l.granted
AND n.nspname='s0_lock' AND l.relation NOT IN (%d,%d) ORDER BY c.oid`, badPID, aOID, vOID)
		if err = printQuery(ctx, r.db, "closure_outside_candidate_reject", outside); err != nil {
			return err
		}
		_ = badTx.Rollback()
		_ = badConn.Close()
		r.close(ctx)
	}
	return nil
}

func experimentPGMatview(ctx context.Context) error {
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return err
		}
		fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SHOW server_version"))
		setup := `CREATE SCHEMA s0_mv;
CREATE TABLE s0_mv.base_a(id integer PRIMARY KEY, secret text);
CREATE TABLE s0_mv.base_b(id integer PRIMARY KEY, note text);
INSERT INTO s0_mv.base_a VALUES(1,'alpha'); INSERT INTO s0_mv.base_b VALUES(1,'bravo');
CREATE MATERIALIZED VIEW s0_mv.mv1 AS SELECT a.id,a.secret,b.note FROM s0_mv.base_a a JOIN s0_mv.base_b b USING(id);
CREATE MATERIALIZED VIEW s0_mv.mv2 AS SELECT id,upper(secret) AS secret_upper FROM s0_mv.mv1`
		if err = mustExec(ctx, r.db, setup); err != nil {
			r.close(ctx)
			return err
		}
		conn, _ := r.db.Conn(ctx)
		tx, _ := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		var pid int64
		_ = tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)
		if _, err = tx.ExecContext(ctx, "PREPARE s0_mv_read AS SELECT secret_upper FROM s0_mv.mv2"); err != nil {
			return err
		}
		if err = printQuery(ctx, r.db, "matview_read_held_locks", fmt.Sprintf(`SELECT c.oid::bigint,c.relname,c.relkind,l.mode,l.granted
FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE l.pid=%d AND l.locktype='relation' AND n.nspname='s0_mv' ORDER BY c.oid`, pid)); err != nil {
			return err
		}
		_ = tx.Rollback()
		_ = conn.Close()
		if err = printQuery(ctx, r.db, "matview_definitions", `SELECT c.relname,pg_get_viewdef(c.oid,true) definition,
length(r.ev_action::text) node_bytes,left(r.ev_action::text,240) node_prefix
FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN'
JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='s0_mv' AND c.relkind='m' ORDER BY c.oid`); err != nil {
			return err
		}
		if err = printQuery(ctx, r.db, "node_format_probe", `SELECT c.relname,
substring(r.ev_action::text from strpos(r.ev_action::text,'{TARGETENTRY') for 520) target_fragment,
substring(r.ev_action::text from strpos(r.ev_action::text,'{RANGETBLENTRY') for 420) rte_fragment
FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN'
JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='s0_mv' AND c.relkind='m' ORDER BY c.oid`); err != nil {
			return err
		}
		if err = printQuery(ctx, r.db, "definition_relation_closure", `WITH RECURSIVE cl(relid) AS (
VALUES('s0_mv.mv2'::regclass) UNION SELECT d.refobjid FROM cl c
JOIN pg_rewrite r ON r.ev_class=c.relid AND r.rulename='_RETURN'
JOIN pg_depend d ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid AND d.refclassid='pg_class'::regclass AND d.deptype='n'
JOIN pg_class rc ON rc.oid=d.refobjid WHERE d.refobjid<>c.relid AND rc.relkind IN('r','p','v','m','f'))
SELECT cl.relid::bigint,cl.relid::regclass::text,pc.relkind FROM cl JOIN pg_class pc ON pc.oid=cl.relid ORDER BY cl.relid`); err != nil {
			return err
		}
		if err = printQuery(ctx, r.db, "rule_source_column_dependencies", `SELECT owner.relname,src.relname,d.refobjsubid,a.attname
FROM pg_class owner JOIN pg_rewrite r ON r.ev_class=owner.oid AND r.rulename='_RETURN'
JOIN pg_depend d ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid AND d.refclassid='pg_class'::regclass AND d.refobjsubid>0
JOIN pg_class src ON src.oid=d.refobjid JOIN pg_attribute a ON a.attrelid=d.refobjid AND a.attnum=d.refobjsubid
JOIN pg_namespace n ON n.oid=owner.relnamespace WHERE n.nspname='s0_mv' ORDER BY owner.oid,src.oid,d.refobjsubid`); err != nil {
			return err
		}
		if err = printEvActionLineage(ctx, r.db); err != nil {
			return err
		}

		// Lock the recursive definition closure in OID order and verify all are held.
		rows, err := r.db.QueryContext(ctx, `WITH RECURSIVE cl(relid) AS (VALUES('s0_mv.mv2'::regclass) UNION SELECT d.refobjid FROM cl c
JOIN pg_rewrite r ON r.ev_class=c.relid AND r.rulename='_RETURN' JOIN pg_depend d ON d.classid='pg_rewrite'::regclass AND d.objid=r.oid
AND d.refclassid='pg_class'::regclass AND d.deptype='n' JOIN pg_class rc ON rc.oid=d.refobjid
WHERE d.refobjid<>c.relid AND rc.relkind IN('r','p','v','m','f'))
SELECT pc.oid::bigint,format('%I.%I',pn.nspname,pc.relname),pc.relkind FROM cl JOIN pg_class pc ON pc.oid=cl.relid JOIN pg_namespace pn ON pn.oid=pc.relnamespace ORDER BY pc.oid`)
		if err != nil {
			return err
		}
		var rels []relOID
		for rows.Next() {
			var o relOID
			if err = rows.Scan(&o.oid, &o.name, &o.kind); err != nil {
				return err
			}
			rels = append(rels, o)
		}
		_ = rows.Close()
		q := make([]string, len(rels))
		for i, o := range rels {
			q[i] = o.name
		}
		lc, _ := r.db.Conn(ctx)
		ltx, _ := lc.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		var lpid int64
		_ = ltx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&lpid)
		fmt.Printf("definition_lock_sql=LOCK TABLE %s IN ACCESS SHARE MODE\n", strings.Join(q, ","))
		_, aggregateErr := ltx.ExecContext(ctx, "LOCK TABLE "+strings.Join(q, ",")+" IN ACCESS SHARE MODE")
		fmt.Printf("aggregate_lock_including_matviews_error=%v\n", aggregateErr)
		_ = ltx.Rollback()
		_ = lc.Close()
		lc, _ = r.db.Conn(ctx)
		ltx, _ = lc.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		_ = ltx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&lpid)
		for _, o := range rels {
			var stmt string
			if o.kind == "m" {
				stmt = "SELECT 1 FROM " + o.name + " LIMIT 0"
			} else {
				stmt = "LOCK TABLE " + o.name + " IN ACCESS SHARE MODE"
			}
			fmt.Printf("ordered_lock_step oid=%d kind=%s sql=%s\n", o.oid, o.kind, stmt)
			if _, err = ltx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if err = printQuery(ctx, r.db, "locked_definition_closure", fmt.Sprintf(`SELECT c.relname,l.mode,l.granted FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=%d AND n.nspname='s0_mv' ORDER BY c.oid`, lpid)); err != nil {
			return err
		}
		_ = ltx.Rollback()
		_ = lc.Close()
		r.close(ctx)
	}
	return nil
}

type lineageKey struct {
	rel int64
	att int
}

func printEvActionLineage(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT c.oid::bigint,c.relname,r.ev_action::text FROM pg_class c JOIN pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN' JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='s0_mv' AND c.relkind='m' ORDER BY c.oid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	// PG14 serializes this node tag as RTE; PG18 uses RANGETBLENTRY.
	rteStart := regexp.MustCompile(`\{(?:RANGETBLENTRY|RTE) `)
	relidRE := regexp.MustCompile(`:rtekind 0 :relid ([0-9]+)`)
	targetRE := regexp.MustCompile(`(?s)\{TARGETENTRY :expr (.*?) :resno ([0-9]+) :resname `)
	varRE := regexp.MustCompile(`\{VAR :varno ([0-9]+) :varattno (-?[0-9]+)`)
	direct := map[lineageKey][]lineageKey{}
	owners := map[int64]string{}
	for rows.Next() {
		var owner int64
		var ownerName, tree string
		if err = rows.Scan(&owner, &ownerName, &tree); err != nil {
			return err
		}
		owners[owner] = ownerName
		starts := rteStart.FindAllStringIndex(tree, -1)
		rtes := map[int]int64{}
		for i, pos := range starts {
			end := len(tree)
			if i+1 < len(starts) {
				end = starts[i+1][0]
			}
			m := relidRE.FindStringSubmatch(tree[pos[0]:end])
			if len(m) == 2 {
				v, _ := strconv.ParseInt(m[1], 10, 64)
				rtes[i+1] = v
			}
		}
		for _, m := range targetRE.FindAllStringSubmatch(tree, -1) {
			resno, _ := strconv.Atoi(m[2])
			if resno <= 0 {
				continue
			}
			dst := lineageKey{owner, resno}
			for _, v := range varRE.FindAllStringSubmatch(m[1], -1) {
				varno, _ := strconv.Atoi(v[1])
				att, _ := strconv.Atoi(v[2])
				if rel, ok := rtes[varno]; ok && att > 0 {
					direct[dst] = append(direct[dst], lineageKey{rel, att})
				}
			}
		}
	}
	name := func(k lineageKey) string {
		var rn, an string
		_ = db.QueryRowContext(ctx, `SELECT c.relname,a.attname FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=$2 WHERE c.oid=$1`, k.rel, k.att).Scan(&rn, &an)
		if rn == "" {
			rn = fmt.Sprint(k.rel)
		}
		return rn + "." + an
	}
	var keys []lineageKey
	for k := range direct {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].rel == keys[j].rel {
			return keys[i].att < keys[j].att
		}
		return keys[i].rel < keys[j].rel
	})
	fmt.Println("[ev_action_per_output_lineage]")
	for _, k := range keys {
		src := direct[k]
		parts := make([]string, len(src))
		for i, s := range src {
			parts[i] = name(s)
		}
		fmt.Printf("%s -> %s\n", name(k), strings.Join(parts, ","))
	}
	var expand func(lineageKey, map[lineageKey]bool) []lineageKey
	expand = func(k lineageKey, seen map[lineageKey]bool) []lineageKey {
		if seen[k] {
			return nil
		}
		seen[k] = true
		if next, ok := direct[k]; ok {
			var z []lineageKey
			for _, n := range next {
				z = append(z, expand(n, seen)...)
			}
			return z
		}
		return []lineageKey{k}
	}
	for _, k := range keys {
		if owners[k.rel] == "mv2" {
			leaf := expand(k, map[lineageKey]bool{})
			parts := make([]string, len(leaf))
			for i, s := range leaf {
				parts[i] = name(s)
			}
			fmt.Printf("recursive %s -> %s\n", name(k), strings.Join(parts, ","))
		}
	}
	return rows.Err()
}

func experimentImplicit(ctx context.Context) error {
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return err
		}
		fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SHOW server_version"))
		setup := `CREATE SCHEMA s0_implicit;
CREATE TABLE s0_implicit.t(id integer,payload text DEFAULT md5(random()::text),doubled integer GENERATED ALWAYS AS(id*2) STORED,slot int4range,CONSTRAINT t_slot_excl EXCLUDE USING gist(slot WITH &&));
CREATE FUNCTION s0_implicit.t_biu() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.payload:=coalesce(NEW.payload,'triggered'); RETURN NEW; END $$;
CREATE TRIGGER t_biu BEFORE INSERT OR UPDATE ON s0_implicit.t FOR EACH ROW EXECUTE FUNCTION s0_implicit.t_biu();
ALTER TABLE s0_implicit.t ENABLE ROW LEVEL SECURITY; CREATE POLICY t_policy ON s0_implicit.t USING(id>0);
CREATE INDEX t_expr_idx ON s0_implicit.t((lower(payload))); CREATE INDEX t_partial_idx ON s0_implicit.t(id) WHERE id>0;
CREATE VIEW s0_implicit.v AS SELECT id,payload FROM s0_implicit.t;
CREATE FUNCTION s0_implicit.v_ins() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO s0_implicit.t(id,payload) VALUES(NEW.id,NEW.payload); RETURN NEW; END $$;
CREATE TRIGGER v_ins INSTEAD OF INSERT ON s0_implicit.v FOR EACH ROW EXECUTE FUNCTION s0_implicit.v_ins()`
		if err = mustExec(ctx, r.db, setup); err != nil {
			return err
		}
		q := `WITH target(relid) AS (SELECT unnest(ARRAY['s0_implicit.t'::regclass,'s0_implicit.v'::regclass])), findings AS (
SELECT t.tgrelid relid,'trigger'::text kind,t.oid object_oid FROM pg_trigger t JOIN target x ON x.relid=t.tgrelid WHERE NOT t.tgisinternal
UNION ALL SELECT d.adrelid,CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'column_default' END,d.oid FROM pg_attrdef d JOIN target x ON x.relid=d.adrelid JOIN pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum
UNION ALL SELECT p.polrelid,'rls_policy',p.oid FROM pg_policy p JOIN target x ON x.relid=p.polrelid
UNION ALL SELECT rw.ev_class,'view_rule',rw.oid FROM pg_rewrite rw JOIN target x ON x.relid=rw.ev_class JOIN pg_class c ON c.oid=rw.ev_class WHERE c.relkind IN('v','m') AND rw.rulename='_RETURN'
UNION ALL SELECT t.tgrelid,'instead_of_trigger',t.oid FROM pg_trigger t JOIN target x ON x.relid=t.tgrelid WHERE NOT t.tgisinternal AND (t.tgtype::int&64)<>0
UNION ALL SELECT i.indrelid,CASE WHEN i.indexprs IS NOT NULL THEN 'expression_index' ELSE 'partial_index' END,i.indexrelid FROM pg_index i JOIN target x ON x.relid=i.indrelid WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
UNION ALL SELECT c.conrelid,'exclusion_constraint',c.oid FROM pg_constraint c JOIN target x ON x.relid=c.conrelid WHERE c.contype='x')
SELECT kind,count(*) count,string_agg(relid::regclass::text,',' ORDER BY object_oid) relations FROM findings GROUP BY kind ORDER BY kind`
		if err = printQuery(ctx, r.db, "pg_implicit_detector_counts", q); err != nil {
			return err
		}
		r.close(ctx)
	}
	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== %s (%s) ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	setup := `CREATE DATABASE s0_implicit;
CREATE TABLE s0_implicit.t(id integer,payload varchar(64) DEFAULT ('d|x'),payload_len integer GENERATED ALWAYS AS(char_length(payload)) STORED,updated_at timestamp DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP);
CREATE TRIGGER s0_implicit.t_bi BEFORE INSERT ON s0_implicit.t FOR EACH ROW SET NEW.payload=coalesce(NEW.payload,'triggered');
CREATE INDEX t_expr_idx ON s0_implicit.t ((lower(payload)));
CREATE VIEW s0_implicit.v AS SELECT id,payload FROM s0_implicit.t;
CREATE EVENT s0_implicit.ev_touch ON SCHEDULE EVERY 1 DAY DO UPDATE s0_implicit.t SET payload=payload WHERE id=-1`
	if err = mustExec(ctx, r.db, setup); err != nil {
		return err
	}
	q := `WITH roots(schema_name,table_name) AS (SELECT 's0_implicit','t' UNION ALL SELECT 's0_implicit','v'), findings AS (
SELECT tr.EVENT_OBJECT_SCHEMA s,tr.EVENT_OBJECT_TABLE t,'trigger' kind,tr.TRIGGER_NAME o FROM information_schema.TRIGGERS tr JOIN roots r ON r.schema_name=tr.EVENT_OBJECT_SCHEMA AND r.table_name=tr.EVENT_OBJECT_TABLE
UNION ALL SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'column_default',c.COLUMN_NAME FROM information_schema.COLUMNS c JOIN roots r ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME JOIN information_schema.TABLES bt ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME AND bt.TABLE_TYPE='BASE TABLE' WHERE c.COLUMN_DEFAULT IS NOT NULL AND coalesce(c.GENERATION_EXPRESSION,'')=''
UNION ALL SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'generated_column',c.COLUMN_NAME FROM information_schema.COLUMNS c JOIN roots r ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME JOIN information_schema.TABLES bt ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME AND bt.TABLE_TYPE='BASE TABLE' WHERE coalesce(c.GENERATION_EXPRESSION,'')<>''
UNION ALL SELECT c.TABLE_SCHEMA,c.TABLE_NAME,'on_update_expression',c.COLUMN_NAME FROM information_schema.COLUMNS c JOIN roots r ON r.schema_name=c.TABLE_SCHEMA AND r.table_name=c.TABLE_NAME JOIN information_schema.TABLES bt ON bt.TABLE_SCHEMA=c.TABLE_SCHEMA AND bt.TABLE_NAME=c.TABLE_NAME AND bt.TABLE_TYPE='BASE TABLE' WHERE lower(c.EXTRA) LIKE '%on update%'
UNION ALL SELECT v.TABLE_SCHEMA,v.TABLE_NAME,'view',v.TABLE_NAME FROM information_schema.VIEWS v JOIN roots r ON r.schema_name=v.TABLE_SCHEMA AND r.table_name=v.TABLE_NAME
UNION ALL SELECT s.TABLE_SCHEMA,s.TABLE_NAME,'expression_index',s.INDEX_NAME FROM information_schema.STATISTICS s JOIN roots r ON r.schema_name=s.TABLE_SCHEMA AND r.table_name=s.TABLE_NAME WHERE s.EXPRESSION IS NOT NULL
UNION ALL SELECT e.EVENT_SCHEMA,'*','event',e.EVENT_NAME FROM information_schema.EVENTS e WHERE e.EVENT_SCHEMA IN(SELECT DISTINCT schema_name FROM roots))
SELECT kind,count(*) count,group_concat(concat(s,'.',t,'.',o) ORDER BY o SEPARATOR ',') objects FROM findings GROUP BY kind ORDER BY kind`
	if err = printQuery(ctx, r.db, "mysql_implicit_detector_counts", q); err != nil {
		return err
	}
	return printQuery(ctx, r.db, "mysql_functional_index_catalog", `SELECT TABLE_SCHEMA,TABLE_NAME,INDEX_NAME,EXPRESSION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='s0_implicit' AND EXPRESSION IS NOT NULL`)
}

func experimentMySQLBackup(ctx context.Context) error {
	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== %s (%s) backup lock DDL matrix ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	setup := `CREATE DATABASE s0_backup; CREATE TABLE s0_backup.t(id int primary key,v int); CREATE TABLE s0_backup.t_trigger_new(id int primary key,v int);
CREATE VIEW s0_backup.v_replace AS SELECT id FROM s0_backup.t; CREATE VIEW s0_backup.v_alter AS SELECT id FROM s0_backup.t; CREATE VIEW s0_backup.v_drop AS SELECT id FROM s0_backup.t; CREATE VIEW s0_backup.v_rename AS SELECT id FROM s0_backup.t;
CREATE PROCEDURE s0_backup.p_alter() SELECT 1; CREATE PROCEDURE s0_backup.p_drop() SELECT 1;
CREATE FUNCTION s0_backup.f_alter() RETURNS INT DETERMINISTIC RETURN 1; CREATE FUNCTION s0_backup.f_drop() RETURNS INT DETERMINISTIC RETURN 1;
CREATE TRIGGER s0_backup.tr_drop BEFORE INSERT ON s0_backup.t FOR EACH ROW SET NEW.v=coalesce(NEW.v,0);
CREATE EVENT s0_backup.ev_alter ON SCHEDULE EVERY 1 DAY DO SELECT 1; CREATE EVENT s0_backup.ev_drop ON SCHEDULE EVERY 1 DAY DO SELECT 1`
	if err = mustExec(ctx, r.db, setup); err != nil {
		return err
	}
	_, pluginSetupErr := r.db.ExecContext(ctx, `INSTALL PLUGIN rewriter SONAME 'rewriter.so'`)
	_, componentSetupErr := r.db.ExecContext(ctx, `INSTALL COMPONENT 'file://component_validate_password'`)
	fmt.Printf("preinstall plugin_rewriter=%v component_validate_password=%v\n", pluginSetupErr, componentSetupErr)
	locker, _ := r.db.Conn(ctx)
	defer locker.Close()
	if _, err = locker.ExecContext(ctx, "LOCK INSTANCE FOR BACKUP"); err != nil {
		return fmt.Errorf("backup lock: %w", err)
	}
	defer locker.ExecContext(context.Background(), "UNLOCK INSTANCE")
	type ddlCase struct{ class, name, sql string }
	cases := []ddlCase{
		{"VIEW", "create", `CREATE VIEW s0_backup.v_create AS SELECT id FROM s0_backup.t`},
		{"VIEW", "replace", `CREATE OR REPLACE VIEW s0_backup.v_replace AS SELECT id,v FROM s0_backup.t`},
		{"VIEW", "alter", `ALTER VIEW s0_backup.v_alter AS SELECT id,v FROM s0_backup.t`},
		{"VIEW", "drop", `DROP VIEW s0_backup.v_drop`},
		{"VIEW", "rename", `RENAME TABLE s0_backup.v_rename TO s0_backup.v_renamed`},
		{"PROCEDURE", "create", `CREATE PROCEDURE s0_backup.p_create() SELECT 1`},
		{"PROCEDURE", "alter", `ALTER PROCEDURE s0_backup.p_alter COMMENT 'changed'`},
		{"PROCEDURE", "drop", `DROP PROCEDURE s0_backup.p_drop`},
		{"FUNCTION", "create_stored", `CREATE FUNCTION s0_backup.f_create() RETURNS INT DETERMINISTIC RETURN 1`},
		{"FUNCTION", "alter_stored", `ALTER FUNCTION s0_backup.f_alter COMMENT 'changed'`},
		{"FUNCTION", "drop_stored", `DROP FUNCTION s0_backup.f_drop`},
		{"TRIGGER", "create", `CREATE TRIGGER s0_backup.tr_create BEFORE INSERT ON s0_backup.t_trigger_new FOR EACH ROW SET NEW.v=coalesce(NEW.v,0)`},
		{"TRIGGER", "alter_syntax", `ALTER TRIGGER s0_backup.tr_drop ENABLE`},
		{"TRIGGER", "drop", `DROP TRIGGER s0_backup.tr_drop`},
		{"EVENT", "create", `CREATE EVENT s0_backup.ev_create ON SCHEDULE EVERY 1 DAY DO SELECT 1`},
		{"EVENT", "alter", `ALTER EVENT s0_backup.ev_alter COMMENT 'changed'`},
		{"EVENT", "drop", `DROP EVENT s0_backup.ev_drop`},
		{"TABLE", "rename", `RENAME TABLE s0_backup.t TO s0_backup.t_renamed`},
		{"TEMPORARY_TABLE", "create", `CREATE TEMPORARY TABLE s0_backup.tmp(id int)`},
		{"UDF", "install_missing_so", `CREATE FUNCTION s0_udf_missing RETURNS STRING SONAME 's0_missing_udf.so'`},
		{"PLUGIN", "install_missing_so", `INSTALL PLUGIN s0_missing_plugin SONAME 's0_missing_plugin.so'`},
		{"COMPONENT", "install_log_sink_json", `INSTALL COMPONENT 'file://component_log_sink_json'`},
		{"UDF", "drop_missing", `DROP FUNCTION s0_udf_missing`},
		{"PLUGIN", "uninstall_rewriter", `UNINSTALL PLUGIN rewriter`},
		{"COMPONENT", "uninstall_validate_password", `UNINSTALL COMPONENT 'file://component_validate_password'`},
	}
	fmt.Println("class\toperation\tresult\terror_no\telapsed_ms\tmessage")
	for _, c := range cases {
		ddl, e := r.db.Conn(ctx)
		if e != nil {
			return e
		}
		if _, e = ddl.ExecContext(ctx, "SET SESSION lock_wait_timeout=1"); e != nil {
			return e
		}
		caseCtx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
		start := time.Now()
		_, e = ddl.ExecContext(caseCtx, c.sql)
		elapsed := time.Since(start)
		cancel()
		_ = ddl.Close()
		result := "allowed_success"
		num := uint16(0)
		msg := "OK"
		if e != nil {
			result = "allowed_past_gate_error"
			msg = e.Error()
			var me *mysqlDriver.MySQLError
			if errors.As(e, &me) {
				num = me.Number
				if num == 1205 || num == 1880 {
					result = "blocked_by_backup_lock"
				}
			}
		}
		if errors.Is(e, context.DeadlineExceeded) {
			result = "blocked_client_timeout"
		}
		msg = strings.ReplaceAll(msg, "\t", " ")
		msg = strings.ReplaceAll(msg, "\n", " ")
		fmt.Printf("%s\t%s\t%s\t%d\t%d\t%s\n", c.class, c.name, result, num, elapsed.Milliseconds(), msg)
	}
	if _, err = locker.ExecContext(ctx, "UNLOCK INSTANCE"); err != nil {
		return err
	}
	fmt.Println("[post_unlock_controls]")
	for _, c := range cases[len(cases)-6:] {
		start := time.Now()
		_, e := r.db.ExecContext(ctx, c.sql)
		num := uint16(0)
		if e != nil {
			var me *mysqlDriver.MySQLError
			if errors.As(e, &me) {
				num = me.Number
			}
		}
		fmt.Printf("%s\t%s\terror_no=%d\telapsed_ms=%d\t%v\n", c.class, c.name, num, time.Since(start).Milliseconds(), e)
	}
	return nil
}

func firstRowMap(ctx context.Context, db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err = rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, v := range vals {
		if v == nil {
			out[cols[i]] = ""
		} else if b, ok := v.([]byte); ok {
			out[cols[i]] = string(b)
		} else {
			out[cols[i]] = fmt.Sprint(v)
		}
	}
	return out, nil
}

func openMySQLUser(ctx context.Context, rootDSN, user, password string) (*sql.DB, error) {
	dsn := strings.Replace(rootDSN, "root:spike@", user+":"+password+"@", 1)
	dsn = strings.Replace(dsn, "/spike?", "/?", 1)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func experimentMySQLChange(ctx context.Context) error {
	r, err := startMySQL(ctx, "--log-bin-trust-function-creators=ON")
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== %s (%s) GTID/binlog/visibility/epoch ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	setup := `CREATE DATABASE s0_change;CREATE TABLE s0_change.t(id int primary key,v int);CREATE VIEW s0_change.v AS SELECT id,v FROM s0_change.t;
CREATE TRIGGER s0_change.tr_bi BEFORE INSERT ON s0_change.t FOR EACH ROW SET NEW.v=coalesce(NEW.v,0);
CREATE PROCEDURE s0_change.p() SELECT 1;CREATE EVENT s0_change.ev ON SCHEDULE EVERY 1 DAY DO SELECT 1`
	if err = mustExec(ctx, r.db, setup); err != nil {
		return err
	}
	pre, err := firstRowMap(ctx, r.db, "SHOW BINARY LOG STATUS")
	if err != nil {
		return err
	}
	preGTID := scalar(ctx, r.db, "SELECT @@GLOBAL.gtid_executed")
	fmt.Printf("fpre file=%s pos=%s gtid=%s\n", pre["File"], pre["Position"], preGTID)
	ddl, _ := r.db.Conn(ctx)
	if _, err = ddl.ExecContext(ctx, "CREATE VIEW s0_change.v_after_fpre AS SELECT id FROM s0_change.t"); err != nil {
		return err
	}
	_ = ddl.Close()
	post, err := firstRowMap(ctx, r.db, "SHOW BINARY LOG STATUS")
	if err != nil {
		return err
	}
	postGTID := scalar(ctx, r.db, "SELECT @@GLOBAL.gtid_executed")
	fmt.Printf("fpost file=%s pos=%s gtid=%s\n", post["File"], post["Position"], postGTID)
	prePos, _ := strconv.ParseInt(pre["Position"], 10, 64)
	postPos, _ := strconv.ParseInt(post["Position"], 10, 64)
	eventSQL := fmt.Sprintf("SHOW BINLOG EVENTS IN '%s' FROM %d LIMIT 1000", strings.ReplaceAll(pre["File"], "'", "''"), prePos)
	if err = printQuery(ctx, r.db, "watcher_events_after_fpre", eventSQL); err != nil {
		return err
	}
	eRows, err := r.db.QueryContext(ctx, eventSQL)
	if err != nil {
		return err
	}
	var seen []string
	maxPos := prePos
	gtidRE := regexp.MustCompile(`(?i)GTID_NEXT= '([^']+)'`)
	for eRows.Next() {
		var logName, pos, eventType, serverID, endPos, info any
		if err = eRows.Scan(&logName, &pos, &eventType, &serverID, &endPos, &info); err != nil {
			return err
		}
		asString := func(v any) string {
			if b, ok := v.([]byte); ok {
				return string(b)
			}
			return fmt.Sprint(v)
		}
		p, _ := strconv.ParseInt(asString(endPos), 10, 64)
		if p > maxPos {
			maxPos = p
		}
		s := asString(info)
		m := gtidRE.FindStringSubmatch(s)
		if len(m) == 2 {
			seen = append(seen, m[1])
		}
	}
	_ = eRows.Close()
	watcherSet := preGTID
	if len(seen) > 0 {
		if watcherSet != "" {
			watcherSet += ","
		}
		watcherSet += strings.Join(seen, ",")
	}
	var subset int
	if err = r.db.QueryRowContext(ctx, "SELECT GTID_SUBSET(?,?)", postGTID, watcherSet).Scan(&subset); err != nil {
		return err
	}
	fmt.Printf("watcher_checkpoint end_pos=%d target_pos=%d pos_reached=%v seen_new_gtids=%v gtid_subset=%d\n", maxPos, postPos, maxPos >= postPos, seen, subset)

	users := `CREATE USER 'u_none'@'%' IDENTIFIED BY 'p';CREATE USER 'u_select'@'%' IDENTIFIED BY 'p';CREATE USER 'u_show'@'%' IDENTIFIED BY 'p';CREATE USER 'u_full'@'%' IDENTIFIED BY 'p';CREATE USER 'u_watcher'@'%' IDENTIFIED BY 'p';
GRANT SELECT ON s0_change.* TO 'u_select'@'%';
GRANT SELECT,SHOW VIEW ON s0_change.* TO 'u_show'@'%';GRANT SHOW_ROUTINE ON *.* TO 'u_show'@'%';
GRANT SELECT,SHOW VIEW,TRIGGER,EVENT ON s0_change.* TO 'u_full'@'%';GRANT SHOW_ROUTINE ON *.* TO 'u_full'@'%';
GRANT REPLICATION SLAVE,REPLICATION CLIENT ON *.* TO 'u_watcher'@'%'`
	if err = mustExec(ctx, r.db, users); err != nil {
		return err
	}
	visibility := `SELECT
(SELECT count(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA='s0_change') triggers,
(SELECT count(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='s0_change') views,
(SELECT count(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='s0_change') routines,
(SELECT count(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='s0_change') events,
(SELECT count(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='s0_change' AND VIEW_DEFINITION IS NOT NULL) view_defs,
(SELECT count(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA='s0_change' AND ROUTINE_DEFINITION IS NOT NULL) routine_defs,
(SELECT count(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='s0_change' AND EVENT_DEFINITION IS NOT NULL) event_defs`
	for _, u := range []string{"u_none", "u_select", "u_show", "u_full"} {
		udb, e := openMySQLUser(ctx, r.dsn, u, "p")
		if e != nil {
			return e
		}
		if e = printQuery(ctx, udb, "visibility_"+u, visibility); e != nil {
			return e
		}
		_ = udb.Close()
	}
	full, err := openMySQLUser(ctx, r.dsn, "u_full", "p")
	if err != nil {
		return err
	}
	_, trErr := full.ExecContext(ctx, `CREATE TRIGGER s0_change.tr_by_inspector BEFORE UPDATE ON s0_change.t FOR EACH ROW SET NEW.v=NEW.v`)
	_, evErr := full.ExecContext(ctx, `CREATE EVENT s0_change.ev_by_inspector ON SCHEDULE EVERY 1 DAY DO SELECT 1`)
	_, viewErr := full.ExecContext(ctx, `CREATE VIEW s0_change.v_by_inspector AS SELECT id FROM s0_change.t`)
	_, routineErr := full.ExecContext(ctx, `CREATE PROCEDURE s0_change.p_by_inspector() SELECT 1`)
	fmt.Printf("full_metadata_role_ddl trigger=%v event=%v view=%v routine=%v\n", trErr, evErr, viewErr, routineErr)
	_ = full.Close()
	watcher, err := openMySQLUser(ctx, r.dsn, "u_watcher", "p")
	if err != nil {
		return err
	}
	_, statusErr := firstRowMap(ctx, watcher, "SHOW BINARY LOG STATUS")
	_, eventsErr := firstRowMap(ctx, watcher, fmt.Sprintf("SHOW BINLOG EVENTS IN '%s' FROM %d LIMIT 1", pre["File"], prePos))
	fmt.Printf("watcher_min_grants status_error=%v events_error=%v\n", statusErr, eventsErr)
	_ = watcher.Close()

	if err = mustExec(ctx, r.db, `CREATE TABLE s0_change.ddl_epoch(id int primary key,epoch bigint not null);INSERT INTO s0_change.ddl_epoch VALUES(1,0)`); err != nil {
		return err
	}
	conn, _ := r.db.Conn(ctx)
	if _, err = conn.ExecContext(ctx, `START TRANSACTION;CREATE VIEW s0_change.v_gap AS SELECT id FROM s0_change.t`); err != nil {
		return err
	}
	_ = conn.Close()
	fmt.Printf("epoch_case_after_ddl_then_rollback view_exists=%s epoch=%s\n", scalar(ctx, r.db, "SELECT count(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA='s0_change' AND TABLE_NAME='v_gap'"), scalar(ctx, r.db, "SELECT epoch FROM s0_change.ddl_epoch WHERE id=1"))
	conn, _ = r.db.Conn(ctx)
	_, failErr := conn.ExecContext(ctx, `START TRANSACTION;UPDATE s0_change.ddl_epoch SET epoch=2 WHERE id=1;CREATE VIEW s0_change.v_gap AS SELECT v FROM s0_change.t`)
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	_ = conn.Close()
	fmt.Printf("epoch_case_bump_then_failed_ddl ddl_error=%v epoch=%s\n", failErr, scalar(ctx, r.db, "SELECT epoch FROM s0_change.ddl_epoch WHERE id=1"))
	return nil
}

type canonicalField struct {
	tag   byte
	value *string
}

func canonicalEncode(fields ...canonicalField) []byte {
	out := binary.AppendUvarint(nil, uint64(len(fields)))
	for _, f := range fields {
		out = append(out, f.tag)
		if f.value == nil {
			out = append(out, 0)
			continue
		}
		out = append(out, 1)
		b := []byte(*f.value)
		out = binary.AppendUvarint(out, uint64(len(b)))
		out = append(out, b...)
	}
	return out
}

func caseProbe(ctx context.Context, extra string) error {
	args := []string{}
	if extra != "" {
		args = append(args, extra)
	}
	r, err := startMySQL(ctx, args...)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("[case_probe requested=%s actual=%s version=%s]\n", extra, scalar(ctx, r.db, "SELECT @@lower_case_table_names"), scalar(ctx, r.db, "SELECT VERSION()"))
	if err = mustExec(ctx, r.db, "CREATE DATABASE `CaseDb`;CREATE TABLE `CaseDb`.`CaseTbl`(id int)"); err != nil {
		return err
	}
	for _, q := range []string{"SELECT count(*) FROM `CaseDb`.`CaseTbl`", "SELECT count(*) FROM `casedb`.`casetbl`"} {
		var n int
		e := r.db.QueryRowContext(ctx, q).Scan(&n)
		fmt.Printf("query=%q count=%d error=%v\n", q, n, e)
	}
	return printQuery(ctx, r.db, "case_catalog", `SELECT TABLE_SCHEMA,TABLE_NAME,
(BINARY TABLE_SCHEMA=BINARY 'CaseDb' AND BINARY TABLE_NAME=BINARY 'CaseTbl') binary_original,
(BINARY TABLE_SCHEMA=BINARY 'casedb' AND BINARY TABLE_NAME=BINARY 'casetbl') binary_lower
FROM information_schema.TABLES WHERE lower(TABLE_SCHEMA)='casedb' AND lower(TABLE_NAME)='casetbl'`)
}

func experimentCanonical(ctx context.Context) error {
	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("=== %s (%s) canonical/identifier ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	if err = printQuery(ctx, r.db, "concat_ws_collisions", `SELECT
CONCAT_WS('|','a|b','c') a,CONCAT_WS('|','a','b|c') b,CONCAT_WS('|','a|b','c')=CONCAT_WS('|','a','b|c') separator_collision,
CONCAT_WS('|','a',NULL,'b') c,CONCAT_WS('|','a','b',NULL) d,CONCAT_WS('|','a',NULL,'b')=CONCAT_WS('|','a','b',NULL) null_collision,
CONCAT_WS('|','expr:x|y','中文😀')=CONCAT_WS('|','expr:x','y|中文😀') multibyte_collision`); err != nil {
		return err
	}
	if err = mustExec(ctx, r.db, "CREATE DATABASE s0_names CHARACTER SET utf8mb4"); err != nil {
		return err
	}
	cn64 := strings.Repeat("表", 64)
	_, cn64Err := r.db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE s0_names.`%s`(id int)", cn64))
	fmt.Printf("chinese64_identifier_create_error=%v\n", cn64Err)
	cn := strings.Repeat("表", 30)
	if _, err = r.db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE s0_names.`%s`(id int)", cn)); err != nil {
		return err
	}
	if err = printQuery(ctx, r.db, "multibyte_identifier_lengths", `SELECT CHAR_LENGTH(TABLE_NAME) chars,LENGTH(TABLE_NAME) utf8_bytes FROM information_schema.TABLES WHERE TABLE_SCHEMA='s0_names'`); err != nil {
		return err
	}
	if err = mustExec(ctx, r.db, "CREATE TEMPORARY TABLE identifier_key(name VARBINARY(64))"); err != nil {
		return err
	}
	_, varbinErr := r.db.ExecContext(ctx, `INSERT INTO identifier_key SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='s0_names'`)
	fmt.Printf("varbinary64_insert_error=%v\n", varbinErr)
	emojiName := "表😀"
	_, emojiErr := r.db.ExecContext(ctx, "CREATE TABLE s0_names.`"+emojiName+"`(id int)")
	fmt.Printf("emoji_identifier_create_error=%v\n", emojiErr)
	r.close(ctx)
	a, b, c, ab, bc, expr1, expr2, mb, ymb := "a", "b", "c", "a|b", "b|c", "expr:x|y", "expr:x", "中文😀", "y|中文😀"
	encoded := [][]byte{
		canonicalEncode(canonicalField{1, &ab}, canonicalField{2, &c}), canonicalEncode(canonicalField{1, &a}, canonicalField{2, &bc}),
		canonicalEncode(canonicalField{1, &a}, canonicalField{2, nil}, canonicalField{1, &b}), canonicalEncode(canonicalField{1, &a}, canonicalField{2, &b}, canonicalField{1, nil}),
		canonicalEncode(canonicalField{3, &expr1}, canonicalField{1, &mb}), canonicalEncode(canonicalField{3, &expr2}, canonicalField{1, &ymb}),
	}
	for i, e := range encoded {
		fmt.Printf("canonical_%d=%s\n", i+1, hex.EncodeToString(e))
	}
	fmt.Printf("encoded_collision_checks separator=%v null=%v expression=%v\n", string(encoded[0]) == string(encoded[1]), string(encoded[2]) == string(encoded[3]), string(encoded[4]) == string(encoded[5]))
	if err = caseProbe(ctx, "--lower-case-table-names=0"); err != nil {
		return err
	}
	if err = caseProbe(ctx, "--lower-case-table-names=1"); err != nil {
		return err
	}
	if err = caseProbe(ctx, "--lower-case-table-names=2"); err != nil {
		return err
	}
	return nil
}

func mysqlErrNo(err error) uint16 {
	var me *mysqlDriver.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

func experimentMySQLCancel(ctx context.Context) error {
	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== %s (%s) KILL QUERY ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	setup := `CREATE DATABASE s0_cancel;CREATE TABLE s0_cancel.t(id int primary key,v int);INSERT INTO s0_cancel.t VALUES(1,0),(2,0);
CREATE PROCEDURE s0_cancel.p_stream() BEGIN SELECT 1 AS first_result;SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0;END;
CREATE USER 'app'@'%' IDENTIFIED BY 'p';CREATE USER 'kill_none'@'%' IDENTIFIED BY 'p';CREATE USER 'kill_process'@'%' IDENTIFIED BY 'p';CREATE USER 'kill_admin'@'%' IDENTIFIED BY 'p';
GRANT SELECT,UPDATE,EXECUTE ON s0_cancel.* TO 'app'@'%';GRANT PROCESS ON *.* TO 'kill_process'@'%';GRANT CONNECTION_ADMIN ON *.* TO 'kill_admin'@'%'`
	if err = mustExec(ctx, r.db, setup); err != nil {
		return err
	}
	users := map[string]*sql.DB{}
	for _, u := range []string{"app", "kill_none", "kill_process", "kill_admin"} {
		d, e := openMySQLUser(ctx, r.dsn, u, "p")
		if e != nil {
			return e
		}
		users[u] = d
		defer d.Close()
	}
	testKill := func(label string, killer *sql.DB) error {
		target, e := users["app"].Conn(ctx)
		if e != nil {
			return e
		}
		defer target.Close()
		var id int64
		if e = target.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); e != nil {
			return e
		}
		done := make(chan error, 1)
		go func() { _, x := target.ExecContext(ctx, "SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0"); done <- x }()
		time.Sleep(200 * time.Millisecond)
		_, killErr := killer.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id))
		if killErr != nil {
			_, _ = r.db.ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id))
		}
		queryErr := <-done
		fmt.Printf("kill_privilege case=%s kill_error_no=%d kill_error=%v target_error_no=%d target_error=%v\n", label, mysqlErrNo(killErr), killErr, mysqlErrNo(queryErr), queryErr)
		return nil
	}
	if err = testKill("different_no_grant", users["kill_none"]); err != nil {
		return err
	}
	if err = testKill("different_PROCESS", users["kill_process"]); err != nil {
		return err
	}
	if err = testKill("different_CONNECTION_ADMIN", users["kill_admin"]); err != nil {
		return err
	}
	if err = testKill("same_account_aux", users["app"]); err != nil {
		return err
	}

	target, err := users["app"].Conn(ctx)
	if err != nil {
		return err
	}
	defer target.Close()
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE s0_cancel.t SET v=1 WHERE id=1"); err != nil {
		return err
	}
	var id int64
	_ = tx.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	done := make(chan error, 1)
	go func() { _, e := tx.ExecContext(ctx, "SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0"); done <- e }()
	time.Sleep(200 * time.Millisecond)
	_, err = users["app"].ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", id))
	if err != nil {
		return err
	}
	cancelErr := <-done
	var activeTrx, targetV, observerV int
	stateErr := r.db.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.INNODB_TRX WHERE trx_mysql_thread_id=?", id).Scan(&activeTrx)
	targetReadErr := tx.QueryRowContext(ctx, "SELECT v FROM s0_cancel.t WHERE id=1").Scan(&targetV)
	observerReadErr := r.db.QueryRowContext(ctx, "SELECT v FROM s0_cancel.t WHERE id=1").Scan(&observerV)
	fmt.Printf("after_cancel error_no=%d active_innodb_trx=%d state_error=%v same_connection_id=%d target_sees=%d target_read_error=%v observer_sees=%d observer_read_error=%v\n", mysqlErrNo(cancelErr), activeTrx, stateErr, id, targetV, targetReadErr, observerV, observerReadErr)
	rollbackErr := tx.Rollback()
	var stillID int64
	reuseErr := target.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&stillID)
	var activeAfter int
	_ = r.db.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.INNODB_TRX WHERE trx_mysql_thread_id=?", id).Scan(&activeAfter)
	fmt.Printf("rollback_error=%v reuse_error=%v reused_connection_id=%d active_trx_after_rollback=%d\n", rollbackErr, reuseErr, stillID, activeAfter)

	stream, err := users["app"].Conn(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	var streamID int64
	_ = stream.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&streamID)
	type queryResult struct {
		rows *sql.Rows
		err  error
	}
	qdone := make(chan queryResult, 1)
	go func() {
		rows, e := stream.QueryContext(ctx, "SELECT id FROM s0_cancel.t WHERE SLEEP(30)=0")
		qdone <- queryResult{rows, e}
	}()
	time.Sleep(200 * time.Millisecond)
	_, killErr := users["app"].ExecContext(ctx, fmt.Sprintf("KILL QUERY %d", streamID))
	qr := <-qdone
	var rowsErr, closeErr error
	if qr.rows != nil {
		for qr.rows.Next() {
		}
		rowsErr = qr.rows.Err()
		closeErr = qr.rows.Close()
	}
	var after int
	afterErr := stream.QueryRowContext(ctx, "SELECT 1").Scan(&after)
	fmt.Printf("result_release kill_error=%v query_error_no=%d query_error=%v rows_error=%v close_error=%v reuse_after_return_and_close=%v value=%d\n", killErr, mysqlErrNo(qr.err), qr.err, rowsErr, closeErr, afterErr, after)
	return nil
}

func percentileMicros(samples []time.Duration, p float64) int64 {
	x := append([]time.Duration(nil), samples...)
	sort.Slice(x, func(i, j int) bool { return x[i] < x[j] })
	idx := int(float64(len(x))*p+0.999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(x) {
		idx = len(x) - 1
	}
	return x[idx].Microseconds()
}

func printLatency(label string, s []time.Duration) {
	fmt.Printf("latency %s n=%d p50_us=%d p95_us=%d p99_us=%d\n", label, len(s), percentileMicros(s, .50), percentileMicros(s, .95), percentileMicros(s, .99))
}

func experimentLatency(ctx context.Context) error {
	const warmup = 50
	const samples = 500
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return err
		}
		fmt.Printf("=== latency %s (%s) ===\n", r.image, scalar(ctx, r.db, "SHOW server_version"))
		if err = mustExec(ctx, r.db, `CREATE SCHEMA s0_latency;CREATE TABLE s0_latency.t1(id int);CREATE TABLE s0_latency.t2(id int);CREATE VIEW s0_latency.v AS SELECT t1.id FROM s0_latency.t1`); err != nil {
			return err
		}
		conn, _ := r.db.Conn(ctx)
		var totals, locks, catalogs []time.Duration
		for i := 0; i < warmup+samples; i++ {
			start := time.Now()
			tx, e := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if e != nil {
				return e
			}
			ls := time.Now()
			_, e = tx.ExecContext(ctx, "LOCK TABLE s0_latency.t1,s0_latency.t2,s0_latency.v IN ACCESS SHARE MODE")
			ld := time.Since(ls)
			if e != nil {
				return e
			}
			cs := time.Now()
			var n int
			e = tx.QueryRowContext(ctx, `WITH target(relid) AS (SELECT unnest(ARRAY['s0_latency.t1'::regclass,'s0_latency.t2'::regclass,'s0_latency.v'::regclass])) SELECT count(*) FROM (SELECT t.oid FROM pg_trigger t JOIN target x ON x.relid=t.tgrelid UNION ALL SELECT d.oid FROM pg_attrdef d JOIN target x ON x.relid=d.adrelid UNION ALL SELECT p.oid FROM pg_policy p JOIN target x ON x.relid=p.polrelid UNION ALL SELECT rw.oid FROM pg_rewrite rw JOIN target x ON x.relid=rw.ev_class UNION ALL SELECT ix.indexrelid FROM pg_index ix JOIN target x ON x.relid=ix.indrelid UNION ALL SELECT c.oid FROM pg_constraint c JOIN target x ON c.conrelid=x.relid OR c.confrelid=x.relid) f`).Scan(&n)
			cd := time.Since(cs)
			if e != nil {
				return e
			}
			if e = tx.Rollback(); e != nil {
				return e
			}
			td := time.Since(start)
			if i >= warmup {
				totals = append(totals, td)
				locks = append(locks, ld)
				catalogs = append(catalogs, cd)
			}
		}
		printLatency(r.image+"_full_4rt", totals)
		printLatency(r.image+"_lock_rtt", locks)
		printLatency(r.image+"_catalog_rtt", catalogs)
		_ = conn.Close()
		r.close(ctx)
	}
	r, err := startMySQL(ctx)
	if err != nil {
		return err
	}
	defer r.close(ctx)
	fmt.Printf("=== latency %s (%s) ===\n", r.image, scalar(ctx, r.db, "SELECT VERSION()"))
	if err = mustExec(ctx, r.db, "CREATE DATABASE s0_latency;CREATE TABLE s0_latency.t1(id int);CREATE TABLE s0_latency.t2(id int);CREATE VIEW s0_latency.v AS SELECT id FROM s0_latency.t1"); err != nil {
		return err
	}
	conn, _ := r.db.Conn(ctx)
	defer conn.Close()
	var totals, locks, catalogs []time.Duration
	for i := 0; i < warmup+samples; i++ {
		start := time.Now()
		ls := time.Now()
		_, e := conn.ExecContext(ctx, "LOCK INSTANCE FOR BACKUP")
		ld := time.Since(ls)
		if e != nil {
			return e
		}
		_, e = conn.ExecContext(ctx, "DROP TEMPORARY TABLE IF EXISTS agentsql_catalog_roots")
		if e != nil {
			return e
		}
		_, e = conn.ExecContext(ctx, "CREATE TEMPORARY TABLE agentsql_catalog_roots(schema_name VARBINARY(256),table_name VARBINARY(256))")
		if e != nil {
			return e
		}
		_, e = conn.ExecContext(ctx, "INSERT INTO agentsql_catalog_roots VALUES('s0_latency','t1'),('s0_latency','t2'),('s0_latency','v')")
		if e != nil {
			return e
		}
		cs := time.Now()
		var n int
		e = conn.QueryRowContext(ctx, `SELECT coalesce(sum((SELECT count(*) FROM information_schema.COLUMNS c WHERE c.TABLE_SCHEMA=CONVERT(r.schema_name USING utf8mb4) AND c.TABLE_NAME=CONVERT(r.table_name USING utf8mb4))+(SELECT count(*) FROM information_schema.TRIGGERS tr WHERE tr.EVENT_OBJECT_SCHEMA=CONVERT(r.schema_name USING utf8mb4) AND tr.EVENT_OBJECT_TABLE=CONVERT(r.table_name USING utf8mb4))+(SELECT count(*) FROM information_schema.VIEWS v WHERE v.TABLE_SCHEMA=CONVERT(r.schema_name USING utf8mb4) AND v.TABLE_NAME=CONVERT(r.table_name USING utf8mb4))),0) FROM agentsql_catalog_roots r`).Scan(&n)
		cd := time.Since(cs)
		if e != nil {
			return e
		}
		_, e = conn.ExecContext(ctx, "UNLOCK INSTANCE")
		if e != nil {
			return e
		}
		td := time.Since(start)
		if i >= warmup {
			totals = append(totals, td)
			locks = append(locks, ld)
			catalogs = append(catalogs, cd)
		}
	}
	printLatency("mysql8_full_6rt", totals)
	printLatency("mysql8_backup_lock_rtt", locks)
	printLatency("mysql8_catalog_rtt", catalogs)
	return nil
}

func experimentVersions(ctx context.Context) error {
	for _, tag := range []string{"14", "18"} {
		r, err := startPostgres(ctx, tag)
		if err != nil {
			return fmt.Errorf("start postgres:%s: %w", tag, err)
		}
		fmt.Printf("image=%s server_version=%s server_version_num=%s\n", r.image,
			scalar(ctx, r.db, "SHOW server_version"), scalar(ctx, r.db, "SHOW server_version_num"))
		r.close(ctx)
	}
	r, err := startMySQL(ctx)
	if err != nil {
		return fmt.Errorf("start mysql:8: %w", err)
	}
	defer r.close(ctx)
	fmt.Printf("image=%s version=%s version_comment=%s lower_case_table_names=%s gtid_mode=%s log_bin=%s\n",
		r.image, scalar(ctx, r.db, "SELECT VERSION()"), scalar(ctx, r.db, "SELECT @@version_comment"),
		scalar(ctx, r.db, "SELECT @@lower_case_table_names"), scalar(ctx, r.db, "SELECT @@gtid_mode"),
		scalar(ctx, r.db, "SELECT @@log_bin"))
	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	experiment := flag.String("experiment", "versions", "versions|fk|pg-locks|pg-matview|implicit|mysql-backup|mysql-change|canonical|mysql-cancel|latency")
	flag.Parse()
	if os.Getenv("DOCKER_HOST") == "" {
		_ = os.Setenv("DOCKER_HOST", "npipe:////./pipe/docker_engine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var err error
	switch strings.ToLower(*experiment) {
	case "versions":
		err = experimentVersions(ctx)
	case "fk":
		err = experimentFK(ctx)
	case "pg-locks":
		err = experimentPGLocks(ctx)
	case "pg-matview":
		err = experimentPGMatview(ctx)
	case "implicit":
		err = experimentImplicit(ctx)
	case "mysql-backup":
		err = experimentMySQLBackup(ctx)
	case "mysql-change":
		err = experimentMySQLChange(ctx)
	case "canonical":
		err = experimentCanonical(ctx)
	case "mysql-cancel":
		err = experimentMySQLCancel(ctx)
	case "latency":
		err = experimentLatency(ctx)
	default:
		err = fmt.Errorf("unknown experiment %q", *experiment)
	}
	if err != nil {
		log.Fatal(err)
	}
}
