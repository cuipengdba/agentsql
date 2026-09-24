package controlledread

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainer "github.com/testcontainers/testcontainers-go/modules/mysql"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestDiscoveryMySQL8EndToEndE2E(t *testing.T) {
	ctx := controlledReadDockerContext(t)
	const database, username, password = "agentsql_discovery", "root", "agentsql-password"
	container, err := mysqlcontainer.Run(ctx, "mysql:8", mysqlcontainer.WithDatabase(database), mysqlcontainer.WithUsername(username), mysqlcontainer.WithPassword(password))
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start mysql:8 after successful Docker probe")
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{ID: "discovery-mysql8", DBType: "mysql", Host: host, Port: port.Int(), Database: database, Username: username, ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100}
	writer := newControlledReadE2EDatabase(t, datasource, password, false)
	for _, statement := range []string{
		"CREATE TABLE customers (id INT PRIMARY KEY, phone VARCHAR(32), email VARCHAR(128), id_card VARCHAR(32), legacy_id_card VARCHAR(32), bank_card VARCHAR(64), client_ip VARCHAR(64), birth_date DATE, description TEXT)",
		"INSERT INTO customers VALUES " +
			"(1,'13812345678','a@example.com','11010519491231002X','130503670401001','4111 1111-1111 1111','192.168.10.20','2000-02-29','" + discoveryE2ESentinel + "')," +
			"(2,'13987654321','b@example.com','11010519491231002X','130503670401001','4111111111111111','2001:db8::1','1990-01-02','ordinary')," +
			"(3,'13711112222','c@example.com','11010519491231002X','130503670401001','4111-1111-1111-1111','203.0.113.9','1988-12-31','ordinary')",
	} {
		_, err = writer.Execute(ctx, statement)
		require.NoError(t, err)
	}

	t.Run("information_schema backticks LIMIT and discovery", func(t *testing.T) {
		service := newDatabaseDiscoveryService(t, datasource, password)
		result, err := service.Discover(ctx, "mysql-admin", datasource.ID, discovery.ScanRequest{
			Tables: []discovery.TableRef{{Schema: database, Table: "customers"}}, SampleRows: 3,
			Categories: legacyDiscoveryCategories(),
		})
		require.NoError(t, err)
		require.Equal(t, 9, result.Stats.ColumnsSeen)
		require.Equal(t, 7, result.Stats.CandidateColumns)
		requireDiscoveryCategories(t, result, map[discovery.Category]int{
			discovery.CategoryPhone: 1, discovery.CategoryEmail: 1, discovery.CategoryIDCard: 2,
			discovery.CategoryBankCard: 1, discovery.CategoryIP: 1, discovery.CategoryBirthdate: 1,
		})
		require.NotContains(t, fmt.Sprintf("%v", result), discoveryE2ESentinel)
	})
	t.Run("sampling false performs zero sample reads", func(t *testing.T) {
		service := newDatabaseDiscoveryService(t, datasource, password)
		no := false
		result, err := service.Discover(ctx, "mysql-no-sample", datasource.ID, discovery.ScanRequest{
			Tables: []discovery.TableRef{{Schema: database, Table: "customers"}}, Sampling: &no,
			Categories: legacyDiscoveryCategories(),
		})
		require.NoError(t, err)
		require.Zero(t, result.Stats.SampledColumns)
		require.Zero(t, result.Stats.SampledValuesCount)
	})
	t.Run("1142 permission sentinel", func(t *testing.T) {
		err := mysqlDatabasePermissionProbe(ctx, datasource, password)
		require.ErrorIs(t, err, executor.ErrPermissionDenied)
		require.NotContains(t, err.Error(), discoveryE2ESentinel)
	})
}

