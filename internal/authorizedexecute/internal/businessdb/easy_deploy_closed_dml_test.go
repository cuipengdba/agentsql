package businessdb

import (
	"context"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/b5dml"
	closedparser "github.com/cuipengdba/agentsql/internal/parser"
)

func TestClosedDMLResolverExactFacts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		sql        string
		action     b5dml.Action
		writes     int
		references map[string]string
		implicit   int
		rowDelete  bool
	}{
		{name: "insert omitted is implicit null", sql: `INSERT INTO app.items (id,name) VALUES (1,'a'),(2,'b')`, action: b5dml.ActionInsert, writes: 3, implicit: 1},
		{name: "update set and where", sql: `UPDATE app.items AS i SET name='x', n=n+1 WHERE i.id=1`, action: b5dml.ActionUpdate, writes: 2, references: map[string]string{"n": "expression", "id": "where"}},
		{name: "delete relation row", sql: `DELETE FROM app.items AS i WHERE i.id=1 AND i.name IS NOT NULL`, action: b5dml.ActionDelete, writes: 1, references: map[string]string{"id": "where", "name": "where"}, rowDelete: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := closedparser.ParsePostgresClosedDML(test.sql)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := resolvePostgresClosedDML(context.Background(), parsed, closedDMLUnitFrame(), "ds-1", closedDMLUnitOperator, &unlimitedPostgresBudget{})
			if err != nil {
				t.Fatal(err)
			}
			if facts.Action != test.action || facts.StatementClass != binderStatementClassForDML(test.action) || len(facts.WriteTargets) != test.writes || len(facts.ColumnUses) != len(test.references) {
				t.Fatalf("unexpected facts: %#v", facts)
			}
			implicit := 0
			for _, write := range facts.WriteTargets {
				if write.Source == b5dml.WriteSourceImplicitNull {
					implicit++
				}
				if test.rowDelete != (write.Kind == b5dml.WriteTargetRow) {
					t.Fatalf("unexpected write kind: %#v", write)
				}
			}
			if implicit != test.implicit {
				t.Fatalf("implicit-null writes=%d facts=%#v", implicit, facts)
			}
			for _, reference := range facts.ColumnUses {
				if test.references[reference.Name] != reference.Site || reference.Usage != SemanticUsageReference || reference.Attnum <= 0 {
					t.Fatalf("unexpected reference: %#v", reference)
				}
			}
		})
	}
}

func TestClosedDMLResolverFailClosedCatalogAndNames(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		sql  string
		edit func(*PostgresCatalogFrame)
		code string
	}{
		{name: "missing target", sql: `UPDATE app.items SET missing=1 WHERE id=1`, code: BinderCodeModeRequired},
		{name: "system reference", sql: `DELETE FROM app.items WHERE ctid IS NOT NULL`, code: "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{name: "wrong qualifier", sql: `DELETE FROM app.items AS i WHERE items.id=1`, code: BinderCodeModeRequired},
		{name: "user type", sql: `UPDATE app.items SET name='x' WHERE id=1`, edit: func(frame *PostgresCatalogFrame) { frame.Columns[1].TypeOID = 20000 }, code: "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{name: "user collation", sql: `UPDATE app.items SET name='x' WHERE id=1`, edit: func(frame *PostgresCatalogFrame) { frame.Columns[1].Collation = 20000 }, code: "AUTH_COLUMN_SHAPE_UNSUPPORTED"},
		{name: "identity", sql: `INSERT INTO app.items (id,name,n) VALUES (1,'a',0)`, edit: func(frame *PostgresCatalogFrame) { frame.Columns[0].Identity = 'a' }, code: string(b5dml.StatementIdentityUnsupported)},
		{name: "generated", sql: `INSERT INTO app.items (id,name) VALUES (1,'a')`, edit: func(frame *PostgresCatalogFrame) { frame.Columns[2].Generated = 's' }, code: string(b5dml.StatementGeneratedUnsupported)},
	} {
		frame := closedDMLUnitFrame()
		if test.edit != nil {
			test.edit(&frame)
		}
		parsed, err := closedparser.ParsePostgresClosedDML(test.sql)
		if err != nil {
			t.Fatalf("%s parser: %v", test.name, err)
		}
		_, err = resolvePostgresClosedDML(context.Background(), parsed, frame, "ds-1", closedDMLUnitOperator, &unlimitedPostgresBudget{})
		requireEasyDeployReason(t, err, test.code)
	}
}

func TestClosedDMLUsesB5GrantLatticeAndBindsRevisionCatalog(t *testing.T) {
	t.Parallel()
	parsed, err := closedparser.ParsePostgresClosedDML(`INSERT INTO app.items (id,name) VALUES (1,'a')`)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := resolvePostgresClosedDML(context.Background(), parsed, closedDMLUnitFrame(), "ds-1", closedDMLUnitOperator, &unlimitedPostgresBudget{})
	if err != nil {
		t.Fatal(err)
	}
	relation := semanticB5Relation(facts.Relations[0])
	grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: b5dml.ActionInsert, Relation: relation}}
	for _, write := range facts.WriteTargets {
		column := b5dml.ColumnIdentity{Relation: relation, Attnum: write.Attnum, Name: write.Name, TypeOID: write.TypeOID, TypeModifier: write.TypeModifier, CollationOID: write.CollationOID}
		grants = append(grants, b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: b5dml.ActionInsert, Relation: relation, WriteKind: b5dml.WriteTargetColumn, Column: column})
	}
	input := closedDMLAuthorizationInput([]b5dml.Policy{{ID: "p1", Revision: 1, PrincipalID: "agent", DatasourceID: "ds-1", Effect: b5dml.GrantAllow, Grants: grants}})
	decision, err := AuthorizeB5(facts, input)
	if err != nil || !decision.Allowed() {
		t.Fatalf("authorization=%#v err=%v", decision, err)
	}
	firstDigest := decision.Digest()
	input.Policies[0].Revision++
	decision, err = AuthorizeB5(facts, input)
	if err != nil || !decision.Allowed() || decision.Digest() == firstDigest {
		t.Fatalf("policy revision not bound: %#v err=%v", decision, err)
	}

	missing := grants[:len(grants)-1]
	input.Policies[0].Grants = missing
	decision, err = AuthorizeB5(facts, input)
	if err != nil || decision.Allowed() || decision.Reason() != b5dml.ReasonWriteTargetGrantMissing {
		t.Fatalf("omitted-column grant bypass: %#v err=%v", decision, err)
	}

	drift := cloneSemanticFacts(facts)
	drift.Identity.CatalogDigest = "other-catalog"
	drift.Relations[0].CatalogFingerprint = "other-catalog"
	input.Policies[0].Grants = grants
	decision, err = AuthorizeB5(drift, input)
	if err != nil || decision.Allowed() {
		t.Fatalf("catalog drift unexpectedly allowed: %#v err=%v", decision, err)
	}
}

