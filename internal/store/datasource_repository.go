package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/model"
)

// DatasourceRepository provides encrypted CRUD operations for datasources.
type DatasourceRepository struct {
	repositoryBase
	cipher *PasswordCipher
}

// Create encrypts plaintextPassword, inserts a datasource, and returns it.
func (repository *DatasourceRepository) Create(
	ctx context.Context,
	datasource model.Datasource,
	plaintextPassword string,
) (model.Datasource, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "create datasource")
	if tenantErr != nil {
		return model.Datasource{}, tenantErr
	}
	passwordEncrypted, err := repository.cipher.Encrypt(plaintextPassword)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("encrypt password for datasource %q: %w", datasource.ID, err)
	}
	_, err = repository.db.ExecContext(ctx, repository.bind(`
INSERT INTO datasources (
  id, tenant_id, name, db_type, host, port, database, username, password_enc,
  conn_limit, stmt_timeout_ms, row_limit, tls_mode, tls_server_name,
  tls_ca_file, trust_server_certificate
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		datasource.ID,
		tenantID,
		datasource.Name,
		datasource.DBType,
		datasource.Host,
		datasource.Port,
		datasource.Database,
		datasource.Username,
		passwordEncrypted,
		datasource.ConnLimit,
		datasource.StmtTimeoutMS,
		datasource.RowLimit,
		datasource.TLSMode,
		datasource.TLSServerName,
		datasource.TLSCAFile,
		datasource.TrustServerCertificate,
	)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("create datasource %q: %w", datasource.ID, err)
	}
	created, err := repository.Get(ctx, datasource.ID)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("read created datasource %q: %w", datasource.ID, err)
	}
	return created, nil
}

// Get returns a datasource by ID with its encrypted password.
func (repository *DatasourceRepository) Get(ctx context.Context, id string) (model.Datasource, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "get datasource")
	if tenantErr != nil {
		return model.Datasource{}, tenantErr
	}
	datasource, err := scanDatasource(repository.db.QueryRowContext(ctx, repository.bind(`
SELECT id, tenant_id, name, db_type, host, port, database, username, password_enc,
       conn_limit, stmt_timeout_ms, row_limit, tls_mode, tls_server_name,
       tls_ca_file, trust_server_certificate, created_at, updated_at
FROM datasources
WHERE id = ? AND tenant_id = ?`), id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Datasource{}, fmt.Errorf("get datasource %q: %w", id, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return model.Datasource{}, fmt.Errorf("get datasource %q: %w", id, err)
	}
	return datasource, nil
}

// List returns every datasource ordered by ID. PasswordEnc is returned only
// for trusted internal use and must never be exposed by an external API.
func (repository *DatasourceRepository) List(ctx context.Context) ([]model.Datasource, error) {
	if ctx == nil {
		return nil, fmt.Errorf("list datasources: %w", ErrNilContext)
	}
	tenantID, err := repository.requireTenant(ctx, "list datasources")
	if err != nil {
		return nil, err
	}
	rows, err := repository.db.QueryContext(ctx, repository.bind(`
SELECT id, tenant_id, name, db_type, host, port, database, username, password_enc,
       conn_limit, stmt_timeout_ms, row_limit, tls_mode, tls_server_name,
       tls_ca_file, trust_server_certificate, created_at, updated_at
FROM datasources
WHERE tenant_id = ?
ORDER BY id ASC`), tenantID)
	if err != nil {
		return nil, fmt.Errorf("list datasources: %w", err)
	}
	datasources := make([]model.Datasource, 0)
	for rows.Next() {
		datasource, err := scanDatasource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan datasource list: %w", closeDatasourceRowsAfterError(rows, err))
		}
		datasources = append(datasources, datasource)
	}
	iterationError := rows.Err()
	closeError := rows.Close()
	if iterationError != nil || closeError != nil {
		return nil, fmt.Errorf("finish datasource list: %w", errors.Join(iterationError, closeError))
	}
	return datasources, nil
}

// Update encrypts plaintextPassword, replaces mutable fields, and returns the datasource.
func (repository *DatasourceRepository) Update(
	ctx context.Context,
	datasource model.Datasource,
	plaintextPassword string,
) (model.Datasource, error) {
	tenantID, tenantErr := repository.requireTenant(ctx, "update datasource")
	if tenantErr != nil {
		return model.Datasource{}, tenantErr
	}
	passwordEncrypted, err := repository.cipher.Encrypt(plaintextPassword)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("encrypt password for datasource %q: %w", datasource.ID, err)
	}
	result, err := repository.db.ExecContext(ctx, repository.bind(`
UPDATE datasources
SET name = ?, db_type = ?, host = ?, port = ?, database = ?, username = ?,
    password_enc = ?, conn_limit = ?, stmt_timeout_ms = ?, row_limit = ?,
    tls_mode = ?, tls_server_name = ?, tls_ca_file = ?, trust_server_certificate = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND tenant_id = ?`),
		datasource.Name,
		datasource.DBType,
		datasource.Host,
		datasource.Port,
		datasource.Database,
		datasource.Username,
		passwordEncrypted,
		datasource.ConnLimit,
		datasource.StmtTimeoutMS,
		datasource.RowLimit,
		datasource.TLSMode,
		datasource.TLSServerName,
		datasource.TLSCAFile,
		datasource.TrustServerCertificate,
		datasource.ID,
		tenantID,
	)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("update datasource %q: %w", datasource.ID, err)
	}
	if err := checkRowsAffected(result, "datasource", datasource.ID); err != nil {
		return model.Datasource{}, fmt.Errorf("update datasource %q: %w", datasource.ID, err)
	}
	updated, err := repository.Get(ctx, datasource.ID)
	if err != nil {
		return model.Datasource{}, fmt.Errorf("read updated datasource %q: %w", datasource.ID, err)
	}
	return updated, nil
}

// Delete removes a datasource by ID.
func (repository *DatasourceRepository) Delete(ctx context.Context, id string) error {
	tenantID, err := repository.requireTenant(ctx, "delete datasource")
	if err != nil {
		return err
	}
	result, err := repository.db.ExecContext(ctx, repository.bind("DELETE FROM datasources WHERE id = ? AND tenant_id = ?"), id, tenantID)
	if err != nil {
		return fmt.Errorf("delete datasource %q: %w", id, err)
	}
	if err := checkRowsAffected(result, "datasource", id); err != nil {
		return fmt.Errorf("delete datasource %q: %w", id, err)
	}
	return nil
}

// DecryptPassword authenticates and decrypts a stored datasource password.
func (repository *DatasourceRepository) DecryptPassword(passwordEncrypted string) (string, error) {
	plaintext, err := repository.cipher.Decrypt(passwordEncrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt stored datasource password: %w", err)
	}
	return plaintext, nil
}

func scanDatasource(scanner rowScanner) (model.Datasource, error) {
	var datasource model.Datasource
	var createdAt, updatedAt databaseTimestamp
	if err := scanner.Scan(
		&datasource.ID,
		&datasource.TenantID,
		&datasource.Name,
		&datasource.DBType,
		&datasource.Host,
		&datasource.Port,
		&datasource.Database,
		&datasource.Username,
		&datasource.PasswordEnc,
		&datasource.ConnLimit,
		&datasource.StmtTimeoutMS,
		&datasource.RowLimit,
		&datasource.TLSMode,
		&datasource.TLSServerName,
		&datasource.TLSCAFile,
		&datasource.TrustServerCertificate,
		&createdAt,
		&updatedAt,
	); err != nil {
		return model.Datasource{}, fmt.Errorf("scan datasource: %w", err)
	}

	var err error
	datasource.CreatedAt, err = createdAt.required("datasources.created_at")
	if err != nil {
		return model.Datasource{}, fmt.Errorf("scan datasource: %w", err)
	}
	datasource.UpdatedAt, err = updatedAt.required("datasources.updated_at")
	if err != nil {
		return model.Datasource{}, fmt.Errorf("scan datasource: %w", err)
	}
	return datasource, nil
}

func closeDatasourceRowsAfterError(rows *sql.Rows, cause error) error {
	if err := rows.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close datasource rows: %w", err))
	}
	return cause
}
