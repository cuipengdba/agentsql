package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

type NativeBackend interface {
	NativeQueryRead(context.Context, model.Datasource, executor.NativeQueryRequest) (executor.NativeQueryResult, error)
	NativeDiscover(context.Context, model.Datasource) (executor.NativeCatalog, error)
	NativeServerVersion(context.Context, model.Datasource) (string, error)
}

const nativeResponseBytes = 64 << 10

// The registry currently has no enumeration API. Keep this key list checked
// against TypeSpecFor so metadata continues to come from the model registry.
var datasourceTypeKeys = []string{
	"postgres", "mysql", "dm", "oracle", "yashan", "sqlserver",
	"redis", "valkey", "memcached", "mongodb", "couchbase", "couchdb",
	"cassandra", "scylla", "hbase", "neo4j", "janusgraph", "nebula",
	"influxdb", "prometheus", "timescaledb", "clickhouse", "doris", "starrocks",
	"milvus", "qdrant", "weaviate",
}

type datasourceTypeView struct {
	Key              string                   `json:"key"`
	DisplayName      string                   `json:"display_name"`
	Category         model.DatasourceCategory `json:"category"`
	DefaultPort      int                      `json:"default_port"`
	Kind             string                   `json:"kind"`
	Capability       string                   `json:"capability"`
	RequiresDatabase bool                     `json:"requires_database"`
	RequiresUsername bool                     `json:"requires_username"`
	RequiresPassword bool                     `json:"requires_password"`
}

func (handler *Handler) datasourceTypesList(writer http.ResponseWriter, _ *http.Request) {
	views := make([]datasourceTypeView, 0, len(datasourceTypeKeys))
	for _, key := range datasourceTypeKeys {
		spec, ok := model.TypeSpecFor(key)
		if !ok {
			handler.fail(writer, http.StatusServiceUnavailable, "datasource type registry is unavailable")
			return
		}
		kind, capability := "nosql", "nosql-connect"
		if !model.IsNative(key) {
			kind, capability = "relational", "relational"
		}
		views = append(views, datasourceTypeView{Key: key, DisplayName: spec.DisplayName,
			Category: spec.Category, DefaultPort: spec.DefaultPort, Kind: kind, Capability: capability,
			RequiresDatabase: spec.RequiresDatabase, RequiresUsername: spec.RequiresUsername,
			RequiresPassword: spec.RequiresPassword})
	}
	handler.ok(writer, views)
}

func (handler *Handler) nativeDatasource(writer http.ResponseWriter, request *http.Request) (model.Datasource, bool) {
	id := request.PathValue("id")
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "\x00\r\n") {
		handler.fail(writer, http.StatusBadRequest, "invalid datasource ID")
		return model.Datasource{}, false
	}
	datasource, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handler.notFound(writer)
		} else {
			handler.internal(writer, err)
		}
		return model.Datasource{}, false
	}
	if !model.IsNative(datasource.DBType) {
		handler.fail(writer, http.StatusBadRequest, "relational datasource: use the existing SQL or ping routes")
		return model.Datasource{}, false
	}
	return datasource, true
}

