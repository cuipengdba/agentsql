package parser

import "testing"

func TestParsePostgresClosedDMLSupportedSubset(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		action   string
		writes   int
		rows     int
		hasWhere bool
		relation PostgresClosedRelation
	}{
		{name: "insert values", sql: `INSERT INTO app.items (id, name) VALUES (1, 'a')`, action: "INSERT", writes: 2, rows: 1, relation: PostgresClosedRelation{Schema: "app", Name: "items"}},
		{name: "update", sql: `UPDATE app.items AS i SET name = 'x', n = n + 1 WHERE i.id = 1 AND n IS NOT NULL`, action: "UPDATE", writes: 2, hasWhere: true, relation: PostgresClosedRelation{Schema: "app", Name: "items", Alias: "i"}},
		{name: "delete", sql: `DELETE FROM app.items AS i WHERE i.id = 1`, action: "DELETE", hasWhere: true, relation: PostgresClosedRelation{Schema: "app", Name: "items", Alias: "i"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := ParsePostgresClosedDML(test.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if parsed.Action != test.action || parsed.Relation != test.relation || len(parsed.InsertColumns)+len(parsed.Assignments) != test.writes || len(parsed.Values) != test.rows || (parsed.Where != nil) != test.hasWhere {
				t.Fatalf("unexpected AST: %#v", parsed)
			}
			if parsed.ASTDigest == "" || parsed.NodeCount == 0 || parsed.MaxDepth == 0 {
				t.Fatalf("missing bounded parse evidence: %#v", parsed)
			}
		})
	}
}

func TestParsePostgresClosedDMLFailClosedCorpus(t *testing.T) {
	tests := []string{
		`INSERT INTO items (id) VALUES (1)`,
		`INSERT INTO app.items VALUES (1)`,
		`INSERT INTO app.items (id) SELECT id FROM app.other`,
		`INSERT INTO app.items (id) VALUES (DEFAULT)`,
		`INSERT INTO app.items (id) VALUES (random())`,
		`INSERT INTO app.items (id) VALUES ((SELECT 1))`,
		`INSERT INTO app.items (id) VALUES (1), (2)`,
		`INSERT INTO app.items (id) VALUES (1), (random())`,
		`INSERT INTO app.items (id) VALUES (1) RETURNING id`,
		`INSERT INTO app.items (id) VALUES (1) ON CONFLICT DO NOTHING`,
		`INSERT INTO app.items (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET id=excluded.id`,
		`WITH q AS (SELECT 1) INSERT INTO app.items (id) VALUES (1)`,
		`UPDATE app.items SET name='x'`,
		`UPDATE app.items SET name=DEFAULT WHERE id=1`,
		`UPDATE app.items SET name=random() WHERE id=1`,
		`UPDATE app.items SET name=(SELECT name FROM app.other LIMIT 1) WHERE id=1`,
		`UPDATE app.items SET name='x' FROM app.other WHERE items.id=other.id`,
		`UPDATE app.items SET name='x' WHERE id=1 RETURNING id`,
		`DELETE FROM app.items`,
		`DELETE FROM app.items USING app.other WHERE items.id=other.id`,
		`DELETE FROM app.items WHERE EXISTS (SELECT 1 FROM app.other)`,
		`DELETE FROM app.items WHERE id=1 RETURNING id`,
		`WITH q AS (SELECT 1) DELETE FROM app.items WHERE id=1`,
		`DELETE FROM ONLY app.items WHERE id=1`,
		`SAVEPOINT x`,
		`DELETE FROM app.items WHERE id=1; DELETE FROM app.items WHERE id=2`,
	}
	for _, sql := range tests {
		t.Run(sql, func(t *testing.T) {
			if parsed, err := ParsePostgresClosedDML(sql); err == nil {
				t.Fatalf("unexpected allow: %#v", parsed)
			}
		})
	}
}

func TestParsePostgresClosedDMLEscapeCorpus(t *testing.T) {
	for _, sql := range []string{
		`UPDATE app.items SET name='x' WHERE (id=1) OR pg_sleep(1) IS NULL`,
		`UPDATE app.items SET name='x' WHERE id IN (SELECT id FROM app.other)`,
		`UPDATE app.items SET (name,n)=('x',1) WHERE id=1`,
		`DELETE FROM app.items i WHERE i.* IS NOT NULL`,
		`DELETE FROM app.items WHERE ctid='(0,1)'`,
		`INSERT INTO app.items (id) VALUES ($1)`,
		`INSERT INTO app.items (id) VALUES (1 /* hidden */ + 0)`,
	} {
		if _, err := ParsePostgresClosedDML(sql); err == nil {
			t.Fatalf("escape accepted: %s", sql)
		}
	}
}
