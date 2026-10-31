package chatbi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAskPassesOnlyCurrentSelectedDatabaseRemarksToModel(t *testing.T) {
	ctx := context.Background()
	service, principal, base, _, token := serviceFixture(t)
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{
		{Schema: "public", Table: "sales", Name: "amount", DataType: "numeric"},
		{Schema: "public", Table: "sales", Name: "region", DataType: "text"},
		{Schema: "public", Table: "private", Name: "secret", DataType: "text"},
	}}
	model := &remarkCaptureModel{sql: "SELECT amount FROM public.sales LIMIT 2"}
	service.Model = model
	binding, revision, _, err := service.bindingSnapshot(ctx, principal, "10")
	require.NoError(t, err)
	binding.SelectedTables = `[{"schema":"public","table":"sales"}]`
	tableComments, err := json.Marshal(map[string]string{
		databaseTableCommentKey("public", "sales"):   "sales report",
		databaseTableCommentKey("public", "private"): "private report",
	})
	require.NoError(t, err)
	fieldComments, err := json.Marshal(map[string]string{
		databaseFieldCommentKey("public", "sales", "amount"):   "sales amount",
		databaseFieldCommentKey("public", "sales", "hidden"):   "hidden column",
		databaseFieldCommentKey("public", "private", "secret"): "private secret",
	})
	require.NoError(t, err)
	binding.TableComments, binding.FieldComments = string(tableComments), string(fieldComments)
	require.NoError(t, service.Store.put(ctx, bindingKey(principal, "10"), binding, revision))
	chat, err := service.Start(ctx, principal, "10", "show sales", 0)
	require.NoError(t, err)
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	_, err = service.Ask(ctx, principal, chat.ID, "show sales", check, nil)
	require.NoError(t, err)
	require.Equal(t, []RemarkHint{
		{Schema: "public", Table: "sales", Remark: "sales report"},
		{Schema: "public", Table: "sales", Column: "amount", Remark: "sales amount"},
	}, model.remarks)
	require.Equal(t, 1, base.queries)

	// A later gateway revocation must close before the model sees old remarks.
	service.Gateway = &selectionTestGateway{fakeGateway: base, columns: []Column{{Schema: "public", Table: "private", Name: "secret"}}}
	_, err = service.Ask(ctx, principal, chat.ID, "show sales again", check, nil)
	require.ErrorIs(t, err, ErrGateway)
	require.Equal(t, 1, base.queries)
	require.Equal(t, 2, len(model.remarks))
}

func TestDatabaseRemarkHintsRejectInvalidStoredDataAndBoundPrompt(t *testing.T) {
	binding := Binding{Dialect: "mysql", Type: "mysql", TableComments: `{"public\u0000sales":"ok"}`,
		FieldComments: `{"public\u0000sales\u0000amount":"sales"}`}
	columns := []Column{{Schema: "public", Table: "sales", Name: "amount"}}
	hints, err := databaseRemarkHints(binding, columns)
	require.NoError(t, err)
	require.Len(t, hints, 2)
	binding.FieldComments = `{"public\u0000sales\u0000amount":"` + strings.Repeat("x", 501) + `"}`
	_, err = databaseRemarkHints(binding, columns)
	require.ErrorIs(t, err, ErrUnavailable)

	large := make([]RemarkHint, 1000)
	for i := range large {
		large[i] = RemarkHint{Schema: "public", Table: "sales", Column: "amount", Remark: strings.Repeat("x", 500)}
	}
	bounded, err := boundRemarkHints(large)
	require.NoError(t, err)
	require.Less(t, len(bounded), len(large))
	require.LessOrEqual(t, len(bounded), 264)
	encoded, err := json.Marshal(bounded)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 256<<10)
}