func TestClosedDMLDifferentialGate(t *testing.T) {
	t.Parallel()
	parsed, err := closedparser.ParsePostgresClosedDML(`UPDATE app.items SET n=n+1 WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	frame := closedDMLUnitFrame()
	facts, err := resolvePostgresClosedDML(context.Background(), parsed, frame, "ds-1", closedDMLUnitOperator, &unlimitedPostgresBudget{})
	if err != nil {
		t.Fatal(err)
	}
	session := SemanticIdentity{DatasourceIdentity: "ds-1", DatabaseOID: frame.DatabaseOID, SessionUser: "agent", CurrentUser: "agent", RoleOID: 10, FixedSearchPath: "pg_catalog"}
	manifest := PostgresDMLManifest{PostgresPreparedManifest: PostgresPreparedManifest{StatementName: "native", RoleOID: 10, RoleName: "agent", SearchPath: "pg_catalog", CommandType: "UPDATE", AnalyzedDigest: "a", DependencyDigest: "d", PlanGeneration: 1}}
	relation := semanticB5Relation(facts.Relations[0])
	statement := b5dml.StatementFacts{Dialect: b5dml.DialectPostgreSQL, Action: b5dml.ActionUpdate, Shape: b5dml.ShapeSimple, Target: relation}
	for _, write := range facts.WriteTargets {
		statement.Writes = append(statement.Writes, b5dml.ColumnWrite(semanticB5Column(relation, write.Attnum, write.Name, write.TypeOID, write.TypeModifier, write.CollationOID), write.Source))
	}
	for _, reference := range facts.ColumnUses {
		site := b5dml.ReferenceExpression
		if reference.Site == "where" {
			site = b5dml.ReferenceWhere
		}
		statement.References = append(statement.References, b5dml.ColumnReference(semanticB5Column(relation, reference.Attnum, reference.Name, reference.TypeOID, reference.TypeModifier, reference.CollationOID), site))
	}
	enrollment := PostgresDMLEnrollment{Manifest: manifest, Catalog: frame, Facts: statement}
	native, err := NativeDMLSemanticFacts("ds-1", session, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	facts.Identity = native.Identity
	closed := BoundProgram{Mode: BinderModeCatalogClosedV1, Facts: facts}
	if err := CompareClosedWithNativeDML(closed, "ds-1", session, enrollment); err != nil {
		t.Fatalf("equal facts rejected: %v", err)
	}
	enrollment.Facts.Writes[0].Source = b5dml.WriteSourceImplicitNull
	err = CompareClosedWithNativeDML(closed, "ds-1", session, enrollment)
	var binderErr *BinderError
	if !errors.As(err, &binderErr) || binderErr.Code != BinderCodeDivergence {
		t.Fatalf("drift error=%#v", err)
	}
}

func TestClosedDMLCandidateCacheIsHintOnlyAndCloned(t *testing.T) {
	t.Parallel()
	frame := closedDMLUnitFrame()
	facts := closedDMLSemanticFacts(frame, frame.Relations[0], "ds-1", b5dml.ActionDelete,
		[]b5dml.WriteTarget{b5dml.RowDelete(semanticB5Relation(SemanticRelation{DatasourceID: "ds-1", DatabaseOID: 9,
			RelationOID: 100, Schema: "app", Name: "items", Kind: 'r', CatalogFingerprint: frame.Fingerprint}))}, nil)
	key := ClosedCacheKey{DatasourceIdentity: "endpoint", DatabaseOID: 9, ServerMajor: 16, RoleOID: 10,
		SearchPathDigest: "path", SQLDigest: "sql", CapabilityDigest: "capability", CatalogFingerprint: frame.Fingerprint}
	entry := ClosedASTCacheEntry{ASTDigest: "dml-ast", Candidate: ClosedCatalogCandidate{Digest: frame.Fingerprint,
		Frame: frame, Refs: []ClosedRelationRef{{Schema: "app", Name: "items"}}}, Facts: facts}
	cache := NewClosedASTCache(1)
	if !cache.Put(key, entry) {
		t.Fatal("put rejected")
	}
	entry.Facts.WriteTargets[0].RelationOID = 999
	got, ok := cache.Get(key)
	if !ok || got.Facts.WriteTargets[0].RelationOID != 100 {
		t.Fatalf("cache alias or miss: %#v", got)
	}
	got.Facts.WriteTargets[0].RelationOID = 888
	again, ok := cache.Get(key)
	if !ok || again.Facts.WriteTargets[0].RelationOID != 100 {
		t.Fatalf("get returned aliased facts: %#v", again)
	}
	cache.InvalidateCatalog(frame.Fingerprint)
	if _, ok := cache.Get(key); ok {
		t.Fatal("catalog invalidation left DML hint")
	}
}

func closedDMLUnitFrame() PostgresCatalogFrame {
	return PostgresCatalogFrame{ServerVersion: 160000, DatabaseOID: 9, Fingerprint: "catalog-v1",
		Relations: []PostgresRelationIdentity{{DatabaseOID: 9, OID: 100, NamespaceOID: 11, Schema: "app", Name: "items", Kind: 'r', Persistence: 'p'}},
		Columns: []PostgresColumnIdentity{
			{RelationOID: 100, Attnum: 1, Name: "id", TypeOID: 23, Typmod: -1},
			{RelationOID: 100, Attnum: 2, Name: "name", TypeOID: 25, Typmod: -1, Collation: 100},
			{RelationOID: 100, Attnum: 3, Name: "n", TypeOID: 23, Typmod: -1},
		}}
}

func closedDMLUnitOperator(_ context.Context, name string, left, right uint32, _ PostgresCatalogBudget) (closedOperatorIdentity, error) {
	result := left
	if closedComparisonOperator(name) {
		result = 16
	} else if left != right {
		return closedOperatorIdentity{}, NewPrecisionFailure(BinderCodeModeRequired)
	}
	return closedOperatorIdentity{OID: 500 + left + right, FunctionOID: 1500 + left + right, ResultType: result}, nil
}

func semanticB5Relation(relation SemanticRelation) b5dml.RelationIdentity {
	return b5dml.RelationIdentity{DatasourceID: relation.DatasourceID, DatabaseOID: relation.DatabaseOID, RelationOID: relation.RelationOID,
		RelationKind: relation.Kind, Schema: relation.Schema, Name: relation.Name, CatalogFingerprint: relation.CatalogFingerprint}
}

func semanticB5Column(relation b5dml.RelationIdentity, attnum int16, name string, typeOID uint32, typmod int32, collation uint32) b5dml.ColumnIdentity {
	return b5dml.ColumnIdentity{Relation: relation, Attnum: attnum, Name: name, TypeOID: typeOID, TypeModifier: typmod, CollationOID: collation}
}

func closedDMLAuthorizationInput(policies []b5dml.Policy) b5dml.AuthorizationInput {
	return b5dml.AuthorizationInput{PrincipalID: "agent", DatasourceID: "ds-1", Dialect: b5dml.DialectPostgreSQL,
		CurrentServerMajor: 16, Policies: policies, PreliminaryAllowed: true, DatasourceSupported: true,
		CatalogConsistent: true, ClosureProven: true, PolicySnapshotDigest: "policy-snapshot",
		ClosureDigest: "closure", PlanDigest: "caller-plan", Attestations: PostgresDMLBinderAttestations()}
}