func TestDiscoveryPostgres18EndToEndE2E(t *testing.T) {
	ctx := controlledReadDockerContext(t)
	const database, username, password = "agentsql_discovery", "agentsql", "agentsql-password"
	container, err := postgrescontainer.Run(ctx, "postgres:18", postgrescontainer.WithDatabase(database), postgrescontainer.WithUsername(username), postgrescontainer.WithPassword(password), postgrescontainer.BasicWaitStrategies())
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		require.NoError(t, err, "start postgres:18 after successful Docker probe")
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	datasource := model.Datasource{ID: "discovery-pg18", DBType: "postgres", Host: host, Port: port.Int(), Database: database, Username: username, ConnLimit: 2, StmtTimeoutMS: 5_000, RowLimit: 100}
	writer := newControlledReadE2EDatabase(t, datasource, password, false)
	for _, statement := range []string{
		"CREATE SCHEMA tenant_a",
		"CREATE SCHEMA tenant_b",
		"CREATE TABLE tenant_a.customers (id integer primary key, phone text, id_card text, legacy_id_card text, bank_card text, client_ip inet, ip_network cidr, birth_date date, description text, price numeric, stock integer, amount numeric, created_at timestamp, ordered_at date, full_name text, name text)",
		"CREATE TABLE tenant_b.customers (id integer primary key, email text)",
		"INSERT INTO tenant_a.customers VALUES " +
			"(1,'13812345678','11010519491231002X','130503670401001','4111 1111-1111 1111','192.0.2.10','198.51.100.0/24','2000-02-29','" + discoveryE2ESentinel + "',19.99,10,49.99,'2026-09-01 08:30:00','2026-09-02','Alice Example','Alice')," +
			"(2,'13987654321','11010519491231002X','130503670401001','4111111111111111','2001:db8::1','2001:db8::/48','1990-01-02','ordinary',29.99,20,59.99,'2026-09-03 09:30:00','2026-09-04','Bob Example','Bob')," +
			"(3,'13711112222','11010519491231002X','130503670401001','4111-1111-1111-1111','203.0.113.9','203.0.113.0/24','1988-12-31','ordinary',39.99,30,69.99,'2026-09-05 10:30:00','2026-09-06','Carol Example','Carol')",
		"INSERT INTO tenant_b.customers VALUES (1,'a@example.com'),(2,'b@example.com'),(3,'c@example.com')",
	} {
		_, err = writer.Execute(ctx, statement)
		require.NoError(t, err)
	}

	t.Run("multiple schemas double quotes LIMIT and discovery", func(t *testing.T) {
		service := newDatabaseDiscoveryService(t, datasource, password)
		result, err := service.Discover(ctx, "pg-admin", datasource.ID, discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "tenant_a", Table: "customers"}, {Schema: "tenant_b", Table: "customers"}}, SampleRows: 3})
		require.NoError(t, err)
		require.Equal(t, 2, result.Stats.TablesScanned)
		require.Equal(t, 16, result.Stats.CandidateColumns)
		requireDiscoveryCategories(t, result, map[discovery.Category]int{
			discovery.CategoryPhone: 1, discovery.CategoryEmail: 1, discovery.CategoryIDCard: 2,
			discovery.CategoryBankCard: 1, discovery.CategoryIP: 2, discovery.CategoryBirthdate: 1,
			discovery.CategoryNumber: 3, discovery.CategoryDate: 2, discovery.CategoryGeneric: 3,
		})
		require.NotContains(t, fmt.Sprintf("%v", result), discoveryE2ESentinel)
	})
	t.Run("invisible table fails whole scope", func(t *testing.T) {
		service := newDatabaseDiscoveryService(t, datasource, password)
		_, err := service.Discover(ctx, "pg-invisible", datasource.ID, discovery.ScanRequest{Tables: []discovery.TableRef{{Schema: "tenant_a", Table: "missing"}}})
		require.ErrorIs(t, err, discovery.ErrScopeNotVisible)
	})
	t.Run("42501 permission sentinel", func(t *testing.T) {
		_, err := writer.Execute(ctx, "CREATE ROLE discovery_limited LOGIN PASSWORD 'limited-password'")
		require.NoError(t, err)
		limited := datasource
		limited.ID = "discovery-pg18-limited"
		limited.Username = "discovery_limited"
		reader := newControlledReadE2EDatabase(t, limited, "limited-password", true)
		_, err = reader.Query(ctx, "SELECT phone FROM tenant_a.customers LIMIT 1", 1)
		require.ErrorIs(t, err, executor.ErrPermissionDenied)
		require.NotContains(t, err.Error(), discoveryE2ESentinel)
	})
}

