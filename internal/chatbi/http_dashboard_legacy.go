package chatbi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

type legacyDashboardInput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ParentID        string `json:"pid"`
	NodeType        string `json:"node_type"`
	Type            string `json:"type"`
	Option          string `json:"opt"`
	ComponentData   string `json:"component_data"`
	CanvasStyleData string `json:"canvas_style_data"`
	CanvasViewInfo  string `json:"canvas_view_info"`
}

type legacyDashboardNode struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	ParentID   string                `json:"pid"`
	NodeType   string                `json:"node_type"`
	Type       string                `json:"type"`
	Leaf       bool                  `json:"leaf"`
	CreateTime int64                 `json:"create_time"`
	UpdateTime int64                 `json:"update_time"`
	Children   []legacyDashboardNode `json:"children"`
}

func legacyDashboardParent(id string) string {
	if id == "" {
		return "root"
	}
	return id
}

func legacyDashboardView(item Dashboard, p Principal) map[string]any {
	components := make([]map[string]any, 0, len(item.Cards))
	for index, card := range item.Cards {
		x, y, sizeX, sizeY := card.X, card.Y, card.SizeX, card.SizeY
		if x == 0 && y == 0 {
			x, y = 1, index*16+1
		}
		if sizeX == 0 {
			sizeX = 36
		}
		if sizeY == 0 {
			sizeY = 14
		}
		component := map[string]any{
			"id": card.ID, "locked": false, "editing": false, "x": x, "y": y,
			"sizeX": sizeX, "sizeY": sizeY, "style": map[string]any{}, "_dragId": card.ID,
		}
		if card.Kind == "text" {
			component["component"], component["name"], component["propValue"] = "SQText", "text", card.Text
		} else {
			component["component"], component["name"], component["propValue"] = "SQView", "view", "&nbsp;"
			component["innerType"] = legacyChartType(card.Kind)
		}
		components = append(components, component)
	}
	componentData, _ := json.Marshal(components)
	return map[string]any{
		"id": item.ID, "name": item.Name, "pid": legacyDashboardParent(item.ParentID),
		"workspace_id": string(p.Workspace), "create_by": string(p.User.ID),
		"node_type": "leaf", "type": "dashboard", "create_time": item.Created.Unix(),
		"update_time": item.Updated.Unix(), "component_data": string(componentData),
		"canvas_style_data": "{}", "canvas_view_info": "{}", "status": 1, "content_id": "0",
	}
}

func legacyFolderView(item DashboardFolder, p Principal) map[string]any {
	return map[string]any{
		"id": item.ID, "name": item.Name, "pid": item.ParentID,
		"workspace_id": string(p.Workspace), "create_by": string(p.User.ID),
		"node_type": "folder", "type": "dashboard", "create_time": item.Created.Unix(),
		"update_time": item.Updated.Unix(),
	}
}

func (h *Handler) legacyDashboardInventory(r *http.Request, p Principal) ([]DashboardFolder, []Dashboard, error) {
	folders, err := h.service.DashboardFolders(r.Context(), p)
	if err != nil {
		return nil, nil, err
	}
	boards, err := h.service.Dashboards(r.Context(), p)
	if err != nil {
		return nil, nil, err
	}
	return folders, boards, nil
}

func (h *Handler) legacyDashboardList(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	if input.NodeType != "" && input.NodeType != "folder" && input.NodeType != "leaf" {
		failure(w, ErrInvalidDashboard)
		return
	}
	folders, boards, err := h.legacyDashboardInventory(r, p)
	if err != nil {
		failure(w, err)
		return
	}
	nodes := make(map[string]legacyDashboardNode, len(folders)+len(boards))
	for _, folder := range folders {
		if input.NodeType == "leaf" {
			continue
		}
		nodes[folder.ID] = legacyDashboardNode{ID: folder.ID, Name: folder.Name, ParentID: folder.ParentID, NodeType: "folder", Type: "dashboard", CreateTime: folder.Created.Unix(), UpdateTime: folder.Updated.Unix(), Children: []legacyDashboardNode{}}
	}
	for _, board := range boards {
		if input.NodeType == "folder" {
			continue
		}
		nodes[board.ID] = legacyDashboardNode{ID: board.ID, Name: board.Name, ParentID: legacyDashboardParent(board.ParentID), NodeType: "leaf", Type: "dashboard", Leaf: true, CreateTime: board.Created.Unix(), UpdateTime: board.Updated.Unix(), Children: []legacyDashboardNode{}}
	}
	children := make(map[string][]legacyDashboardNode)
	for _, node := range nodes {
		parent := node.ParentID
		if parent != "root" {
			if _, exists := nodes[parent]; !exists {
				// A node type filter may hide the parent folder. The original
				// folder-only picker still needs a reachable tree.
				if input.NodeType == "leaf" {
					parent = "root"
				} else {
					failure(w, ErrUnavailable)
					return
				}
			}
		}
		children[parent] = append(children[parent], node)
	}
	var build func(string, int) []legacyDashboardNode
	build = func(parent string, depth int) []legacyDashboardNode {
		if depth > 9 {
			return []legacyDashboardNode{}
		}
		items := children[parent]
		if items == nil {
			return []legacyDashboardNode{}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].CreateTime > items[j].CreateTime })
		for i := range items {
			items[i].Children = build(items[i].ID, depth+1)
		}
		return items
	}
	okay(w, build("root", 0))
}

