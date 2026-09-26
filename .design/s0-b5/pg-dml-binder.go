//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const s0Label = "agentsql.b5.s0"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	for _, major := range []string{"14", "18"} {
		runCurrentReject(ctx, major)
		ctxDir, cleanup := makeDMLBuildContext(major)
		runDML(ctx, major, ctxDir)
		cleanup()
	}
}

func runCurrentReject(ctx context.Context, major string) {
	c, dsn := start(ctx, major, "agentsql-binder-test:pg"+major, "")
	defer terminate(c)
	conn, err := pgx.Connect(ctx, dsn)
	must(err)
	defer conn.Close(ctx)
	setupExtension(ctx, conn)
	_, err = conn.Exec(ctx, `SELECT agentsql_catalog.prepare('agentsql_dml_reject','UPDATE b5.target SET value=''x'' WHERE id=1')`)
	print(map[string]any{"case": "current_binder_dml", "pg": major, "accepted": err == nil, "error": errString(err)})
}

func runDML(ctx context.Context, major, buildContext string) {
	tag := "pg" + major + "-" + fmt.Sprint(time.Now().UnixNano())
	c, dsn := start(ctx, major, "", buildContext+"|"+tag)
	defer terminate(c)
	conn, err := pgx.Connect(ctx, dsn)
	must(err)
	defer conn.Close(ctx)
	setupExtension(ctx, conn)
	_, err = conn.Exec(ctx, `CREATE TABLE b5.audit_log(target_id int, seen text);
CREATE FUNCTION b5.log_target() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN INSERT INTO b5.audit_log VALUES(NEW.id,NEW.value); RETURN NEW; END$$;
CREATE TRIGGER target_audit AFTER UPDATE ON b5.target FOR EACH ROW EXECUTE FUNCTION b5.log_target()`)
	must(err)
	tx, err := conn.Begin(ctx)
	must(err)
	defer tx.Rollback(context.Background())
	var pidBefore int
	must(tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pidBefore))
	sqlText := `UPDATE b5.target t SET value=r.note FROM b5.ref r WHERE t.ref_id=r.id AND t.id=1 RETURNING t.id,t.value`
	prepareStarted := time.Now()
	_, err = tx.Exec(ctx, `SELECT agentsql_catalog.prepare('agentsql_dml_probe',$1)`, sqlText)
	must(err)
	prepareUS := time.Since(prepareStarted).Microseconds()
	manifestStarted := time.Now()
	var command, abi string
	must(tx.QueryRow(ctx, `SELECT command_type FROM agentsql_catalog.prepared_manifest('agentsql_dml_probe')`).Scan(&command))
	must(tx.QueryRow(ctx, `SELECT agentsql_catalog.capabilities()->>'abi'`).Scan(&abi))
	relations := queryStrings(ctx, tx, `SELECT n.nspname||'.'||c.relname FROM agentsql_catalog.prepared_relations('agentsql_dml_probe') p JOIN pg_class c ON c.oid=p.relation_oid JOIN pg_namespace n ON n.oid=c.relnamespace ORDER BY 1`)
	columns := queryStrings(ctx, tx, `SELECT p.usage||':'||n.nspname||'.'||c.relname||'.'||a.attname FROM agentsql_catalog.prepared_vars('agentsql_dml_probe') p JOIN pg_class c ON c.oid=p.relation_oid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=p.relation_oid AND a.attnum=p.attnum ORDER BY 1`)
	locksBefore := locks(ctx, tx)
	manifestUS := time.Since(manifestStarted).Microseconds()
	sealStarted := time.Now()
	_, err = tx.Exec(ctx, `SELECT agentsql_catalog.seal_prepared('agentsql_dml_probe')`)
	must(err)
	sealUS := time.Since(sealStarted).Microseconds()
	ddl, err := pgx.Connect(ctx, dsn)
	must(err)
	_, _ = ddl.Exec(ctx, `SET lock_timeout='250ms'`)
	startDDL := time.Now()
	_, ddlErr := ddl.Exec(ctx, `ALTER TABLE b5.target ADD COLUMN should_block int`)
	ddl.Close(ctx)
	executeStarted := time.Now()
	rows, err := tx.Query(ctx, `EXECUTE agentsql_dml_probe`)
	must(err)
	var returned []string
	for rows.Next() {
		var id int
		var value string
		must(rows.Scan(&id, &value))
		returned = append(returned, fmt.Sprintf("%d:%s", id, value))
	}
	rows.Close()
	must(rows.Err())
	executeUS := time.Since(executeStarted).Microseconds()
	var pidAfter int
	must(tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pidAfter))
	locksAfter := locks(ctx, tx)
	var auditRows int
	must(tx.QueryRow(ctx, `SELECT count(*) FROM b5.audit_log`).Scan(&auditRows))
	manifestSet := make(map[string]bool)
	for _, r := range relations {
		manifestSet[r] = true
	}
	var lockOnly []string
	for _, l := range locksAfter {
		rel := strings.SplitN(l, ":", 2)[0]
		if !manifestSet[rel] && !strings.HasPrefix(rel, "pg_") {
			lockOnly = append(lockOnly, l)
		}
	}
	sort.Strings(lockOnly)
	print(map[string]any{"case": "dml_binder_spike", "pg": major, "abi": abi, "command": command, "backend_before": pidBefore, "backend_after": pidAfter, "same_backend": pidBefore == pidAfter, "relations": relations, "columns": columns, "locks_before_execute": locksBefore, "locks_after_execute": locksAfter, "implicit_lock_not_in_manifest": lockOnly, "returning": returned, "trigger_rows_in_tx": auditRows, "ddl_error": errString(ddlErr), "ddl_wait_ms": time.Since(startDDL).Milliseconds(), "prepare_us": prepareUS, "manifest_and_lock_scan_us": manifestUS, "seal_us": sealUS, "execute_returning_us": executeUS})
	must(tx.Rollback(ctx))
}