const discoveryE2ESentinel = "T38_E2E_SENTINEL_DO_NOT_LEAK_90d1"

func requireDiscoveryCategories(t *testing.T, result discovery.ScanResult, expected map[discovery.Category]int) {
	t.Helper()
	actual := make(map[discovery.Category]int)
	for _, finding := range result.Findings {
		actual[finding.Category]++
		switch finding.Category {
		case discovery.CategoryNumber:
			require.True(t, finding.Applicable, finding.Column)
			require.NotNil(t, finding.RecommendedRule, finding.Column)
			require.Equal(t, "number", string(finding.RecommendedRule.SensitiveType), finding.Column)
			require.Equal(t, "block", string(finding.RecommendedRule.Algo), finding.Column)
			require.Nil(t, finding.RecommendedRule.Range, finding.Column)
		case discovery.CategoryDate:
			require.True(t, finding.Applicable, finding.Column)
			require.NotNil(t, finding.RecommendedRule, finding.Column)
			require.Equal(t, "date", string(finding.RecommendedRule.SensitiveType), finding.Column)
			require.Equal(t, "range", string(finding.RecommendedRule.Algo), finding.Column)
			require.NotNil(t, finding.RecommendedRule.Range, finding.Column)
			require.Equal(t, "month", finding.RecommendedRule.Range.Granularity, finding.Column)
		case discovery.CategoryGeneric:
			if finding.Column == "full_name" {
				require.True(t, finding.Applicable, finding.Column)
				require.NotNil(t, finding.RecommendedRule, finding.Column)
				require.Equal(t, "block", string(finding.RecommendedRule.Algo), finding.Column)
			} else {
				require.False(t, finding.Applicable, finding.Column)
				require.Nil(t, finding.RecommendedRule, finding.Column)
				require.Equal(t, discovery.GenericReviewReason, finding.Reason, finding.Column)
			}
		default:
			require.True(t, finding.Applicable, finding.Column)
			require.NotNil(t, finding.RecommendedRule, finding.Column)
			require.Equal(t, string(finding.Category), string(finding.RecommendedRule.SensitiveType), finding.Column)
			require.Equal(t, "mask", string(finding.RecommendedRule.Algo), finding.Column)
		}
	}
	require.Equal(t, expected, actual)
}

func legacyDiscoveryCategories() []discovery.Category {
	return []discovery.Category{
		discovery.CategoryPhone,
		discovery.CategoryEmail,
		discovery.CategoryIDCard,
		discovery.CategoryBankCard,
		discovery.CategoryIP,
		discovery.CategoryBirthdate,
	}
}

func newDatabaseDiscoveryService(t *testing.T, datasource model.Datasource, password string) *Service {
	t.Helper()
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	require.NoError(t, err)
	datasource.PasswordEnc, err = cipher.Encrypt(password)
	require.NoError(t, err)
	service, err := NewService(fakeDatasourceReader{datasource: datasource}, executor.NewGateway(true), secret, rules.NewDefaultTokenBucketLimiter())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Close()) })
	return service
}

