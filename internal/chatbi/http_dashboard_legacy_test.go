package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyDashboardResourceTreeAndEmptyCanvas(t *testing.T) {
	service, principal, _, _, token := serviceFixture(t)
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) (int, json.RawMessage) {
		request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &envelope)
		return response.Code, envelope.Data
	}
	if status, data := call(http.MethodPost, "/api/v1/dashboard/check_name", `{"name":"Finance","opt":"newFolder"}`); status != 200 || string(data) != "true" {
		t.Fatalf("name check: %d %s", status, data)
	}
	status, data := call(http.MethodPost, "/api/v1/dashboard/create_resource", `{"name":"Finance","pid":"root","node_type":"folder","type":"dashboard"}`)
	if status != 200 {
		t.Fatalf("create folder: %d %s", status, data)
	}
	var folder struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &folder) != nil || !validDashboardID(folder.ID) {
		t.Fatalf("folder response: %s", data)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/check_name", `{"name":"Finance","opt":"newFolder"}`)
	if status != 200 || string(data) != "false" {
		t.Fatalf("duplicate name check: %d %s", status, data)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/create_canvas", `{"name":"Revenue","pid":"`+folder.ID+`","node_type":"leaf","type":"dashboard","component_data":"[]","canvas_style_data":"{}","canvas_view_info":"{}"}`)
	if status != 200 {
		t.Fatalf("create canvas: %d %s", status, data)
	}
	var canvas struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &canvas) != nil || !validDashboardID(canvas.ID) {
		t.Fatalf("canvas response: %s", data)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/list_resource", `{}`)
	var tree []legacyDashboardNode
	if status != 200 || json.Unmarshal(data, &tree) != nil || len(tree) != 1 || tree[0].ID != folder.ID || len(tree[0].Children) != 1 || tree[0].Children[0].ID != canvas.ID {
		t.Fatalf("resource tree: %d %s", status, data)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/load_resource", `{"id":"`+canvas.ID+`"}`)
	if status != 200 || !bytes.Contains(data, []byte(`"component_data":"[]"`)) {
		t.Fatalf("load canvas: %d %s", status, data)
	}
	component, _ := json.Marshal([]map[string]any{{
		"id": "123", "component": "SQText", "propValue": `<p>Hello <strong>world</strong><img src=x onerror=alert(1)><a href="javascript:alert(1)">bad link</a></p>`,
		"x": 1, "y": 2, "sizeX": 36, "sizeY": 14, "style": map[string]any{},
	}})
	update, _ := json.Marshal(map[string]any{"id": canvas.ID, "name": "Revenue", "component_data": string(component), "canvas_style_data": "{}", "canvas_view_info": "{}"})
	status, data = call(http.MethodPost, "/api/v1/dashboard/update_canvas", string(update))
	if status != 200 {
		t.Fatalf("update text: %d %s", status, data)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/load_resource", `{"id":"`+canvas.ID+`"}`)
	if status != 200 || !bytes.Contains(data, []byte("Hello")) || bytes.Contains(data, []byte("onerror")) || bytes.Contains(data, []byte("javascript:")) {
		t.Fatalf("sanitized text load: %d %s", status, data)
	}
	status, _ = call(http.MethodPost, "/api/v1/dashboard/update_canvas", `{"id":"`+canvas.ID+`","name":"Revenue","component_data":"[{\"sql\":\"select secret\"}]"}`)
	if status != 422 {
		t.Fatalf("unsafe canvas accepted: %d", status)
	}
	status, _ = call(http.MethodDelete, "/api/v1/dashboard/delete_resource/"+folder.ID+"/Finance", "")
	if status != 409 {
		t.Fatalf("non-empty folder deleted: %d", status)
	}
	status, _ = call(http.MethodDelete, "/api/v1/dashboard/delete_resource/"+canvas.ID+"/Revenue", "")
	if status != 200 {
		t.Fatalf("delete canvas: %d", status)
	}
	status, _ = call(http.MethodDelete, "/api/v1/dashboard/delete_resource/"+folder.ID+"/Finance", "")
	if status != 200 {
		t.Fatalf("delete folder: %d", status)
	}
	status, data = call(http.MethodPost, "/api/v1/dashboard/list_resource", `{}`)
	if status != 200 || string(data) != "[]" {
		t.Fatalf("resource cleanup: %d %s", status, data)
	}
	if _, err := service.DashboardFolder(t.Context(), principal, folder.ID); err != ErrNotFound {
		t.Fatalf("deleted folder remains readable: %v", err)
	}
}

func TestLegacyDashboardChartUsesOwnedRecordAndFreshGatewayData(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	record := finishedFixtureRecord(t, service, principal, token)
	router, err := NewHandler(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("X-SQLBOT-TOKEN", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	component, _ := json.Marshal([]map[string]any{{"id": "123", "component": "SQView", "x": 1, "y": 1, "sizeX": 36, "sizeY": 14, "style": map[string]any{}}})
	view, _ := json.Marshal(map[string]any{"123": map[string]any{
		"sourceId": string(record.Chat) + "_" + string(record.ID), "datasource": record.Datasource,
		"chart": map[string]any{"type": "column"}, "sql": "SELECT confidential FROM other_table",
		"data": map[string]any{"data": []any{map[string]any{"secret": "client-forged"}}},
	}})
	input := legacyDashboardInput{ComponentData: string(component), CanvasStyleData: "{}", CanvasViewInfo: string(view)}
	parsed, err := router.(*Handler).legacyCanvasCards(ctx, principal, input)
	if err != nil || len(parsed) != 1 || parsed[0].RecordID != record.ID || parsed[0].Kind != "bar" {
		t.Fatalf("owned chart extraction: %#v %v", parsed, err)
	}
	chatPageView, _ := json.Marshal(map[string]any{"123": map[string]any{
		"id": "123", "datasource": record.Datasource, "sql": record.SQL,
		"chart": map[string]any{"type": "column", "title": "Revenue"}, "data": map[string]any{"data": []any{map[string]any{"secret": "client-forged"}}},
	}})
	input.CanvasViewInfo = string(chatPageView)
	parsed, err = router.(*Handler).legacyCanvasCards(ctx, principal, input)
	if err != nil || len(parsed) != 1 || parsed[0].RecordID != record.ID || parsed[0].Title != "Revenue" {
		t.Fatalf("original chat page SQL lookup: %#v %v", parsed, err)
	}
	forgedPageView := strings.ReplaceAll(string(chatPageView), record.SQL, "SELECT confidential FROM other_table")
	if _, err := router.(*Handler).legacyCanvasCards(ctx, principal, legacyDashboardInput{ComponentData: input.ComponentData, CanvasViewInfo: forgedPageView}); err == nil {
		t.Fatal("unowned client SQL accepted")
	}
	createBody, _ := json.Marshal(map[string]any{
		"name": "Sales chart", "pid": "root", "node_type": "leaf", "type": "dashboard",
		"component_data": input.ComponentData, "canvas_style_data": input.CanvasStyleData, "canvas_view_info": input.CanvasViewInfo,
	})
	created := call("/api/v1/dashboard/create_canvas", createBody)
	var createResult struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &createResult) != nil || !validDashboardID(createResult.Data.ID) {
		t.Fatalf("original canvas create: %d %s", created.Code, created.Body.String())
	}
	stored, err := service.Dashboard(ctx, principal, createResult.Data.ID, false)
	storedJSON, _ := json.Marshal(stored.Dashboard)
	if err != nil || bytes.Contains(storedJSON, []byte("client-forged")) || bytes.Contains(storedJSON, []byte("confidential")) {
		t.Fatalf("client query or rows persisted: %v %s", err, storedJSON)
	}
	before := gateway.queries
	response := call("/api/v1/dashboard/load_resource", []byte(`{"id":"`+createResult.Data.ID+`"}`))
	var loaded struct {
		Data struct {
			CanvasViewInfo string `json:"canvas_view_info"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &loaded) != nil || gateway.queries != before+1 || bytes.Contains(response.Body.Bytes(), []byte("client-forged")) || bytes.Contains(response.Body.Bytes(), []byte("confidential")) {
		t.Fatalf("fresh chart read: %d queries=%d body=%s", response.Code, gateway.queries-before, response.Body.String())
	}
	var chartViews map[string]struct {
		Data QueryData `json:"data"`
	}
	if json.Unmarshal([]byte(loaded.Data.CanvasViewInfo), &chartViews) != nil || chartViews["123"].Data.AuditID != 1 || chartViews["123"].Data.Data[0]["amount"] != "123" {
		t.Fatalf("fresh chart payload: %s", loaded.Data.CanvasViewInfo)
	}
	wrongSource := input
	wrongSource.CanvasViewInfo = strings.ReplaceAll(string(view), string(record.Chat)+"_", "999_")
	if _, err := router.(*Handler).legacyCanvasCards(ctx, principal, wrongSource); err == nil {
		t.Fatal("foreign chat reference accepted")
	}
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke datasource")
	}
	response = call("/api/v1/dashboard/load_resource", []byte(`{"id":"`+createResult.Data.ID+`"}`))
	if response.Code == 200 || bytes.Contains(response.Body.Bytes(), []byte("123")) {
		t.Fatalf("revoked chart released data: %d %s", response.Code, response.Body.String())
	}
}
