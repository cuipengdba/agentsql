package businessdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestDBErrorMessagesAndSuggestions(t *testing.T) {
	tests := []struct {
		code       DBErrorCode
		message    string
		suggestion string
	}{
		{DBErrorCodeObjectNotFound, "表或对象不存在", "请检查对象名称和当前数据库"},
		{DBErrorCodeColumnNotFound, "列不存在", "请检查列名、SELECT 列表与条件字段"},
		{DBErrorCodeAlreadyExists, "对象已存在", "请更换名称或检查现有对象"},
		{DBErrorCodeSyntax, "SQL 语法有误", "请检查 SQL 语句的拼写与结构"},
		{DBErrorCodeSemantic, "SQL 语义有误", "请检查字段、类型与语句结构"},
		{DBErrorCodeData, "数据异常，语句未执行", "请检查输入数据的类型与取值"},
		{DBErrorCodeConstraint, "操作违反数据库约束，数据未被修改", "请检查唯一值、外键关联与必填字段"},
		{DBErrorCodeRetryable, "数据库锁冲突或并发冲突，可稍后重试", "请稍后重试；网关不会自动重试写操作"},
		{DBErrorCodeTransaction, "事务状态异常", "请检查事务控制语句后重试"},
		{DBErrorCodeResource, "数据库资源不足", "请缩小查询范围或联系数据库管理员"},
		{DBErrorCodeTimeout, "语句执行超时", "请缩小查询范围或稍后重试"},
		{DBErrorCodeInterrupted, "语句执行被中断", "如仍需要结果请重新发起查询"},
		{DBErrorCodePermission, "数据库账号权限不足", "请联系数据库管理员授权"},
		{DBErrorCodeReadOnly, "目标数据库或连接为只读，写入被拒绝", "请在可写库上执行或调整语句"},
		{DBErrorCodeAuthentication, "数据库认证失败", "请检查数据源账号与密码配置"},
		{DBErrorCodeDatabaseNotFound, "目标数据库不存在", "请检查数据源中的数据库名配置"},
		{DBErrorCodeConnection, "无法连接到数据源", "请检查数据源地址、网络与端口，或稍后重试"},
		{DBErrorCodeExecution, "数据库执行失败", "请检查语句后重试；如反复出现请查看审计记录并联系管理员"},
	}

	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			err := newDBError(DBErrorKindExecution, test.code, DBStageQuery, "secret-code", nil)
			require.Equal(t, test.message, err.Error())
			require.Equal(t, test.suggestion, Suggestion(test.code))
		})
	}

	unknown := DBErrorCode("UNKNOWN")
	require.Equal(t, genericDBErrorMessage, newDBError("unknown", unknown, DBStageQuery, "", nil).Error())
	require.Equal(t, genericDBErrorSuggestion, Suggestion(unknown))
}

func TestDBErrorDoesNotExposeDriverDetails(t *testing.T) {
	driverError := &pgconn.PgError{
		Code:          "42P01",
		Message:       "relation secret_table does not exist",
		Detail:        "password=secret-password",
		Hint:          "SELECT * FROM secret_table",
		InternalQuery: "postgres://admin:secret@private-host:5432/app",
	}
	err := postgresDatabaseError(context.Background(), DBStageExplain, "explain", driverError)

	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, DBStageExplain, databaseError.Stage)
	require.Equal(t, "42P01", databaseError.DriverCode)
	require.Equal(t, "42P01", mustDriverCodeForLog(t, databaseError))

	encoded, marshalError := json.Marshal(err)
	require.NoError(t, marshalError)
	for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), string(encoded)} {
		for _, secret := range []string{
			"42P01",
			"secret_table",
			"secret-password",
			"SELECT *",
			"private-host",
			"5432",
			"admin",
		} {
			require.NotContains(t, rendered, secret)
		}
	}

	var unwrapped *pgconn.PgError
	require.False(t, errors.As(err, &unwrapped))
	require.False(t, errors.Is(err, driverError))

	withoutDriverCode := newDBError(DBErrorKindReadOnly, DBErrorCodeReadOnly, DBStageExecute, "", nil)
	code, ok := withoutDriverCode.DriverCodeForLog()
	require.Empty(t, code)
	require.False(t, ok)
}