func mysqlDatabasePermissionProbe(ctx context.Context, datasource model.Datasource, password string) error {
	admin, err := newControlledReadE2EDatabaseE(ctx, datasource, password, false)
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err = admin.Execute(ctx, "CREATE USER 'discovery_limited'@'%' IDENTIFIED BY 'limited-password'"); err != nil {
		return err
	}
	database, err := quoteIdentifier("mysql", datasource.Database)
	if err != nil {
		return err
	}
	// A MySQL account with no privilege on the configured schema is rejected at
	// COM_INIT_DB with 1044, a connection-stage failure reported as datasource
	// unreachable. Model the realistic "can attach to the schema but cannot read
	// the target table" account: grant SELECT on a throwaway marker table so
	// COM_INIT_DB succeeds, then querying customers fails at query time with
	// table-level 1142 -> executor.ErrPermissionDenied (verified on mysql:8).
	if _, err = admin.Execute(ctx, "CREATE TABLE IF NOT EXISTS "+database+".`discovery_connect_marker` (id INT)"); err != nil {
		return err
	}
	if _, err = admin.Execute(ctx, "GRANT SELECT ON "+database+".`discovery_connect_marker` TO 'discovery_limited'@'%'"); err != nil {
		return err
	}
	limited := datasource
	limited.ID = "discovery-mysql8-limited"
	limited.Username = "discovery_limited"
	reader, err := newControlledReadE2EDatabaseE(ctx, limited, "limited-password", true)
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = reader.Query(ctx, "SELECT phone FROM `"+datasource.Database+"`.`customers` LIMIT 1", 1)
	return err
}

type controlledReadE2EDatabase struct {
	gateway    *executor.Gateway
	datasource model.Datasource
	secret     []byte
}

func newControlledReadE2EDatabase(t *testing.T, datasource model.Datasource, password string, readOnly bool) *controlledReadE2EDatabase {
	t.Helper()
	database, err := newControlledReadE2EDatabaseE(context.Background(), datasource, password, readOnly)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return database
}

func newControlledReadE2EDatabaseE(_ context.Context, datasource model.Datasource, password string, readOnly bool) (*controlledReadE2EDatabase, error) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return nil, err
	}
	datasource.PasswordEnc, err = cipher.Encrypt(password)
	if err != nil {
		return nil, err
	}
	return &controlledReadE2EDatabase{gateway: executor.NewGateway(readOnly), datasource: datasource, secret: secret}, nil
}

func (database *controlledReadE2EDatabase) Execute(ctx context.Context, sqlText string) (model.QueryResult, error) {
	statement, err := database.gateway.AuthorizedExecute(ctx, database.datasource, database.secret, sqlText, "")
	if err != nil {
		return model.QueryResult{}, err
	}
	defer statement.Close()
	return statement.Execute(ctx)
}

func (database *controlledReadE2EDatabase) Query(ctx context.Context, sqlText string, limit int) (model.QueryResult, error) {
	statement, err := database.gateway.AuthorizedExecute(ctx, database.datasource, database.secret, sqlText, "")
	if err != nil {
		return model.QueryResult{}, err
	}
	defer statement.Close()
	return statement.Query(ctx, limit)
}

func (database *controlledReadE2EDatabase) Close() error { return database.gateway.CloseAll() }

func controlledReadDockerContext(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("discovery database E2E is an integration test")
	}
	unavailable, err := probeControlledDocker()
	if unavailable {
		t.Logf("docker daemon unavailable: %v", err)
		t.Skip("docker daemon unavailable")
	}
	require.NoError(t, err, "Docker probe failed after client construction")
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(stop)
	return ctx
}

func probeControlledDocker() (unavailable bool, err error) {
	clientConstructed := false
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker probe panic: %v", recovered)
			unavailable = !clientConstructed || dockerUnavailableError(err)
		}
	}()
	probe, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := testcontainers.NewDockerClient()
	if err != nil {
		return true, err
	}
	clientConstructed = true
	if _, err = client.Ping(probe); err != nil {
		_ = client.Close()
		return dockerUnavailableError(err), err
	}
	return false, client.Close()
}

func dockerUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"cannot connect", "daemon is not running", "connection refused", "no such file", "cannot find the file", "permission denied", "access is denied", "dockerdesktoplinuxengine", "docker_engine"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return errors.Is(err, context.DeadlineExceeded)
}
