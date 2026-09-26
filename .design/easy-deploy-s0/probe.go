//go:build ignore

// Command probe records what a gateway can and cannot prove using only the
// PostgreSQL SQL/catalog surface. It is an S0 artifact, not production code.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const s0Label = "agentsql.easy-deploy.s0"

type runningPG struct {
	container testcontainers.Container
	db        *sql.DB
	dsn       string
	major     int
	image     string
	version   string
}

type statementCase struct {
	ID       string
	Class    string
	SQL      string
	Expected string
}

type statementEvidence struct {
	Major                    int      `json:"major"`
	ServerVersion            string   `json:"server_version"`
	Role                     string   `json:"role"`
	Superuser                bool     `json:"superuser"`
	StatementID              string   `json:"statement_id"`
	Class                    string   `json:"class"`
	SQL                      string   `json:"sql"`
	Expected                 string   `json:"expected"`
	PrepareOK                bool     `json:"prepare_ok"`
	PrepareError             string   `json:"prepare_error,omitempty"`
	PreparedMetadata         string   `json:"prepared_metadata,omitempty"`
	PreparedMetadataDigest   string   `json:"prepared_metadata_digest,omitempty"`
	LockedRelations          []string `json:"locked_relations,omitempty"`
	LockedRelationOIDs       []uint32 `json:"locked_relation_oids,omitempty"`
	PostExplainLocks         []string `json:"post_explain_locked_relations,omitempty"`
	PostExplainLockOIDs      []uint32 `json:"post_explain_locked_relation_oids,omitempty"`
	CatalogSnapshot          string   `json:"catalog_snapshot,omitempty"`
	CatalogSnapshotDigest    string   `json:"catalog_snapshot_digest,omitempty"`
	ExplainOK                bool     `json:"explain_ok"`
	ExplainError             string   `json:"explain_error,omitempty"`
	ExplainMillis            float64  `json:"explain_millis"`
	ExplainJSON              string   `json:"explain_json,omitempty"`
	ExplainDigest            string   `json:"explain_digest,omitempty"`
	PlanRelations            []string `json:"plan_relations,omitempty"`
	PlanOutputs              []string `json:"plan_outputs,omitempty"`
	PlanHasOIDOrAttnum       bool     `json:"plan_has_oid_or_attnum"`
	PreparedHasAnalyzedTree  bool     `json:"prepared_has_analyzed_tree"`
	PreparedHasDependencies  bool     `json:"prepared_has_dependencies"`
	PreparedHasInvalidation  bool     `json:"prepared_has_invalidation_generation"`
	PreparedCatalogObjectOID uint32   `json:"prepared_catalog_object_oid"`
}

type matrixRow struct {
	Major, Role, Statement, Class, Prepare, Explain, Locks, PostExplainLocks, PlanRelations, PlanOutputs, ExplainDigest string
}

type timingRow struct {
	Major              int
	Role, Path         string
	N                  int
	Avg, P50, P95, P99 float64
}

var cases = []statementCase{
	{"select_direct", "SELECT", `SELECT c.name FROM edb.customers AS c WHERE c.id = 1`, "base table + output/reference columns"},
	{"select_join", "SELECT", `SELECT c.name,o.amount FROM edb.customers AS c JOIN edb.orders AS o ON o.customer_id=c.id WHERE o.amount>10`, "join contributors and predicate columns"},
	{"select_view", "SELECT", `SELECT v.name FROM edb.v_customer AS v WHERE v.id=1`, "view identity plus base output/reference lineage"},
	{"select_nested_view", "SELECT", `SELECT v.name FROM edb.v_customer_nested AS v WHERE v.id=1`, "multi-level view identity and lineage"},
	{"select_cte", "SELECT", `WITH q AS (SELECT o.customer_id,o.amount FROM edb.orders o WHERE o.amount>10) SELECT c.name,q.amount FROM edb.customers c JOIN q ON q.customer_id=c.id`, "non-recursive CTE scope and lineage"},
	{"select_lateral", "SELECT", `SELECT c.name,x.total FROM edb.customers c LEFT JOIN LATERAL (SELECT pg_catalog.sum(o.amount) AS total FROM edb.orders o WHERE o.customer_id=c.id) x ON true`, "correlated LATERAL lineage"},
	{"select_whole_row", "SELECT", `SELECT c FROM edb.customers c`, "whole-row Var must be detected/rejected"},
	{"select_system_columns", "SELECT", `SELECT c.ctid,c.xmin FROM edb.customers c`, "negative attnums must be detected/rejected"},
	{"select_partition", "SELECT", `SELECT p.payload FROM edb.p_events p WHERE p.id=1`, "partition parent and runtime children"},
	{"select_inheritance_only", "SELECT", `SELECT p.payload FROM ONLY edb.i_parent p WHERE p.id=1`, "inheritance exists even when ONLY suppresses expansion"},
	{"select_rls", "SELECT", `SELECT r.secret FROM edb.rls_data r WHERE r.id=1`, "role-sensitive RLS security quals"},
	{"select_immutable_function", "SELECT", `SELECT edb.plan_probe()`, "EXPLAIN planning may execute an IMMUTABLE user function"},
	{"insert_values", "INSERT", `INSERT INTO edb.dml_plain(a,b) VALUES (1,'x')`, "write targets a,b"},
	{"insert_omitted", "INSERT", `INSERT INTO edb.dml_plain(a) VALUES (2)`, "explicit a plus implicit NULL write to b"},
	{"update_simple", "UPDATE", `UPDATE edb.dml_plain SET b='y' WHERE a=1`, "write target b and reference a"},
	{"update_from_returning", "UPDATE", `UPDATE edb.customers c SET name=o.note FROM edb.orders o WHERE o.customer_id=c.id RETURNING c.id,o.amount`, "target/reference/RETURNING split"},
	{"delete_simple", "DELETE", `DELETE FROM edb.dml_plain WHERE a=1`, "row delete target and reference a"},
	{"update_trigger", "UPDATE", `UPDATE edb.trigger_target SET payload='changed' WHERE id=1`, "implicit trigger function and audit_log write"},
	{"update_rule", "UPDATE", `UPDATE edb.rule_target SET payload='changed' WHERE id=1`, "rewrite rule implicit audit_log write"},
}

