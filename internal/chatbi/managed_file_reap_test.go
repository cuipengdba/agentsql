package chatbi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAbandonedManagedFileCleanupFencesClaimAndRetries(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, p.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	p, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	service.ManagedFileDatasource = "10"
	managed := &fakeManagedFileGateway{fakeGateway: gateway, deleteErr: ErrGateway}
	service.Gateway = managed
	filePath := "agentsql-file-abandoned.csv"
	stage := fileStage{FilePath: filePath, Original: "old.csv", Data: []byte("x\n1\n"),
		Expires: time.Now().UTC().Add(-time.Minute)}
	require.NoError(t, service.Store.put(ctx, fileStageKey(p), stage, 0))
	created := time.Now().UTC().Add(-2 * time.Hour)
	receipt := managedFileReceipt{OperationID: "22222222222222222222222222222222",
		BindingID:      "22222222-2222-4222-8222-222222222222",
		DatasourceName: "agentsql-import-33333333333333333333333333333333",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Filename:       filePath, Created: created,
		Sheets: []importedFileSheet{{SheetName: "Sheet1", TableName: "agentsql_file_11111111111111111111111111111111", Rows: 1}}}
	key := managedFileReceiptKey(p, filePath)
	require.NoError(t, service.Store.put(ctx, key, receipt, 0))

	count, err := service.ReapAbandonedManagedFiles(ctx, created.Add(30*time.Minute))
	require.NoError(t, err)
	require.Zero(t, count, "an active wizard must retain its receipt")
	require.Zero(t, managed.deletes)

	count, err = service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, count)
	_, err = service.Store.get(ctx, key, &receipt)
	require.NoError(t, err)
	require.False(t, receipt.CleanupStarted.IsZero(), "failed physical cleanup must remain fenced and retryable")
	require.Zero(t, managed.deletes)

	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agentsql/datasource/claim-import",
		bytes.NewBufferString(`{"filePath":"agentsql-file-abandoned.csv","name":"Abandoned"}`))
	request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.NotEqual(t, http.StatusOK, response.Code, "a fenced receipt cannot be claimed after deletion starts")

	managed.deleteErr = nil
	count, err = service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 1, managed.deletes)
	_, err = service.Store.get(ctx, key, &receipt)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.Store.get(ctx, fileStageKey(p), &stage)
	require.ErrorIs(t, err, ErrNotFound)
	count, err = service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Zero(t, count, "successful cleanup is not repeated")
}

func TestAbandonedManagedFileCleanupLeavesClaimedSource(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, _, _ := serviceFixture(t)
	service.ManagedFileDatasource = "10"
	managed := &fakeManagedFileGateway{fakeGateway: gateway}
	service.Gateway = managed
	receipt := managedFileReceipt{OperationID: "22222222222222222222222222222222",
		BindingID:      "22222222-2222-4222-8222-222222222222",
		DatasourceName: "agentsql-import-33333333333333333333333333333333",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Filename:       "agentsql-file-claimed.csv", Created: time.Now().UTC().Add(-24 * time.Hour),
		ClaimedDatasource: "123"}
	key := managedFileReceiptKey(p, receipt.Filename)
	require.NoError(t, service.Store.put(ctx, key, receipt, 0))
	count, err := service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Zero(t, count)
	require.Zero(t, managed.deletes)
	_, err = service.Store.get(ctx, key, &receipt)
	require.NoError(t, err)
}

func TestAbandonedManagedFileCleanupResumesAfterStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	secret := []byte(strings.Repeat("s", 32))
	store, err := OpenStore(ctx, path, secret)
	require.NoError(t, err)
	p := Principal{User: User{ID: "1"}, Workspace: "1"}
	base := Binding{Datasource: "10", Workspace: p.Workspace, User: p.User.ID,
		Dialect: "pg", BindingID: "base-binding", ConnectorToken: "base-connector"}
	require.NoError(t, store.put(ctx, bindingKey(p, "10"), base, 0))
	created := time.Now().UTC().Add(-2 * time.Hour)
	receipt := managedFileReceipt{OperationID: "22222222222222222222222222222222",
		BindingID: "22222222-2222-4222-8222-222222222222",
		DatasourceName: "agentsql-import-33333333333333333333333333333333",
		ConnectorToken: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Filename: "agentsql-file-restart.csv", Created: created}
	key := managedFileReceiptKey(p, receipt.Filename)
	require.NoError(t, store.put(ctx, key, receipt, 0))
	stage := fileStage{FilePath: receipt.Filename, Original: "restart.csv",
		Data: []byte("x\n1\n"), Expires: created.Add(time.Hour)}
	require.NoError(t, store.put(ctx, fileStageKey(p), stage, 0))
	managed := &fakeManagedFileGateway{fakeGateway: &fakeGateway{}, deleteErr: ErrGateway}
	service := &Service{Store: store, Gateway: managed, ManagedFileDatasource: "10"}
	count, err := service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.ErrorIs(t, err, ErrGateway)
	require.Zero(t, count)
	require.NoError(t, store.Close())

	store, err = OpenStore(ctx, path, secret)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	service.Store = store
	_, err = store.get(ctx, key, &receipt)
	require.NoError(t, err)
	require.False(t, receipt.CleanupStarted.IsZero(), "the persisted cleanup fence survives a restart")
	managed.deleteErr = nil
	count, err = service.ReapAbandonedManagedFiles(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 1, managed.deletes)
	_, err = store.get(ctx, key, &receipt)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.get(ctx, fileStageKey(p), &stage)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAbandonedManagedFileScanRotatesFailedAttempts(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, _ := serviceFixture(t)
	now := time.Now().UTC()
	for _, filename := range []string{"agentsql-file-a.csv", "agentsql-file-b.csv"} {
		receipt := managedFileReceipt{Filename: filename, Created: now.Add(-2 * time.Hour)}
		if filename == "agentsql-file-a.csv" {
			receipt.CleanupStarted = now.Add(-time.Minute)
		}
		require.NoError(t, service.Store.put(ctx, managedFileReceiptKey(p, filename), receipt, 0))
	}
	keys, err := service.Store.abandonedManagedFileReceipts(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, "agentsql-file-b.csv", keys[0].ID, "never-attempted cleanup should precede a prior failure")
	var receipt managedFileReceipt
	revision, err := service.Store.get(ctx, keys[0], &receipt)
	require.NoError(t, err)
	receipt.CleanupStarted = now
	require.NoError(t, service.Store.put(ctx, keys[0], receipt, revision))
	keys, err = service.Store.abandonedManagedFileReceipts(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, "agentsql-file-a.csv", keys[0].ID, "oldest failed attempt should rotate to the front")
}
