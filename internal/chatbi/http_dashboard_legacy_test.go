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

func TestLegacyDashboardEmptyTabRoundTripAndRejectsUnownedNestedContent(t *testing.T) {
	service, principal, _, _, token := serviceFixture(t)
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
	component := map[string]any{
		"id": "2232545529898901504", "component": "SQTab", "name": "new-tabs",
		"activeTabName": "tab_2232545529903095808", "x": 1, "y": 1,
		"sizeX": 36, "sizeY": 14, "style": map[string]any{},
		"propValue": []any{map[string]any{
			"name": "tab_2232545529903095808", "title": "New Tab",
			"componentData": []any{}, "closable": true,
		}},
	}
	encode := func(item map[string]any) []byte {
		canvas, err := json.Marshal([]any{item})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{
			"name": "Tabs", "pid": "root", "node_type": "leaf", "type": "dashboard",
			"component_data": string(canvas), "canvas_style_data": "{}", "canvas_view_info": "{}",
		})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	created := call("/api/v1/dashboard/create_canvas", encode(component))
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &envelope) != nil || !validDashboardID(envelope.Data.ID) {
		t.Fatalf("create empty original SQTab: %d %s", created.Code, created.Body.String())
	}
	stored, err := service.Dashboard(t.Context(), principal, envelope.Data.ID, false)
	if err != nil || len(stored.Dashboard.Cards) != 1 || stored.Dashboard.Cards[0].Kind != "tab" {
		t.Fatalf("stored tab: %#v %v", stored, err)
	}
	loadBody, _ := json.Marshal(map[string]any{"id": envelope.Data.ID})
	loaded := call("/api/v1/dashboard/load_resource", loadBody)
	if loaded.Code != 200 || !bytes.Contains(loaded.Body.Bytes(), []byte("SQTab")) || !bytes.Contains(loaded.Body.Bytes(), []byte("tab_2232545529903095808")) {
		t.Fatalf("load tab: %d %s", loaded.Code, loaded.Body.String())
	}

	// Client-forged nested charts without an owned record stay blocked.
	component["propValue"] = []any{map[string]any{
		"name": "tab_2232545529903095808", "title": "New Tab",
		"componentData": []any{map[string]any{
			"id": "99", "component": "SQView", "sql": "select secret", "data": []any{"forged"},
		}}, "closable": true,
	}}
	rejected := call("/api/v1/dashboard/create_canvas", encode(component))
	if rejected.Code != 422 || bytes.Contains(rejected.Body.Bytes(), []byte("select secret")) {
		t.Fatalf("nested chart accepted or echoed: %d %s", rejected.Code, rejected.Body.String())
	}
	component["propValue"] = []any{map[string]any{
		"name": "tab_2232545529903095808", "title": "New Tab",
		"componentData": []any{}, "closable": true,
	}}
	component["activeTabName"] = "tab_999"
	rejected = call("/api/v1/dashboard/create_canvas", encode(component))
	if rejected.Code != 422 {
		t.Fatalf("unknown active tab accepted: %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestLegacyDashboardNestedTextAndOwnedChartReauthorizeOnRead(t *testing.T) {
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
	components, _ := json.Marshal([]map[string]any{{
		"id": "300", "component": "SQTab", "activeTabName": "tab_123", "style": map[string]any{},
		"propValue": []any{map[string]any{
			"name": "tab_123", "title": "Details", "closable": true,
			"componentData": []any{
				map[string]any{"id": "301", "component": "SQText", "propValue": `<p>safe<img src=x onerror=alert(1)></p>`, "style": map[string]any{}},
				map[string]any{"id": "302", "component": "SQView", "style": map[string]any{}},
			},
		}},
	}})
	views, _ := json.Marshal(map[string]any{"302": map[string]any{
		"sourceId":   string(record.Chat) + "_" + string(record.ID),
		"datasource": record.Datasource, "chart": map[string]any{"type": "column", "title": "Revenue"},
		"sql": "SELECT forged_secret FROM unknown_table", "data": map[string]any{"data": []any{map[string]any{"secret": "client-forged"}}},
	}})
	input, _ := json.Marshal(map[string]any{
		"name": "Nested", "pid": "root", "node_type": "leaf", "type": "dashboard",
		"component_data": string(components), "canvas_style_data": "{}", "canvas_view_info": string(views),
	})
	created := call("/api/v1/dashboard/create_canvas", input)
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &response) != nil || !validDashboardID(response.Data.ID) {
		t.Fatalf("nested tab create: %d %s", created.Code, created.Body.String())
	}
	stored, err := service.Dashboard(t.Context(), principal, response.Data.ID, false)
	storedJSON, _ := json.Marshal(stored.Dashboard)
	if err != nil || len(stored.Cards) != 1 || len(stored.Cards[0].TabPages) != 1 ||
		len(stored.Cards[0].TabPages[0].Cards) != 2 || bytes.Contains(storedJSON, []byte("client-forged")) ||
		bytes.Contains(storedJSON, []byte("forged_secret")) || bytes.Contains(storedJSON, []byte("onerror")) {
		t.Fatalf("unsafe nested content persisted: %v %s", err, storedJSON)
	}
	before := gateway.queries
	loaded := call("/api/v1/dashboard/load_resource", []byte(`{"id":"`+response.Data.ID+`"}`))
	if loaded.Code != 200 || gateway.queries != before+1 || !bytes.Contains(loaded.Body.Bytes(), []byte("SQText")) ||
		!bytes.Contains(loaded.Body.Bytes(), []byte("SQView")) || bytes.Contains(loaded.Body.Bytes(), []byte("client-forged")) ||
		bytes.Contains(loaded.Body.Bytes(), []byte("forged_secret")) || bytes.Contains(loaded.Body.Bytes(), []byte("onerror")) {
		t.Fatalf("nested fresh read: status=%d queries=%d body=%s", loaded.Code, gateway.queries-before, loaded.Body.String())
	}
	revision, err := service.Store.get(t.Context(), bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(t.Context(), bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke datasource")
	}
	denied := call("/api/v1/dashboard/load_resource", []byte(`{"id":"`+response.Data.ID+`"}`))
	if denied.Code == 200 || bytes.Contains(denied.Body.Bytes(), []byte("safe")) || bytes.Contains(denied.Body.Bytes(), []byte("Revenue")) {
		t.Fatalf("revoked nested chart leaked: %d %s", denied.Code, denied.Body.String())
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