func main() {
	var out, majors string
	var iterations int
	flag.StringVar(&out, "out", ".design/easy-deploy-s0/raw", "evidence output directory")
	flag.StringVar(&majors, "majors", "14,15,16,17,18", "comma-separated PostgreSQL majors")
	flag.IntVar(&iterations, "iterations", 120, "timing iterations per path and role")
	flag.Parse()
	if os.Getenv("DOCKER_HOST") == "" {
		_ = os.Setenv("DOCKER_HOST", "npipe:////./pipe/docker_engine")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(out, "run.log"))
	if err != nil {
		log.Fatal(err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)

	parsed, err := parseMajors(majors)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	var envRows []map[string]any
	var matrix []matrixRow
	var timings []timingRow
	concurrencyFile, err := os.Create(filepath.Join(out, "concurrency.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	defer concurrencyFile.Close()
	concurrencyEncoder := json.NewEncoder(concurrencyFile)

	for _, major := range parsed {
		pg, err := startPostgres(ctx, major, logger)
		if err != nil {
			log.Fatalf("start pg%d: %v", major, err)
		}
		func() {
			defer pg.close(context.Background(), logger)
			if err := setupFixture(ctx, pg.db); err != nil {
				log.Fatalf("setup pg%d: %v", major, err)
			}
			envRows = append(envRows, map[string]any{"major": major, "image": pg.image, "server_version": pg.version})
			roles := []struct {
				name, dsn string
				super     bool
			}{{"super", pg.dsn, true}, {"regular", regularDSN(pg.dsn), false}}
			for _, role := range roles {
				db, err := sql.Open("pgx", role.dsn)
				if err != nil {
					log.Fatal(err)
				}
				db.SetMaxOpenConns(4)
				db.SetMaxIdleConns(4)
				if err := db.PingContext(ctx); err != nil {
					log.Fatalf("pg%d %s ping: %v", major, role.name, err)
				}
				metadata, err := collectMetadata(ctx, db, major, role.name, role.super)
				if err != nil {
					log.Fatalf("metadata pg%d %s: %v", major, role.name, err)
				}
				writeJSON(filepath.Join(out, fmt.Sprintf("pg%d-%s-metadata.json", major, role.name)), metadata)
				file, err := os.Create(filepath.Join(out, fmt.Sprintf("pg%d-%s.jsonl", major, role.name)))
				if err != nil {
					log.Fatal(err)
				}
				enc := json.NewEncoder(file)
				for i, c := range cases {
					ev := runStatement(ctx, db, pg.version, major, role.name, role.super, i, c)
					if err := enc.Encode(ev); err != nil {
						log.Fatal(err)
					}
					matrix = append(matrix, matrixRow{strconv.Itoa(major), role.name, c.ID, c.Class, boolText(ev.PrepareOK), boolText(ev.ExplainOK), strings.Join(ev.LockedRelations, ";"), strings.Join(ev.PostExplainLocks, ";"), strings.Join(ev.PlanRelations, ";"), strings.Join(ev.PlanOutputs, " | "), ev.ExplainDigest})
				}
				_ = file.Close()
				for _, row := range measurePaths(ctx, db, major, role.name, iterations) {
					timings = append(timings, row)
				}
				jitter := planJitter(ctx, db, major, role.name)
				_ = concurrencyEncoder.Encode(jitter)
				search := searchPathProbe(ctx, db, major, role.name)
				_ = concurrencyEncoder.Encode(search)
				aba := preparedABA(ctx, db, pg.db, major, role.name)
				_ = concurrencyEncoder.Encode(aba)
				_ = db.Close()
			}
			lock := ddlLockProbe(ctx, pg.db, major)
			_ = concurrencyEncoder.Encode(lock)
		}()
	}
	writeJSON(filepath.Join(out, "environment.json"), map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339Nano), "docker_host": os.Getenv("DOCKER_HOST"), "go_toolchain": os.Getenv("GOTOOLCHAIN"), "servers": envRows, "iterations": iterations})
	writeMatrix(filepath.Join(out, "matrix.csv"), matrix)
	writeTimings(filepath.Join(out, "timing.csv"), timings)
	logger.Printf("complete majors=%v", parsed)
}

func parseMajors(raw string) ([]int, error) {
	var values []int
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 14 || n > 18 {
			return nil, fmt.Errorf("invalid major %q", part)
		}
		values = append(values, n)
	}
	return values, nil
}

