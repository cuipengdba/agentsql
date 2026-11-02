package chatbi

import (
	"context"
	"math"
	"sort"
	"strings"
)

type modelTableCandidate struct {
	key      string
	name     string
	comment  string
	text     string
	index    int
	vector   float64
	keyword  float64
	explicit bool
}

// selectModelTables only narrows schema already authorized by the live gateway.
// An incomplete model or failed inference preserves every authorized table;
// the controlled query boundary still checks the generated SQL independently.
func selectModelTables(ctx context.Context, model EmbeddingModel, question string, columns []Column,
	remarks []RemarkHint, limit int, alpha float64, keywordEnabled bool) ([]Column, []RemarkHint, error) {
	if model == nil || limit < 1 || len(question) > 4096 {
		return columns, remarks, nil
	}
	if !validSimilarity(alpha) {
		return nil, nil, ErrUnavailable
	}
	tables := make([]modelTableCandidate, 0)
	positions := make(map[string]int)
	for _, column := range columns {
		key := column.Schema + "\x00" + column.Table
		index, found := positions[key]
		if !found {
			index = len(tables)
			positions[key] = index
			tables = append(tables, modelTableCandidate{key: key, name: column.Table, index: index,
				text: column.Schema + "." + column.Table})
		}
		tables[index].text += " (" + column.Name + ":" + column.DataType + ")"
	}
	if len(tables) <= limit || len(tables) > 500 {
		return columns, remarks, nil
	}
	for _, remark := range remarks {
		if index, found := positions[remark.Schema+"\x00"+remark.Table]; found {
			tables[index].text += " " + remark.Remark
			if remark.Column == "" {
				tables[index].comment = remark.Remark
			}
		}
	}
	for _, table := range tables {
		if len(table.text) == 0 || len(table.text) > 4096 {
			return columns, remarks, nil
		}
	}
	query, err := model.Encode(ctx, []string{question})
	if err != nil || len(query) != 1 || len(query[0]) != 768 {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return columns, remarks, nil
	}
	for start := 0; start < len(tables); start += 32 {
		end := min(start+32, len(tables))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = tables[index].text
		}
		vectors, err := model.Encode(ctx, texts)
		if err != nil || len(vectors) != len(texts) {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return columns, remarks, nil
		}
		for offset, vector := range vectors {
			if len(vector) != 768 {
				return columns, remarks, nil
			}
			for dimension, value := range vector {
				tables[start+offset].vector += float64(value) * float64(query[0][dimension])
			}
			if math.IsNaN(tables[start+offset].vector) || math.IsInf(tables[start+offset].vector, 0) {
				return columns, remarks, nil
			}
		}
	}
	explicit := explicitTableMentions(question, tables)
	for index := range tables {
		table := &tables[index]
		table.explicit = explicit[table.key]
		if keywordEnabled {
			if table.explicit {
				table.keyword = 1
				if !mentionedIdentifier(question, table.name) && table.comment != "" &&
					strings.Contains(strings.ToLower(question), strings.ToLower(table.comment)) {
					table.keyword = 0.9
				}
			} else {
				for _, remark := range remarks {
					if remark.Schema+"\x00"+remark.Table == table.key && remark.Remark != "" &&
						strings.Contains(strings.ToLower(question), strings.ToLower(remark.Remark)) {
						table.keyword = max(table.keyword, 0.7)
					}
				}
			}
		}
	}
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].explicit != tables[j].explicit {
			return tables[i].explicit
		}
		left, right := tables[i].vector, tables[j].vector
		if keywordEnabled {
			left = alpha*left + (1-alpha)*tables[i].keyword
			right = alpha*right + (1-alpha)*tables[j].keyword
		}
		if left == right {
			return tables[i].index < tables[j].index
		}
		return left > right
	})
	selected := make(map[string]bool)
	for _, table := range tables {
		if len(selected) < limit || table.explicit {
			selected[table.key] = true
		}
	}
	filteredColumns := make([]Column, 0, len(columns))
	for _, column := range columns {
		if selected[column.Schema+"\x00"+column.Table] {
			filteredColumns = append(filteredColumns, column)
		}
	}
	filteredRemarks := make([]RemarkHint, 0, len(remarks))
	for _, remark := range remarks {
		if selected[remark.Schema+"\x00"+remark.Table] {
			filteredRemarks = append(filteredRemarks, remark)
		}
	}
	return filteredColumns, filteredRemarks, nil
}

func mentionedIdentifier(question, name string) bool {
	if name == "" {
		return false
	}
	question, name = strings.ToLower(question), strings.ToLower(name)
	for start := 0; start < len(question); {
		offset := strings.Index(question[start:], name)
		if offset < 0 {
			return false
		}
		position := start + offset
		end := position + len(name)
		before := position == 0 || !asciiIdentifier(question[position-1])
		after := end == len(question) || !asciiIdentifier(question[end])
		if before && after {
			return true
		}
		start = position + 1
	}
	return false
}

func asciiIdentifier(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '$'
}
