package chatbi

import (
	"context"
	"errors"
	"testing"
)

type narrativeFixtureModel struct {
	fakeModel
	after func()
	seen  QueryData
	calls int
}

func (model *narrativeFixtureModel) Narrate(_ context.Context, _ ModelConfig, _, _ string, data QueryData) (string, error) {
	model.calls++
	model.seen = data
	if model.after != nil {
		model.after()
	}
	return "Observed authorized sample; no causal conclusion.", nil
}

func finishedFixtureRecord(t *testing.T, service *Service, p Principal, token string) Record {
	t.Helper()
	ctx := context.Background()
	chat, err := service.Start(ctx, p, "10", "sales", 0)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error { _, err := service.Identity.Verify(ctx, token); return err }
	record, err := service.Ask(ctx, p, chat.ID, "show sales", check, nil)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestNarrativeRequiresTwoAuditedCurrentReads(t *testing.T) {
	ctx := context.Background()
	service, p, gateway, _, token := serviceFixture(t)
	model := &narrativeFixtureModel{}
	service.Model = model
	record := finishedFixtureRecord(t, service, p, token)
	before := gateway.queries
	result, err := service.NarrateRecord(ctx, p, record.ID, "analysis")
	if err != nil || gateway.queries != before+2 || result.AuditID <= 0 || result.AnalyzedRows != 1 || model.calls != 1 {
		t.Fatalf("narrative audit boundary: result=%#v queries=%d model=%d err=%v", result, gateway.queries-before, model.calls, err)
	}
	if len(model.seen.Data) != 1 || model.seen.Data[0]["amount"] != "123" {
		t.Fatalf("model sample: %#v", model.seen)
	}
	childID, err := service.SaveNarrative(ctx, p, result)
	if err != nil || !childID.Valid() {
		t.Fatalf("analysis child record: %s, %v", childID, err)
	}
	withoutData, err := service.ReadChat(ctx, p, record.Chat, false)
	if err != nil || len(withoutData.Records) != 2 || withoutData.Records[1].Analysis != "" || withoutData.Records[1].DataDigest != "" {
		t.Fatalf("metadata read leaked saved analysis: %#v, %v", withoutData.Records, err)
	}
	withData, err := service.ReadChat(ctx, p, record.Chat, true)
	if err != nil || len(withData.Records) != 2 || withData.Records[1].Analysis != result.Content || withData.Records[1].AnalysisOf != record.ID || withData.Records[1].Data == nil || withData.Records[1].DataDigest != "" {
		t.Fatalf("audited child read: %#v, %v", withData.Records, err)
	}
	model.after = func() {
		revision, err := service.Store.get(ctx, bindingKey(p, "10"), new(Binding))
		if err != nil || service.Store.delete(ctx, bindingKey(p, "10"), revision) != nil {
			t.Fatal("could not revoke binding")
		}
	}
	before = gateway.queries
	if _, err := service.NarrateRecord(ctx, p, record.ID, "predict"); err == nil || gateway.queries != before+1 {
		t.Fatalf("revoked model narrative escaped: %v, queries=%d", err, gateway.queries-before)
	}
}

func TestNarrativeRejectsMissingModelAndUnauthorizedRecord(t *testing.T) {
	ctx := context.Background()
	service, p, _, _, token := serviceFixture(t)
	record := finishedFixtureRecord(t, service, p, token)
	if _, err := service.NarrateRecord(ctx, p, record.ID, "analysis"); !errors.Is(err, ErrModel) {
		t.Fatalf("missing narrative model: %v", err)
	}
	other := p
	other.User.ID = "999"
	if _, err := service.NarrateRecord(ctx, other, record.ID, "analysis"); err == nil {
		t.Fatal("other user obtained narrative")
	}
	if _, err := service.NarrateRecord(ctx, p, record.ID, "unknown"); !errors.Is(err, ErrModel) {
		t.Fatalf("invalid action: %v", err)
	}
}
