// Package parser converts approved PostgreSQL, MySQL, DM8, and Oracle SQL into
// the frozen model AST.
//
// PostgreSQL uses pg_query_go/v5, which embeds the PostgreSQL parser. MySQL
// uses Vitess vt/sqlparser v0.21.6. Vitess was selected over xwb1989/sqlparser
// because both expose the required AST data under Apache-2.0, while Vitess is
// actively maintained and offers strict DDL parsing; v0.21.6 is the release
// compatible with the project's Go 1.23.10 baseline.
//
// DM8 and Oracle use a separate, dependency-free parser for the documented
// v0.5 read-only SELECT profile. That parser intentionally rejects every SQL
// construct outside the profile instead of treating either vendor as another
// registered dialect.
package parser