func TestPostgresErrorCodeMatrix(t *testing.T) {
	tests := []struct {
		driverCode string
		kind       DBErrorKind
		code       DBErrorCode
	}{
		{"42P01", DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{"42704", DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{"42883", DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{"3F000", DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{"42703", DBErrorKindColumnNotFound, DBErrorCodeColumnNotFound},
		{"42P04", DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{"42P06", DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{"42P07", DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{"42710", DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{"42723", DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{"42601", DBErrorKindSyntax, DBErrorCodeSyntax},
		{"42803", DBErrorKindSemantic, DBErrorCodeSemantic},
		{"42P18", DBErrorKindSemantic, DBErrorCodeSemantic},
		{"42804", DBErrorKindSemantic, DBErrorCodeSemantic},
		{"42P10", DBErrorKindSemantic, DBErrorCodeSemantic},
		{"42702", DBErrorKindSemantic, DBErrorCodeSemantic},
		{"22012", DBErrorKindData, DBErrorCodeData},
		{"23505", DBErrorKindConstraint, DBErrorCodeConstraint},
		{"44000", DBErrorKindConstraint, DBErrorCodeConstraint},
		{"40001", DBErrorKindRetryable, DBErrorCodeRetryable},
		{"40P01", DBErrorKindRetryable, DBErrorCodeRetryable},
		{"55P03", DBErrorKindRetryable, DBErrorCodeRetryable},
		{"25001", DBErrorKindTransaction, DBErrorCodeTransaction},
		{"2D000", DBErrorKindTransaction, DBErrorCodeTransaction},
		{"53100", DBErrorKindResource, DBErrorCodeResource},
		{"54001", DBErrorKindResource, DBErrorCodeResource},
		{"57014", DBErrorKindInterrupted, DBErrorCodeInterrupted},
		{"42501", DBErrorKindPermission, DBErrorCodePermission},
		{"25006", DBErrorKindReadOnly, DBErrorCodeReadOnly},
		{"28000", DBErrorKindAuthentication, DBErrorCodeAuthentication},
		{"28P01", DBErrorKindAuthentication, DBErrorCodeAuthentication},
		{"3D000", DBErrorKindDatabaseNotFound, DBErrorCodeDatabaseNotFound},
		{"08006", DBErrorKindConnection, DBErrorCodeConnection},
		{"57P01", DBErrorKindConnection, DBErrorCodeConnection},
		{"57P02", DBErrorKindConnection, DBErrorCodeConnection},
		{"57P03", DBErrorKindConnection, DBErrorCodeConnection},
		{"57P04", DBErrorKindConnection, DBErrorCodeConnection},
		{"57P05", DBErrorKindConnection, DBErrorCodeConnection},
		{"XX000", DBErrorKindExecution, DBErrorCodeExecution},
	}

	for _, test := range tests {
		t.Run(test.driverCode, func(t *testing.T) {
			driverError := &pgconn.PgError{Code: test.driverCode, Message: "secret driver message"}
			err := postgresDatabaseError(context.Background(), DBStageQuery, "query", driverError)
			databaseError := requireDBError(t, err, test.kind, test.code, DBStageQuery)
			require.Equal(t, test.driverCode, mustDriverCodeForLog(t, databaseError))
			var raw *pgconn.PgError
			require.False(t, errors.As(err, &raw))
		})
	}
}

func TestPostgresClassificationPriorityAndCancellation(t *testing.T) {
	t.Run("context deadline precedes exact SQLSTATE", func(t *testing.T) {
		cause := errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "42501"})
		err := postgresDatabaseError(context.Background(), DBStageExplain, "explain", cause)
		requireDBError(t, err, DBErrorKindTimeout, DBErrorCodeTimeout, DBStageExplain)
	})
	t.Run("exact code precedes class prefix", func(t *testing.T) {
		err := postgresDatabaseError(context.Background(), DBStageQuery, "query", &pgconn.PgError{Code: "42501"})
		requireDBError(t, err, DBErrorKindPermission, DBErrorCodePermission, DBStageQuery)
	})
	t.Run("class prefix precedes driver fallback", func(t *testing.T) {
		err := postgresDatabaseError(context.Background(), DBStageQuery, "query", &pgconn.PgError{Code: "42ZZZ"})
		requireDBError(t, err, DBErrorKindSemantic, DBErrorCodeSemantic, DBStageQuery)
	})
	t.Run("57014 deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := postgresDatabaseError(ctx, DBStageQuery, "query", &pgconn.PgError{Code: "57014"})
		requireDBError(t, err, DBErrorKindTimeout, DBErrorCodeTimeout, DBStageQuery)
		require.ErrorIs(t, err, ErrQueryTimeout)
	})
	t.Run("57014 explicit cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := postgresDatabaseError(ctx, DBStageQuery, "query", &pgconn.PgError{Code: "57014"})
		requireDBError(t, err, DBErrorKindInterrupted, DBErrorCodeInterrupted, DBStageQuery)
		require.ErrorIs(t, err, ErrQueryTimeout)
	})
}

func TestMySQLErrorCodeMatrix(t *testing.T) {
	tests := []struct {
		driverCode uint16
		kind       DBErrorKind
		code       DBErrorCode
	}{
		{1146, DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{1051, DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{1305, DBErrorKindObjectNotFound, DBErrorCodeObjectNotFound},
		{1054, DBErrorKindColumnNotFound, DBErrorCodeColumnNotFound},
		{1050, DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{1304, DBErrorKindAlreadyExists, DBErrorCodeAlreadyExists},
		{1064, DBErrorKindSyntax, DBErrorCodeSyntax},
		{1149, DBErrorKindSyntax, DBErrorCodeSyntax},
		{1055, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1056, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1058, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1060, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1066, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1113, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1222, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1318, DBErrorKindSemantic, DBErrorCodeSemantic},
		{1264, DBErrorKindData, DBErrorCodeData},
		{1265, DBErrorKindData, DBErrorCodeData},
		{1292, DBErrorKindData, DBErrorCodeData},
		{1365, DBErrorKindData, DBErrorCodeData},
		{1366, DBErrorKindData, DBErrorCodeData},
		{1406, DBErrorKindData, DBErrorCodeData},
		{1048, DBErrorKindConstraint, DBErrorCodeConstraint},
		{1062, DBErrorKindConstraint, DBErrorCodeConstraint},
		{1364, DBErrorKindConstraint, DBErrorCodeConstraint},
		{1451, DBErrorKindConstraint, DBErrorCodeConstraint},
		{1452, DBErrorKindConstraint, DBErrorCodeConstraint},
		{3819, DBErrorKindConstraint, DBErrorCodeConstraint},
		{1205, DBErrorKindRetryable, DBErrorCodeRetryable},
		{1213, DBErrorKindRetryable, DBErrorCodeRetryable},
		{3572, DBErrorKindRetryable, DBErrorCodeRetryable},
		{1192, DBErrorKindTransaction, DBErrorCodeTransaction},
		{1568, DBErrorKindTransaction, DBErrorCodeTransaction},
		{1021, DBErrorKindResource, DBErrorCodeResource},
		{1037, DBErrorKindResource, DBErrorCodeResource},
		{1038, DBErrorKindResource, DBErrorCodeResource},
		{1040, DBErrorKindResource, DBErrorCodeResource},
		{1114, DBErrorKindResource, DBErrorCodeResource},
		{1203, DBErrorKindResource, DBErrorCodeResource},
		{1206, DBErrorKindResource, DBErrorCodeResource},
		{1226, DBErrorKindResource, DBErrorCodeResource},
		{1390, DBErrorKindResource, DBErrorCodeResource},
		{1461, DBErrorKindResource, DBErrorCodeResource},
		{1473, DBErrorKindResource, DBErrorCodeResource},
		{4025, DBErrorKindResource, DBErrorCodeResource},
		{3024, DBErrorKindTimeout, DBErrorCodeTimeout},
		{1317, DBErrorKindInterrupted, DBErrorCodeInterrupted},
		{1044, DBErrorKindPermission, DBErrorCodePermission},
		{1142, DBErrorKindPermission, DBErrorCodePermission},
		{1143, DBErrorKindPermission, DBErrorCodePermission},
		{1227, DBErrorKindPermission, DBErrorCodePermission},
		{1370, DBErrorKindPermission, DBErrorCodePermission},
		{1792, DBErrorKindReadOnly, DBErrorCodeReadOnly},
		{1045, DBErrorKindAuthentication, DBErrorCodeAuthentication},
		{1049, DBErrorKindDatabaseNotFound, DBErrorCodeDatabaseNotFound},
		{1290, DBErrorKindExecution, DBErrorCodeExecution},
		{1180, DBErrorKindExecution, DBErrorCodeExecution},
		{2003, DBErrorKindExecution, DBErrorCodeExecution},
		{2006, DBErrorKindExecution, DBErrorCodeExecution},
		{2013, DBErrorKindExecution, DBErrorCodeExecution},
		{9999, DBErrorKindExecution, DBErrorCodeExecution},
	}

	for _, test := range tests {
		t.Run(fmt.Sprintf("%d", test.driverCode), func(t *testing.T) {
			driverError := &mysqldriver.MySQLError{Number: test.driverCode, Message: "secret driver message"}
			err := mysqlDatabaseError(context.Background(), DBStageExecute, "execute", driverError)
			databaseError := requireDBError(t, err, test.kind, test.code, DBStageExecute)
			require.Equal(t, fmt.Sprintf("%d", test.driverCode), mustDriverCodeForLog(t, databaseError))
			var raw *mysqldriver.MySQLError
			require.False(t, errors.As(err, &raw))
		})
	}
}

func TestMySQLClassificationPriorityAndConnectionPaths(t *testing.T) {
	t.Run("context cancel precedes server code", func(t *testing.T) {
		cause := errors.Join(context.Canceled, &mysqldriver.MySQLError{Number: 3024})
		err := mysqlDatabaseError(context.Background(), DBStageQuery, "query", cause)
		requireDBError(t, err, DBErrorKindInterrupted, DBErrorCodeInterrupted, DBStageQuery)
	})
	t.Run("confirmed driver error precedes network fallback", func(t *testing.T) {
		cause := errors.Join(&mysqldriver.MySQLError{Number: 2003}, io.EOF)
		err := mysqlDatabaseError(context.Background(), DBStageConnect, "connect", cause)
		requireDBError(t, err, DBErrorKindExecution, DBErrorCodeExecution, DBStageConnect)
	})

	connectionErrors := []error{
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		io.EOF,
		io.ErrUnexpectedEOF,
		driver.ErrBadConn,
		mysqldriver.ErrInvalidConn,
		errors.New("Error 2003: Can't connect to MySQL server"),
		errors.New("Error 2006: MySQL server has gone away"),
		errors.New("Error 2013: Lost connection to MySQL server"),
	}
	for index, cause := range connectionErrors {
		t.Run(fmt.Sprintf("connection_%d", index), func(t *testing.T) {
			err := mysqlDatabaseError(context.Background(), DBStageConnect, "connect", cause)
			databaseError := requireDBError(
				t,
				err,
				DBErrorKindConnection,
				DBErrorCodeConnection,
				DBStageConnect,
			)
			_, ok := databaseError.DriverCodeForLog()
			require.False(t, ok)
			require.ErrorIs(t, err, ErrDatasourceUnreachable)
		})
	}
}

func TestUnknownOrdinaryErrorsRemainInternal(t *testing.T) {
	for _, test := range []struct {
		name     string
		classify func(context.Context, DBStage, string, error) error
	}{
		{name: "postgres", classify: postgresDatabaseError},
		{name: "mysql", classify: mysqlDatabaseError},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.classify(context.Background(), DBStageQuery, "query", errors.New("boom secret SQL"))
			var databaseError *DBError
			require.False(t, errors.As(err, &databaseError))
			require.Equal(t, "query: database operation failed", err.Error())
			require.NotContains(t, err.Error(), "boom")
			require.NotContains(t, err.Error(), "secret SQL")
		})
	}
}

func TestDBErrorCompatibilitySentinels(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		sentinel error
	}{
		{
			name:     "timeout",
			err:      postgresDatabaseError(context.Background(), DBStageQuery, "query", context.DeadlineExceeded),
			sentinel: ErrQueryTimeout,
		},
		{
			name:     "interrupted remains timeout compatible",
			err:      mysqlDatabaseError(context.Background(), DBStageQuery, "query", &mysqldriver.MySQLError{Number: 1317}),
			sentinel: ErrQueryTimeout,
		},
		{
			name:     "permission",
			err:      postgresDatabaseError(context.Background(), DBStageQuery, "query", &pgconn.PgError{Code: "42501"}),
			sentinel: ErrPermissionDenied,
		},
		{
			name:     "read only",
			err:      mysqlDatabaseError(context.Background(), DBStageExecute, "execute", &mysqldriver.MySQLError{Number: 1792}),
			sentinel: ErrReadOnlyViolated,
		},
		{
			name:     "connection",
			err:      postgresDatabaseError(context.Background(), DBStageConnect, "connect", io.EOF),
			sentinel: ErrDatasourceUnreachable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorIs(t, test.err, test.sentinel)
		})
	}
}

func TestManagerPreservesDBErrorAndKeepsOrdinaryOpenerFailureInternal(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	datasource := encryptedDatasource(t, secret, "db_error", "postgres")
	source := newDBError(
		DBErrorKindAuthentication,
		DBErrorCodeAuthentication,
		DBStagePing,
		"28P01",
		nil,
	)
	manager := newManager(false, func(model.Datasource, string, bool) (Executor, error) {
		return nil, source
	})

	_, err := manager.GetOrOpen(datasource, secret)
	require.Same(t, source, err)
	requireDBError(t, err, DBErrorKindAuthentication, DBErrorCodeAuthentication, DBStagePing)

	ordinaryManager := newManager(false, func(model.Datasource, string, bool) (Executor, error) {
		return nil, errors.New("decrypt/config secret failure")
	})
	_, err = ordinaryManager.GetOrOpen(datasource, secret)
	require.Error(t, err)
	var databaseError *DBError
	require.False(t, errors.As(err, &databaseError))
	require.ErrorIs(t, err, ErrDatasourceUnreachable)
	require.NotContains(t, err.Error(), "decrypt/config secret failure")
}

func requireDBError(
	t *testing.T,
	err error,
	kind DBErrorKind,
	code DBErrorCode,
	stage DBStage,
) *DBError {
	t.Helper()
	var databaseError *DBError
	require.ErrorAs(t, err, &databaseError)
	require.Equal(t, kind, databaseError.Kind)
	require.Equal(t, code, databaseError.Code)
	require.Equal(t, stage, databaseError.Stage)
	require.Equal(t, databaseError.Error(), err.Error())
	return databaseError
}

func mustDriverCodeForLog(t *testing.T, err *DBError) string {
	t.Helper()
	code, ok := err.DriverCodeForLog()
	require.True(t, ok)
	return code
}
