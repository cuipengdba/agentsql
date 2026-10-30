package chatbi

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// A relationship graph is display data supplied by an administrator. Rebuild
// every cell from the currently visible gateway schema before storing or using
// it; X6 cells can otherwise carry arbitrary markup, links and foreign IDs.
var errInvalidRelationGraph = errors.New("invalid table relationship graph")

type relationEndpoint struct {
	Cell json.RawMessage `json:"cell"`
	Port json.RawMessage `json:"port"`
}

type relationPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type relationCellInput struct {
	ID       json.RawMessage  `json:"id"`
	Shape    string           `json:"shape"`
	Position relationPosition `json:"position"`
	Source   relationEndpoint `json:"source"`
	Target   relationEndpoint `json:"target"`
}

type RelationHint struct {
	SourceSchema string `json:"source_schema"`
	SourceTable  string `json:"source_table"`
	SourceField  string `json:"source_field"`
	TargetSchema string `json:"target_schema"`
	TargetTable  string `json:"target_table"`
	TargetField  string `json:"target_field"`
}

func relationHints(binding Binding, columns []Column) ([]RelationHint, error) {
	if binding.TableRelations == "" {
		return nil, nil
	}
	graph, err := relationGraph([]byte(binding.TableRelations), binding.Datasource, columns)
	if err != nil {
		return nil, ErrGateway
	}
	tables, valid := visibleBrowserTables(binding.Datasource, columns)
	if !valid {
		return nil, ErrGateway
	}
	byID := make(map[int64]visibleBrowserTable, len(tables))
	fieldNames := make(map[int64]map[int64]string, len(tables))
	for _, table := range tables {
		byID[table.id] = table
		fieldNames[table.id] = make(map[int64]string, len(table.columns))
		for _, column := range table.columns {
			fieldNames[table.id][browserFieldID(table.id, column.Name)] = column.Name
		}
	}
	hints := make([]RelationHint, 0)
	for _, cell := range graph {
		if cell["shape"] != "edge" {
			continue
		}
		sourceID, sourcePort, sourceOK := normalizedRelationEndpoint(cell["source"])
		targetID, targetPort, targetOK := normalizedRelationEndpoint(cell["target"])
		left, leftOK := byID[sourceID]
		right, rightOK := byID[targetID]
		if !sourceOK || !targetOK || !leftOK || !rightOK ||
			fieldNames[sourceID][sourcePort] == "" || fieldNames[targetID][targetPort] == "" {
			return nil, ErrGateway
		}
		hints = append(hints, RelationHint{SourceSchema: left.schema, SourceTable: left.name,
			SourceField: fieldNames[sourceID][sourcePort], TargetSchema: right.schema, TargetTable: right.name,
			TargetField: fieldNames[targetID][targetPort]})
	}
	return hints, nil
}

func normalizedRelationEndpoint(value any) (int64, int64, bool) {
	endpoint, ok := value.(map[string]any)
	if !ok {
		return 0, 0, false
	}
	cell, cellOK := endpoint["cell"].(string)
	port, portOK := endpoint["port"].(string)
	if !cellOK || !portOK {
		return 0, 0, false
	}
	cellID, cellErr := strconv.ParseInt(cell, 10, 64)
	portID, portErr := strconv.ParseInt(port, 10, 64)
	return cellID, portID, cellErr == nil && portErr == nil
}

func relationNumericID(raw json.RawMessage) (int64, error) {
	var value string
	if len(raw) == 0 {
		return 0, errInvalidRelationGraph
	}
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, errInvalidRelationGraph
		}
	} else {
		value = string(raw)
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 || id > maxSQLBotBrowserID || strconv.FormatInt(id, 10) != value {
		return 0, errInvalidRelationGraph
	}
	return id, nil
}

