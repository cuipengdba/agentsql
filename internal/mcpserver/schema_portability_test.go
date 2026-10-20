package mcpserver

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAdvertisedToolSchemasUseConcretePortableTypes(t *testing.T) {
	fixture := newMCPFixture(t, "dml")
	handler, err := NewHTTPHandler(fixture.runtime, statefulHTTPTestConfig(100), zerolog.Nop(),
		WithB5Sessions(B5Options{B5Sessions: true, Service: &b5ServiceSpy{}}))
	require.NoError(t, err)
	initialized := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost, initializeRequest, "")
	require.Equal(t, http.StatusOK, initialized.Code, initialized.Body.String())
	listed := serveMCPWithSession(t, handler, fixture.handlers.apiKey, http.MethodPost,
		listToolsRequest, initialized.Header().Get(mcpSessionIDHeader))
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var response struct {
		Result struct {
			Tools []struct {
				Name         string         `json:"name"`
				InputSchema  map[string]any `json:"inputSchema"`
				OutputSchema map[string]any `json:"outputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &response))
	require.Len(t, response.Result.Tools, 20)
	for _, tool := range response.Result.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			inputProperties, _ := tool.InputSchema["properties"].(map[string]any)
			for _, name := range []string{"expected_seq", "statements"} {
				if property, present := inputProperties[name]; present {
					typeName, ok := property.(map[string]any)["type"].(string)
					require.True(t, ok, "%s.%s must have one concrete type", tool.Name, name)
					require.NotEmpty(t, typeName)
				}
			}
			outputProperties, ok := tool.OutputSchema["properties"].(map[string]any)
			require.True(t, ok)
			data, ok := outputProperties["data"].(map[string]any)
			require.True(t, ok)
			want := "object"
			if tool.Name == "list_datasources" || tool.Name == "list_schema" {
				want = "array"
			}
			require.Equal(t, want, data["type"])
		})
	}
}
