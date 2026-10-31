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

type fieldStatusCaptureModel struct {
	columns []Column
	sql     string
}

func (model *fieldStatusCaptureModel) Generate(_ context.Context, _ ModelConfig, _, _ string, columns []Column) (SQLGeneration, error) {
	model.columns = append([]Column(nil), columns...)
	if model.sql != "" {
		return SQLGeneration{SQL: model.sql}, nil
	}
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
	before := gateway.queries
	model.sql = `SELECT amount FROM public.sales`
	_, err = service.Ask(ctx, principal, chat.ID, "show amount", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, before, gateway.queries)
	for _, sql := range []string{
		`SELECT amount FROM public.sales`,
		`SELECT region FROM public.sales WHERE amount > 1`,
		`SELECT * FROM public.sales`,
	} {
		_, err := service.query(ctx, principal, current, sql)
		require.ErrorIs(t, err, ErrGateway, sql)
		require.Equal(t, before, gateway.queries)
	}
	require.NoError(t, authorizeEnabledFieldsSQL(current, `SELECT region FROM public.sales`))
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

func TestDisabledFieldSQLRejectsMySQLColumnUses(t *testing.T) {
	binding := Binding{Dialect: "mysql"}
	require.NoError(t, setFieldStatus(&binding, "agentsql_demo", "orders", "amount", false))
	for _, sql := range []string{
		"SELECT amount FROM agentsql_demo.orders",
		"SELECT AMOUNT FROM AGENTSQL_DEMO.ORDERS",
		"SELECT region FROM agentsql_demo.orders WHERE amount > 1",
		"SELECT * FROM agentsql_demo.orders",
	} {
		require.ErrorIs(t, authorizeEnabledFieldsSQL(binding, sql), ErrGateway, sql)
	}
	require.NoError(t, authorizeEnabledFieldsSQL(binding, "SELECT region FROM agentsql_demo.orders"))
	require.NoError(t, authorizeEnabledFieldsSQL(binding, "SELECT amount FROM other_schema.orders"))
	require.NoError(t, setFieldStatus(&binding, "agentsql_demo", "orders", "amount", true))
	require.NoError(t, authorizeEnabledFieldsSQL(binding, "SELECT amount FROM agentsql_demo.orders"))
}

func TestDisabledFieldSQLRejectsPostgresJoinAndSubqueryUses(t *testing.T) {
	binding := Binding{Dialect: "pg"}
	require.NoError(t, setFieldStatus(&binding, "public", "sales", "amount", false))
	require.NoError(t, setFieldStatus(&binding, "public", "secret", "amount", false))
	for _, sql := range []string{
		`SELECT s.region FROM public.sales AS s JOIN public.teams AS t ON s.amount = t.id`,
		`SELECT region FROM public.sales WHERE EXISTS (SELECT id FROM public.sales AS s WHERE s.amount > 1)`,
		`SELECT s.region FROM public.sales AS s JOIN public.teams AS t ON EXISTS (SELECT id FROM public.secret AS p WHERE p.amount > 1)`,
	} {
		require.ErrorIs(t, authorizeEnabledFieldsSQL(binding, sql), ErrGateway, sql)
	}
	require.NoError(t, authorizeEnabledFieldsSQL(binding, `SELECT s.region FROM public.sales AS s JOIN public.teams AS t ON s.team_id = t.id`))
	require.NoError(t, authorizeEnabledFieldsSQL(binding, `SELECT amount FROM public.other`))
}
