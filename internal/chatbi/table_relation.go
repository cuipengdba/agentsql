package chatbi

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
)

type relationPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type relationEndpoint struct {
	Cell json.RawMessage `json:"cell"`
	Port json.RawMessage `json:"port"`
}

type relationInputCell struct {
	ID       json.RawMessage  `json:"id"`
	Shape    string           `json:"shape"`
	Position *relationPoint   `json:"position"`
	Source   relationEndpoint `json:"source"`
	Target   relationEndpoint `json:"target"`
}

type relationEdge struct {
	sourceTable, sourceField int64
	targetTable, targetField int64
}

func relationNumericID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	value := string(raw)
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0 && id <= maxSQLBotBrowserID && strconv.FormatInt(id, 10) == value
}

func relationGraph(binding Binding, columns []Column, input []relationInputCell) ([]map[string]any, []SemanticHint, error) {
	if len(input) > 768 {
		return nil, nil, ErrGateway
	}
	tables, valid := visibleBrowserTables(binding.Datasource, columns)
	if !valid {
		return nil, nil, ErrGateway
	}
	byID := make(map[int64]visibleBrowserTable, len(tables))
	fields := make(map[int64]map[int64]Column, len(tables))
	for _, table := range tables {
		byID[table.id] = table
		fields[table.id] = make(map[int64]Column, len(table.columns))
		for _, column := range table.columns {
			id := browserFieldID(table.id, column.Name)
			if _, duplicate := fields[table.id][id]; duplicate {
				return nil, nil, ErrGateway
			}
			fields[table.id][id] = column
		}
	}
	result := make([]map[string]any, 0, len(input))
	nodes := make(map[int64]bool)
	for _, cell := range input {
		if cell.Shape != "er-rect" {
			continue
		}
		id, ok := relationNumericID(cell.ID)
		table, visible := byID[id]
		if !ok || !visible || nodes[id] || cell.Position == nil ||
			math.IsNaN(cell.Position.X) || math.IsNaN(cell.Position.Y) ||
			math.IsInf(cell.Position.X, 0) || math.IsInf(cell.Position.Y, 0) ||
			math.Abs(cell.Position.X) > 100000 || math.Abs(cell.Position.Y) > 100000 {
			return nil, nil, ErrGateway
		}
		nodes[id] = true
		ports := make([]map[string]any, 0, len(table.columns))
		for _, column := range table.columns {
			ports = append(ports, map[string]any{"id": strconv.FormatInt(browserFieldID(id, column.Name), 10),
				"group": "list", "attrs": map[string]any{
					"portNameLabel": map[string]any{"text": column.Name},
					"portTypeLabel": map[string]any{"text": column.DataType}}})
		}
		result = append(result, map[string]any{"id": strconv.FormatInt(id, 10), "shape": "er-rect",
			"label": table.name, "attrs": map[string]any{"label": map[string]any{"text": table.name}},
			"position": cell.Position, "ports": ports})
	}
	if len(nodes) > 256 {
		return nil, nil, ErrGateway
	}
	var hints []SemanticHint
	edges := make(map[relationEdge]bool)
	for _, cell := range input {
		if cell.Shape == "er-rect" {
			continue
		}
		if cell.Shape != "edge" {
			return nil, nil, ErrGateway
		}
		sourceTable, sourceOK := relationNumericID(cell.Source.Cell)
		targetTable, targetOK := relationNumericID(cell.Target.Cell)
		sourceField, sourceFieldOK := relationNumericID(cell.Source.Port)
		targetField, targetFieldOK := relationNumericID(cell.Target.Port)
		sourceColumn, sourceVisible := fields[sourceTable][sourceField]
		targetColumn, targetVisible := fields[targetTable][targetField]
		edge := relationEdge{sourceTable, sourceField, targetTable, targetField}
		if !sourceOK || !targetOK || !sourceFieldOK || !targetFieldOK ||
			!nodes[sourceTable] || !nodes[targetTable] || sourceTable == targetTable ||
			!sourceVisible || !targetVisible || edges[edge] {
			return nil, nil, ErrGateway
		}
		edges[edge] = true
		result = append(result, map[string]any{
			"id": "agentsql-relation-" + strconv.FormatInt(sourceTable, 10) + "-" + strconv.FormatInt(sourceField, 10) +
				"-" + strconv.FormatInt(targetTable, 10) + "-" + strconv.FormatInt(targetField, 10),
			"shape":  "edge",
			"source": map[string]any{"cell": strconv.FormatInt(sourceTable, 10), "port": strconv.FormatInt(sourceField, 10)},
			"target": map[string]any{"cell": strconv.FormatInt(targetTable, 10), "port": strconv.FormatInt(targetField, 10)},
		})
		if len(hints) < 8 {
			source, target := byID[sourceTable], byID[targetTable]
			hints = append(hints, SemanticHint{Word: "table relationship",
				Description: source.schema + "." + source.name + "." + sourceColumn.Name +
					" relates to " + target.schema + "." + target.name + "." + targetColumn.Name +
					"; suggestion only, not authorization"})
		}
	}
	if len(edges) > 512 {
		return nil, nil, ErrGateway
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 1<<20 {
		return nil, nil, ErrGateway
	}
	return result, hints, nil
}

func relationHintsForModel(binding Binding, columns []Column, vocabulary []SemanticHint) ([]SemanticHint, error) {
	if binding.TableRelations == "" {
		return vocabulary, nil
	}
	if len(binding.TableRelations) > 1<<20 {
		return nil, ErrGateway
	}
	var stored []relationInputCell
	if json.Unmarshal([]byte(binding.TableRelations), &stored) != nil {
		return nil, ErrGateway
	}
	_, relations, err := relationGraph(binding, columns, stored)
	if err != nil {
		return nil, err
	}
	for _, relation := range relations {
		if len(vocabulary) == 8 {
			break
		}
		candidate := append(append([]SemanticHint(nil), vocabulary...), relation)
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return nil, ErrUnavailable
		}
		if len(encoded) > 4096 {
			break
		}
		vocabulary = candidate
	}
	return vocabulary, nil
}

