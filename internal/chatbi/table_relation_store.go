package chatbi

import (
	"context"
	"time"
)

func (s *Store) updateTableRelations(ctx context.Context, p Principal, prior Binding, graph string) error {
	if s == nil || ctx == nil || prior.Workspace != p.Workspace || prior.User != p.User.ID ||
		!prior.Datasource.Valid() || len(graph) > 1<<20 {
		return ErrNotFound
	}
	if prior.TableRelations == graph {
		return nil
	}
	key := bindingKey(p, prior.Datasource)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := s.verifyManagedWorkspaceActor(ctx, tx, p); err != nil {
		return err
	}
	var body []byte
	var revision int64
	if err := tx.QueryRowContext(ctx,
		`SELECT revision,body FROM chatbi_objects WHERE kind=? AND scope=? AND owner=? AND id=?`,
		key.Kind, key.Scope, key.Owner, key.ID).Scan(&revision, &body); controlNoRows(err) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	var current Binding
	if s.decode(key, body, &current) != nil || current != prior {
		return ErrConflict
	}
	current.TableRelations = graph
	sealed, err := s.seal(key, current)
	if err != nil {
		return err
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	auditKey := objectKey{"management-audit", "system", "system", string(auditID)}
	audit, err := s.seal(auditKey, map[string]any{"action": "datasource.table-relations",
		"actor": p.User.ID, "workspace": p.Workspace, "datasource": prior.Datasource,
		"graph_bytes": len(graph), "time": time.Now().UTC()})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE chatbi_objects SET revision=revision+1,body=? WHERE kind=? AND scope=? AND owner=? AND id=? AND revision=?`,
		sealed, key.Kind, key.Scope, key.Owner, key.ID, revision)
	if err != nil {
		return ErrUnavailable
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chatbi_objects(kind,scope,owner,id,revision,body) VALUES(?,?,?,?,1,?)`,
		auditKey.Kind, auditKey.Scope, auditKey.Owner, auditKey.ID, audit); err != nil {
		return ErrUnavailable
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