type nativePingView struct {
	OK           bool    `json:"ok"`
	LatencyMS    int64   `json:"latency_ms"`
	Version      string  `json:"version"`
	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

func (handler *Handler) nativePing(writer http.ResponseWriter, request *http.Request) {
	datasource, ok := handler.nativeDatasource(writer, request)
	if !ok {
		return
	}
	started := time.Now()
	result, err := handler.deps.DatasourcePinger.Ping(request.Context(), datasource)
	latencyMS := result.LatencyMS
	if latencyMS <= 0 {
		latencyMS = time.Since(started).Milliseconds()
	}
	view := nativePingView{OK: result.OK && err == nil, LatencyMS: latencyMS,
		ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage}
	if err != nil {
		// Adapter errors can contain credentials or hostnames. Return a fixed message.
		view.OK = false
		view.ErrorCode, view.ErrorMessage = stringPointer("NATIVE_PING_FAILED"), stringPointer("native connection test failed")
	} else if view.OK && handler.deps.Native != nil {
		if version, versionErr := handler.deps.Native.NativeServerVersion(request.Context(), datasource); versionErr == nil {
			view.Version = nativeShorten(version, 256)
		}
	}
	handler.ok(writer, view)
}

type nativeQueryInput struct {
	Namespace string   `json:"namespace"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	ReadOnly  *bool    `json:"read_only"`
}

func (handler *Handler) nativeQuery(writer http.ResponseWriter, request *http.Request) {
	datasource, ok := handler.nativeDatasource(writer, request)
	if !ok {
		return
	}
	var input nativeQueryInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.fail(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	command := strings.ToUpper(strings.TrimSpace(input.Command))
	if command == "" || len(command) > 32 || len(input.Namespace) > 256 || strings.ContainsAny(input.Namespace, "\x00\r\n") || len(input.Args) > 32 {
		handler.fail(writer, http.StatusUnprocessableEntity, "invalid native query scope")
		return
	}
	for _, letter := range command {
		if letter != '_' && letter != ' ' && (letter < 'A' || letter > 'Z') {
			handler.fail(writer, http.StatusUnprocessableEntity, "invalid native command")
			return
		}
	}
	for _, arg := range input.Args {
		if len(arg) > 4096 {
			handler.fail(writer, http.StatusUnprocessableEntity, "native argument exceeds size limit")
			return
		}
	}
	nativeRequest := executor.NativeQueryRequest{Namespace: input.Namespace, Command: command, Args: input.Args}
	access, err := executor.ClassifyNativeCommand(datasource.DBType, nativeRequest)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "native command is not allowed")
		return
	}
	if access == "write" || input.ReadOnly != nil && !*input.ReadOnly {
		if !handler.recordNativeQueryDecision(writer, request, datasource.ID, command, len(input.Args), "deny", "write_rejected") {
			return
		}
		handler.fail(writer, http.StatusForbidden, "native writes require an approved execution path")
		return
	}
	if handler.deps.Native == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "native query preview is unavailable")
		return
	}
	if !handler.recordNativeQueryDecision(writer, request, datasource.ID, command, len(input.Args), "allow", "preview") {
		return
	}
	result, err := handler.deps.Native.NativeQueryRead(request.Context(), datasource, nativeRequest)
	if err != nil {
		handler.fail(writer, http.StatusUnprocessableEntity, "native query was rejected or failed")
		return
	}
	handler.ok(writer, boundedNativeQuery(result))
}

func (handler *Handler) recordNativeQueryDecision(writer http.ResponseWriter, request *http.Request, datasourceID, command string, argumentCount int, decision, result string) bool {
	// Record only the decision and command; never persist arguments or values.
	if handler.deps.Runtime.ManagementAudit == nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return false
	}
	action, actorType, actorID, id := "native.query", "admin", handler.adminUser, datasourceID
	details, _ := json.Marshal(map[string]any{"command": command, "read_only": decision == "allow",
		"argument_count": argumentCount, "result": result})
	detailText := string(details)
	_, err := handler.deps.Runtime.ManagementAudit.Record(request.Context(), model.AuditLog{
		DatasourceID: &id, Decision: decision, Action: &action, ActorType: &actorType,
		ActorID: &actorID, DetailsJSON: &detailText,
	})
	if err != nil {
		handler.fail(writer, http.StatusServiceUnavailable, "audit unavailable")
		return false
	}
	return true
}

func (handler *Handler) nativeSchema(writer http.ResponseWriter, request *http.Request) {
	datasource, ok := handler.nativeDatasource(writer, request)
	if !ok {
		return
	}
	if handler.deps.Native == nil {
		handler.nativeCapabilityError(writer, http.StatusServiceUnavailable, "NATIVE_SCHEMA_UNAVAILABLE", "native schema discovery is unavailable")
		return
	}
	catalog, err := handler.deps.Native.NativeDiscover(request.Context(), datasource)
	if err != nil {
		if errors.Is(err, executor.ErrNativeUnsupported) {
			handler.nativeCapabilityError(writer, http.StatusNotImplemented, "NATIVE_SCHEMA_UNSUPPORTED", "native schema discovery is unsupported for this datasource")
		} else {
			handler.nativeCapabilityError(writer, http.StatusBadGateway, "NATIVE_SCHEMA_FAILED", "native schema discovery failed")
		}
		return
	}
	handler.ok(writer, boundedNativeCatalog(catalog))
}

func (handler *Handler) nativeCapabilityError(writer http.ResponseWriter, status int, code, message string) {
	handler.write(writer, status, status, message, map[string]string{"error_code": code})
}

type nativeQueryView struct {
	Columns   []string          `json:"columns"`
	Rows      [][]string        `json:"rows"`
	Info      map[string]string `json:"info,omitempty"`
	Raw       string            `json:"raw,omitempty"`
	Truncated bool              `json:"truncated"`
}

func nativeShorten(value string, maxBytes int) string {
	if len(value) > maxBytes {
		value = value[:maxBytes]
	}
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func boundedNativeQuery(result executor.NativeQueryResult) nativeQueryView {
	view := nativeQueryView{Columns: []string{}, Rows: [][]string{}, Info: map[string]string{}, Raw: nativeShorten(result.Raw, 4096)}
	view.Truncated = len(view.Raw) < len(result.Raw)
	for i, column := range result.Columns {
		if i == 32 {
			view.Truncated = true
			break
		}
		short := nativeShorten(column, 256)
		view.Truncated = view.Truncated || len(short) < len(column)
		view.Columns = append(view.Columns, short)
	}
	for key, value := range result.Info {
		if len(view.Info) == 16 {
			view.Truncated = true
			break
		}
		shortKey, shortValue := nativeShorten(key, 128), nativeShorten(value, 512)
		view.Truncated = view.Truncated || len(shortKey) < len(key) || len(shortValue) < len(value)
		view.Info[shortKey] = shortValue
	}
	for i, row := range result.Rows {
		if i == 100 {
			view.Truncated = true
			break
		}
		bounded := make([]string, 0, min(len(row), 32))
		for j, cell := range row {
			if j == 32 {
				view.Truncated = true
				break
			}
			short := nativeShorten(cell, 4096)
			view.Truncated = view.Truncated || len(short) < len(cell)
			bounded = append(bounded, short)
		}
		view.Rows = append(view.Rows, bounded)
	}
	for {
		encoded, _ := json.Marshal(view)
		if len(encoded) <= nativeResponseBytes {
			break
		}
		view.Truncated = true
		if len(view.Rows) > 0 {
			view.Rows = view.Rows[:len(view.Rows)-1]
			continue
		}
		if len(view.Raw) > 0 {
			view.Raw = nativeShorten(view.Raw, len(view.Raw)/2)
			continue
		}
		if len(view.Info) > 0 {
			for key := range view.Info {
				delete(view.Info, key)
				break
			}
			continue
		}
		if len(view.Columns) > 0 {
			view.Columns = view.Columns[:len(view.Columns)-1]
			continue
		}
		break
	}
	return view
}

type nativeSchemaView struct {
	Category      model.DatasourceCategory `json:"category"`
	ServerVersion string                   `json:"server_version"`
	Namespaces    []nativeNamespaceView    `json:"namespaces"`
	Truncated     bool                     `json:"truncated"`
}
type nativeNamespaceView struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	ItemCount int64             `json:"item_count"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func boundedNativeCatalog(catalog executor.NativeCatalog) nativeSchemaView {
	view := nativeSchemaView{Category: catalog.Category, ServerVersion: nativeShorten(catalog.ServerVersion, 256), Namespaces: []nativeNamespaceView{}}
	view.Truncated = len(view.ServerVersion) < len(catalog.ServerVersion)
	for i, item := range catalog.Namespaces {
		if i == 100 {
			view.Truncated = true
			break
		}
		name, kind := nativeShorten(item.Name, 256), nativeShorten(item.Kind, 64)
		view.Truncated = view.Truncated || len(name) < len(item.Name) || len(kind) < len(item.Kind)
		entry := nativeNamespaceView{Name: name, Kind: kind, ItemCount: item.ItemCount, Metadata: map[string]string{}}
		for key, value := range item.Metadata {
			if len(entry.Metadata) == 16 {
				view.Truncated = true
				break
			}
			shortKey, shortValue := nativeShorten(key, 128), nativeShorten(value, 512)
			view.Truncated = view.Truncated || len(shortKey) < len(key) || len(shortValue) < len(value)
			entry.Metadata[shortKey] = shortValue
		}
		view.Namespaces = append(view.Namespaces, entry)
	}
	for {
		encoded, _ := json.Marshal(view)
		if len(encoded) <= nativeResponseBytes {
			break
		}
		view.Truncated = true
		if len(view.Namespaces) > 0 {
			view.Namespaces = view.Namespaces[:len(view.Namespaces)-1]
			continue
		}
		break
	}
	return view
}
