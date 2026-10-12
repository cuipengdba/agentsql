package businessdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type WeaviateExecutor struct {
	baseURL  string
	client   *http.Client
	apiKey   string
	id       string
	readOnly bool
}

type weaviateHTTPConfig struct {
	Host    string
	Scheme  string
	Timeout time.Duration
	APIKey  string
}

type weaviateClass struct {
	Class      string             `json:"class"`
	Vectorizer string             `json:"vectorizer"`
	Properties []weaviateProperty `json:"properties"`
}

type weaviateProperty struct {
	Name     string   `json:"name"`
	DataType []string `json:"dataType"`
}

type weaviateMeta struct {
	Version string `json:"version"`
}

type weaviateGraphQLResponse struct {
	Data   json.RawMessage   `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type weaviateCreatedObject struct {
	ID string `json:"id"`
}

func weaviateConfig(ds model.Datasource, password string) (weaviateHTTPConfig, error) {
	if ds.DBType != "weaviate" || strings.TrimSpace(ds.Host) == "" {
		return weaviateHTTPConfig{}, fmt.Errorf("weaviate host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 8080
	}
	if port < 1 || port > 65535 {
		return weaviateHTTPConfig{}, fmt.Errorf("weaviate port is invalid")
	}
	if ds.Database != "" || ds.Username != "" {
		return weaviateHTTPConfig{}, fmt.Errorf("weaviate database and username are unsupported")
	}
	if ds.TrustServerCertificate || ds.TLSServerName != "" || ds.TLSCAFile != "" {
		return weaviateHTTPConfig{}, fmt.Errorf("custom or unverified weaviate TLS is unsupported")
	}
	scheme := "http"
	if ds.TLSMode != "" {
		if ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
			return weaviateHTTPConfig{}, fmt.Errorf("weaviate TLS mode is invalid")
		}
		scheme = "https"
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return weaviateHTTPConfig{}, err
	}
	return weaviateHTTPConfig{Host: net.JoinHostPort(ds.Host, strconv.Itoa(port)), Scheme: scheme, Timeout: time.Duration(ms) * time.Millisecond, APIKey: password}, nil
}
func NewWeaviateExecutor(ds model.Datasource, password string, readOnly bool) (*WeaviateExecutor, error) {
	cfg, err := weaviateConfig(ds, password)
	if err != nil {
		return nil, err
	}
	e := &WeaviateExecutor{baseURL: cfg.Scheme + "://" + cfg.Host, client: &http.Client{Timeout: cfg.Timeout}, apiKey: cfg.APIKey, id: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (*WeaviateExecutor) Category() model.DatasourceCategory { return model.CategoryVector }
func (*WeaviateExecutor) Dialect() model.DBDialect           { return "weaviate" }
func (*WeaviateExecutor) Close() error                       { return nil }
func (e *WeaviateExecutor) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		summary := boundedNativeString(strings.TrimSpace(string(b)))
		if summary == "" {
			return nil, resp.StatusCode, fmt.Errorf("weaviate HTTP %d", resp.StatusCode)
		}
		return nil, resp.StatusCode, fmt.Errorf("weaviate HTTP %d: %s", resp.StatusCode, summary)
	}
	return b, resp.StatusCode, nil
}
func (e *WeaviateExecutor) Ping(ctx context.Context) error {
	b, status, err := e.do(ctx, http.MethodGet, "/v1/.well-known/live", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK || strings.TrimSpace(string(b)) == "false" {
		return fmt.Errorf("weaviate is not live")
	}
	return nil
}
func (e *WeaviateExecutor) ServerVersion(ctx context.Context) (string, error) {
	b, _, err := e.do(ctx, http.MethodGet, "/v1/meta", nil)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return "", fmt.Errorf("weaviate meta is unavailable")
	}
	var meta weaviateMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return "", err
	}
	return boundedNativeString(meta.Version), nil
}
func weaviateNamespace(c *weaviateClass) NativeNamespace {
	n := NativeNamespace{Name: boundedNativeString(c.Class), Kind: "class", Metadata: map[string]string{"vector_dimensions": "unavailable"}}
	n.Metadata["vectorizer"] = boundedNativeString(c.Vectorizer)
	props := []string{}
	for _, p := range c.Properties {
		if len(props) < 100 {
			props = append(props, p.Name)
		}
	}
	n.Metadata["properties"] = boundedNativeString(strings.Join(props, ","))
	return n
}
func (e *WeaviateExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	b, _, err := e.do(ctx, http.MethodGet, "/v1/schema", nil)
	if err != nil {
		return NativeCatalog{}, err
	}
	var s struct {
		Classes []weaviateClass `json:"classes"`
	}
	if err := json.Unmarshal(b, &s); err != nil || len(s.Classes) == 0 {
		return NativeCatalog{}, fmt.Errorf("weaviate schema is unavailable")
	}
	out := NativeCatalog{Category: model.CategoryVector, ServerVersion: v}
	for _, c := range s.Classes {
		if len(out.Namespaces) >= nativeMaxRows {
			break
		}
		if c.Class != "" {
			out.Namespaces = append(out.Namespaces, weaviateNamespace(&c))
		}
	}
	if len(out.Namespaces) == 0 {
		return NativeCatalog{}, fmt.Errorf("weaviate schema is unavailable")
	}
	return out, nil
}
func weaviateCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "SEARCH", "GET", "DESCRIBE", "STATUS", "HEALTH":
		return nil
	case "INSERT", "UPDATE":
		if !readOnly {
			return nil
		}
		return fmt.Errorf("weaviate write forbidden in read-only mode")
	default:
		return fmt.Errorf("weaviate command %s is not allowed", command)
	}
}

var weaviateUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func weaviateFields(c *weaviateClass) []string {
	fields := []string{}
	for _, p := range c.Properties {
		if vectorName.MatchString(p.Name) && len(fields) < 100 && len(p.DataType) > 0 && (p.DataType[0] == "text" || p.DataType[0] == "string" || p.DataType[0] == "int" || p.DataType[0] == "number" || p.DataType[0] == "boolean" || p.DataType[0] == "date") {
			fields = append(fields, p.Name)
		}
	}
	return append(fields, "_additional{id}")
}
func weaviateRows(resp *weaviateGraphQLResponse, class string) (NativeQueryResult, error) {
	if resp == nil || len(resp.Errors) > 0 {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL query failed")
	}
	var payload map[string]map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &payload); err != nil {
		return NativeQueryResult{}, err
	}
	classes, ok := payload["Get"]
	if !ok {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL result is incomplete")
	}
	rawItems, ok := classes[class]
	if !ok {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL class result is missing")
	}
	var items []json.RawMessage
	if rawItems = bytes.TrimSpace(rawItems); len(rawItems) == 0 || rawItems[0] != '[' || json.Unmarshal(rawItems, &items) != nil {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL class result is invalid")
	}
	rows := [][]string{}
	for _, item := range items {
		if len(rows) >= 100 {
			break
		}
		rows = append(rows, []string{string(item)})
	}
	return boundedNativeRows([]string{"object"}, rows), nil
}
func weaviateGraphQLString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
func (e *WeaviateExecutor) query(ctx context.Context, query string) (*weaviateGraphQLResponse, error) {
	b, _, err := e.do(ctx, http.MethodPost, "/v1/graphql", map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	var resp weaviateGraphQLResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
func (e *WeaviateExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.id, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if err = weaviateCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if command == "STATUS" || command == "HEALTH" {
		if req.Namespace != "" || len(args) != 0 {
			return result, fmt.Errorf("weaviate health takes no arguments")
		}
		err = e.Ping(ctx)
		if err != nil {
			return result, err
		}
		return boundedNativeRows([]string{"status"}, [][]string{{"live"}}), nil
	}
	if err = vectorCollection(req.Namespace); err != nil {
		return result, err
	}
	b, _, err := e.do(ctx, http.MethodGet, "/v1/schema/"+req.Namespace, nil)
	if err != nil {
		return result, err
	}
	var c weaviateClass
	if json.Unmarshal(b, &c) != nil || c.Class == "" || c.Class != req.Namespace {
		return result, fmt.Errorf("weaviate class is unavailable")
	}
	switch command {
	case "DESCRIBE":
		if len(args) != 0 {
			return result, fmt.Errorf("weaviate describe takes no arguments")
		}
		n := weaviateNamespace(&c)
		b, _ := json.Marshal(n.Metadata)
		return boundedNativeRows([]string{"class", "metadata"}, [][]string{{n.Name, string(b)}}), nil
	case "SEARCH":
		if len(args) != 3 {
			return result, fmt.Errorf("weaviate search requires nearVector/nearText, value, limit")
		}
		limit, x := vectorLimit(args[2])
		if x != nil {
			return result, x
		}
		clause := ""
		switch strings.ToUpper(args[0]) {
		case "NEARVECTOR":
			vec, x := parseFloatVector(args[1])
			if x != nil {
				return result, x
			}
			vectorJSON, x := json.Marshal(vec)
			if x != nil {
				return result, x
			}
			clause = ",nearVector:{vector:" + string(vectorJSON) + "}"
		case "NEARTEXT":
			if strings.TrimSpace(args[1]) == "" || strings.ContainsAny(args[1], ";\r\n") {
				return result, fmt.Errorf("weaviate nearText is invalid")
			}
			clause = ",nearText:{concepts:[" + weaviateGraphQLString(args[1]) + "]}"
		default:
			return result, fmt.Errorf("weaviate search mode is invalid")
		}
		query := fmt.Sprintf("{Get{%s(limit:%d%s){%s}}}", req.Namespace, limit, clause, strings.Join(weaviateFields(&c), " "))
		resp, x := e.query(ctx, query)
		if x != nil {
			return result, x
		}
		return weaviateRows(resp, req.Namespace)
	case "GET":
		if len(args) != 3 {
			return result, fmt.Errorf("weaviate get requires property, value, limit")
		}
		if !vectorName.MatchString(args[0]) {
			return result, fmt.Errorf("weaviate filter property is invalid")
		}
		found := false
		for _, p := range c.Properties {
			if p.Name == args[0] && len(p.DataType) > 0 && (p.DataType[0] == "text" || p.DataType[0] == "string") {
				found = true
			}
		}
		if !found {
			return result, fmt.Errorf("weaviate filter requires a text property")
		}
		limit, x := vectorLimit(args[2])
		if x != nil {
			return result, x
		}
		clause := ",where:{path:[" + weaviateGraphQLString(args[0]) + "],operator:Equal,valueText:" + weaviateGraphQLString(args[1]) + "}"
		query := fmt.Sprintf("{Get{%s(limit:%d%s){%s}}}", req.Namespace, limit, clause, strings.Join(weaviateFields(&c), " "))
		resp, x := e.query(ctx, query)
		if x != nil {
			return result, x
		}
		return weaviateRows(resp, req.Namespace)
	case "INSERT":
		if len(args) != 1 {
			return result, fmt.Errorf("weaviate insert requires JSON objects")
		}
		var objects []map[string]any
		if json.Unmarshal([]byte(args[0]), &objects) != nil || len(objects) == 0 || len(objects) > 100 {
			return result, fmt.Errorf("weaviate insert requires 1..100 JSON objects")
		}
		rows := [][]string{}
		for _, o := range objects {
			if len(o) == 0 {
				return result, fmt.Errorf("weaviate object is empty")
			}
			b, _, x := e.do(ctx, http.MethodPost, "/v1/objects", map[string]any{"class": req.Namespace, "properties": o})
			if x != nil {
				return result, x
			}
			var created weaviateCreatedObject
			if json.Unmarshal(b, &created) != nil || created.ID == "" {
				return result, fmt.Errorf("weaviate create result is incomplete")
			}
			rows = append(rows, []string{created.ID})
		}
		return boundedNativeRows([]string{"id"}, rows), nil
	case "UPDATE":
		if len(args) != 2 || !weaviateUUID.MatchString(args[0]) {
			return result, fmt.Errorf("weaviate update requires UUID and JSON properties")
		}
		var o map[string]any
		if json.Unmarshal([]byte(args[1]), &o) != nil || len(o) == 0 {
			return result, fmt.Errorf("weaviate update properties are invalid")
		}
		_, status, err := e.do(ctx, http.MethodPatch, "/v1/objects/"+req.Namespace+"/"+args[0], map[string]any{"properties": o})
		if err != nil {
			return result, err
		}
		if status != http.StatusNoContent {
			return result, fmt.Errorf("weaviate update returned HTTP %d, expected 204", status)
		}
		return boundedNativeRows([]string{"updated"}, [][]string{{args[0]}}), nil
	}
	return result, fmt.Errorf("weaviate command is not allowed")
}
