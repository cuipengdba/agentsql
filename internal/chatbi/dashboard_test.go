package chatbi

import (
	"context"
	"errors"
	"testing"
)

func TestDashboardRequeriesAndFailsClosedAfterRevocation(t *testing.T) {
	ctx := context.Background()
	service, principal, gateway, _, token := serviceFixture(t)
	chat, err := service.Start(ctx, principal, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	record, err := service.Ask(ctx, principal, chat.ID, "show sales", check, nil)
	if err != nil {
		t.Fatal(err)
	}
	card := DashboardCard{ID: "11111111111111111111111111111111", Kind: "bar", Title: "Sales", RecordID: record.ID}
	tab := DashboardTab{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "Regional results"}
	card.TabID, card.Span = tab.ID, 2
	item, err := service.SaveDashboard(ctx, principal, Dashboard{Name: "Regional sales", Tabs: []DashboardTab{tab}, Cards: []DashboardCard{card}})
	if err != nil {
		t.Fatal(err)
	}
	if item.Revision != 1 {
		t.Fatalf("revision = %d", item.Revision)
	}
	before := gateway.queries
	view, err := service.Dashboard(ctx, principal, item.ID, true)
	if err != nil || gateway.queries != before+1 || view.Results[card.ID].AuditID != gateway.audit || len(view.Tabs) != 1 || view.Cards[0].Span != 2 || view.Cards[0].TabID != tab.ID {
		t.Fatalf("dashboard did not requery with audit: view=%#v, err=%v", view, err)
	}
	items, err := service.Dashboards(ctx, principal)
	if err != nil || len(items) != 1 || len(items[0].Cards) != 0 {
		t.Fatalf("metadata list: %#v, %v", items, err)
	}
	gateway.schemaErr = ErrGateway
	if _, err := service.Record(ctx, principal, record.ID, false); !errors.Is(err, ErrGateway) {
		t.Fatalf("record metadata survived gateway outage: %v", err)
	}
	if _, err := service.Dashboard(ctx, principal, item.ID, false); !errors.Is(err, ErrGateway) {
		t.Fatalf("dashboard card metadata survived gateway outage: %v", err)
	}
	gateway.schemaErr = nil
	if _, err := service.SaveDashboard(ctx, principal, Dashboard{ID: item.ID, Name: "stale", Revision: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	other := principal
	other.User.ID = "999"
	if _, err := service.Dashboard(ctx, other, item.ID, true); err == nil {
		t.Fatal("other user read dashboard")
	}
	revision, err := service.Store.get(ctx, bindingKey(principal, "10"), new(Binding))
	if err != nil || service.Store.delete(ctx, bindingKey(principal, "10"), revision) != nil {
		t.Fatal("could not revoke source")
	}
	before = gateway.queries
	if _, err := service.Dashboard(ctx, principal, item.ID, true); err == nil || gateway.queries != before {
		t.Fatalf("revoked dashboard returned data or reached gateway: %v", err)
	}
	if _, err := service.Dashboard(ctx, principal, item.ID, false); err == nil {
		t.Fatalf("revoked dashboard returned card metadata: %v", err)
	}
	if _, err := service.Record(ctx, principal, record.ID, false); err == nil {
		t.Fatalf("revoked record returned saved SQL metadata: %v", err)
	}
	if err := service.DeleteDashboard(ctx, principal, item.ID, item.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Dashboard(ctx, principal, item.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted dashboard: %v", err)
	}
}

func TestDashboardRejectsInvalidAndForeignRecords(t *testing.T) {
	ctx := context.Background()
	service, principal, _, _, _ := serviceFixture(t)
	cases := []Dashboard{
		{Name: "empty card ID", Cards: []DashboardCard{{Kind: "text", Text: "hello"}}},
		{Name: "HTML reference", Cards: []DashboardCard{{ID: "22222222222222222222222222222222", Kind: "bar", RecordID: "999"}}},
		{Name: "raw SQL", Cards: []DashboardCard{{ID: "33333333333333333333333333333333", Kind: "sql", Text: "select *"}}},
		{Name: "unknown tab", Cards: []DashboardCard{{ID: "44444444444444444444444444444444", Kind: "text", TabID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{Name: "invalid span", Cards: []DashboardCard{{ID: "55555555555555555555555555555555", Kind: "text", Span: 3}}},
		{Name: "duplicate tab", Tabs: []DashboardTab{{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "One"}, {ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "Two"}}},
	}
	for _, item := range cases {
		if _, err := service.SaveDashboard(ctx, principal, item); err == nil {
			t.Fatalf("accepted %s", item.Name)
		}
	}
}
