package chatbi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrGateway = errors.New("controlled gateway rejected or could not complete this operation")

// Binding is server-owned and encrypted at rest. External datasource IDs are
// never accepted as gateway datasource IDs, and browser requests never set keys.
type Binding struct {
	Datasource     ID        `json:"datasource"`
	Workspace      ID        `json:"workspace"`
	User           ID        `json:"user"`
	Name           string    `json:"name"`
	ResolveName    string    `json:"resolve_name,omitempty"`
	Type           string    `json:"type,omitempty"`
	Description    string    `json:"description,omitempty"`
	Configuration  string    `json:"configuration,omitempty"`
	ManagedFileID  string    `json:"managed_file_id,omitempty"`
	SelectedTables string    `json:"selected_tables,omitempty"`
	Created        time.Time `json:"created,omitempty"`
	Dialect        string    `json:"dialect"`
	BindingID      string    `json:"binding_id"`
	ConnectorToken string    `json:"connector_token"`
	AgentKey       string    `json:"agent_key"`
}

type QueryData struct {
	Fields     []string         `json:"fields"`
	Data       []map[string]any `json:"data"`
	FieldsInfo []FieldInfo      `json:"fields_info"`
	AuditID    int64            `json:"audit_id"`
	Truncated  bool             `json:"truncated"`
}
type FieldInfo struct {
	Name    string `json:"name"`
	Numeric bool   `json:"is_numeric"`
}

type Gateway interface {
	Schema(context.Context, Principal, Binding) ([]Column, error)
	Query(context.Context, Principal, Binding, string) (QueryData, error)
}

type ControlledGateway struct {
	Endpoint  string
	AllowHTTP bool
	Transport http.RoundTripper
	mu        sync.Mutex
	next      time.Time
}

type resolvedBinding struct {
	ID             string    `json:"binding_id"`
	Revision       int64     `json:"revision"`
	Agent          string    `json:"agent_id"`
	Datasource     string    `json:"datasource_id"`
	Schema         string    `json:"schema"`
	Dialect        string    `json:"dialect"`
	Token          string    `json:"user_token"`
	Expires        time.Time `json:"expires_at"`
	NumericColumns []string  `json:"numeric_columns"`
}

