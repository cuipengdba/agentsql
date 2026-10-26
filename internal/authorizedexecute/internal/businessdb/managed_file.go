package businessdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/managedfile"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5"
)

const ManagedFileSchema = "agentsql_files"

var managedFileTableName = regexp.MustCompile(`^agentsql_file_[0-9a-f]{32}$`)
var ErrManagedFileInvalid = errors.New("invalid managed file load")

// ManagedFileSheet is a typed, bounded import payload. It contains no SQL,
// datasource credentials, table name, or schema chosen by the client.
type ManagedFileSheet struct {
	Columns []ManagedFileColumn
	Rows    [][]any
}

type ManagedFileColumn struct {
	Name string
	Type string
}

type ManagedFileTable struct {
	Schema string
	Name   string
	Rows   int
}

type ManagedFileOperationID = managedfile.OperationID

func NewManagedFileOperationID() (ManagedFileOperationID, error) {
	return managedfile.NewOperationID()
}

func ParseManagedFileOperationID(value string) (ManagedFileOperationID, error) {
	id, err := managedfile.ParseOperationID(value)
	if err != nil {
		return ManagedFileOperationID{}, ErrManagedFileInvalid
	}
	return id, nil
}

// validateManagedFileSheets must run before opening any database connection.
func validateManagedFileSheets(sheets []ManagedFileSheet) error {
	if len(sheets) == 0 || len(sheets) > 8 {
		return ErrManagedFileInvalid
	}
	var bytesTotal int64
	for _, sheet := range sheets {
		if len(sheet.Columns) == 0 || len(sheet.Columns) > 32 || len(sheet.Rows) > 1000 {
			return ErrManagedFileInvalid
		}
		seen := map[string]bool{}
		for _, column := range sheet.Columns {
			if column.Name == "" || len(column.Name) > 63 || strings.TrimSpace(column.Name) != column.Name ||
				!utf8.ValidString(column.Name) || strings.IndexFunc(column.Name, unicode.IsControl) >= 0 || seen[strings.ToLower(column.Name)] {
				return ErrManagedFileInvalid
			}
			seen[strings.ToLower(column.Name)] = true
			bytesTotal += int64(len(column.Name))
			if managedFileSQLType(column.Type) == "" {
				return ErrManagedFileInvalid
			}
		}
		for _, row := range sheet.Rows {
			if len(row) != len(sheet.Columns) {
				return ErrManagedFileInvalid
			}
			for index, value := range row {
				if !validManagedFileValue(sheet.Columns[index].Type, value) {
					return ErrManagedFileInvalid
				}
				if text, ok := value.(string); ok {
					bytesTotal += int64(len(text))
				} else {
					bytesTotal += 16
				}
				if bytesTotal > 16<<20 {
					return ErrManagedFileInvalid
				}
			}
		}
	}
	return nil
}

func managedFileSQLType(kind string) string {
	switch kind {
	case "string":
		return "text"
	case "int":
		return "bigint"
	case "float":
		return "double precision"
	case "datetime":
		return "timestamp with time zone"
	default:
		return ""
	}
}

func validManagedFileValue(kind string, value any) bool {
	if value == nil {
		return true
	}
	switch kind {
	case "string":
		text, ok := value.(string)
		return ok && len(text) <= 65536 && utf8.ValidString(text) && strings.IndexFunc(text, func(r rune) bool { return r == 0 }) < 0
	case "int":
		_, ok := value.(int64)
		return ok
	case "float":
		number, ok := value.(float64)
		return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
	case "datetime":
		instant, ok := value.(time.Time)
		return ok && instant.Year() >= 1 && instant.Year() <= 9999
	default:
		return false
	}
}

// The operation ID must be journaled in the control store before staging.
// Every table name is recoverable after a process crash without listing or
// guessing tables in an unrelated schema.
func ManagedFileName(operation ManagedFileOperationID, sheet int) (string, error) {
	name, err := managedfile.TableName(operation, sheet)
	if err != nil {
		return "", ErrManagedFileInvalid
	}
	return name, nil
}

