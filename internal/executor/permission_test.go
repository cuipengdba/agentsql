package executor

import (
	"errors"
	"fmt"
	"net"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestPostgresQueryPermissionErrorIsTypedAndRedacted(t *testing.T) {
	driverError := &pgconn.PgError{Code: "42501", Message: "sentinel-secret SELECT secret_column denied"}
	require.True(t, classifyPostgresPermission(driverError))

	err := postgresDatabaseError("query PostgreSQL datasource", driverError)
	require.ErrorIs(t, err, ErrPermissionDenied)
	require.NotContains(t, err.Error(), driverError.Message)
	require.NotContains(t, err.Error(), "42501")
	require.NotContains(t, err.Error(), "SELECT secret_column")
}

func TestMySQLQueryPermissionErrorsAreTypedAndRedacted(t *testing.T) {
	for _, code := range []uint16{1044, 1142, 1143, 1227} {
		t.Run(fmt.Sprintf("code_%d", code), func(t *testing.T) {
			driverError := &mysqldriver.MySQLError{Number: code, Message: "sentinel-secret SELECT secret_column denied"}
			require.True(t, classifyMySQLPermission(driverError))

			err := mysqlDatabaseError("query MySQL datasource", driverError)
			require.ErrorIs(t, err, ErrPermissionDenied)
			require.NotContains(t, err.Error(), driverError.Message)
			require.NotContains(t, err.Error(), "sentinel-secret")
			require.NotContains(t, err.Error(), fmt.Sprintf("%d", code))
			require.NotContains(t, err.Error(), "SELECT secret_column")
		})
	}
}

func TestConnectionErrorsKeepNonPermissionFailuresUnreachableAndRedacted(t *testing.T) {
	tests := []struct {
		name     string
		classify func(string, error) error
		cause    error
		secrets  []string
	}{
		{
			name:     "MySQL 1044 database access denied",
			classify: mysqlConnectionError,
			cause:    &mysqldriver.MySQLError{Number: 1044, Message: "sentinel-secret database access denied"},
			secrets:  []string{"1044", "sentinel-secret", "database access denied"},
		},
		{
			name:     "MySQL 1045 authentication failure",
			classify: mysqlConnectionError,
			cause:    &mysqldriver.MySQLError{Number: 1045, Message: "sentinel-secret authentication failure"},
			secrets:  []string{"1045", "sentinel-secret", "authentication failure"},
		},
		{
			name:     "MySQL 1049 invalid database",
			classify: mysqlConnectionError,
			cause:    &mysqldriver.MySQLError{Number: 1049, Message: "sentinel-secret unknown database"},
			secrets:  []string{"1049", "sentinel-secret", "unknown database"},
		},
		{
			name:     "MySQL network failure",
			classify: mysqlConnectionError,
			cause:    &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("sentinel-secret network failure")},
			secrets:  []string{"sentinel-secret", "network failure"},
		},
		{
			name:     "PostgreSQL 3D000 invalid catalog name",
			classify: postgresConnectionError,
			cause:    &pgconn.PgError{Code: "3D000", Message: "sentinel-secret invalid_catalog_name"},
			secrets:  []string{"3D000", "sentinel-secret", "invalid_catalog_name"},
		},
		{
			name:     "PostgreSQL 42501 connection permission failure",
			classify: postgresConnectionError,
			cause:    &pgconn.PgError{Code: "42501", Message: "sentinel-secret connection permission failure"},
			secrets:  []string{"42501", "sentinel-secret", "connection permission failure"},
		},
		{
			name:     "PostgreSQL authentication failure",
			classify: postgresConnectionError,
			cause:    &pgconn.PgError{Code: "28P01", Message: "sentinel-secret authentication failure"},
			secrets:  []string{"28P01", "sentinel-secret", "authentication failure"},
		},
		{
			name:     "PostgreSQL network failure",
			classify: postgresConnectionError,
			cause:    &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("sentinel-secret network failure")},
			secrets:  []string{"sentinel-secret", "network failure"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.classify("connect to datasource", test.cause)
			require.ErrorIs(t, err, ErrDatasourceUnreachable)
			require.NotErrorIs(t, err, ErrPermissionDenied)
			for _, secret := range test.secrets {
				require.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestNonPermissionQueryErrorsKeepGenericDatabaseClassification(t *testing.T) {
	mysqlDriverError := &mysqldriver.MySQLError{Number: 1064, Message: "sentinel-secret invalid SQL"}
	require.False(t, classifyMySQLPermission(mysqlDriverError))
	mysqlErr := mysqlDatabaseError("query MySQL datasource", mysqlDriverError)
	require.NotErrorIs(t, mysqlErr, ErrPermissionDenied)
	require.Equal(t, "query MySQL datasource: database operation failed", mysqlErr.Error())

	postgresDriverError := &pgconn.PgError{Code: "42601", Message: "sentinel-secret invalid SQL"}
	require.False(t, classifyPostgresPermission(postgresDriverError))
	postgresErr := postgresDatabaseError("query PostgreSQL datasource", postgresDriverError)
	require.NotErrorIs(t, postgresErr, ErrPermissionDenied)
	require.Equal(t, "query PostgreSQL datasource: database operation failed", postgresErr.Error())
}
