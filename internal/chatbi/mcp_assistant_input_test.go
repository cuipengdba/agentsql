package chatbi

import "testing"

func TestMCPAssistantInputKeepsOriginalShapeWithoutGrantingAuthority(t *testing.T) {
	input := mcpAssistantInput{Question: "How many orders?", URL: "https://catalog.example.test/data?tenant=1",
		Authorization: `[{"key":"X-Catalog-Token","value":"synthetic-token","target":"header"},{"key":"tenant","value":"east","target":"param"}]`}
	credentials, stream, err := parseMCPAssistantInput(input)
	if err != nil || !stream || len(credentials) != 2 || credentials[0].Target != "header" {
		t.Fatalf("original request shape rejected: %v", err)
	}
	noStream := false
	input.Stream = &noStream
	if _, stream, err := parseMCPAssistantInput(input); err != nil || stream {
		t.Fatalf("explicit nonstream request rejected: %v", err)
	}
}

func TestMCPAssistantInputRejectsCredentialAndURLConfusion(t *testing.T) {
	base := mcpAssistantInput{Question: "How many orders?", URL: "https://catalog.example.test/data",
		Authorization: `[{"key":"X-Token","value":"synthetic-token","target":"header"}]`}
	for _, change := range []func(*mcpAssistantInput){
		func(v *mcpAssistantInput) { v.URL = "file:///etc/passwd" },
		func(v *mcpAssistantInput) { v.URL = "https://user:secret@catalog.example.test/data" },
		func(v *mcpAssistantInput) { v.URL = "https://catalog.example.test/data#fragment" },
		func(v *mcpAssistantInput) { v.Authorization = `[]` },
		func(v *mcpAssistantInput) { v.Authorization = `[{"key":"X-Token","value":"a\r\nb","target":"header"}]` },
		func(v *mcpAssistantInput) {
			v.Authorization = `[{"key":"X-Token","value":"a","target":"header"},{"key":"x-token","value":"b","target":"header"}]`
		},
		func(v *mcpAssistantInput) { v.Authorization = `[{"key":"X-Token","value":"a","target":"unknown"}]` },
		func(v *mcpAssistantInput) {
			v.Authorization = `[{"key":"Host","value":"attacker.example.test","target":"header"}]`
		},
	} {
		input := base
		change(&input)
		if _, _, err := parseMCPAssistantInput(input); err == nil {
			t.Fatalf("unsafe request accepted: url=%q authorization=%q", input.URL, input.Authorization)
		}
	}
}
