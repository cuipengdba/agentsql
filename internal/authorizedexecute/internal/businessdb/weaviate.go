package businessdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/weaviate/weaviate-go-client/v4/weaviate"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/auth"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/filters"
	"github.com/weaviate/weaviate-go-client/v4/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"
)

type WeaviateExecutor struct {
	client   *weaviate.Client
	id       string
	readOnly bool
}

func weaviateConfig(ds model.Datasource, password string) (weaviate.Config, error) {
	if ds.DBType != "weaviate" || strings.TrimSpace(ds.Host) == "" {
		return weaviate.Config{}, fmt.Errorf("weaviate host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 8080
	}
	if port < 1 || port > 65535 {
		return weaviate.Config{}, fmt.Errorf("weaviate port is invalid")
	}
	if ds.Database != "" || ds.Username != "" {
		return weaviate.Config{}, fmt.Errorf("weaviate database and username are unsupported")
	}
	if ds.TrustServerCertificate || ds.TLSServerName != "" || ds.TLSCAFile != "" {
		return weaviate.Config{}, fmt.Errorf("custom or unverified weaviate TLS is unsupported")
	}
	scheme := "http"
	if ds.TLSMode != "" {
		if ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
			return weaviate.Config{}, fmt.Errorf("weaviate TLS mode is invalid")
		}
		scheme = "https"
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return weaviate.Config{}, err
	}
	cfg := weaviate.Config{Host: net.JoinHostPort(ds.Host, strconv.Itoa(port)), Scheme: scheme, Timeout: time.Duration(ms) * time.Millisecond, StartupTimeout: time.Duration(ms) * time.Millisecond}
	if password != "" {
		cfg.AuthConfig = auth.ApiKey{Value: password}
	}
	return cfg, nil
}
func NewWeaviateExecutor(ds model.Datasource, password string, readOnly bool) (*WeaviateExecutor, error) {
	cfg, err := weaviateConfig(ds, password)
	if err != nil {
		return nil, err
	}
	c, err := weaviate.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	e := &WeaviateExecutor{client: c, id: ds.ID, readOnly: readOnly}
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
func (e *WeaviateExecutor) Ping(ctx context.Context) error {
	live, err := e.client.Misc().LiveChecker().Do(ctx)
	if err != nil {
		return err
	}
	if !live {
		return fmt.Errorf("weaviate is not live")
	}
	return nil
}
func (e *WeaviateExecutor) ServerVersion(ctx context.Context) (string, error) {
	meta, err := e.client.Misc().MetaGetter().Do(ctx)
	if err != nil {
		return "", err
	}
	if meta == nil {
		return "", nil
	}
	return boundedNativeString(meta.Version), nil
}
func weaviateNamespace(c *models.Class) NativeNamespace {
	n := NativeNamespace{Name: boundedNativeString(c.Class), Kind: "class", Metadata: map[string]string{"vector_dimensions": "unavailable"}}
	n.Metadata["vectorizer"] = boundedNativeString(c.Vectorizer)
	props := []string{}
	for _, p := range c.Properties {
		if p != nil && len(props) < 100 {
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
	s, err := e.client.Schema().Getter().Do(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	if s == nil {
		return NativeCatalog{}, fmt.Errorf("weaviate schema is unavailable")
	}
	out := NativeCatalog{Category: model.CategoryVector, ServerVersion: v}
	for _, c := range s.Classes {
		if len(out.Namespaces) >= nativeMaxRows {
			break
		}
		if c != nil && c.Class != "" {
			out.Namespaces = append(out.Namespaces, weaviateNamespace(c))
		}
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

func weaviateFields(c *models.Class) []graphql.Field {
	fields := []graphql.Field{{Name: "_additional", Fields: []graphql.Field{{Name: "id"}}}}
	for _, p := range c.Properties {
		if p != nil && vectorName.MatchString(p.Name) && len(fields) < 101 && len(p.DataType) > 0 && (p.DataType[0] == "text" || p.DataType[0] == "string" || p.DataType[0] == "int" || p.DataType[0] == "number" || p.DataType[0] == "boolean" || p.DataType[0] == "date") {
			fields = append(fields, graphql.Field{Name: p.Name})
		}
	}
	return fields
}
func weaviateRows(resp *models.GraphQLResponse, class string) (NativeQueryResult, error) {
	if resp == nil || len(resp.Errors) > 0 {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL query failed")
	}
	b, err := json.Marshal(resp.Data)
	if err != nil {
		return NativeQueryResult{}, err
	}
	var payload map[string]map[string][]json.RawMessage
	if err = json.Unmarshal(b, &payload); err != nil {
		return NativeQueryResult{}, err
	}
	classes, ok := payload["Get"]
	if !ok {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL result is incomplete")
	}
	items, ok := classes[class]
	if !ok {
		return NativeQueryResult{}, fmt.Errorf("weaviate GraphQL class result is missing")
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
	c, err := e.client.Schema().ClassGetter().WithClassName(req.Namespace).Do(ctx)
	if err != nil {
		return result, err
	}
	if c == nil {
		return result, fmt.Errorf("weaviate class is unavailable")
	}
	switch command {
	case "DESCRIBE":
		if len(args) != 0 {
			return result, fmt.Errorf("weaviate describe takes no arguments")
		}
		n := weaviateNamespace(c)
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
		builder := e.client.GraphQL().Get().WithClassName(req.Namespace).WithFields(weaviateFields(c)...).WithLimit(limit)
		switch strings.ToUpper(args[0]) {
		case "NEARVECTOR":
			vec, x := parseFloatVector(args[1])
			if x != nil {
				return result, x
			}
			builder = builder.WithNearVector(e.client.GraphQL().NearVectorArgBuilder().WithVector(vec))
		case "NEARTEXT":
			if strings.TrimSpace(args[1]) == "" || strings.ContainsAny(args[1], ";\r\n") {
				return result, fmt.Errorf("weaviate nearText is invalid")
			}
			builder = builder.WithNearText(e.client.GraphQL().NearTextArgBuilder().WithConcepts([]string{args[1]}))
		default:
			return result, fmt.Errorf("weaviate search mode is invalid")
		}
		resp, x := builder.Do(ctx)
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
			if p != nil && p.Name == args[0] && len(p.DataType) > 0 && (p.DataType[0] == "text" || p.DataType[0] == "string") {
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
		builder := e.client.GraphQL().Get().WithClassName(req.Namespace).WithFields(weaviateFields(c)...).WithWhere(filters.Where().WithPath([]string{args[0]}).WithOperator(filters.Equal).WithValueString(args[1])).WithLimit(limit)
		resp, x := builder.Do(ctx)
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
			created, x := e.client.Data().Creator().WithClassName(req.Namespace).WithProperties(o).Do(ctx)
			if x != nil {
				return result, x
			}
			if created == nil || created.Object == nil {
				return result, fmt.Errorf("weaviate create result is incomplete")
			}
			rows = append(rows, []string{string(created.Object.ID)})
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
		err = e.client.Data().Updater().WithClassName(req.Namespace).WithID(args[0]).WithProperties(o).WithMerge().Do(ctx)
		if err != nil {
			return result, err
		}
		return boundedNativeRows([]string{"updated"}, [][]string{{args[0]}}), nil
	}
	return result, fmt.Errorf("weaviate command is not allowed")
}
