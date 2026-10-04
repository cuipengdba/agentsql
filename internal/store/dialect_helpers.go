package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const DefaultTenantID = "tenant_default"

var ErrTenantRequired = errors.New("tenant ID is required")

type tenantContextKey struct{}

// WithTenant binds an authenticated tenant to repository calls.
func WithTenant(ctx context.Context, tenantID string) (context.Context, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, ErrTenantRequired
	}
	return context.WithValue(ctx, tenantContextKey{}, tenantID), nil
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type repositoryBase struct {
	db      *sql.DB
	dialect Dialect
}

func (repository repositoryBase) tenant(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", ErrNilContext
	}
	if tenantID, ok := ctx.Value(tenantContextKey{}).(string); ok {
		if strings.TrimSpace(tenantID) == "" {
			return "", ErrTenantRequired
		}
		return tenantID, nil
	}
	// Legacy internal and MCP paths remain the explicit default-tenant boundary.
	// Authenticated console requests always override this value through WithTenant.
	return DefaultTenantID, nil
}

func (repository repositoryBase) explicitTenant(ctx context.Context) (string, bool, error) {
	if ctx == nil {
		return "", false, ErrNilContext
	}
	tenantID, ok := ctx.Value(tenantContextKey{}).(string)
	if !ok {
		return DefaultTenantID, false, nil
	}
	if strings.TrimSpace(tenantID) == "" {
		return "", true, ErrTenantRequired
	}
	return tenantID, true, nil
}

func (repository repositoryBase) requireTenant(ctx context.Context, operation string) (string, error) {
	tenantID, err := repository.tenant(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", operation, err)
	}
	return tenantID, nil
}

func (repository repositoryBase) bind(query string) string {
	if repository.dialect == DialectPostgres {
		return rebindPostgres(query)
	}
	return query
}

func insertReturningID(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	insertSQL string,
	args ...any,
) (int64, error) {
	switch dialect {
	case DialectSQLite:
		result, err := executor.ExecContext(ctx, insertSQL, args...)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	case DialectPostgres:
		var id int64
		query := rebindPostgres(insertSQL + " RETURNING id")
		if err := executor.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	default:
		return 0, fmt.Errorf("insert returning ID: unsupported metadata dialect %q", dialect)
	}
}

func approvalCAS(
	ctx context.Context,
	executor sqlExecutor,
	dialect Dialect,
	updateSQL string,
	args ...any,
) (bool, error) {
	switch dialect {
	case DialectSQLite:
		result, err := executor.ExecContext(ctx, updateSQL, args...)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		switch affected {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, fmt.Errorf("conditional approval update affected %d rows", affected)
		}
	case DialectPostgres:
		var id string
		query := rebindPostgres(updateSQL + " RETURNING id")
		err := executor.QueryRowContext(ctx, query, args...).Scan(&id)
		if err == nil {
			return true, nil
		}
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	default:
		return false, fmt.Errorf("approval compare-and-swap: unsupported metadata dialect %q", dialect)
	}
}

func indexHint(dialect Dialect, sqliteFragment string) string {
	if dialect == DialectSQLite {
		return sqliteFragment
	}
	return ""
}

func likeOperator(dialect Dialect) string {
	if dialect == DialectPostgres {
		return "ILIKE"
	}
	return "LIKE"
}

func auditDaySelect(dialect Dialect) string {
	if dialect == DialectPostgres {
		return "to_char((ts AT TIME ZONE 'UTC')::date,'YYYY-MM-DD')"
	}
	return "date(substr(ts,1,19))"
}

func auditDayGroupBy(dialect Dialect) string {
	if dialect == DialectPostgres {
		return "(ts AT TIME ZONE 'UTC')::date"
	}
	return "date(substr(ts,1,19))"
}
