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

type fieldStatusCaptureModel struct{ columns []Column }

func (model *fieldStatusCaptureModel) Generate(_ context.Context, _ ModelConfig, _, _ string, columns []Column) (SQLGeneration, error) {
	model.columns = append([]Column(nil), columns...)
	return SQLGeneration{SQL: `SELECT region FROM public.sales`}, nil
}

func TestOriginalDatabaseFieldStatusControlsPreviewAndRollsBackWithAudit(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, principal.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	principal, err = service.Identity.Verify(ctx, token)
	require.NoError(t, err)
	gateway.columns = []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
	}
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	tableID := browserTableID("10", "public", "sales")
	fieldID := browserFieldID(tableID, "amount")
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	edit := func(checked bool, field string) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"id": fieldID, "ds_id": 10, "table_id": tableID,
			"field_name": field, "field_type": "numeric", "field_index": 0,
			"field_comment": nil, "custom_comment": "", "checked": checked,
		})
		require.NoError(t, err)
		return string(body)
	}
	listPath := "/api/v1/datasource/fieldList/10/" + strconv.FormatInt(tableID, 10)
	require.Equal(t, http.StatusOK, request("/api/v1/datasource/editField", edit(false, "amount")).Code)
	current, err := service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err := disabledFields(current)
	require.NoError(t, err)
	require.True(t, disabled[disabledFieldKey("public", "sales", "amount")])
	list := request(listPath, `{}`)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), `"checked":false`)
	semantic, err := service.semanticSchema(ctx, principal, current)
	require.NoError(t, err)
	require.Equal(t, []Column{{Schema: "public", Table: "sales", Name: "region", DataType: "text"}}, semantic)
	model := &fieldStatusCaptureModel{}
	service.Model = model
	chat, err := service.Start(ctx, principal, "10", "field status", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "show region", check, nil)
	require.NoError(t, err)
	require.Equal(t, semantic, model.columns)
	preview := request("/api/v1/datasource/previewData/10", `{"table":{"id":1,"table_name":"sales"}}`)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Equal(t, `SELECT "region" FROM "public"."sales" LIMIT 100`, gateway.lastQuery)
	require.NotEqual(t, http.StatusOK, request("/api/v1/datasource/editField", edit(true, "forged")).Code)
	_, err = service.Store.db.ExecContext(ctx, `CREATE TRIGGER reject_field_status_audit BEFORE INSERT ON chatbi_objects
		WHEN NEW.kind='management-audit' BEGIN SELECT RAISE(FAIL, 'audit offline'); END`)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, request("/api/v1/datasource/editField", edit(true, "amount")).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err = disabledFields(current)
	require.NoError(t, err)
	require.True(t, disabled[disabledFieldKey("public", "sales", "amount")])
	_, err = service.Store.db.ExecContext(ctx, `DROP TRIGGER reject_field_status_audit`)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, request("/api/v1/datasource/editField", edit(true, "amount")).Code)
	current, err = service.binding(ctx, principal, "10")
	require.NoError(t, err)
	disabled, err = disabledFields(current)
	require.NoError(t, err)
	require.Empty(t, disabled)
	list = request(listPath, `{}`)
	require.Equal(t, http.StatusOK, list.Code)
	require.NotContains(t, list.Body.String(), `"checked":false`)
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	require.NoError(t, err)
	states := map[bool]bool{}
	for _, raw := range audits {
		var event struct {
			Action  string `json:"action"`
			Checked *bool  `json:"checked"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		if event.Action == "database.field-status" {
			require.NotNil(t, event.Checked)
			states[*event.Checked] = true
		}
	}
	require.True(t, states[false])
	require.True(t, states[true])
}

func TestDisabledFieldMetadataRejectsMalformedStoredState(t *testing.T) {
	for _, encoded := range []string{`{"public\u0000sales\u0000amount":false}`, `{"bad":true}`, `[]`, `null`} {
		_, err := disabledFields(Binding{DisabledFields: encoded})
		require.Error(t, err)
	}
}
