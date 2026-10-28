package chatbi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestUserVariableBindingValuesRespectCurrentDefinition(t *testing.T) {
	for _, check := range []struct {
		kind, definition, selected string
		valid                      bool
	}{
		{"text", `["east","west"]`, `["east"]`, true},
		{"text", `["east","west"]`, `["north"]`, false},
		{"kv", `[{"key":"wx","value":"微信"}]`, `["微信"]`, true},
		{"kv", `[{"key":"wx","value":"微信"}]`, `["wx"]`, false},
		{"number", `[0,100]`, `[0]`, true},
		{"number", `[0,100]`, `[101]`, false},
		{"datetime", `["2026-01-01","2026-12-31"]`, `["2026-04-01"]`, true},
		{"datetime", `["2026-01-01","2026-12-31"]`, `["2027-01-01"]`, false},
	} {
		var values []json.RawMessage
		if err := json.Unmarshal([]byte(check.selected), &values); err != nil {
			t.Fatal(err)
		}
		definition := SystemVariable{VarType: check.kind, Value: json.RawMessage(check.definition)}
		if got := validUserVariableValues(definition, values); got != check.valid {
			t.Fatalf("binding validation kind=%s definition=%s selected=%s: got %t", check.kind, check.definition, check.selected, got)
		}
	}
}

func TestManagedUserVariableBindingsAreScopedValidatedAndAudited(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	ctx := context.Background()
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save",
		`{"name":"region","var_type":"text","value":["east","west"]}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("create variable: %d %s", created.Code, created.Body.String())
	}
	definitions, err := service.Store.SystemVariables(ctx, "1", "region")
	if err != nil || len(definitions) != 1 {
		t.Fatalf("variable definition: %+v %v", definitions, err)
	}
	id := definitions[0].ID
	valid := fmt.Sprintf(`{"id":2,"account":"bob","name":"Bob","oid_list":[1],"system_variables":[{"variableId":%d,"variableValues":["east"]}]}`, id)
	changed := accountRequest(t, handler, http.MethodPut, "/api/v1/user", valid, adminToken)
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"variableValues":["east"]`) {
		t.Fatalf("user binding update: %d %s", changed.Code, changed.Body.String())
	}
	bob, _, err := service.Store.User(ctx, "2")
	if err != nil || len(bob.SystemVariables) != 1 || bob.SystemVariables[0].Workspace != "1" ||
		bob.SystemVariables[0].VariableID != id || string(bob.SystemVariables[0].VariableValues[0]) != `"east"` {
		t.Fatalf("stored binding: %+v %v", bob.SystemVariables, err)
	}
	if other := legacyUserView(bob, "2")["system_variables"].([]map[string]any); len(other) != 0 {
		t.Fatalf("cross-workspace binding exposed: %+v", other)
	}
	page := accountRequest(t, handler, http.MethodGet, "/api/v1/user/pager/1/10", "", adminToken)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"variableValues":["east"]`) {
		t.Fatalf("original user list missing assignment: %d %s", page.Code, page.Body.String())
	}
	for _, values := range []string{
		fmt.Sprintf(`[ {"variableId":%d,"variableValues":["north"]} ]`, id),
		fmt.Sprintf(`[ {"variableId":%d,"variableValues":["east"]}, {"variableId":%d,"variableValues":["west"]} ]`, id, id),
		`[{"variableId":999999,"variableValues":["east"]}]`,
		fmt.Sprintf(`[ {"variableId":%d,"variableValues":["east"],"extra":true} ]`, id),
	} {
		input := fmt.Sprintf(`{"id":2,"account":"bob","name":"Bob","oid_list":[1],"system_variables":%s}`, values)
		denied := accountRequest(t, handler, http.MethodPut, "/api/v1/user", input, adminToken)
		if denied.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid binding accepted: %d %s", denied.Code, input)
		}
	}
	bob, _, err = service.Store.User(ctx, "2")
	if err != nil || len(bob.SystemVariables) != 1 || string(bob.SystemVariables[0].VariableValues[0]) != `"east"` {
		t.Fatalf("rejected binding changed storage: %+v %v", bob.SystemVariables, err)
	}
	kept := accountRequest(t, handler, http.MethodPut, "/api/v1/user",
		`{"id":2,"account":"bob","name":"Bob","oid_list":[1]}`, adminToken)
	if kept.Code != http.StatusOK {
		t.Fatalf("update without binding field: %d %s", kept.Code, kept.Body.String())
	}
	bob, _, err = service.Store.User(ctx, "2")
	if err != nil || len(bob.SystemVariables) != 1 || string(bob.SystemVariables[0].VariableValues[0]) != `"east"` {
		t.Fatalf("omitted binding field erased assignment: %+v %v", bob.SystemVariables, err)
	}
	cleared := accountRequest(t, handler, http.MethodPut, "/api/v1/user",
		`{"id":2,"account":"bob","name":"Bob","oid_list":[1],"system_variables":[]}`, adminToken)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear binding: %d %s", cleared.Code, cleared.Body.String())
	}
	bob, _, err = service.Store.User(ctx, "2")
	if err != nil || len(bob.SystemVariables) != 0 {
		t.Fatalf("binding not cleared: %+v %v", bob.SystemVariables, err)
	}
	audits, err := service.Store.list(ctx, "management-audit", "system", "system")
	if err != nil || len(audits) != 4 {
		t.Fatalf("variable and user changes not audited: %d %v", len(audits), err)
	}
}

func TestManagedUserCreateWithSystemVariableBinding(t *testing.T) {
	service, handler, adminToken, _ := adminUsersFixture(t)
	created := accountRequest(t, handler, http.MethodPost, "/api/v1/sys_variable/save",
		`{"name":"channel","var_type":"kv","value":[{"key":"wx","value":"微信"}]}`, adminToken)
	if created.Code != http.StatusOK {
		t.Fatalf("create variable: %d %s", created.Code, created.Body.String())
	}
	definitions, err := service.Store.SystemVariables(context.Background(), "1", "channel")
	if err != nil || len(definitions) != 1 {
		t.Fatalf("variable definition: %+v %v", definitions, err)
	}
	input := fmt.Sprintf(`{"account":"charlie","name":"Charlie","oid_list":[1],"initial_password":"Synthetic-password-4","system_variables":[{"variableId":%d,"variableValues":["微信"]}]}`, definitions[0].ID)
	user := accountRequest(t, handler, http.MethodPost, "/api/v1/user", input, adminToken)
	if user.Code != http.StatusOK || !strings.Contains(user.Body.String(), `"variableValues":["微信"]`) {
		t.Fatalf("create with binding: %d %s", user.Code, user.Body.String())
	}
}
