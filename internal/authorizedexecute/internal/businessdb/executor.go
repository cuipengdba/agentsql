package businessdb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/rules"
	"github.com/cuipengdba/agentsql/internal/store"
)

const (
	defaultConnectionLimit  = 5
	defaultStatementTimeout = 5_000
)

// Executor is one dialect-specific controlled database connection pool.
type Executor interface {
	Dialect() string
	Ping(ctx context.Context) error
	OpenSession(ctx context.Context, sessionID string) (Session, error)
	BeginWriteTx(ctx context.Context) (WriteTx, error)
	Explain(ctx context.Context, sql string) (model.ExplainInfo, error)
	Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error)
	Execute(ctx context.Context, sql string) (model.QueryResult, error)
	Close() error
}

// Session is one physical database connection bound to an AgentSQL session.
// The non-matching dialect transaction-state method returns an error.
type Session interface {
	BeginWriteTx(ctx context.Context) (WriteTx, error)
	Query(ctx context.Context, sql string, rowLimit int) (model.QueryResult, error)
	Execute(ctx context.Context, sql string) (model.QueryResult, error)
	Explain(ctx context.Context, sql string) (model.ExplainInfo, error)
	TransactionState() (rules.TransactionState, error)
	MysqlTransactionState() (rules.MysqlTransactionState, error)
	Close() error
}

// WriteTx is a dialect-independent business write transaction. Implementations
// retain ownership of their native transaction and connection handles.
type WriteTx interface {
	Execute(ctx context.Context, sql string) (model.QueryResult, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type executorOpener func(
	datasource model.Datasource,
	password string,
	readOnly bool,
) (Executor, error)

// Manager owns one reusable executor pool per datasource ID.
type Manager struct {
	mu        sync.RWMutex
	executors map[string]Executor
	readOnly  bool
	opener    executorOpener
}

// NewManager returns a concurrency-safe pool manager.
func NewManager(readOnly bool) *Manager {
	return newManager(readOnly, openExecutor)
}

func newManager(readOnly bool, opener executorOpener) *Manager {
	return &Manager{
		executors: make(map[string]Executor),
		readOnly:  readOnly,
		opener:    opener,
	}
}

// GetOrOpen returns an existing datasource pool or opens it exactly once.
func (manager *Manager) GetOrOpen(
	datasource model.Datasource,
	secret []byte,
) (Executor, error) {
	if manager == nil || isNilFunction(manager.opener) {
		return nil, fmt.Errorf("get executor: manager is not initialized")
	}
	if strings.TrimSpace(datasource.ID) == "" {
		return nil, fmt.Errorf("get executor: datasource ID is required")
	}

	manager.mu.RLock()
	existing := manager.executors[datasource.ID]
	manager.mu.RUnlock()
	if !isNilExecutor(existing) {
		return existing, nil
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.executors == nil {
		manager.executors = make(map[string]Executor)
	}
	if existing = manager.executors[datasource.ID]; !isNilExecutor(existing) {
		return existing, nil
	}

	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return nil, safeError("initialize datasource decryption", ErrDatasourceUnreachable, err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil {
		return nil, safeError("decrypt datasource credential", ErrDatasourceUnreachable, err)
	}
	executor, err := manager.opener(datasource, password, manager.readOnly)
	if err != nil {
		var databaseError *DBError
		if errors.As(err, &databaseError) {
			return nil, err
		}
		return nil, safeError("open datasource", ErrDatasourceUnreachable, err)
	}
	if isNilExecutor(executor) {
		return nil, safeError("open datasource", ErrDatasourceUnreachable, errors.New("opener returned nil executor"))
	}
	manager.executors[datasource.ID] = executor
	return executor, nil
}

// Close closes and removes one datasource pool.
func (manager *Manager) Close(id string) error {
	if manager == nil {
		return fmt.Errorf("close executor: manager is nil")
	}
	manager.mu.Lock()
	executor, exists := manager.executors[id]
	if exists {
		delete(manager.executors, id)
	}
	manager.mu.Unlock()
	if !exists || isNilExecutor(executor) {
		return nil
	}
	if err := executor.Close(); err != nil {
		return fmt.Errorf("close executor for datasource %q: %w", id, err)
	}
	return nil
}

// CloseAll closes every datasource pool and empties the manager.
func (manager *Manager) CloseAll() error {
	if manager == nil {
		return fmt.Errorf("close all executors: manager is nil")
	}
	manager.mu.Lock()
	executors := manager.executors
	manager.executors = make(map[string]Executor)
	manager.mu.Unlock()

	closeErrors := make([]error, 0)
	for id, executor := range executors {
		if isNilExecutor(executor) {
			continue
		}
		if err := executor.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close datasource %q: %w", id, err))
		}
	}
	if len(closeErrors) > 0 {
		return fmt.Errorf("close all executors: %w", errors.Join(closeErrors...))
	}
	return nil
}

func openExecutor(
	datasource model.Datasource,
	password string,
	readOnly bool,
) (Executor, error) {
	switch datasource.DBType {
	case "postgres":
		return NewPostgresExecutor(context.Background(), datasource, password, readOnly)
	case "mysql":
		return NewMySQLExecutor(context.Background(), datasource, password, readOnly)
	default:
		return nil, fmt.Errorf("unsupported datasource type %q", datasource.DBType)
	}
}

func normalizedConnectionLimit(value int) (int, error) {
	if value == 0 {
		return defaultConnectionLimit, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("connection limit cannot be negative")
	}
	return value, nil
}

func normalizedStatementTimeout(value int) (int, error) {
	if value == 0 {
		return defaultStatementTimeout, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("statement timeout cannot be negative")
	}
	return value, nil
}

func validateDatasource(datasource model.Datasource) error {
	if strings.TrimSpace(datasource.ID) == "" || strings.TrimSpace(datasource.Host) == "" ||
		strings.TrimSpace(datasource.Database) == "" || strings.TrimSpace(datasource.Username) == "" {
		return fmt.Errorf("datasource identity and connection fields are required")
	}
	if datasource.Port <= 0 || datasource.Port > 65_535 {
		return fmt.Errorf("datasource port is invalid")
	}
	return nil
}

func isNilExecutor(executor Executor) bool {
	return isNilValue(executor)
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func isNilFunction(function executorOpener) bool {
	return function == nil
}