func startPostgres(ctx context.Context, major int, logger *log.Logger) (*runningPG, error) {
	image := fmt.Sprintf("postgres:%d", major)
	name := fmt.Sprintf("agentsql-easy-deploy-s0-pg%d-%d", major, time.Now().UnixNano())
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Name: name, Image: image, Labels: map[string]string{s0Label: "true"}, Env: map[string]string{"POSTGRES_DB": "easy", "POSTGRES_USER": "spike", "POSTGRES_PASSWORD": "spike"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(120 * time.Second)}, Started: true})
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
	dsn := fmt.Sprintf("postgres://spike:spike@%s:%s/easy?sslmode=disable", host, port.Port())
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, err
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = db.Close()
			_ = c.Terminate(ctx)
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
	var version string
	if err = db.QueryRowContext(ctx, `SELECT version()`).Scan(&version); err != nil {
		return nil, err
	}
	logger.Printf("started name=%s image=%s version=%s", name, image, version)
	return &runningPG{container: c, db: db, dsn: dsn, major: major, image: image, version: version}, nil
}

func (p *runningPG) close(ctx context.Context, logger *log.Logger) {
	if p.db != nil {
		_ = p.db.Close()
	}
	if p.container != nil {
		if err := p.container.Terminate(ctx); err != nil {
			logger.Printf("cleanup pg%d: %v", p.major, err)
		} else {
			logger.Printf("terminated pg%d", p.major)
		}
	}
}

func regularDSN(dsn string) string {
	return strings.Replace(strings.Replace(dsn, "spike:spike@", "regular:regular@", 1), "user=spike", "user=regular", 1)
}

