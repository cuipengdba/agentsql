package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestSQLBotRenameReturnsConversationTitle(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, token := serviceFixture(t)
	chat, err := service.Start(ctx, principal, "10", "Original", 0)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/chat/rename"
	body := `{"id":"` + string(chat.ID) + `","brief":"Renamed"}`
	if response := accountRequest(t, handler, http.MethodPost, path, body, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous rename returned %d", response.Code)
	}
	response := accountRequest(t, handler, http.MethodPost, path, body, token)
	var envelope struct {
		Data string `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Data != "Renamed" {
		t.Fatalf("SQLBot rename response: %d %s", response.Code, response.Body.String())
	}
	stored, err := service.ReadChat(ctx, principal, chat.ID, false)
	if err != nil || stored.Brief != envelope.Data {
		t.Fatalf("rename response differs from stored title: %+v %v", stored, err)
	}
}
