package chatbi

import (
	"context"
	"errors"
	"sort"
	"time"
)

// A published import can outlive an abandoned SQLBot datasource wizard.
// Its encrypted receipt is the only safe source for the child connector and
// operation ID; the browser cannot authorize cleanup by naming a table.
const abandonedFileImportAge = time.Hour

func (s *Store) abandonedManagedFileReceipts(ctx context.Context, now time.Time, limit int) ([]objectKey, error) {
	if s == nil || ctx == nil || limit < 1 || limit > 32 {
		return nil, ErrUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `SELECT scope,owner,id,body FROM chatbi_objects
		WHERE kind='managed-file-receipt' ORDER BY scope,owner,id`)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	type pending struct {
		key         objectKey
		lastAttempt time.Time
	}
	candidates := make([]pending, 0)
	for rows.Next() {
		var key objectKey
		var body []byte
		key.Kind = "managed-file-receipt"
		if err := rows.Scan(&key.Scope, &key.Owner, &key.ID, &body); err != nil || !key.valid() || len(body) > 1<<20 {
			return nil, ErrUnavailable
		}
		var receipt managedFileReceipt
		if err := s.decode(key, body, &receipt); err != nil {
			return nil, err
		}
		if receipt.ClaimedDatasource != "" || receipt.Created.IsZero() ||
			(receipt.CleanupStarted.IsZero() && receipt.Created.Add(abandonedFileImportAge).After(now)) {
			continue
		}
		candidates = append(candidates, pending{key: key, lastAttempt: receipt.CleanupStarted})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	// Never let a repeatedly failing operation starve later abandoned imports.
	// A zero attempt time goes first; retries rotate behind newly expired work.
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].lastAttempt.Equal(candidates[j].lastAttempt) {
			return candidates[i].lastAttempt.Before(candidates[j].lastAttempt)
		}
		a, b := candidates[i].key, candidates[j].key
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.ID < b.ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([]objectKey, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.key)
	}
	return result, nil
}

// ReapAbandonedManagedFiles closes at most 32 old, unclaimed imports per call.
// The audited local cleanup marker fences a concurrent datasource/add; after a
// crash it stays retryable. Gateway deletion is idempotent and independently
// checks the child binding, owner, policy and physical PostgreSQL table.
func (service *Service) ReapAbandonedManagedFiles(ctx context.Context, now time.Time) (int, error) {
	if service == nil || service.Store == nil || service.ManagedFileDatasource == "" || now.IsZero() {
		return 0, nil
	}
	deleter, ok := service.Gateway.(managedFileDeleter)
	if !ok {
		return 0, ErrGateway
	}
	keys, err := service.Store.abandonedManagedFileReceipts(ctx, now, 32)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	var failures error
	for _, key := range keys {
		if err := service.reapOneAbandonedManagedFile(ctx, now, key, deleter); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				continue // A concurrent claim or cleanup won the local revision.
			}
			failures = errors.Join(failures, err)
			continue
		}
		cleaned++
	}
	return cleaned, failures
}

func (service *Service) reapOneAbandonedManagedFile(ctx context.Context, now time.Time, key objectKey, deleter managedFileDeleter) error {
	store := service.Store
	var receipt managedFileReceipt
	revision, err := store.get(ctx, key, &receipt)
	if err != nil {
		return err
	}
	if receipt.ClaimedDatasource != "" ||
		(receipt.CleanupStarted.IsZero() && receipt.Created.Add(abandonedFileImportAge).After(now)) {
		return ErrConflict
	}
	if receipt.Filename != key.ID || receipt.Created.IsZero() ||
		!managedFileOperationPattern.MatchString(receipt.OperationID) ||
		!managedFileChildNamePattern.MatchString(receipt.DatasourceName) || receipt.BindingID == "" || receipt.ConnectorToken == "" {
		return ErrUnavailable
	}
	receipt.CleanupStarted = now.UTC()
	if err := store.writeSemanticWithDetails(ctx, key, receipt, revision, "file-import.abandoned-cleanup-start",
		map[string]any{"operation_id": receipt.OperationID}); err != nil {
		return err
	}
	revision++
	p := Principal{User: User{ID: ID(key.Owner)}, Workspace: ID(key.Scope)}
	var base Binding
	if _, err := store.get(ctx, bindingKey(p, service.ManagedFileDatasource), &base); err != nil ||
		base.User != p.User.ID || base.Workspace != p.Workspace || base.Datasource != service.ManagedFileDatasource ||
		base.Dialect != "pg" || base.BindingID == receipt.BindingID {
		return ErrGateway
	}
	child := base
	child.BindingID, child.ConnectorToken, child.Name = receipt.BindingID, receipt.ConnectorToken, receipt.DatasourceName
	child.ResolveName = receipt.DatasourceName
	if err := deleter.DeleteFile(ctx, p, child, receipt.OperationID); err != nil {
		return err
	}
	var stage fileStage
	if stageRevision, err := store.get(ctx, fileStageKey(p), &stage); err == nil && stage.FilePath == receipt.Filename {
		if err := store.deleteSemantic(ctx, fileStageKey(p), stageRevision, "file-stage.abandoned-cleaned"); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return store.deleteSemantic(ctx, key, revision, "file-import.abandoned-cleaned")
}