func relationGraph(raw []byte, datasource ID, columns []Column) ([]map[string]any, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, errInvalidRelationGraph
	}
	var cells []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cells); err != nil || cells == nil || len(cells) > 128 {
		return nil, errInvalidRelationGraph
	}
	input := make([]relationCellInput, len(cells))
	for i, cell := range cells {
		encoded, err := json.Marshal(cell)
		if err != nil || json.Unmarshal(encoded, &input[i]) != nil {
			return nil, errInvalidRelationGraph
		}
	}
	tables, valid := visibleBrowserTables(datasource, columns)
	if !valid || len(tables) > 64 {
		return nil, errInvalidRelationGraph
	}
	byID := make(map[int64]visibleBrowserTable, len(tables))
	ports := make(map[int64]map[int64]string, len(tables))
	for _, table := range tables {
		byID[table.id] = table
		ports[table.id] = make(map[int64]string, len(table.columns))
		for _, column := range table.columns {
			id := browserFieldID(table.id, column.Name)
			if ports[table.id][id] != "" { // A hash collision must fail closed.
				return nil, errInvalidRelationGraph
			}
			ports[table.id][id] = column.Name
		}
	}
	result := make([]map[string]any, 0, len(input))
	selected := make(map[int64]bool)
	for _, cell := range input {
		if cell.Shape != "er-rect" {
			continue
		}
		id, err := relationNumericID(cell.ID)
		table, exists := byID[id]
		if err != nil || !exists || selected[id] || !finiteRelationPosition(cell.Position) {
			return nil, errInvalidRelationGraph
		}
		selected[id] = true
		items := make([]map[string]any, 0, len(table.columns))
		for _, column := range table.columns {
			items = append(items, map[string]any{"id": strconv.FormatInt(browserFieldID(id, column.Name), 10), "group": "list",
				"attrs": map[string]any{"portNameLabel": map[string]string{"text": column.Name}}})
		}
		result = append(result, map[string]any{"id": strconv.FormatInt(id, 10), "shape": "er-rect", "label": table.name,
			"position": cell.Position, "height": 51, "width": 180, "ports": items,
			"attrs": map[string]any{"label": map[string]any{"text": table.name, "textAnchor": "left", "refX": 34, "refY": 28}}})
	}
	seenEdges := make(map[string]bool)
	for _, cell := range input {
		if cell.Shape == "er-rect" {
			continue
		}
		if cell.Shape != "edge" || len(seenEdges) >= 64 {
			return nil, errInvalidRelationGraph
		}
		sourceTable, err1 := relationNumericID(cell.Source.Cell)
		sourceField, err2 := relationNumericID(cell.Source.Port)
		targetTable, err3 := relationNumericID(cell.Target.Cell)
		targetField, err4 := relationNumericID(cell.Target.Port)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || !selected[sourceTable] ||
			!selected[targetTable] || sourceTable == targetTable || ports[sourceTable][sourceField] == "" || ports[targetTable][targetField] == "" {
			return nil, errInvalidRelationGraph
		}
		key := strconv.FormatInt(sourceTable, 10) + ":" + strconv.FormatInt(sourceField, 10) + ":" +
			strconv.FormatInt(targetTable, 10) + ":" + strconv.FormatInt(targetField, 10)
		reverse := strconv.FormatInt(targetTable, 10) + ":" + strconv.FormatInt(targetField, 10) + ":" +
			strconv.FormatInt(sourceTable, 10) + ":" + strconv.FormatInt(sourceField, 10)
		if seenEdges[key] || seenEdges[reverse] {
			return nil, errInvalidRelationGraph
		}
		seenEdges[key] = true
		result = append(result, map[string]any{"shape": "edge", "source": map[string]any{
			"cell": strconv.FormatInt(sourceTable, 10), "port": strconv.FormatInt(sourceField, 10)},
			"target": map[string]any{"cell": strconv.FormatInt(targetTable, 10), "port": strconv.FormatInt(targetField, 10)}})
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 64<<10 {
		return nil, errInvalidRelationGraph
	}
	return result, nil
}

func finiteRelationPosition(p relationPosition) bool {
	return !math.IsNaN(p.X) && !math.IsInf(p.X, 0) && !math.IsNaN(p.Y) && !math.IsInf(p.Y, 0) &&
		p.X >= -100000 && p.X <= 100000 && p.Y >= -100000 && p.Y <= 100000
}