func (gateway *ControlledGateway) resolve(ctx context.Context, p Principal, b Binding) (resolvedBinding, error) {
	if b.User != p.User.ID || b.Workspace != p.Workspace || !b.Datasource.Valid() || b.Name == "" || b.BindingID == "" || b.ConnectorToken == "" || b.AgentKey == "" || (b.Dialect != "pg" && b.Dialect != "mysql") {
		return resolvedBinding{}, ErrGateway
	}
	u, err := validateEndpoint(gateway.Endpoint, gateway.AllowHTTP)
	if err != nil || !strings.HasSuffix(u.Path, "/mcp") {
		return resolvedBinding{}, ErrGateway
	}
	resolveName := b.Name
	if b.ResolveName != "" {
		resolveName = b.ResolveName
	}
	payload, _ := json.Marshal(map[string]string{"binding_id": b.BindingID, "workspace_id": string(p.Workspace), "sqlbot_user_id": string(p.User.ID), "datasource_name": resolveName})
	u.Path = strings.TrimSuffix(u.Path, "/mcp") + "/api/v1/integrations/sqlbot/resolve"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return resolvedBinding{}, ErrGateway
	}
	req.Header.Set("Authorization", "Bearer "+b.ConnectorToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: gateway.Transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return resolvedBinding{}, ErrGateway
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return resolvedBinding{}, ErrGateway
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return resolvedBinding{}, ErrGateway
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data resolvedBinding `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return resolvedBinding{}, ErrGateway
	}
	r := envelope.Data
	if r.ID != b.BindingID || r.Revision < 1 || r.Agent == "" || r.Datasource == "" || r.Schema == "" || r.Dialect != b.Dialect || len(r.Token) > 16384 || !strings.HasPrefix(r.Token, "SQB1.") || !r.Expires.After(time.Now()) {
		return resolvedBinding{}, ErrGateway
	}
	return r, nil
}

type gatewayTransport struct {
	gateway         *ControlledGateway
	endpoint        *url.URL
	agentKey, token string
}

func (transport gatewayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != transport.endpoint.Scheme || request.URL.Host != transport.endpoint.Host || request.URL.Path != transport.endpoint.Path {
		return nil, ErrGateway
	}
	// Reuse the gateway's bounded rate budget across sessions. Respect cancellation
	// during pacing; never retry a database operation after a network failure.
	transport.gateway.mu.Lock()
	wait := time.Until(transport.gateway.next)
	if wait < 0 {
		wait = 0
	}
	transport.gateway.next = time.Now().Add(wait + 600*time.Millisecond)
	transport.gateway.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-timer.C:
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+transport.agentKey)
	clone.Header.Set("X-AgentSQL-User-Authorization", "Bearer "+transport.token)
	base := transport.gateway.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func (gateway *ControlledGateway) call(ctx context.Context, p Principal, b Binding, name string, arguments func(resolvedBinding) map[string]any) (json.RawMessage, resolvedBinding, error) {
	r, err := gateway.resolve(ctx, p, b)
	if err != nil {
		return nil, r, err
	}
	u, _ := url.Parse(gateway.Endpoint)
	client := mcp.NewClient(&mcp.Implementation{Name: "agentsql-chatbi", Version: "1"}, nil)
	httpClient := &http.Client{Transport: gatewayTransport{gateway, u, b.AgentKey, r.Token}, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: gateway.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1, MaxEventSize: 8 << 20}, nil)
	if err != nil {
		return nil, r, ErrGateway
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments(r)})
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		return nil, r, ErrGateway
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok || len(content.Text) > 8<<20 {
		return nil, r, ErrGateway
	}
	var envelope struct {
		Decision string          `json:"decision"`
		Data     json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(content.Text), &envelope) != nil || (envelope.Decision != "allow" && !(name == "query" && envelope.Decision == "warn")) || len(envelope.Data) == 0 {
		return nil, r, ErrGateway
	}
	return envelope.Data, r, nil
}

func (gateway *ControlledGateway) Schema(ctx context.Context, p Principal, b Binding) ([]Column, error) {
	raw, r, err := gateway.call(ctx, p, b, "list_schema", func(r resolvedBinding) map[string]any { return map[string]any{"datasource_id": r.Datasource} })
	if err != nil {
		return nil, err
	}
	var columns []Column
	if json.Unmarshal(raw, &columns) != nil || len(columns) > 5000 {
		return nil, ErrGateway
	}
	visible := make([]Column, 0, len(columns))
	for _, column := range columns {
		if column.Schema == r.Schema && column.Table != "" && column.Name != "" && column.DataType != "" {
			visible = append(visible, column)
		}
	}
	if len(visible) == 0 {
		return nil, ErrGateway
	}
	return filterManagedFileSchema(b, visible)
}

func (gateway *ControlledGateway) Query(ctx context.Context, p Principal, b Binding, query string) (QueryData, error) {
	if len(strings.TrimSpace(query)) == 0 || len(query) > 65536 {
		return QueryData{}, ErrGateway
	}
	if err := authorizeManagedFileSQL(b, query); err != nil {
		return QueryData{}, err
	}
	raw, _, err := gateway.call(ctx, p, b, "query", func(r resolvedBinding) map[string]any {
		return map[string]any{"datasource_id": r.Datasource, "sql": query}
	})
	if err != nil {
		return QueryData{}, err
	}
	var result struct {
		AuditID int64 `json:"audit_id"`
		Result  *struct {
			Columns   []string
			Rows      [][]string
			Truncated bool
		} `json:"result"`
	}
	if json.Unmarshal(raw, &result) != nil || result.AuditID <= 0 || result.Result == nil || len(result.Result.Columns) == 0 {
		return QueryData{}, ErrGateway
	}
	data := QueryData{Fields: result.Result.Columns, Data: make([]map[string]any, 0, len(result.Result.Rows)), FieldsInfo: make([]FieldInfo, 0, len(result.Result.Columns)), AuditID: result.AuditID, Truncated: result.Result.Truncated}
	seen := map[string]bool{}
	for _, name := range data.Fields {
		if name == "" || seen[name] {
			return QueryData{}, ErrGateway
		}
		seen[name] = true
		data.FieldsInfo = append(data.FieldsInfo, FieldInfo{Name: name})
	}
	for _, row := range result.Result.Rows {
		if len(row) != len(data.Fields) {
			return QueryData{}, ErrGateway
		}
		item := map[string]any{}
		for i, name := range data.Fields {
			item[name] = row[i]
		}
		data.Data = append(data.Data, item)
	}
	return data, nil
}
