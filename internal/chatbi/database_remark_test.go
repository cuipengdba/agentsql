package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseRemarksUseOriginalEditorAndCloseOnRevocation(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "regions", Name: "label", DataType: "text"},
	}}
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	request := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if bearer != "" {
			r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	salesID := strconv.FormatInt(browserTableID("10", "public", "sales"), 10)
	regionsID := strconv.FormatInt(browserTableID("10", "public", "regions"), 10)
	fieldID := strconv.FormatInt(browserFieldID(browserTableID("10", "public", "regions"), "label"), 10)
	encode := func(value any) string {
		t.Helper()
		body, err := json.Marshal(value)
		require.NoError(t, err)
		return string(body)
	}
	tableBody := func(id, name, comment string) string {
		return encode(map[string]any{"id": id, "ds_id": "10", "table_name": name,
			"table_comment": "", "custom_comment": comment, "checked": true, "embedding": nil})
	}
	fieldBody := func(id, field, comment string) string {
		return encode(map[string]any{"id": id, "ds_id": "10", "table_id": regionsID,
			"field_name": field, "field_type": "text", "field_index": 0,
			"field_comment": nil, "custom_comment": comment, "checked": true})
	}
	list := request(http.MethodPost, "/api/v1/datasource/tableList/10", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), "region meaning")
	require.Equal(t, http.StatusUnauthorized, request(http.MethodPost, "/api/v1/datasource/editTable", tableBody(regionsID, "regions", "region meaning"), "").Code)
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editTable", tableBody(salesID, "regions", "forged"), token).Code)
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editField", fieldBody(fieldID, "other", "forged"), token).Code)
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editTable", tableBody(regionsID, "regions", "region meaning"), token).Code)
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editField", fieldBody(fieldID, "label", "English region label"), token).Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tableComments, err := databaseRemarks(current, false)
	require.NoError(t, err)
	fieldComments, err := databaseRemarks(current, true)
	require.NoError(t, err)
	require.Equal(t, "region meaning", tableComments[databaseTableRemarkKey("public", "regions")])
	require.Equal(t, "English region label", fieldComments[databaseFieldRemarkKey("public", "regions", "label")])
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	seenTable, seenField := false, false
	for _, raw := range audits {
		var event struct {
			Action string `json:"action"`
			Schema string `json:"schema"`
			Table  string `json:"table"`
			Field  string `json:"field"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "database.table-comment" || event.Action == "database.field-comment" {
			require.Equal(t, "public", event.Schema)
			require.Equal(t, "regions", event.Table)
			require.NotContains(t, string(raw), "region meaning")
			require.NotContains(t, string(raw), "English region label")
			seenTable = seenTable || event.Action == "database.table-comment"
			seenField = seenField || event.Action == "database.field-comment" && event.Field == "label"
		}
	}
	require.True(t, seenTable && seenField)
	list = request(http.MethodPost, "/api/v1/datasource/tableList/10", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), "region meaning")
	fields := request(http.MethodPost, "/api/v1/datasource/fieldList/10/"+regionsID, `{}`, token)
	require.Equal(t, http.StatusOK, fields.Code)
	require.Contains(t, fields.Body.String(), "English region label")
	hints, err := datasourceRemarkHints(current, service.Gateway.(*selectionTestGateway).columns)
	require.NoError(t, err)
	require.Equal(t, []RemarkHint{{Schema: "public", Table: "regions", Remark: "region meaning"},
		{Schema: "public", Table: "regions", Column: "label", Remark: "English region label"}}, hints)

	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_database_remark_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editTable", tableBody(regionsID, "regions", "uncommitted"), token).Code)
	require.NoError(t, func() error {
		_, e := service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_database_remark_audit`)
		return e
	}())
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	tableComments, err = databaseRemarks(current, false)
	require.NoError(t, err)
	require.Equal(t, "region meaning", tableComments[databaseTableRemarkKey("public", "regions")])

	choose := `[{"table_name":"sales","table_comment":""}]`
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/chooseTables/10", choose, token).Code)
	list = request(http.MethodPost, "/api/v1/datasource/tableList/10", "", token)
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), "region meaning")
	require.NotEqual(t, http.StatusOK, request(http.MethodPost, "/api/v1/datasource/editTable", tableBody(regionsID, "regions", "hidden edit"), token).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	visible, err := service.schema(ctx, principal, current)
	require.NoError(t, err)
	hints, err = datasourceRemarkHints(current, visible)
	require.NoError(t, err)
	require.Empty(t, hints)
}
