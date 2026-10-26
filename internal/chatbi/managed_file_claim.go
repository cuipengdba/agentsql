package chatbi

import (
	"context"
	"strings"
	"time"
)

// claimManagedFileReceipt couples SQLBot's datasource ID, the child connector,
// the one-use import receipt and its management audit in one local transaction.
// The gateway has already published only to this child binding; a failure
// here cannot expose the imported tables through another datasource.
func (s *Store) claimManagedFileReceipt(ctx context.Context, p Principal, filePath string,
	revision int64, receipt managedFileReceipt, binding Binding,
) error {
	if s == nil || ctx == nil || revision < 1 || receipt.ClaimedDatasource != "" ||
		receipt.Filename != filePath || receipt.OperationID != binding.ManagedFileID ||
		receipt.BindingID != binding.BindingID || receipt.DatasourceName != binding.ResolveName ||
		receipt.ConnectorToken != binding.ConnectorToken || binding.Type != "excel" ||
		binding.Workspace != p.Workspace || binding.User != p.User.ID || !binding.Datasource.Valid() {
		return ErrUnavailable
	}
	receipt.ClaimedDatasource = binding.Datasource
	receiptKey := managedFileReceiptKey(p, filePath)
	bindingKey := bindingKey(p, binding.Datasource)
	nameKey := objectKey{"datasource-name", string(p.Workspace), string(p.User.ID), strings.ToLower(binding.Name)}
	receiptBody, err := s.seal(receiptKey, receipt)
	if err != nil {
		return err
	}
	bindingBody, err := s.seal(bindingKey, binding)
	if err != nil {
		return err
	}
	nameBody, err := s.seal(nameKey, binding.Datasource)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	auditBody, err := s.seal(auditKey, map[string]any{"action": "file-import.claim", "workspace": p.Workspace,
		"owner": p.User.ID, "datasource": binding.Datasource, "operation_id": receipt.OperationID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		receiptBody, receiptKey.Kind, receiptKey.Scope, receiptKey.Owner, receiptKey.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		nameKey.Kind, nameKey.Scope, nameKey.Owner, nameKey.ID, nameBody)
	if err != nil {
		return ErrUnavailable
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?) ON CONFLICT DO NOTHING`,
		bindingKey.Kind, bindingKey.Scope, bindingKey.Owner, bindingKey.ID, bindingBody)
	if err != nil {
		return ErrUnavailable
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, auditBody); err != nil {
		return ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

// retireManagedFileDatasource removes the SQLBot-facing alias and its unique
// name only after the gateway has durably deleted the matching operation.
func (s *Store) retireManagedFileDatasource(ctx context.Context, p Principal, binding Binding) error {
	if s == nil || ctx == nil || binding.Type != "excel" || binding.ManagedFileID == "" ||
		binding.User != p.User.ID || binding.Workspace != p.Workspace {
		return ErrUnavailable
	}
	bindingKey := bindingKey(p, binding.Datasource)
	nameKey := objectKey{"datasource-name", string(p.Workspace), string(p.User.ID), strings.ToLower(binding.Name)}
	var current Binding
	bindingRevision, err := s.get(ctx, bindingKey, &current)
	if err != nil || current != binding {
		return ErrConflict
	}
	var mapped ID
	nameRevision, err := s.get(ctx, nameKey, &mapped)
	if err != nil || mapped != binding.Datasource {
		return ErrConflict
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	auditBody, err := s.seal(auditKey, map[string]any{"action": "file-import.datasource-delete", "workspace": p.Workspace,
		"owner": p.User.ID, "datasource": binding.Datasource, "operation_id": binding.ManagedFileID, "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, item := range []struct {
		key      objectKey
		revision int64
	}{{bindingKey, bindingRevision}, {nameKey, nameRevision}} {
		result, err := tx.ExecContext(ctx, `DELETE FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
			item.key.Kind, item.key.Scope, item.key.Owner, item.key.ID, item.revision)
		if err != nil {
			return ErrUnavailable
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, auditBody); err != nil {
		return ErrUnavailable
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}