func setupFixture(ctx context.Context, db *sql.DB) error {
	steps := []string{
		`CREATE ROLE regular LOGIN PASSWORD 'regular' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT`,
		`CREATE SCHEMA edb AUTHORIZATION spike`, `CREATE SCHEMA alt AUTHORIZATION spike`,
		`CREATE TABLE edb.customers(id integer NOT NULL,name text,secret text,region integer)`,
		`CREATE INDEX customers_id_idx ON edb.customers(id)`,
		`CREATE TABLE edb.orders(customer_id integer,amount integer,note text)`,
		`INSERT INTO edb.customers SELECT g,'name-'||g,'secret-'||g,(g%3)+1 FROM pg_catalog.generate_series(1,5000) g`,
		`INSERT INTO edb.orders SELECT g,(g%100),'note-'||g FROM pg_catalog.generate_series(1,5000) g`,
		`CREATE VIEW edb.v_customer AS SELECT c.id,c.name,c.secret FROM edb.customers c WHERE c.region>0`,
		`CREATE VIEW edb.v_customer_nested AS SELECT v.id,v.name FROM edb.v_customer v WHERE v.secret<>''`,
		`CREATE TABLE edb.p_events(id integer,payload text) PARTITION BY RANGE(id)`,
		`CREATE TABLE edb.p_events_lo PARTITION OF edb.p_events FOR VALUES FROM (0) TO (100)`,
		`INSERT INTO edb.p_events VALUES(1,'p')`,
		`CREATE TABLE edb.i_parent(id integer,payload text)`, `CREATE TABLE edb.i_child(extra text) INHERITS(edb.i_parent)`, `INSERT INTO edb.i_parent VALUES(1,'parent')`,
		`CREATE TABLE edb.rls_data(id integer,owner_name name,secret text)`, `INSERT INTO edb.rls_data VALUES(1,'regular','visible'),(2,'spike','super')`,
		`ALTER TABLE edb.rls_data ENABLE ROW LEVEL SECURITY`, `CREATE POLICY owner_policy ON edb.rls_data USING(owner_name=current_user)`,
		`CREATE TABLE edb.dml_plain(a integer,b text)`,
		`CREATE TABLE edb.audit_log(event text)`, `CREATE TABLE edb.trigger_target(id integer,payload text)`, `INSERT INTO edb.trigger_target VALUES(1,'before')`,
		`CREATE FUNCTION edb.trigger_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN INSERT INTO edb.audit_log(event) VALUES(NEW.payload); RETURN NEW; END$$`,
		`CREATE TRIGGER trigger_target_audit AFTER UPDATE ON edb.trigger_target FOR EACH ROW EXECUTE FUNCTION edb.trigger_audit()`,
		`CREATE TABLE edb.rule_target(id integer,payload text)`, `INSERT INTO edb.rule_target VALUES(1,'before')`,
		`CREATE RULE rule_target_audit AS ON UPDATE TO edb.rule_target DO ALSO INSERT INTO edb.audit_log(event) VALUES(NEW.payload)`,
		`CREATE TABLE edb.aba(value text)`, `INSERT INTO edb.aba VALUES('old')`,
		`CREATE FUNCTION edb.plan_probe() RETURNS integer LANGUAGE plpgsql IMMUTABLE AS $$BEGIN PERFORM pg_catalog.pg_sleep(0.050); RETURN 1; END$$`,
		`CREATE FUNCTION edb.resolve_me() RETURNS text LANGUAGE sql IMMUTABLE AS $$SELECT 'edb'::text$$`,
		`CREATE FUNCTION alt.resolve_me() RETURNS text LANGUAGE sql IMMUTABLE AS $$SELECT 'alt'::text$$`,
		`GRANT USAGE ON SCHEMA edb,alt TO regular`, `GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA edb TO regular`,
		`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA edb TO regular`, `GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA alt TO regular`,
		`ANALYZE edb.customers`, `ANALYZE edb.orders`,
	}
	for _, q := range steps {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

func runStatement(ctx context.Context, db *sql.DB, version string, major int, role string, super bool, ordinal int, c statementCase) statementEvidence {
	ev := statementEvidence{Major: major, ServerVersion: version, Role: role, Superuser: super, StatementID: c.ID, Class: c.Class, SQL: c.SQL, Expected: c.Expected}
	conn, err := db.Conn(ctx)
	if err != nil {
		ev.PrepareError = err.Error()
		return ev
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: false})
	if err != nil {
		ev.PrepareError = err.Error()
		return ev
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(ctx, `SET LOCAL search_path=pg_catalog`)
	_, _ = tx.ExecContext(ctx, `SET LOCAL lock_timeout='1s'`)
	_, _ = tx.ExecContext(ctx, `SET LOCAL statement_timeout='3s'`)
	name := fmt.Sprintf("ed_%d", ordinal)
	if _, err = tx.ExecContext(ctx, `PREPARE `+name+` AS `+c.SQL); err != nil {
		ev.PrepareError = err.Error()
		return ev
	}
	ev.PrepareOK = true
	ev.PreparedMetadata = queryScalar(ctx, tx, `SELECT pg_catalog.jsonb_build_object('name',name,'statement',statement,'prepare_time',prepare_time,'parameter_types',parameter_types::text,'from_sql',from_sql,'generic_plans',generic_plans,'custom_plans',custom_plans)::text FROM pg_catalog.pg_prepared_statements WHERE name=$1`, name)
	ev.PreparedMetadataDigest = digest(ev.PreparedMetadata)
	ev.LockedRelations, ev.LockedRelationOIDs = lockedRelations(ctx, tx)
	ev.CatalogSnapshot = queryScalar(ctx, tx, catalogSnapshotSQL, oidArray(ev.LockedRelationOIDs))
	ev.CatalogSnapshotDigest = digest(ev.CatalogSnapshot)
	start := time.Now()
	ev.ExplainJSON = queryScalar(ctx, tx, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE `+name)
	ev.ExplainMillis = float64(time.Since(start).Microseconds()) / 1000
	if strings.HasPrefix(ev.ExplainJSON, "ERROR:") {
		ev.ExplainError = strings.TrimPrefix(ev.ExplainJSON, "ERROR: ")
	} else {
		ev.ExplainOK = true
		ev.ExplainDigest = digest(ev.ExplainJSON)
		ev.PlanRelations, ev.PlanOutputs = extractPlanFacts(ev.ExplainJSON)
	}
	ev.PostExplainLocks, ev.PostExplainLockOIDs = lockedRelations(ctx, tx)
	// Neither SQL surface contains authority-bearing OID/attnum fields. Keep
	// these explicit booleans in every record so later summaries do not infer
	// their absence from a particular textual plan.
	ev.PlanHasOIDOrAttnum = false
	ev.PreparedHasAnalyzedTree = false
	ev.PreparedHasDependencies = false
	ev.PreparedHasInvalidation = false
	ev.PreparedCatalogObjectOID = 0
	_, _ = tx.ExecContext(ctx, `DEALLOCATE `+name)
	return ev
}

const catalogSnapshotSQL = `WITH target AS (SELECT unnest($1::oid[]) oid), rel AS (
 SELECT c.oid,n.nspname,c.relname,c.relkind::text,c.relpersistence::text,c.relispartition,c.relhassubclass,c.relrowsecurity,c.relforcerowsecurity
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace JOIN target t ON t.oid=c.oid
), attrs AS (SELECT a.attrelid,a.attnum,a.attname,a.atttypid,a.atttypmod,a.attcollation,a.attidentity::text,a.attgenerated::text FROM pg_catalog.pg_attribute a JOIN target t ON t.oid=a.attrelid WHERE a.attnum<>0 AND NOT a.attisdropped), implicit AS (
 SELECT tgrelid owner,'trigger' kind,oid obj FROM pg_catalog.pg_trigger WHERE tgrelid=ANY($1::oid[]) AND NOT tgisinternal
 UNION ALL SELECT adrelid,'default',oid FROM pg_catalog.pg_attrdef WHERE adrelid=ANY($1::oid[])
 UNION ALL SELECT ev_class,'rule',oid FROM pg_catalog.pg_rewrite WHERE ev_class=ANY($1::oid[]) AND rulename<>'_RETURN'
 UNION ALL SELECT conrelid,'constraint',oid FROM pg_catalog.pg_constraint WHERE conrelid=ANY($1::oid[]) OR confrelid=ANY($1::oid[])
), deps AS (SELECT classid,objid,objsubid,refclassid,refobjid,refobjsubid,deptype::text FROM pg_catalog.pg_depend WHERE objid=ANY($1::oid[]) OR refobjid=ANY($1::oid[]))
SELECT pg_catalog.jsonb_build_object('relations',COALESCE((SELECT jsonb_agg(to_jsonb(rel) ORDER BY oid) FROM rel),'[]'::jsonb),'attributes',COALESCE((SELECT jsonb_agg(to_jsonb(attrs) ORDER BY attrelid,attnum) FROM attrs),'[]'::jsonb),'implicit',COALESCE((SELECT jsonb_agg(to_jsonb(implicit) ORDER BY owner,kind,obj) FROM implicit),'[]'::jsonb),'dependencies',COALESCE((SELECT jsonb_agg(to_jsonb(deps) ORDER BY classid,objid,objsubid,refclassid,refobjid,refobjsubid) FROM deps),'[]'::jsonb))::text`

func collectMetadata(ctx context.Context, db *sql.DB, major int, role string, super bool) (map[string]any, error) {
	queries := map[string]string{
		"identity":                      `SELECT jsonb_build_object('current_user',current_user,'session_user',session_user,'role_oid',(SELECT oid FROM pg_roles WHERE rolname=current_user),'superuser',(SELECT rolsuper FROM pg_roles WHERE rolname=current_user),'search_path',current_setting('search_path'),'temp_schema',pg_my_temp_schema())::text`,
		"prepared_columns":              `SELECT jsonb_agg(jsonb_build_object('name',column_name,'type',data_type) ORDER BY ordinal_position)::text FROM information_schema.columns WHERE table_schema='pg_catalog' AND table_name='pg_prepared_statements'`,
		"view_definition":               `SELECT pg_get_viewdef('edb.v_customer_nested'::regclass,true)`,
		"view_rewrite_tree_digest":      `SELECT encode(sha256(convert_to(ev_action::text,'UTF8')),'hex') FROM pg_rewrite WHERE ev_class='edb.v_customer_nested'::regclass AND rulename='_RETURN'`,
		"view_dependencies":             `SELECT COALESCE(jsonb_agg(jsonb_build_object('objid',objid,'objsubid',objsubid,'refobjid',refobjid,'refobjsubid',refobjsubid,'deptype',deptype) ORDER BY objid,objsubid,refobjid,refobjsubid),'[]'::jsonb)::text FROM pg_depend WHERE objid=(SELECT oid FROM pg_rewrite WHERE ev_class='edb.v_customer_nested'::regclass AND rulename='_RETURN')`,
		"trigger_definition":            `SELECT pg_get_triggerdef(oid,true) FROM pg_trigger WHERE tgrelid='edb.trigger_target'::regclass AND NOT tgisinternal`,
		"trigger_function_definition":   `SELECT pg_get_functiondef('edb.trigger_audit()'::regprocedure)`,
		"trigger_function_dependencies": `SELECT COALESCE(jsonb_agg(jsonb_build_object('refclassid',refclassid,'refobjid',refobjid,'refobjsubid',refobjsubid,'deptype',deptype) ORDER BY refclassid,refobjid,refobjsubid),'[]'::jsonb)::text FROM pg_depend WHERE classid='pg_proc'::regclass AND objid='edb.trigger_audit()'::regprocedure`,
		"policy_expression":             `SELECT pg_get_expr(polqual,polrelid,true) FROM pg_policy WHERE polrelid='edb.rls_data'::regclass`,
		"catalog_counts":                `SELECT jsonb_build_object('class',(SELECT count(*) FROM pg_class),'attribute',(SELECT count(*) FROM pg_attribute),'depend',(SELECT count(*) FROM pg_depend),'rewrite',(SELECT count(*) FROM pg_rewrite),'trigger',(SELECT count(*) FROM pg_trigger),'policy',(SELECT count(*) FROM pg_policy))::text`,
	}
	result := map[string]any{"major": major, "role": role, "superuser": super}
	for name, q := range queries {
		result[name] = queryScalarDB(ctx, db, q)
	}
	// GENERIC_PLAN is version-dependent. Capture the exact server response.
	result["generic_plan_probe"] = queryScalarDB(ctx, db, `EXPLAIN (GENERIC_PLAN TRUE,VERBOSE,FORMAT JSON,COSTS OFF) SELECT 1`)
	return result, nil
}

func lockedRelations(ctx context.Context, tx *sql.Tx) ([]string, []uint32) {
	rows, err := tx.QueryContext(ctx, `SELECT c.oid,n.nspname||'.'||c.relname||':'||c.relkind::text||':'||l.mode FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=pg_backend_pid() AND l.granted AND n.nspname='edb' ORDER BY c.oid,l.mode`)
	if err != nil {
		return []string{"ERROR:" + err.Error()}, nil
	}
	defer rows.Close()
	var names []string
	var oids []uint32
	seen := map[uint32]bool{}
	for rows.Next() {
		var oid uint32
		var name string
		_ = rows.Scan(&oid, &name)
		names = append(names, name)
		if !seen[oid] {
			seen[oid] = true
			oids = append(oids, oid)
		}
	}
	return names, oids
}

func extractPlanFacts(raw string) ([]string, []string) {
	var root any
	if json.Unmarshal([]byte(raw), &root) != nil {
		return nil, nil
	}
	relSet := map[string]bool{}
	outSet := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, ok := x["Relation Name"].(string); ok {
				schema, _ := x["Schema"].(string)
				relSet[schema+"."+r] = true
			}
			if values, ok := x["Output"].([]any); ok {
				for _, v := range values {
					outSet[fmt.Sprint(v)] = true
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(root)
	rels := keys(relSet)
	outs := keys(outSet)
	return rels, outs
}

func planJitter(ctx context.Context, db *sql.DB, major int, role string) map[string]any {
	conn, err := db.Conn(ctx)
	if err != nil {
		return map[string]any{"kind": "plan_jitter", "major": major, "role": role, "error": err.Error()}
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, `DEALLOCATE ALL`)
	_, _ = conn.ExecContext(ctx, `SET enable_indexscan=on`)
	_, _ = conn.ExecContext(ctx, `SET enable_bitmapscan=on`)
	_, _ = conn.ExecContext(ctx, `SET enable_seqscan=on`)
	_, err = conn.ExecContext(ctx, `PREPARE jitter_a AS SELECT c.name FROM edb.customers c WHERE c.id=2500`)
	if err != nil {
		return map[string]any{"kind": "plan_jitter", "major": major, "role": role, "error": err.Error()}
	}
	a := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE jitter_a`)
	metaA := queryScalarConn(ctx, conn, `SELECT jsonb_build_object('generic_plans',generic_plans,'custom_plans',custom_plans)::text FROM pg_prepared_statements WHERE name='jitter_a'`)
	_, _ = conn.ExecContext(ctx, `DEALLOCATE jitter_a`)
	_, _ = conn.ExecContext(ctx, `SET enable_indexscan=off`)
	_, _ = conn.ExecContext(ctx, `SET enable_bitmapscan=off`)
	_, _ = conn.ExecContext(ctx, `SET enable_seqscan=on`)
	_, err = conn.ExecContext(ctx, `PREPARE jitter_b AS SELECT c.name FROM edb.customers c WHERE c.id=2500`)
	if err != nil {
		return map[string]any{"kind": "plan_jitter", "major": major, "role": role, "error": err.Error()}
	}
	b := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE jitter_b`)
	metaB := queryScalarConn(ctx, conn, `SELECT jsonb_build_object('generic_plans',generic_plans,'custom_plans',custom_plans)::text FROM pg_prepared_statements WHERE name='jitter_b'`)
	_, _ = conn.ExecContext(ctx, `DEALLOCATE jitter_b`)
	return map[string]any{"kind": "plan_jitter", "major": major, "role": role, "same_raw_sql": true, "same_catalog": true, "plan_a_digest": digest(a), "plan_b_digest": digest(b), "digests_equal": digest(a) == digest(b), "plan_a": a, "plan_b": b, "prepared_metadata_a": metaA, "prepared_metadata_b": metaB}
}

func searchPathProbe(ctx context.Context, db *sql.DB, major int, role string) map[string]any {
	conn, err := db.Conn(ctx)
	if err != nil {
		return map[string]any{"kind": "search_path", "error": err.Error()}
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, `SET search_path=edb,pg_catalog`)
	_, _ = conn.ExecContext(ctx, `PREPARE sp1 AS SELECT resolve_me()`)
	a := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE sp1`)
	_, _ = conn.ExecContext(ctx, `DEALLOCATE sp1`)
	_, _ = conn.ExecContext(ctx, `SET search_path=alt,pg_catalog`)
	_, _ = conn.ExecContext(ctx, `PREPARE sp2 AS SELECT resolve_me()`)
	b := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE sp2`)
	_, _ = conn.ExecContext(ctx, `DEALLOCATE sp2`)
	return map[string]any{"kind": "search_path", "major": major, "role": role, "raw_sql": "SELECT resolve_me()", "edb_digest": digest(a), "alt_digest": digest(b), "equal": digest(a) == digest(b), "edb_plan": a, "alt_plan": b}
}

func preparedABA(ctx context.Context, db, admin *sql.DB, major int, role string) map[string]any {
	conn, err := db.Conn(ctx)
	if err != nil {
		return map[string]any{"kind": "prepared_aba", "error": err.Error()}
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, `DEALLOCATE ALL`)
	oldOID := queryScalarDB(ctx, admin, `SELECT 'edb.aba'::regclass::oid::text`)
	_, err = conn.ExecContext(ctx, `PREPARE aba_probe AS SELECT a.value FROM edb.aba a`)
	if err != nil {
		return map[string]any{"kind": "prepared_aba", "major": major, "role": role, "error": err.Error()}
	}
	before := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE aba_probe`)
	_, dropErr := admin.ExecContext(ctx, `DROP TABLE edb.aba`)
	if dropErr == nil {
		_, dropErr = admin.ExecContext(ctx, `CREATE TABLE edb.aba(value text)`)
	}
	if dropErr == nil {
		_, dropErr = admin.ExecContext(ctx, `INSERT INTO edb.aba VALUES('new')`)
	}
	if dropErr == nil {
		_, dropErr = admin.ExecContext(ctx, `GRANT SELECT ON edb.aba TO regular`)
	}
	newOID := queryScalarDB(ctx, admin, `SELECT 'edb.aba'::regclass::oid::text`)
	after := queryScalarConn(ctx, conn, `EXPLAIN (VERBOSE,FORMAT JSON,COSTS OFF) EXECUTE aba_probe`)
	meta := queryScalarConn(ctx, conn, `SELECT jsonb_build_object('statement',statement,'generic_plans',generic_plans,'custom_plans',custom_plans)::text FROM pg_prepared_statements WHERE name='aba_probe'`)
	_, _ = conn.ExecContext(ctx, `DEALLOCATE aba_probe`)
	return map[string]any{"kind": "prepared_aba", "major": major, "role": role, "drop_recreate_error": errorText(dropErr), "old_oid": oldOID, "new_oid": newOID, "oid_changed": oldOID != newOID, "before_digest": digest(before), "after_digest": digest(after), "execute_after_aba_ok": !strings.HasPrefix(after, "ERROR:"), "sql_visible_metadata": meta, "sql_visible_invalidation_generation": false, "before_plan": before, "after_plan": after}
}

