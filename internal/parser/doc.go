// Package parser converts PostgreSQL and MySQL SQL into the frozen model AST.
//
// PostgreSQL uses pg_query_go/v5, which embeds the PostgreSQL parser. MySQL
// uses Vitess vt/sqlparser v0.21.6. Vitess was selected over xwb1989/sqlparser
// because both expose the required AST data under Apache-2.0, while Vitess is
// actively maintained and offers strict DDL parsing; v0.21.6 is the release
// compatible with the project's Go 1.23.10 baseline.
package parser
