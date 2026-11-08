package chatbi

import "testing"

func TestVisibleHistoryTableSurvivesVectorNarrowing(t *testing.T) {
	authorized := []Column{
		{Schema: "public", Table: "orders", Name: "region", DataType: "text"},
		{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "decoy", Name: "amount", DataType: "numeric"},
	}
	selected := []Column{authorized[2]}
	remarks := []RemarkHint{{Schema: "public", Table: "orders", Remark: "Orders"}}
	history := []TurnContext{{Question: "show west orders", SQL: "SELECT region, amount FROM public.orders WHERE region = 'west' LIMIT 20"}}
	columns, selectedRemarks := includeVisibleHistoryTables("pg", authorized, remarks, selected, nil, history)
	if len(columns) != 3 || columns[1] != authorized[0] || columns[2] != authorized[1] || len(selectedRemarks) != 1 {
		t.Fatalf("visible history table was omitted: columns=%#v remarks=%#v", columns, selectedRemarks)
	}
	if len(visibleHistoryForModel("pg", columns, history)) != 1 {
		t.Fatal("visible prior turn did not survive schema narrowing")
	}

	// A revoked column makes the old SQL ineligible even though the table name
	// is still visible. Historical SQL must not restore the revoked column.
	revoked := []Column{authorized[0], authorized[2]}
	columns, selectedRemarks = includeVisibleHistoryTables("pg", revoked, remarks, []Column{authorized[2]}, nil, history)
	if len(columns) != 1 || len(selectedRemarks) != 0 || len(visibleHistoryForModel("pg", revoked, history)) != 0 {
		t.Fatalf("revoked historical field reached model schema: columns=%#v remarks=%#v", columns, selectedRemarks)
	}
}

func TestHistoricalSQLCannotExpandModelSchema(t *testing.T) {
	authorized := []Column{{Schema: "public", Table: "orders", Name: "amount", DataType: "numeric"}}
	selected := []Column{{Schema: "public", Table: "other", Name: "amount", DataType: "numeric"}}
	for _, sql := range []string{
		"SELECT secret FROM public.orders LIMIT 20",
		"SELECT amount FROM public.private_orders LIMIT 20",
		"SELECT amount FROM public.orders; DROP TABLE public.orders",
	} {
		columns, _ := includeVisibleHistoryTables("pg", authorized, nil, selected, nil, []TurnContext{{Question: "old", SQL: sql}})
		if len(columns) != 1 || columns[0].Table != "other" {
			t.Fatalf("untrusted history expanded model schema for %q: %#v", sql, columns)
		}
	}
}