func ddlLockProbe(ctx context.Context, admin *sql.DB, major int) map[string]any {
	a, _ := admin.Conn(ctx)
	defer a.Close()
	b, _ := admin.Conn(ctx)
	defer b.Close()
	tx, err := a.BeginTx(ctx, nil)
	if err != nil {
		return map[string]any{"kind": "ddl_lock", "error": err.Error()}
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `PREPARE lock_probe AS SELECT c.name FROM edb.customers c WHERE c.id=1`)
	start := time.Now()
	_, _ = b.ExecContext(ctx, `SET lock_timeout='250ms'`)
	_, ddlErr := b.ExecContext(ctx, `ALTER TABLE edb.customers ADD COLUMN lock_probe_column integer`)
	elapsed := time.Since(start)
	_ = tx.Rollback()
	_, afterErr := b.ExecContext(ctx, `ALTER TABLE edb.customers ADD COLUMN lock_probe_column integer`)
	if afterErr == nil {
		_, afterErr = b.ExecContext(ctx, `ALTER TABLE edb.customers DROP COLUMN lock_probe_column`)
	}
	return map[string]any{"kind": "ddl_lock", "major": major, "prepare_error": errorText(err), "concurrent_ddl_error": errorText(ddlErr), "concurrent_ddl_millis": float64(elapsed.Microseconds()) / 1000, "ddl_blocked": ddlErr != nil, "ddl_after_unlock_error": errorText(afterErr)}
}

