package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func embeddedSelectionFixture(t *testing.T) (*Service, Principal, Principal, AssistantApplication) {
	t.Helper()
	ctx := context.Background()
	service, admin, _, _, _ := serviceFixture(t)
	user, revision, err := service.Store.User(ctx, admin.User.ID)
	require.NoError(t, err)
	user.Admin = true
	require.NoError(t, service.Store.put(ctx, userKey(user.ID), user, revision))
	admin.User = user
	guest := testUser(t, "2", "embedded-select")
	require.NoError(t, service.Store.CreateUser(ctx, guest))
	binding, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	binding.User = guest.ID
	binding.BindingID = "embedded-select-binding"
	binding.ConnectorToken = "embedded-select-connector"
	require.NoError(t, service.Store.ConfigureConnections(ctx, ConnectionConfig{Bindings: []Binding{binding}}))
	app, err := service.SaveAssistant(ctx, admin, AssistantApplication{
		Name: "Embedded selection", Type: 4, Domain: "https://app.example",
		DatasourceIDs: []ID{"10"}, Enabled: true,
	})
	require.NoError(t, err)
	p := Principal{User: guest, Workspace: admin.Workspace, EmbeddedApp: app.ID,
		EmbeddedRevision: app.Revision, EmbeddedType: 4}
	return service, admin, p, app
}

func TestEmbeddedSourceSelectionTextExcludesInvisibleRemarksAndColumns(t *testing.T) {
	binding := Binding{Type: "pg", Dialect: "pg", Name: "opaque", Description: "retail",
		TableComments: `{"public\u0000sales":"Visible revenue","public\u0000secret":"Hidden project"}`,
		FieldComments: `{"public\u0000sales\u0000amount":"Visible amount","public\u0000sales\u0000password":"Hidden credential"}`}
	text, complete, err := embeddedSourceSelectionText(binding, []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "integer"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
	})
	require.NoError(t, err)
	require.True(t, complete)
	require.Contains(t, text, "Visible revenue")
	require.Contains(t, text, "Visible amount")
	require.Contains(t, text, "sales (amount:integer)")
	require.NotContains(t, text, "Hidden project")
	require.NotContains(t, text, "Hidden credential")
	require.NotContains(t, text, "password")
}

