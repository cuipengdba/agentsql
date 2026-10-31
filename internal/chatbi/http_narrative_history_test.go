package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyConversationReloadRevalidatesNarrative(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	record := finishedFixtureRecord(t, service, principal, token)
	service.Model = &narrativeFixtureModel{}
	narrative, err := service.NarrateRecord(ctx, principal, record.ID, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveNarrative(ctx, principal, narrative); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	read := func() *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/chat/"+string(record.Chat), nil)
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	before := gateway.queries
	response := read()
	if response.Code != http.StatusOK || gateway.queries != before+1 {
		t.Fatalf("legacy reload status=%d queries=%d body=%s", response.Code, gateway.queries-before, response.Body.String())
	}
	var envelope struct {
		Data Chat `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || len(envelope.Data.Records) != 3 {
		t.Fatalf("legacy reload envelope: %v %s", err, response.Body.String())
	}
	child := envelope.Data.Records[2]
	if child.Analysis != narrative.Content || child.Data != nil || child.DataDigest != "" {
		t.Fatalf("audited narrative reload: %#v", child)
	}

	// A newly authorized result that differs from the saved sample must hide
	// the old model text, even though the conversation metadata still loads.
	service.Gateway = &forecastGateway{data: QueryData{Fields: []string{"amount"}, Data: []map[string]any{{"amount": "999"}}}}
	response = read()
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), narrative.Content) {
		t.Fatalf("stale narrative escaped: %d %s", response.Code, response.Body.String())
	}
	envelope = struct {
		Data Chat `json:"data"`
	}{}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Data.Records[2].Analysis != "" {
		t.Fatalf("stale narrative remained visible: %v %s", err, response.Body.String())
	}

	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke datasource binding")
	}
	response = read()
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), narrative.Content) {
		t.Fatalf("revoked narrative escaped: %d %s", response.Code, response.Body.String())
	}
}