func measurePaths(ctx context.Context, db *sql.DB, major int, role string, n int) []timingRow {
	if n < 1 {
		return nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil
	}
	defer conn.Close()
	for i := 0; i < 20; i++ {
		_, _ = oneFullBind(ctx, conn, i)
		_, _ = oneCatalogBind(ctx, conn)
	}
	full := make([]float64, 0, n)
	catalog := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		_, err := oneFullBind(ctx, conn, i)
		if err == nil {
			full = append(full, float64(time.Since(start).Microseconds())/1000)
		}
		start = time.Now()
		_, err = oneCatalogBind(ctx, conn)
		if err == nil {
			catalog = append(catalog, float64(time.Since(start).Microseconds())/1000)
		}
	}
	return []timingRow{timingStats(major, role, "sql_prepare_lock_catalog_deallocate", full), timingStats(major, role, "catalog_lock_revalidate_cached_shape", catalog)}
}

func oneFullBind(ctx context.Context, conn *sql.Conn, i int) (string, error) {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	name := fmt.Sprintf("tm_%d", i)
	if _, err = tx.ExecContext(ctx, `SET LOCAL search_path=pg_catalog`); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE edb.customers IN ACCESS SHARE MODE`); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `PREPARE `+name+` AS SELECT c.name FROM edb.customers c WHERE c.id=1`); err != nil {
		return "", err
	}
	v := queryScalar(ctx, tx, catalogSnapshotSQL, `{`+queryScalar(ctx, tx, `SELECT 'edb.customers'::regclass::oid::text`)+`}`)
	if strings.HasPrefix(v, "ERROR:") {
		return "", errors.New(v)
	}
	if _, err = tx.ExecContext(ctx, `DEALLOCATE `+name); err != nil {
		return "", err
	}
	return v, tx.Rollback()
}

func oneCatalogBind(ctx context.Context, conn *sql.Conn) (string, error) {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `LOCK TABLE edb.customers IN ACCESS SHARE MODE`); err != nil {
		return "", err
	}
	oid := queryScalar(ctx, tx, `SELECT 'edb.customers'::regclass::oid::text`)
	v := queryScalar(ctx, tx, catalogSnapshotSQL, `{`+oid+`}`)
	if strings.HasPrefix(v, "ERROR:") {
		return "", errors.New(v)
	}
	return v, tx.Rollback()
}

func timingStats(major int, role, path string, v []float64) timingRow {
	sort.Float64s(v)
	r := timingRow{Major: major, Role: role, Path: path, N: len(v)}
	if len(v) == 0 {
		return r
	}
	for _, x := range v {
		r.Avg += x
	}
	r.Avg /= float64(len(v))
	r.P50 = percentile(v, .50)
	r.P95 = percentile(v, .95)
	r.P99 = percentile(v, .99)
	return r
}
func percentile(v []float64, p float64) float64 {
	i := int(p * float64(len(v)))
	if i < 1 {
		i = 1
	}
	if i > len(v) {
		i = len(v)
	}
	return v[i-1]
}

func queryScalar(ctx context.Context, tx *sql.Tx, q string, args ...any) string {
	var v any
	if err := tx.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		return "ERROR: " + err.Error()
	}
	return scalarText(v)
}
func queryScalarDB(ctx context.Context, db *sql.DB, q string, args ...any) string {
	var v any
	if err := db.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		return "ERROR: " + err.Error()
	}
	return scalarText(v)
}
func queryScalarConn(ctx context.Context, c *sql.Conn, q string, args ...any) string {
	var v any
	if err := c.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		return "ERROR: " + err.Error()
	}
	return scalarText(v)
}
func scalarText(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
func digest(s string) string {
	if s == "" {
		return ""
	}
	v := sha256.Sum256([]byte(s))
	return hex.EncodeToString(v[:])
}
func oidArray(v []uint32) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatUint(uint64(n), 10)
	}
	return `{` + strings.Join(parts, ",") + `}`
}
func keys(m map[string]bool) []string {
	v := make([]string, 0, len(m))
	for k := range m {
		v = append(v, k)
	}
	sort.Strings(v)
	return v
}
func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	data = append(data, '\n')
	if err = os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
}
func writeMatrix(path string, rows []matrixRow) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"major", "role", "statement", "class", "prepare_ok", "explain_ok", "prepare_locked_relations", "post_explain_locked_relations", "plan_relations", "plan_outputs", "explain_sha256"})
	for _, r := range rows {
		_ = w.Write([]string{r.Major, r.Role, r.Statement, r.Class, r.Prepare, r.Explain, r.Locks, r.PostExplainLocks, r.PlanRelations, r.PlanOutputs, r.ExplainDigest})
	}
}
func writeTimings(path string, rows []timingRow) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"major", "role", "path", "n", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "source"})
	for _, r := range rows {
		_ = w.Write([]string{strconv.Itoa(r.Major), r.Role, r.Path, strconv.Itoa(r.N), fmt.Sprintf("%.3f", r.Avg), fmt.Sprintf("%.3f", r.P50), fmt.Sprintf("%.3f", r.P95), fmt.Sprintf("%.3f", r.P99), "this_s0"})
	}
	_ = w.Write([]string{"", "", "c_extension_per_request_reference", "", "", "", "86.000", "", "user_supplied_existing_baseline"})
}
