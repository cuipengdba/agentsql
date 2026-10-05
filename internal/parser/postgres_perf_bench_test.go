package parser

import (
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v5"
)

// BenchmarkPostgresParseCorpus measures the complete public parser path for
// representative query shapes. The P99 release gate is a separate test:
// Go's benchmark ns/op is an average and cannot stand in for a percentile.
func BenchmarkPostgresParseCorpus(b *testing.B) {
	cases := []struct {
		name string
		sql  string
	}{
		{"select", "SELECT c.phone FROM public.customers c"},
		{"join", "SELECT c.phone, o.total FROM public.customers c JOIN public.orders o ON o.customer_id = c.id"},
		{"where_literals", "SELECT c.phone FROM public.customers c WHERE c.id = 42 AND c.status = 'active'"},
		{"cte", "WITH active AS (SELECT c.id, c.phone FROM public.customers c WHERE c.active) SELECT a.phone FROM active a"},
		{"ddl", "CREATE TABLE public.parser_bench (id bigint PRIMARY KEY, name text)"},
		{"typical_union", projectionLineageSetSQL(postgresDialect, 8, 8)},
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			approved := &postgresParser{}
			if _, err := approved.Parse(test.sql); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(test.sql)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := approved.Parse(test.sql); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPostgresTypicalStages isolates the C parser and Go JSON decoding
// from the full parse. Run it with -benchmem and -cpuprofile to investigate
// changes to the release-gate query without changing the gate itself.
func BenchmarkPostgresTypicalStages(b *testing.B) {
	sql := projectionLineageSetSQL(postgresDialect, 8, 8)
	parsedJSON, err := pg_query.ParseToJSON(sql)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("pg_query_json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := pg_query.ParseToJSON(sql); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode_json", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := decodePostgresDocument(parsedJSON); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("full_parse", func(b *testing.B) {
		approved := &postgresParser{}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := approved.Parse(sql); err != nil {
				b.Fatal(err)
			}
		}
	})
}
