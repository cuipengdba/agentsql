package adminapi

import (
	"errors"
	"net/http"

	"github.com/cuipengdba/agentsql/internal/store"
)

// policiesBindPostgres accepts an enrollment SELECT, never executes it, and
// returns only the committed policy projection. It currently supports the
// PostgreSQL closed binder for one regular table.
func (handler *Handler) policiesBindPostgres(writer http.ResponseWriter, request *http.Request) {
	id, expected, status := parsePolicyIfMatch(request)
	if status != 0 {
		handler.fail(writer, status, http.StatusText(status))
		return
	}
	if id != request.PathValue("id") {
		handler.fail(writer, http.StatusBadRequest, "If-Match policy does not match request path")
		return
	}
	var input struct {
		SQL string `json:"sql"`
	}
	if err := decodeJSON(writer, request, &input); err != nil || input.SQL == "" {
		handler.fail(writer, http.StatusBadRequest, "invalid enrollment input")
		return
	}
	bound, err := handler.deps.Runtime.BindPostgresColumnPolicy(request.Context(), id, expected, input.SQL)
	if err != nil {
		if errors.Is(err, store.ErrRevisionMismatch) {
			handler.fail(writer, http.StatusPreconditionFailed, "policy revision changed or cannot be enrolled")
			return
		}
		handler.fail(writer, http.StatusConflict, "PostgreSQL column catalog or permission enrollment failed")
		return
	}
	writer.Header().Set("ETag", policyETag(bound.ID, bound.Revision))
	handler.ok(writer, policyToView(bound))
}
