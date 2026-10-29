package chatbi

import (
	"net/http"
)

// The unchanged basic assistant asks for its datasource list after receiving
// the scoped token. Metadata comes only from the current guest bindings and
// authorized gateway schema; browser-supplied IDs never expand this list.
func (h *Handler) publicAssistantDatasources(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	if p.EmbeddedApp == "" || p.EmbeddedType != 0 || !validVisitorScope(p.VisitorScope) {
		failure(w, ErrUnauthenticated)
		return
	}
	if err := h.service.live(r.Context(), p); err != nil {
		failure(w, err)
		return
	}
	var app AssistantApplication
	if _, err := h.service.Store.get(r.Context(), assistantKey(p.Workspace, p.EmbeddedApp), &app); err != nil ||
		app.Type != 0 || app.PublicGuestUser != p.User.ID || !app.Enabled || len(app.DatasourceIDs) == 0 {
		failure(w, ErrUnauthenticated)
		return
	}
	result := make([]map[string]any, 0, len(app.DatasourceIDs))
	for _, id := range app.DatasourceIDs {
		binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
		if err != nil {
			failure(w, err)
			return
		}
		columns, err := h.service.schema(r.Context(), p, binding)
		if err != nil {
			failure(w, err)
			return
		}
		if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
			failure(w, err)
			return
		}
		tables := map[string]bool{}
		for _, column := range columns {
			tables[column.Schema+"."+column.Table] = true
		}
		kind, typeName := binding.Dialect, ""
		if binding.Type == "excel" {
			kind, typeName = "excel", "Excel"
		} else if kind == "pg" {
			typeName = "PostgreSQL"
		} else if kind == "mysql" {
			typeName = "MySQL"
		}
		result = append(result, map[string]any{
			"id": binding.Datasource, "name": binding.Name,
			"description": binding.Description, "type": kind,
			"type_name": typeName, "num": len(tables),
		})
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	okay(w, result)
}