func (h *Handler) relationBinding(w http.ResponseWriter, r *http.Request, p Principal, token string) (Binding, int64, [32]byte, []Column, bool) {
	if err := h.service.semanticAdmin(r.Context(), p); err != nil {
		failure(w, err)
		return Binding{}, 0, [32]byte{}, nil, false
	}
	id := ID(r.PathValue("id"))
	if !id.Valid() {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid datasource ID")
		return Binding{}, 0, [32]byte{}, nil, false
	}
	binding, revision, digest, err := h.service.bindingSnapshot(r.Context(), p, id)
	if err != nil {
		failure(w, err)
		return Binding{}, 0, [32]byte{}, nil, false
	}
	columns, err := h.service.schema(r.Context(), p, binding)
	if err != nil {
		failure(w, err)
		return Binding{}, 0, [32]byte{}, nil, false
	}
	if err := h.currentIdentity(r.Context(), p, token); err != nil {
		failure(w, err)
		return Binding{}, 0, [32]byte{}, nil, false
	}
	if err := h.service.bindingUnchanged(r.Context(), p, binding, revision, digest); err != nil {
		failure(w, err)
		return Binding{}, 0, [32]byte{}, nil, false
	}
	return binding, revision, digest, columns, true
}

func (h *Handler) getTableRelations(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	binding, _, _, columns, ok := h.relationBinding(w, r, p, token)
	if !ok {
		return
	}
	var stored []relationInputCell
	if binding.TableRelations != "" && (len(binding.TableRelations) > 1<<20 ||
		json.Unmarshal([]byte(binding.TableRelations), &stored) != nil) {
		failure(w, ErrGateway)
		return
	}
	graph, _, err := relationGraph(binding, columns, stored)
	if err != nil {
		failure(w, err)
		return
	}
	okay(w, graph)
}

func (h *Handler) saveTableRelations(w http.ResponseWriter, r *http.Request, p Principal, token string) {
	var input []relationInputCell
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, http.StatusUnprocessableEntity, "Invalid relation graph")
		return
	}
	binding, _, _, columns, ok := h.relationBinding(w, r, p, token)
	if !ok {
		return
	}
	graph, _, err := relationGraph(binding, columns, input)
	if err != nil {
		failure(w, err)
		return
	}
	encoded := ""
	if len(graph) > 0 {
		body, err := json.Marshal(graph)
		if err != nil {
			failure(w, ErrUnavailable)
			return
		}
		encoded = string(body)
	}
	if err := h.service.Store.updateTableRelations(r.Context(), p, binding, encoded); err != nil {
		failure(w, err)
		return
	}
	okay(w, true)
}
