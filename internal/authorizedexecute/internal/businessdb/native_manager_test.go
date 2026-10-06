package businessdb

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

type fakeNativeExecutor struct{ closed atomic.Int32 }

func (*fakeNativeExecutor) Category() model.DatasourceCategory            { return model.CategoryKeyValue }
func (*fakeNativeExecutor) Dialect() model.DBDialect                      { return model.DialectRedis }
func (*fakeNativeExecutor) Ping(context.Context) error                    { return nil }
func (*fakeNativeExecutor) ServerVersion(context.Context) (string, error) { return "test", nil }
func (*fakeNativeExecutor) Discover(context.Context) (NativeCatalog, error) {
	return NativeCatalog{}, nil
}
func (*fakeNativeExecutor) NativeQuery(context.Context, NativeQueryRequest) (NativeQueryResult, error) {
	return NativeQueryResult{}, nil
}
func (executor *fakeNativeExecutor) Close() error { executor.closed.Add(1); return nil }

func TestNativeManagerConcurrentReuseAndClose(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}
	datasource := model.Datasource{ID: "native", DBType: "redis", PasswordEnc: encrypted}
	var opened atomic.Int32
	manager := newNativeManager(true, func(_ model.Datasource, password string, readOnly bool) (NativeExecutor, error) {
		if password != "secret" || !readOnly {
			t.Error("wrong opener inputs")
		}
		opened.Add(1)
		return &fakeNativeExecutor{}, nil
	})
	var wg sync.WaitGroup
	results := make(chan NativeExecutor, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := manager.GetOrOpen(context.Background(), datasource, secret)
			if err != nil {
				t.Error(err)
			}
			results <- value
		}()
	}
	wg.Wait()
	close(results)
	var first NativeExecutor
	for value := range results {
		if first == nil {
			first = value
		} else if first != value {
			t.Error("cache was not reused")
		}
	}
	if opened.Load() != 1 {
		t.Fatalf("opened %d times", opened.Load())
	}
	if err := manager.Close("native"); err != nil {
		t.Fatal(err)
	}
	if first.(*fakeNativeExecutor).closed.Load() != 1 {
		t.Fatal("not closed")
	}
	if _, err := manager.GetOrOpen(context.Background(), datasource, secret); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 {
		t.Fatal("not reopened")
	}
}

func TestNativeManagerAllowsEmptyPassword(t *testing.T) {
	manager := newNativeManager(false, func(_ model.Datasource, password string, _ bool) (NativeExecutor, error) {
		if password != "" {
			t.Errorf("password = %q", password)
		}
		return &fakeNativeExecutor{}, nil
	})
	_, err := manager.GetOrOpen(context.Background(), model.Datasource{ID: "empty", DBType: "redis"}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
}