func (h *Handler) legacyDashboardName(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	if input.Option != "newLeaf" && input.Option != "newFolder" && input.Option != "updateLeaf" && input.Option != "updateFolder" && input.Option != "rename" {
		failure(w, ErrInvalidDashboard)
		return
	}
	if strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 64 || (strings.HasPrefix(input.Option, "update") || input.Option == "rename") && !validDashboardID(input.ID) {
		failure(w, ErrInvalidDashboard)
		return
	}
	folders, boards, err := h.legacyDashboardInventory(r, p)
	if err != nil {
		failure(w, err)
		return
	}
	for _, item := range folders {
		if item.ID != input.ID && item.Name == input.Name {
			okay(w, false)
			return
		}
	}
	for _, item := range boards {
		if item.ID != input.ID && item.Name == input.Name {
			okay(w, false)
			return
		}
	}
	okay(w, true)
}

func (h *Handler) legacyDashboardCreateFolder(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	if input.ID != "" || input.NodeType != "folder" || input.Type != "dashboard" {
		failure(w, ErrInvalidDashboard)
		return
	}
	item, err := h.service.SaveDashboardFolder(r.Context(), p, DashboardFolder{Name: input.Name, ParentID: legacyDashboardParent(input.ParentID)})
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyFolderView(item, p))
}

func (h *Handler) legacyDashboardUpdateResource(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	if !validDashboardID(input.ID) {
		failure(w, ErrInvalidDashboard)
		return
	}
	if folder, err := h.service.DashboardFolder(r.Context(), p, input.ID); err == nil {
		folder.Name = input.Name
		saved, err := h.service.SaveDashboardFolder(r.Context(), p, folder)
		if err != nil {
			failure(w, err)
			return
		}
		okay(w, legacyFolderView(saved, p))
		return
	} else if err != ErrNotFound {
		failure(w, err)
		return
	}
	view, err := h.service.Dashboard(r.Context(), p, input.ID, false)
	if err != nil {
		failure(w, err)
		return
	}
	view.Name = input.Name
	saved, err := h.service.SaveDashboard(r.Context(), p, view.Dashboard)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyDashboardView(saved, p))
}

func legacyEmptyCanvas(input legacyDashboardInput) bool {
	for _, pair := range [][2]string{{input.ComponentData, "[]"}, {input.CanvasStyleData, "{}"}, {input.CanvasViewInfo, "{}"}} {
		if pair[0] == "" {
			continue
		}
		var actual, empty any
		if json.Unmarshal([]byte(pair[0]), &actual) != nil || json.Unmarshal([]byte(pair[1]), &empty) != nil {
			return false
		}
		if len(pair[0]) > 16384 || !equalEmptyCanvasValue(actual, empty) {
			return false
		}
	}
	return true
}

type legacyTextComponent struct {
	ID        string         `json:"id"`
	Component string         `json:"component"`
	Name      string         `json:"name"`
	Text      string         `json:"propValue"`
	X         int            `json:"x"`
	Y         int            `json:"y"`
	SizeX     int            `json:"sizeX"`
	SizeY     int            `json:"sizeY"`
	Style     map[string]any `json:"style"`
}

