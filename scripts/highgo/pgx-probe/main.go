package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func main() {
	ctx := context.Background()
	dsn := os.Getenv("HIGHGO_RO_DSN")
	if dsn == "" {
		panic("HIGHGO_RO_DSN missing")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)
	var user, version, phone string
	if err := conn.QueryRow(ctx, "SELECT current_user, version()").Scan(&user, &version); err != nil {
		panic(err)
	}
	if err := conn.QueryRow(ctx,
		"SELECT phone FROM public.agentsql_batch70_verify WHERE id=$1", 1).Scan(&phone); err != nil {
		panic(err)
	}
	_, err = conn.Exec(ctx, "UPDATE public.agentsql_batch70_verify SET name='forbidden' WHERE id=$1", 1)
	if err == nil {
		panic("read-only role unexpectedly wrote a row")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		panic(fmt.Sprintf("write rejected without PostgreSQL error code: %v", err))
	}
	if pgErr.Code != "42501" {
		panic(fmt.Sprintf("unexpected read-only SQLSTATE: %s", pgErr.Code))
	}
	fmt.Printf("pgx_user=%s bind_phone=%s denied_write_sqlstate=%s version=%s\n", user, phone, pgErr.Code, version)
}
