package parser

import "testing"

func TestParsePostgresClosedSelectAcceptedSubset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		sql       string
		relations int
	}{
		{"direct", `SELECT a.id, a.note FROM app.accounts AS a WHERE a.id > 0 AND a.note IS NOT NULL`, 1},
		{"self-join", `SELECT a.id, b.note FROM app.accounts a INNER JOIN app.accounts b ON a.id = b.id WHERE a.id > 0`, 2},
		{"left-join-expression", `SELECT a.id + b.id FROM app.accounts a LEFT JOIN app.other b ON a.id = b.id WHERE NOT (a.id = 0)`, 2},
		{"exists", `SELECT a.id FROM app.accounts a WHERE EXISTS (SELECT b.id FROM app.other b WHERE b.id = a.id)`, 2},
		{"in-subquery", `SELECT a.id FROM app.accounts a WHERE a.id IN (SELECT b.id FROM app.other b WHERE b.id > 0)`, 2},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := ParsePostgresClosedSelect(test.sql)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if len(parsed.Relations) != test.relations || parsed.ASTDigest == "" || parsed.NodeCount == 0 || parsed.MaxDepth == 0 {
				t.Fatalf("parsed = %#v", parsed)
			}
		})
	}
}

func TestParsePostgresClosedSelectFailClosedCorpus(t *testing.T) {
	t.Parallel()
	tests := []string{
		`SELECT * FROM app.accounts`,
		`SELECT a.* FROM app.accounts a`,
		`SELECT id FROM accounts`,
		`SELECT id FROM app.accounts; SELECT id FROM app.other`,
		`WITH q AS (SELECT id FROM app.accounts) SELECT id FROM q`,
		`SELECT q.id FROM app.accounts a, LATERAL (SELECT a.id) q`,
		`SELECT lower(a.note) FROM app.accounts a`,
		`SELECT a.id::bigint FROM app.accounts a`,
		`SELECT DISTINCT a.id FROM app.accounts a`,
		`SELECT count(a.id) FROM app.accounts a`,
		`SELECT a.id FROM app.accounts a GROUP BY a.id`,
		`SELECT a.id FROM app.accounts a ORDER BY a.id`,
		`SELECT a.id INTO app.copy FROM app.accounts a`,
		`SELECT a.id FROM app.accounts a FOR UPDATE`,
		`SELECT a.id FROM app.accounts a RIGHT JOIN app.other b ON a.id=b.id`,
		`SELECT a.id FROM app.accounts a CROSS JOIN app.other b`,
		`SELECT a.id FROM app.accounts a NATURAL JOIN app.other b`,
		`SELECT a.id FROM app.accounts a JOIN app.other b USING (id)`,
		`SELECT a.id FROM app.accounts a UNION SELECT b.id FROM app.other b`,
		`SELECT dblink('x','SELECT 1') FROM app.accounts a`,
		`UPDATE app.accounts SET id=1`,
	}
	for _, sql := range tests {
		if parsed, err := ParsePostgresClosedSelect(sql); err == nil {
			t.Fatalf("unsafe SQL accepted: %s => %#v", sql, parsed)
		}
	}
}

func TestParsePostgresClosedSelectQuotedIdentifiers(t *testing.T) {
	t.Parallel()
	parsed, err := ParsePostgresClosedSelect(`SELECT "A"."ID" FROM "App"."Accounts" AS "A" WHERE "A"."ID" = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Relations[0]; got.Schema != "App" || got.Name != "Accounts" || got.Alias != "A" {
		t.Fatalf("quoted relation changed: %#v", got)
	}
}