type legacyChartInput struct {
	SourceID   string `json:"sourceId"`
	Datasource *ID    `json:"datasource"`
	SQL        string `json:"sql"`
	Chart      struct {
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"chart"`
}

func (h *Handler) legacyChartRecord(ctx context.Context, p Principal, view legacyChartInput) (Record, error) {
	if view.SourceID != "" {
		parts := strings.Split(view.SourceID, "_")
		if len(parts) != 2 || !ID(parts[0]).Valid() || !ID(parts[1]).Valid() {
			return Record{}, ErrInvalidDashboard
		}
		record, err := h.service.Record(ctx, p, ID(parts[1]), false)
		if err != nil {
			return Record{}, err
		}
		if record.Chat != ID(parts[0]) || view.Datasource != nil && *view.Datasource != record.Datasource {
			return Record{}, ErrInvalidDashboard
		}
		return record, nil
	}
	// The original chat page does not include a record ID in its
	// add-to-dashboard request. Treat client SQL only as an exact lookup key
	// against this user's completed history; it is never executed or stored.
	if view.Datasource == nil || !view.Datasource.Valid() || view.SQL == "" || len(view.SQL) > 8192 {
		return Record{}, ErrInvalidDashboard
	}
	chats, err := h.service.Store.list(ctx, "chat", string(p.Workspace), string(p.User.ID))
	if err != nil {
		return Record{}, err
	}
	if len(chats) > 1000 {
		return Record{}, ErrInvalidDashboard
	}
	var matched Record
	count := 0
	for _, raw := range chats {
		var chat Chat
		if json.Unmarshal(raw, &chat) != nil || chat.Workspace != p.Workspace || chat.Creator != p.User.ID {
			return Record{}, ErrUnavailable
		}
		count += len(chat.Records)
		if count > 5000 {
			return Record{}, ErrInvalidDashboard
		}
		for _, record := range chat.Records {
			if record.Finish && record.Error == "" && record.SQL == view.SQL && record.Datasource == *view.Datasource && record.Created.After(matched.Created) {
				matched = record
			}
		}
	}
	if !matched.ID.Valid() {
		return Record{}, ErrNotFound
	}
	record, err := h.service.Record(ctx, p, matched.ID, false)
	if err != nil {
		return Record{}, err
	}
	if record.SQL != view.SQL || record.Datasource != *view.Datasource || !record.Finish || record.Error != "" {
		return Record{}, ErrInvalidDashboard
	}
	return record, nil
}

func legacyChartType(kind string) string {
	if kind == "bar" {
		return "column"
	}
	return kind
}

func (h *Handler) legacyChartViews(ctx context.Context, p Principal, view DashboardView) (string, error) {
	views := make(map[string]any)
	for _, card := range view.Cards {
		if card.RecordID == "" {
			continue
		}
		data, exists := view.Results[card.ID]
		if !exists || data.AuditID <= 0 {
			return "", ErrGateway
		}
		record, err := h.service.Record(ctx, p, card.RecordID, false)
		if err != nil {
			return "", err
		}
		columns := make([]map[string]string, 0, len(data.Fields))
		for _, field := range data.Fields {
			columns = append(columns, map[string]string{"name": field, "value": field})
		}
		var xAxis, yAxis []map[string]string
		if len(data.Fields) >= 2 {
			valueField := data.Fields[1]
			for _, info := range data.FieldsInfo {
				if info.Numeric {
					valueField = info.Name
					break
				}
			}
			labelField := data.Fields[0]
			if labelField == valueField {
				labelField = data.Fields[1]
			}
			xAxis = []map[string]string{{"name": labelField, "value": labelField}}
			yAxis = []map[string]string{{"name": valueField, "value": valueField}}
		}
		views[card.ID] = map[string]any{
			"id": card.ID, "sourceId": string(record.Chat) + "_" + string(record.ID),
			"datasource": record.Datasource, "status": "success", "message": "",
			"data": data, "chart": map[string]any{
				"type": legacyChartType(card.Kind), "sourceType": legacyChartType(card.Kind),
				"title": card.Title, "columns": columns, "xAxis": xAxis, "yAxis": yAxis,
				"series": []any{}, "multiQuotaName": "",
			},
		}
	}
	raw, err := json.Marshal(views)
	if err != nil {
		return "", ErrUnavailable
	}
	return string(raw), nil
}

func (h *Handler) legacyCanvasCards(ctx context.Context, p Principal, input legacyDashboardInput) ([]DashboardCard, error) {
	if !legacyEmptyCanvas(legacyDashboardInput{CanvasStyleData: input.CanvasStyleData}) || len(input.ComponentData) > 32000 || len(input.CanvasViewInfo) > 32000 {
		return nil, ErrInvalidDashboard
	}
	viewInfo := make(map[string]legacyChartInput)
	if input.CanvasViewInfo != "" && json.Unmarshal([]byte(input.CanvasViewInfo), &viewInfo) != nil {
		return nil, ErrInvalidDashboard
	}
	if len(viewInfo) > 32 {
		return nil, ErrInvalidDashboard
	}
	if input.ComponentData == "" {
		input.ComponentData = "[]"
	}
	var components []legacyTextComponent
	if json.Unmarshal([]byte(input.ComponentData), &components) != nil || len(components) > 32 {
		return nil, ErrInvalidDashboard
	}
	cards := make([]DashboardCard, 0, len(components))
	for _, component := range components {
		if !(validDashboardID(component.ID) || ID(component.ID).Valid()) || len(component.Style) != 0 {
			return nil, ErrInvalidDashboard
		}
		card := DashboardCard{ID: component.ID, Title: "Text", X: component.X, Y: component.Y, SizeX: component.SizeX, SizeY: component.SizeY}
		switch component.Component {
		case "SQText":
			if _, exists := viewInfo[component.ID]; exists {
				return nil, ErrInvalidDashboard
			}
			clean, err := sanitizeDashboardText(component.Text)
			if err != nil {
				return nil, err
			}
			card.Kind, card.Text = "text", clean
		case "SQView":
			view, exists := viewInfo[component.ID]
			if !exists {
				return nil, ErrInvalidDashboard
			}
			delete(viewInfo, component.ID)
			record, err := h.legacyChartRecord(ctx, p, view)
			if err != nil {
				return nil, err
			}
			card.RecordID = record.ID
			switch view.Chart.Type {
			case "table", "bar", "line", "pie":
				card.Kind = view.Chart.Type
			case "column":
				card.Kind = "bar"
			default:
				return nil, ErrInvalidDashboard
			}
			card.Title = strings.TrimSpace(view.Chart.Title)
			if card.Title == "" {
				card.Title = "Chart"
			}
			if len([]rune(card.Title)) > 120 {
				return nil, ErrInvalidDashboard
			}
		default:
			return nil, ErrInvalidDashboard
		}
		cards = append(cards, card)
	}
	if len(viewInfo) != 0 {
		return nil, ErrInvalidDashboard
	}
	return cards, nil
}

func equalEmptyCanvasValue(actual, empty any) bool {
	switch empty.(type) {
	case []any:
		value, okay := actual.([]any)
		return okay && len(value) == 0
	case map[string]any:
		value, okay := actual.(map[string]any)
		return okay && len(value) == 0
	}
	return false
}

func (h *Handler) legacyDashboardCreateCanvas(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	if input.ID != "" || input.NodeType != "leaf" || input.Type != "dashboard" {
		failure(w, ErrInvalidDashboard)
		return
	}
	cards, err := h.legacyCanvasCards(r.Context(), p, input)
	if err != nil {
		failure(w, err)
		return
	}
	item, err := h.service.SaveDashboard(r.Context(), p, Dashboard{Name: input.Name, ParentID: legacyDashboardParent(input.ParentID), Cards: cards})
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyDashboardView(item, p))
}

func (h *Handler) legacyDashboardLoad(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	view, err := h.service.Dashboard(r.Context(), p, input.ID, true)
	if err != nil {
		failure(w, err)
		return
	}
	chartViews, err := h.legacyChartViews(r.Context(), p, view)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return
	}
	result := legacyDashboardView(view.Dashboard, p)
	result["canvas_view_info"] = chartViews
	okay(w, result)
}

func (h *Handler) legacyDashboardUpdateCanvas(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	var input legacyDashboardInput
	if !readBody(w, r, &input) {
		return
	}
	cards, err := h.legacyCanvasCards(r.Context(), p, input)
	if err != nil {
		failure(w, ErrInvalidDashboard)
		return
	}
	view, err := h.service.Dashboard(r.Context(), p, input.ID, false)
	if err != nil {
		failure(w, err)
		return
	}
	view.Name = input.Name
	view.Cards = cards
	saved, err := h.service.SaveDashboard(r.Context(), p, view.Dashboard)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, legacyDashboardView(saved, p))
}

func (h *Handler) legacyDashboardDelete(w http.ResponseWriter, r *http.Request, p Principal, _ string) {
	id := r.PathValue("id")
	if folder, err := h.service.DashboardFolder(r.Context(), p, id); err == nil {
		if err := h.service.DeleteDashboardFolder(r.Context(), p, folder.ID); err != nil {
			failure(w, err)
			return
		}
		okay(w, true)
		return
	} else if err != ErrNotFound {
		failure(w, err)
		return
	}
	view, err := h.service.Dashboard(r.Context(), p, id, false)
	if err != nil {
		failure(w, err)
		return
	}
	if err := h.service.DeleteDashboard(r.Context(), p, id, view.Revision); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