func setupExtension(ctx context.Context, conn *pgx.Conn) {
	_, err := conn.Exec(ctx, `CREATE SCHEMA agentsql_catalog; CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog;
CREATE SCHEMA b5; CREATE TABLE b5.ref(id int primary key,note text); CREATE TABLE b5.target(id int primary key,value text,ref_id int);
INSERT INTO b5.ref VALUES(10,'joined'); INSERT INTO b5.target VALUES(1,'old',10)`)
	must(err)
}

func start(ctx context.Context, major, image, buildSpec string) (testcontainers.Container, string) {
	name := fmt.Sprintf("agentsql-b5-s0-pg%s-%d", major, time.Now().UnixNano())
	req := testcontainers.ContainerRequest{Name: name, Image: image, Labels: map[string]string{s0Label: "true"}, Env: map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "pw"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(90 * time.Second)}
	if buildSpec != "" {
		parts := strings.Split(buildSpec, "|")
		req.Image = ""
		req.FromDockerfile = testcontainers.FromDockerfile{Context: parts[0], Dockerfile: "Dockerfile", Repo: "agentsql-b5-s0-pgdml", Tag: parts[1], KeepImage: false}
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	must(err)
	h, _ := c.Host(ctx)
	p, _ := c.MappedPort(ctx, "5432/tcp")
	return c, fmt.Sprintf("postgres://agentsql:pw@%s:%s/agentsql?sslmode=disable", h, p.Port())
}

func makeDMLBuildContext(major string) (string, func()) {
	dir, err := os.MkdirTemp("", "agentsql-b5-s0-pgdml-")
	must(err)
	srcDir := filepath.Join("dbext", "postgres", "agentsql_binder")
	for _, name := range []string{"Makefile", "agentsql_binder.control", "agentsql_binder--0.4.sql"} {
		b, e := os.ReadFile(filepath.Join(srcDir, name))
		must(e)
		must(os.WriteFile(filepath.Join(dir, name), b, 0644))
	}
	b, err := os.ReadFile(filepath.Join(srcDir, "agentsql_binder.c"))
	must(err)
	s := string(b)
	s = replaceOne(s, `#define AGENTSQL_ABI "agentsql-binder-4.1"`, `#define AGENTSQL_ABI "agentsql-binder-4.1-dml-spike"`)
	s = replaceOne(s, `\tSelectStmt *select;`, `\tNode *raw_statement;`)
	s = replaceOne(s, `\tif (list_length(raw) != 1)
\t\tereport(ERROR, (errcode(ERRCODE_SYNTAX_ERROR), errmsg("AgentSQL requires one SELECT")));
\tstatement = linitial_node(RawStmt, raw);
\tif (!IsA(statement->stmt, SelectStmt))
\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL supports SELECT only")));
\tselect = (SelectStmt *) statement->stmt;
\tif (select->intoClause != NULL || select->lockingClause != NIL || select->withClause != NULL)
\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL SELECT shape is unsupported")));
\t(void) preflight_walker(statement->stmt, NULL);`, `\tif (list_length(raw) != 1)
\t\tereport(ERROR, (errcode(ERRCODE_SYNTAX_ERROR), errmsg("AgentSQL requires one statement")));
\tstatement = linitial_node(RawStmt, raw);
\traw_statement = statement->stmt;
\tif (!(IsA(raw_statement, SelectStmt) || IsA(raw_statement, InsertStmt) || IsA(raw_statement, UpdateStmt) || IsA(raw_statement, DeleteStmt)))
\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL supports SELECT/DML only")));
\tif (IsA(raw_statement, SelectStmt))
\t{
\t\tSelectStmt *select = (SelectStmt *) raw_statement;
\t\tif (select->intoClause != NULL || select->lockingClause != NIL || select->withClause != NULL)
\t\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL SELECT shape is unsupported")));
\t}
\t(void) preflight_walker(raw_statement, NULL);`)
	s = replaceOne(s, `\trecord->command_type = pstrdup("SELECT");`, `\t{
\t\tQuery *top = linitial_node(Query, prepared->plansource->query_list);
\t\tswitch (top->commandType) { case CMD_SELECT: record->command_type=pstrdup("SELECT"); break; case CMD_INSERT: record->command_type=pstrdup("INSERT"); break; case CMD_UPDATE: record->command_type=pstrdup("UPDATE"); break; case CMD_DELETE: record->command_type=pstrdup("DELETE"); break; default: ereport(ERROR,(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),errmsg("AgentSQL command unsupported"))); }
\t}`)
	s = replaceOne(s, `\t\t\tif (query->commandType != CMD_SELECT)
\t\t\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL rewrite produced non-SELECT")));`, `\t\t\tif (query->commandType != CMD_SELECT && query->commandType != CMD_INSERT && query->commandType != CMD_UPDATE && query->commandType != CMD_DELETE)
\t\t\t\tereport(ERROR, (errcode(ERRCODE_FEATURE_NOT_SUPPORTED), errmsg("AgentSQL rewrite produced unsupported command")));`)
	old := `\tforeach(cell, query->targetList)
\t{
\t\tTargetEntry *entry = lfirst_node(TargetEntry, cell);
\t\tif (entry->resjunk)
\t\t\tcontinue;
\t\toutput++;
\t\tif (is_composite_type(exprType((Node *) entry->expr)))
\t\t\tadd_var(context, InvalidOid, InvalidAttrNumber,
\t\t\t\texprType((Node *) entry->expr), exprCollation((Node *) entry->expr),
\t\t\t\tfalse, false, exprType((Node *) entry->expr), true);
\t\twalk_expression(context, query, (Node *) entry->expr,
\t\t\t\t\t\tpsprintf("target.%d", output), "output", output);
\t}`
	neu := `\tforeach(cell, query->targetList)
\t{
\t\tTargetEntry *entry = lfirst_node(TargetEntry, cell);
\t\tRangeTblEntry *target_rte = query->resultRelation > 0 ? rt_fetch(query->resultRelation, query->rtable) : NULL;
\t\tif (entry->resjunk) continue;
\t\toutput++;
\t\tif (query->commandType == CMD_SELECT)
\t\t\twalk_expression(context, query, (Node *) entry->expr, psprintf("target.%d", output), "output", output);
\t\telse
\t\t{
\t\t\tconst char *save_site=context->site,*save_usage=context->usage; int save_group=context->contributor_group;
\t\t\tif (target_rte == NULL || target_rte->rtekind != RTE_RELATION) ereport(ERROR,(errcode(ERRCODE_FEATURE_NOT_SUPPORTED),errmsg("AgentSQL DML target unsupported")));
\t\t\tcontext->site=psprintf("write_target.%d",output); context->usage="write_target"; context->contributor_group=++context->record->edge_count;
\t\t\tadd_var(context,target_rte->relid,entry->resno,exprType((Node*)entry->expr),exprCollation((Node*)entry->expr),true,false,exprType((Node*)entry->expr),false);
\t\t\tcontext->site=save_site;context->usage=save_usage;context->contributor_group=save_group;
\t\t\twalk_expression(context,query,(Node*)entry->expr,psprintf("write_expr.%d",output),"reference",++context->record->edge_count);
\t\t}
\t}
\tforeach(cell, query->returningList)
\t{
\t\tTargetEntry *entry=lfirst_node(TargetEntry,cell); if (!entry->resjunk) walk_expression(context,query,(Node*)entry->expr,"returning","reference",++context->record->edge_count);
\t}`
	s = replaceOne(s, old, neu)
	must(os.WriteFile(filepath.Join(dir, "agentsql_binder.c"), []byte(s), 0644))
	dockerfile := fmt.Sprintf("FROM agentsql-binder-test:pg%s\nCOPY . /tmp/b5dml\nRUN make -C /tmp/b5dml clean all install && rm -rf /tmp/b5dml\n", major)
	must(os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0644))
	return dir, func() { _ = os.RemoveAll(dir) }
}

func replaceOne(s, old, neu string) string {
	old = strings.ReplaceAll(old, `\t`, "\t")
	neu = strings.ReplaceAll(neu, `\t`, "\t")
	if strings.Count(s, old) != 1 {
		log.Fatalf("patch needle count=%d for %.60q", strings.Count(s, old), old)
	}
	return strings.Replace(s, old, neu, 1)
}
func locks(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) []string {
	return queryStrings(ctx, q, `SELECT n.nspname||'.'||c.relname||':'||l.mode FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=pg_backend_pid() AND n.nspname='b5' ORDER BY 1`)
}
func queryStrings(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, sql string) []string {
	rows, err := q.Query(ctx, sql)
	must(err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		must(rows.Scan(&s))
		out = append(out, s)
	}
	must(rows.Err())
	return out
}
func terminate(c testcontainers.Container) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx)); err != nil {
		log.Printf("cleanup: %v", err)
	}
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
func print(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
