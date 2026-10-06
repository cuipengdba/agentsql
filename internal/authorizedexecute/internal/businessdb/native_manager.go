package businessdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

type nativeExecutorOpener func(model.Datasource, string, bool) (NativeExecutor, error)

type NativeManager struct {
	mu        sync.RWMutex
	executors map[string]NativeExecutor
	readOnly  bool
	opener    nativeExecutorOpener
}

func NewNativeManager(readOnly bool) *NativeManager {
	return newNativeManager(readOnly, openNativeExecutor)
}
func newNativeManager(readOnly bool, opener nativeExecutorOpener) *NativeManager {
	return &NativeManager{executors: make(map[string]NativeExecutor), readOnly: readOnly, opener: opener}
}

func (manager *NativeManager) GetOrOpen(ctx context.Context, datasource model.Datasource, secret []byte) (NativeExecutor, error) {
	if ctx == nil {
		return nil, fmt.Errorf("native context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if manager == nil || manager.opener == nil {
		return nil, fmt.Errorf("native manager is not initialized")
	}
	if strings.TrimSpace(datasource.ID) == "" {
		return nil, fmt.Errorf("native datasource ID is required")
	}
	manager.mu.RLock()
	existing := manager.executors[datasource.ID]
	manager.mu.RUnlock()
	if !isNilValue(existing) {
		return existing, nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.executors == nil {
		manager.executors = make(map[string]NativeExecutor)
	}
	if existing = manager.executors[datasource.ID]; !isNilValue(existing) {
		return existing, nil
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return nil, safeError("initialize native datasource decryption", ErrDatasourceUnreachable, err)
	}
	password := ""
	if datasource.PasswordEnc != "" {
		password, err = cipher.Decrypt(datasource.PasswordEnc)
		if err != nil {
			return nil, safeError("decrypt native datasource credential", ErrDatasourceUnreachable, err)
		}
	}
	executor, err := manager.opener(datasource, password, manager.readOnly)
	if err != nil {
		return nil, err
	}
	if isNilValue(executor) {
		return nil, fmt.Errorf("native opener returned nil executor")
	}
	manager.executors[datasource.ID] = executor
	return executor, nil
}

func (manager *NativeManager) Close(id string) error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	executor := manager.executors[id]
	delete(manager.executors, id)
	manager.mu.Unlock()
	if isNilValue(executor) {
		return nil
	}
	return executor.Close()
}
func (manager *NativeManager) CloseAll() error {
	if manager == nil {
		return nil
	}
	manager.mu.Lock()
	executors := manager.executors
	manager.executors = make(map[string]NativeExecutor)
	manager.mu.Unlock()
	var errs []error
	for id, executor := range executors {
		if !isNilValue(executor) {
			if err := executor.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close native datasource %q: %w", id, err))
			}
		}
	}
	return errors.Join(errs...)
}