func TestEmbeddedUnboundHTTPQuestionSelectsCurrentSource(t *testing.T) {
	service, _, embedded, app := embeddedSelectionFixture(t)
	handler, err := NewHandler(service, "test")
	require.NoError(t, err)
	token := signedEmbeddedFixture(t, app.AppSecret, map[string]any{
		"account": embedded.User.Account, "embeddedId": app.ID,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-SQLBOT-ASSISTANT-TOKEN", "Embedded "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	started := post("/api/v1/chat/assistant/start", `{"question":"show sales","origin":2}`)
	require.Equal(t, http.StatusOK, started.Code, started.Body.String())
	var response struct {
		Data struct {
			ID         ID                `json:"id"`
			Datasource ID                `json:"datasource"`
			Records    []json.RawMessage `json:"records"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(started.Body.Bytes(), &response))
	require.True(t, response.Data.ID.Valid())
	require.Empty(t, response.Data.Datasource)
	require.Empty(t, response.Data.Records)
	invalid := post("/api/v1/chat/question", `{"chat_id":"`+string(response.Data.ID)+`","question":" "}`)
	require.Equal(t, http.StatusUnprocessableEntity, invalid.Code)
	before, err := service.ReadChat(context.Background(), embedded, response.Data.ID, false)
	require.NoError(t, err)
	require.Empty(t, before.Datasource)
	answered := post("/api/v1/chat/question", `{"chat_id":"`+string(response.Data.ID)+`","question":"show sales"}`)
	require.Equal(t, http.StatusOK, answered.Code, answered.Body.String())
	require.Contains(t, answered.Body.String(), `"type":"datasource"`)
	require.Contains(t, answered.Body.String(), `"id":"10"`)
	require.Contains(t, answered.Body.String(), `"type":"finish"`)
}

func TestEmbeddedUnboundChatBindsOnlyCurrentAllowedSource(t *testing.T) {
	ctx := context.Background()
	service, _, embedded, _ := embeddedSelectionFixture(t)
	chat, err := service.StartEmbeddedUnbound(ctx, embedded, "show sales")
	require.NoError(t, err)
	require.Empty(t, chat.Datasource)
	require.Equal(t, 2, chat.Origin)
	read, err := service.ReadChat(ctx, embedded, chat.ID, false)
	require.NoError(t, err)
	require.Empty(t, read.Datasource)
	listed, err := service.Chats(ctx, embedded)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	chosen, model, _, err := service.selectEmbeddedSource(ctx, embedded, "show sales", nil)
	require.NoError(t, err)
	require.Equal(t, ID("10"), chosen)
	require.Empty(t, model.ID)
	require.NoError(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, chosen))
	read, err = service.ReadChat(ctx, embedded, chat.ID, false)
	require.NoError(t, err)
	require.Equal(t, chosen, read.Datasource)
	require.ErrorIs(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, chosen), ErrConflict)
}

func TestEmbeddedUnboundSelectionClosesOnSourceOrAssistantRevocation(t *testing.T) {
	ctx := context.Background()
	service, admin, embedded, app := embeddedSelectionFixture(t)
	chat, err := service.StartEmbeddedUnbound(ctx, embedded, "show sales")
	require.NoError(t, err)
	bindingRevision, err := service.Store.get(ctx, bindingKey(embedded, "10"), new(Binding))
	require.NoError(t, err)
	require.NoError(t, service.Store.delete(ctx, bindingKey(embedded, "10"), bindingRevision))
	_, _, _, err = service.selectEmbeddedSource(ctx, embedded, "show sales", nil)
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, "10"), ErrNotFound)
	app.Enabled = false
	app, err = service.SaveAssistant(ctx, admin, app)
	require.NoError(t, err)
	require.ErrorIs(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, "10"), ErrUnauthenticated)
	embedded.EmbeddedRevision = app.Revision
	require.ErrorIs(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, "10"), ErrUnauthenticated)
}

func TestEmbeddedUnboundModelChoiceCannotEscapeOrOutliveCandidates(t *testing.T) {
	ctx := context.Background()
	service, admin, embedded, app := embeddedSelectionFixture(t)
	adminSource, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	adminSource.Datasource, adminSource.Name, adminSource.BindingID = "11", "inventory", "inventory-admin-binding"
	require.NoError(t, service.Store.put(ctx, bindingKey(admin, "11"), adminSource, 0))
	guestSource, err := service.binding(ctx, embedded, "10")
	require.NoError(t, err)
	guestSource.Datasource, guestSource.Name, guestSource.BindingID = "11", "inventory", "inventory-guest-binding"
	require.NoError(t, service.Store.put(ctx, bindingKey(embedded, "11"), guestSource, 0))
	app.DatasourceIDs = []ID{"10", "11"}
	app, err = service.SaveAssistant(ctx, admin, app)
	require.NoError(t, err)
	embedded.EmbeddedRevision = app.Revision
	chat, err := service.StartEmbeddedUnbound(ctx, embedded, "show inventory")
	require.NoError(t, err)
	selector := &fixtureCatalogSelector{fakeModel: &fakeModel{}, choice: "99"}
	service.Model = selector
	_, _, _, err = service.selectEmbeddedSource(ctx, embedded, "show inventory", nil)
	require.ErrorIs(t, err, ErrModel)
	require.Equal(t, 1, selector.selectCalls)
	selector.choice = "11"
	selected, _, _, err := service.selectEmbeddedSource(ctx, embedded, "show inventory", nil)
	require.NoError(t, err)
	require.Equal(t, ID("11"), selected)
	selector.afterSelect = func() {
		revision, getErr := service.Store.get(ctx, bindingKey(embedded, "11"), new(Binding))
		require.NoError(t, getErr)
		require.NoError(t, service.Store.delete(ctx, bindingKey(embedded, "11"), revision))
	}
	_, _, _, err = service.selectEmbeddedSource(ctx, embedded, "show inventory", nil)
	require.Error(t, err)
	require.ErrorIs(t, service.BindEmbeddedSource(ctx, embedded, chat.ID, "11"), ErrNotFound)
}

func TestEmbeddedSourceSelectionRejectsPermissionChangeDuringEmbedding(t *testing.T) {
	ctx := context.Background()
	service, admin, embedded, app := embeddedSelectionFixture(t)
	adminSource, err := service.binding(ctx, admin, "10")
	require.NoError(t, err)
	adminSource.Datasource, adminSource.Name, adminSource.BindingID = "11", "inventory", "inventory-admin-binding"
	require.NoError(t, service.Store.put(ctx, bindingKey(admin, "11"), adminSource, 0))
	guestSource, err := service.binding(ctx, embedded, "10")
	require.NoError(t, err)
	guestSource.Datasource, guestSource.Name, guestSource.BindingID = "11", "inventory", "inventory-guest-binding"
	require.NoError(t, service.Store.put(ctx, bindingKey(embedded, "11"), guestSource, 0))
	app.DatasourceIDs = []ID{"10", "11"}
	app, err = service.SaveAssistant(ctx, admin, app)
	require.NoError(t, err)
	embedded.EmbeddedRevision = app.Revision
	require.NoError(t, service.Store.UpdateChatSettings(ctx, admin,
		map[string]string{"embedding.datasource_count": "1"}))
	// Warm the encrypted source index before revoking a row permission. The
	// cached vectors must never bypass the post-selection permission check.
	changing := &catalogChangingEmbedding{}
	service.Embedding = changing
	_, _, _, err = service.selectEmbeddedSource(ctx, embedded, "show inventory", nil)
	require.NoError(t, err)
	var changeErr error
	changing.change = func() {
		rule := testActiveRowPermission(t, "east")
		rule.Users = []int64{2}
		changeErr = service.Store.put(ctx, permissionRuleKey(embedded.Workspace, rule.ID), rule, 0)
	}
	_, _, _, err = service.selectEmbeddedSource(ctx, embedded, "show inventory", nil)
	require.NoError(t, changeErr)
	require.ErrorIs(t, err, ErrGateway)
}