// StageManagedFiles creates and loads all sheets in one PostgreSQL transaction.
// The dedicated schema must already exist, and its provisioning role must be
// separate from every read-only Agent datasource role. No SELECT grant is made
// here; publication requires a later policy/audit commit and explicit grant.
// A crash after this commit leaves hidden tables for journal reconciliation.
func StageManagedFiles(ctx context.Context, datasource model.Datasource, secret, readRole string, operation ManagedFileOperationID, sheets []ManagedFileSheet) ([]ManagedFileTable, error) {
	if ctx == nil || datasource.DBType != "postgres" || readRole == "" || readRole == datasource.Username ||
		operation == (ManagedFileOperationID{}) || validateManagedFileSheets(sheets) != nil {
		return nil, ErrManagedFileInvalid
	}
	executor, err := NewPostgresExecutor(ctx, datasource, secret, false)
	if err != nil {
		return nil, err
	}
	defer executor.Close()
	if executor.restrictedVendorDML {
		return nil, ErrManagedFileInvalid
	}
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if !managedFilePostgresIdentity(ctx, tx) {
		return nil, ErrManagedFileInvalid
	}
	var ownsSchema bool
	if err := tx.QueryRow(ctx, `SELECT n.nspowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) FROM pg_catalog.pg_namespace n WHERE n.nspname=$1`, ManagedFileSchema).Scan(&ownsSchema); err != nil || !ownsSchema {
		return nil, ErrManagedFileInvalid
	}
	results := make([]ManagedFileTable, 0, len(sheets))
	for index, sheet := range sheets {
		name, err := ManagedFileName(operation, index)
		if err != nil {
			return nil, err
		}
		if !managedFileTableName.MatchString(name) {
			return nil, ErrManagedFileInvalid
		}
		qualified := pgx.Identifier{ManagedFileSchema, name}.Sanitize()
		definitions := make([]string, len(sheet.Columns))
		columnNames := make([]string, len(sheet.Columns))
		for index, column := range sheet.Columns {
			definitions[index] = pgx.Identifier{column.Name}.Sanitize() + " " + managedFileSQLType(column.Type)
			columnNames[index] = column.Name
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE "+qualified+" ("+strings.Join(definitions, ", ")+")"); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "REVOKE ALL ON TABLE "+qualified+" FROM PUBLIC"); err != nil {
			return nil, err
		}
		// A role's default privileges may grant a newly created table to other
		// roles. Refuse to commit any table with a non-owner ACL entry.
		var foreignGrants int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl, pg_catalog.acldefault('r',c.relowner))) a
			WHERE n.nspname=$1 AND c.relname=$2 AND a.grantee<>c.relowner`, ManagedFileSchema, name).Scan(&foreignGrants); err != nil || foreignGrants != 0 {
			return nil, ErrManagedFileInvalid
		}
		// A different login name is insufficient isolation: a reader can
		// inherit the owner's privileges through PostgreSQL role membership.
		// Check the effective privilege on each freshly created relation
		// before committing any data or returning a successful stage.
		var readerCanSelect bool
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1::name,c.oid,'SELECT')
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$2 AND c.relname=$3`, readRole, ManagedFileSchema, name).Scan(&readerCanSelect); err != nil || readerCanSelect {
			return nil, ErrManagedFileInvalid
		}
		if len(sheet.Rows) > 0 {
			copied, err := tx.CopyFrom(ctx, pgx.Identifier{ManagedFileSchema, name}, columnNames, pgx.CopyFromRows(sheet.Rows))
			if err != nil || copied != int64(len(sheet.Rows)) {
				return nil, fmt.Errorf("managed file load failed")
			}
		}
		results = append(results, ManagedFileTable{Schema: ManagedFileSchema, Name: name, Rows: len(sheet.Rows)})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

