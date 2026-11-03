package chatbi

import "testing"

func TestTableKeywordScoreMatchesOriginalV1103Cases(t *testing.T) {
	table := modelTableCandidate{
		name: "sales_order", comment: "销售订单",
		fields: []Column{{Name: "customer_id"}, {Name: "amount"}},
	}
	comments := map[string]string{"customer_id": "客户编号", "amount": "销售收入"}
	for _, item := range []struct {
		input string
		want  float64
	}{
		{"sales_order", 1},
		{"销售订单", 0.9},
		{"订单", 0.7},
		{"sales", 0.38},
		{"客户编号", 0.5},
		{"收入", 0.4},
		{"customer", 0.3},
		{"unknown", 0},
		{"客户编号,unknown", 0.25},
		{"customer,unknown", 0.15},
		{"sales_order,unknown", 1},
		{"收入,收入", 0.2},
	} {
		if got := scoreTableKeywords(item.input, table, comments); got != item.want {
			t.Errorf("keyword %q: got %v, want %v", item.input, got, item.want)
		}
	}
	// A field remark for an unauthorized column is never supplied to scoring.
	if got := scoreTableKeywords("secret", modelTableCandidate{name: "public"}, map[string]string{"hidden": "secret"}); got != 0 {
		t.Fatalf("unseen field influenced table ranking: %v", got)
	}
}

func TestTableKeywordExactNamePrecedesHigherVectorCommentMatch(t *testing.T) {
	tables := []modelTableCandidate{
		{key: "weather", vector: 1, keyword: 0.9, explicit: true},
		{key: "sales", vector: 0, keyword: 1, explicit: true},
	}
	rankModelTables(tables, 1, true)
	if tables[0].key != "sales" || tables[1].key != "weather" {
		t.Fatalf("exact keyword table name lost original priority: %v", tables)
	}
}
