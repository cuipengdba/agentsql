package adminapi

import (
	"errors"
	"net/http"

	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/store"
)

type mysqlColumnBindingInput struct {
	Schema string                       `json:"schema"`
	Table  string                       `json:"table"`
	Grants []bootstrap.MySQLColumnGrant `json:"grants"`
}

func (handler *Handler) policiesBindMySQL(writer http.ResponseWriter, request *http.Request) {
	id, expected, status := parsePolicyIfMatch(request)
	if status != 0 {
		handler.fail(writer, status, http.StatusText(status))
		return
	}
	if id != request.PathValue("id") {
		handler.fail(writer, http.StatusBadRequest, "If-Match policy does not match request path")
		return
	}
	var input mysqlColumnBindingInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	bound, err := handler.deps.Runtime.BindMySQLColumnPolicy(request.Context(), id, expected,
		input.Schema, input.Table, input.Grants)
	if err != nil {
		if errors.Is(err, store.ErrRevisionMismatch) {
			handler.fail(writer, http.StatusPreconditionFailed, "policy revision changed or cannot be enrolled")
			return
		}
		handler.fail(writer, http.StatusConflict, "MySQL column catalog or permission enrollment failed")
		return
	}
	writer.Header().Set("ETag", policyETag(bound.ID, bound.Revision))
	handler.ok(writer, policyToView(bound))
}