// DropManagedFiles is idempotent cleanup for a journaled, unpublished import.
// It only addresses exact names derived from one operation, refuses tables
// owned by another role, and never uses CASCADE.
func DropManagedFiles(ctx context.Context, datasource model.Datasource, secret string, operation ManagedFileOperationID, sheetCount int) error {
	if ctx == nil || datasource.DBType != "postgres" || operation == (ManagedFileOperationID{}) || sheetCount < 1 || sheetCount > 8 {
		return ErrManagedFileInvalid
	}
	executor, err := NewPostgresExecutor(ctx, datasource, secret, false)
	if err != nil {
		return err
	}
	defer executor.Close()
	if executor.restrictedVendorDML {
		return ErrManagedFileInvalid
	}
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !managedFilePostgresIdentity(ctx, tx) {
		return ErrManagedFileInvalid
	}
	var ownsSchema bool
	if err := tx.QueryRow(ctx, `SELECT n.nspowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) FROM pg_catalog.pg_namespace n WHERE n.nspname=$1`, ManagedFileSchema).Scan(&ownsSchema); err != nil || !ownsSchema {
		return ErrManagedFileInvalid
	}
	for index := 0; index < sheetCount; index++ {
		name, err := ManagedFileName(operation, index)
		if err != nil {
			return err
		}
		var owned bool
		err = tx.QueryRow(ctx, `SELECT c.relkind='r' AND c.relowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$1 AND c.relname=$2`, ManagedFileSchema, name).Scan(&owned)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil || !owned {
			return ErrManagedFileInvalid
		}
		if _, err := tx.Exec(ctx, "DROP TABLE "+pgx.Identifier{ManagedFileSchema, name}.Sanitize()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GrantManagedFileRead is the physical publication step. It accepts only
// journal-derived names and grants SELECT to the configured read role after
// proving that every table is still owned by the provisioning role. The
// control-store owner, policy and audit gates must run before this method;
// until the journal is published, the gateway still denies reads.
func GrantManagedFileRead(ctx context.Context, datasource model.Datasource, secret, readRole string, operation ManagedFileOperationID, sheetCount int) error {
	if ctx == nil || datasource.DBType != "postgres" || readRole == "" || readRole == datasource.Username ||
		operation == (ManagedFileOperationID{}) || sheetCount < 1 || sheetCount > 8 {
		return ErrManagedFileInvalid
	}
	executor, err := NewPostgresExecutor(ctx, datasource, secret, false)
	if err != nil {
		return err
	}
	defer executor.Close()
	if executor.restrictedVendorDML {
		return ErrManagedFileInvalid
	}
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !managedFilePostgresIdentity(ctx, tx) {
		return ErrManagedFileInvalid
	}
	var ownsSchema bool
	if err := tx.QueryRow(ctx, `SELECT n.nspowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
		FROM pg_catalog.pg_namespace n WHERE n.nspname=$1`, ManagedFileSchema).Scan(&ownsSchema); err != nil || !ownsSchema {
		return ErrManagedFileInvalid
	}
	var readerCanUseSchema bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.has_schema_privilege($1::name,$2,'USAGE')`, readRole, ManagedFileSchema).Scan(&readerCanUseSchema); err != nil || !readerCanUseSchema {
		return ErrManagedFileInvalid
	}
	for index := 0; index < sheetCount; index++ {
		name, err := ManagedFileName(operation, index)
		if err != nil {
			return err
		}
		qualified := pgx.Identifier{ManagedFileSchema, name}.Sanitize()
		if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return ErrManagedFileInvalid
		}
		var ownerIsCurrent bool
		err = tx.QueryRow(ctx, `SELECT c.relkind='r' AND c.relowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$1 AND c.relname=$2`, ManagedFileSchema, name).Scan(&ownerIsCurrent)
		if err != nil || !ownerIsCurrent {
			return ErrManagedFileInvalid
		}
		// A role with inherited owner privileges cannot be safely isolated
		// by revoking the explicit grant later.
		var inheritedOwner bool
		err = tx.QueryRow(ctx, `SELECT pg_catalog.pg_has_role($1::name,c.relowner,'USAGE')
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$2 AND c.relname=$3`, readRole, ManagedFileSchema, name).Scan(&inheritedOwner)
		if err != nil || inheritedOwner {
			return ErrManagedFileInvalid
		}
		var foreignGrants int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner))) a
			WHERE n.nspname=$1 AND c.relname=$2 AND a.grantee<>c.relowner
			AND (a.grantee<>(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=$3) OR a.privilege_type<>'SELECT')`,
			ManagedFileSchema, name, readRole).Scan(&foreignGrants); err != nil || foreignGrants != 0 {
			return ErrManagedFileInvalid
		}
		if _, err := tx.Exec(ctx, "GRANT SELECT ON TABLE "+qualified+" TO "+pgx.Identifier{readRole}.Sanitize()); err != nil {
			return err
		}
		var readable bool
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1::name,$2,'SELECT')`, readRole, qualified).Scan(&readable); err != nil || !readable {
			return ErrManagedFileInvalid
		}
	}
	return tx.Commit(ctx)
}

func managedFilePostgresIdentity(ctx context.Context, tx pgx.Tx) bool {
	var product string
	return tx.QueryRow(ctx, `SELECT pg_catalog.version()`).Scan(&product) == nil && strings.HasPrefix(product, "PostgreSQL ")
}
