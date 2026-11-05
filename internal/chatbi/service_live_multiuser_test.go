package chatbi

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Run explicitly with a local OpenAI-compatible model. The gateway is a
// separate fixture for each user, so this checks the complete Ask lifecycle
// and user-scoped persistence without claiming a real database result.
func TestLiveAskConcurrentUsersKeepResultsSeparate(t *testing.T) {
	endpoint := os.Getenv("AGENTSQL_LOCAL_MODEL_ENDPOINT")
	if endpoint == "" {
		t.Skip("local model endpoint is not configured")
	}
	modelName := os.Getenv("AGENTSQL_LOCAL_MODEL_NAME")
	if modelName == "" {
		modelName = "agentsql-local-qwen"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	service, first, firstGateway, _, firstToken := serviceFixture(t)
	var old ModelConfig
	revision, err := service.Store.get(ctx, objectKey{"model", "1", "workspace", "default"}, &old)
	if err != nil {
		t.Fatal(err)
	}
	old.Model, old.Endpoint, old.AllowHTTP = modelName, endpoint, true
	if err := service.Store.put(ctx, objectKey{"model", "1", "workspace", "default"}, old, revision); err != nil {
		t.Fatal(err)
	}
	service.Model = OpenAIModel{}
	firstGateway.queryData = &QueryData{Fields: []string{"amount"}, Data: []map[string]any{{"amount": "111"}}, AuditID: 1}
	secondUser := testUser(t, "2", "second")
	if err := service.Store.CreateUser(ctx, secondUser); err != nil {
		t.Fatal(err)
	}
	_, secondToken, err := service.Identity.Login(ctx, secondUser.Account, "Synthetic-password-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Identity.Verify(ctx, secondToken)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Datasource: "10", Workspace: "1", User: "2", Name: "fixture", Dialect: "pg", BindingID: "binding-2", ConnectorToken: "fixture-connector-2", AgentKey: "fixture-agent-2"}
	if err := service.Store.put(ctx, bindingKey(second, "10"), binding, 0); err != nil {
		t.Fatal(err)
	}
	secondGateway := &fakeGateway{queryData: &QueryData{Fields: []string{"amount"}, Data: []map[string]any{{"amount": "222"}}, AuditID: 2}}
	otherService := *service
	otherService.Gateway = secondGateway
	firstChat, err := service.Start(ctx, first, "10", "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	secondChat, err := otherService.Start(ctx, second, "10", "second", 0)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		record Record
		err    error
	}
	var results [2]outcome
	var wait sync.WaitGroup
	for i, item := range []struct {
		service *Service
		user    Principal
		chat    ID
		token   string
	}{
		{service, first, firstChat.ID, firstToken},
		{&otherService, second, secondChat.ID, secondToken},
	} {
		wait.Add(1)
		go func(i int, item struct {
			service *Service
			user    Principal
			chat    ID
			token   string
		}) {
			defer wait.Done()
			check := func(ctx context.Context) error {
				_, err := item.service.Identity.Verify(ctx, item.token)
				return err
			}
			results[i].record, results[i].err = item.service.Ask(ctx, item.user, item.chat,
				"Select amount from public.sales, one row.", check, nil)
		}(i, item)
	}
	wait.Wait()
	for i, expected := range []string{"111", "222"} {
		result := results[i]
		if result.err != nil || result.record.Data == nil || result.record.Data.AuditID != int64(i+1) ||
			len(result.record.Data.Data) != 1 || result.record.Data.Data[0]["amount"] != expected ||
			!result.record.Finish || result.record.Error != "" || !strings.Contains(strings.ToLower(result.record.SQL), "sales") {
			t.Fatalf("user %d did not receive only its audited result: %+v, %v", i+1, result.record, result.err)
		}
	}
	if firstGateway.queries != 1 || secondGateway.queries != 1 {
		t.Fatalf("unexpected gateway queries: %d, %d", firstGateway.queries, secondGateway.queries)
	}
	if _, err := service.ReadChat(ctx, first, secondChat.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first user read second user's chat: %v", err)
	}
	if _, err := otherService.ReadChat(ctx, second, firstChat.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second user read first user's chat: %v", err)
	}
	for i, item := range []struct {
		service *Service
		user    Principal
		chat    ID
	}{
		{service, first, firstChat.ID},
		{&otherService, second, secondChat.ID},
	} {
		history, err := item.service.ReadChat(ctx, item.user, item.chat, true)
		if err != nil || len(history.Records) != 1 || history.Records[0].Data == nil ||
			len(history.Records[0].Data.Data) != 1 || history.Records[0].Data.Data[0]["amount"] != []string{"111", "222"}[i] {
			t.Fatalf("user %d could not reopen its own audited result: %+v, %v", i+1, history, err)
		}
	}
	t.Logf("two concurrent local-model Ask calls completed with separate audited results")
}
