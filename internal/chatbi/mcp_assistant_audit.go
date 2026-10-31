package chatbi

import (
	"context"
	"time"
)

// auditAssistantCatalogAccess records the identity and server-owned source
// before model or database work. The external URL and credential are omitted.
func (s *Store) auditAssistantCatalogAccess(ctx context.Context, p Principal, catalogID, datasource ID) error {
	if !p.User.ID.Valid() || !p.Workspace.Valid() || !catalogID.Valid() || !datasource.Valid() {
		return ErrUnavailable
	}
	id, err := newID()
	if err != nil {
		return ErrUnavailable
	}
	key := objectKey{"management-audit", "system", "system", string(id)}
	return s.put(ctx, key, map[string]any{"action": "assistant-catalog.access", "actor": p.User.ID,
		"workspace": p.Workspace, "catalog_id": catalogID, "datasource": datasource, "time": time.Now().UTC()}, 0)
}
