package businessdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// qdrantAPI contains only the SDK operations exposed by this adapter.
type qdrantAPI interface {
	HealthCheck(context.Context) (*qdrant.HealthCheckReply, error)
	ListCollections(context.Context) ([]string, error)
	GetCollectionInfo(context.Context, string) (*qdrant.CollectionInfo, error)
	Query(context.Context, *qdrant.QueryPoints) ([]*qdrant.ScoredPoint, error)
	ScrollAndOffset(context.Context, *qdrant.ScrollPoints) ([]*qdrant.RetrievedPoint, *qdrant.PointId, error)
	Upsert(context.Context, *qdrant.UpsertPoints) (*qdrant.UpdateResult, error)
	Close() error
}

type QdrantExecutor struct {
	client   qdrantAPI
	id       string
	readOnly bool
}

func qdrantConfig(ds model.Datasource, apiKey string) (*qdrant.Config, error) {
	if ds.DBType != "qdrant" || strings.TrimSpace(ds.Host) == "" {
		return nil, fmt.Errorf("qdrant host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 6334
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("qdrant port is invalid")
	}
	if ds.Username != "" || ds.Database != "" {
		return nil, fmt.Errorf("qdrant username and database settings are unsupported")
	}
	if ds.TrustServerCertificate || ds.TLSServerName != "" || ds.TLSCAFile != "" || ds.TLSMode != "" {
		return nil, fmt.Errorf("qdrant TLS settings are unsupported")
	}
	// SDK v1.19.3 uses Config.APIKey, which supplies its gRPC api-key header.
	return &qdrant.Config{Host: ds.Host, Port: port, APIKey: apiKey, PoolSize: 1, SkipCompatibilityCheck: true}, nil
}

func NewQdrantExecutor(ds model.Datasource, password string, readOnly bool) (*QdrantExecutor, error) {
	cfg, err := qdrantConfig(ds, password)
	if err != nil {
		return nil, err
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	client, err := qdrant.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	e := &QdrantExecutor{client: client, id: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ms)*time.Millisecond)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

func (*QdrantExecutor) Category() model.DatasourceCategory { return model.CategoryVector }
func (*QdrantExecutor) Dialect() model.DBDialect           { return "qdrant" }
func (e *QdrantExecutor) Close() error                     { return e.client.Close() }
func (e *QdrantExecutor) Ping(ctx context.Context) error {
	reply, err := e.client.HealthCheck(ctx)
	if err != nil {
		return err
	}
	if reply == nil {
		return fmt.Errorf("qdrant health response is unavailable")
	}
	return nil
}
func (e *QdrantExecutor) ServerVersion(ctx context.Context) (string, error) {
	reply, err := e.client.HealthCheck(ctx)
	if err != nil {
		return "", err
	}
	if reply == nil {
		return "", fmt.Errorf("qdrant service info is unavailable")
	}
	// A healthy older server may omit the version; retain an empty value then.
	return boundedNativeString(reply.GetVersion()), nil
}

func qdrantNamespace(name string, info *qdrant.CollectionInfo) (NativeNamespace, error) {
	if info == nil {
		return NativeNamespace{}, fmt.Errorf("qdrant collection metadata is unavailable")
	}
	n := NativeNamespace{Name: boundedNativeString(name), Kind: "collection", Metadata: map[string]string{"vector_dimensions": "unavailable", "points_count": "unavailable"}}
	if info.PointsCount != nil {
		n.Metadata["points_count"] = strconv.FormatUint(info.GetPointsCount(), 10)
		if info.GetPointsCount() > math.MaxInt64 {
			n.ItemCount = math.MaxInt64
		} else {
			n.ItemCount = int64(info.GetPointsCount())
		}
	}
	if vectors := info.GetConfig().GetParams().GetVectorsConfig(); vectors != nil {
		if params := vectors.GetParams(); params != nil {
			n.Metadata["vector_dimensions"] = strconv.FormatUint(params.GetSize(), 10)
		} else if named := vectors.GetParamsMap(); named != nil {
			keys := make([]string, 0, len(named.GetMap()))
			for key := range named.GetMap() {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			dims := make([]string, 0, min(len(keys), 100))
			for _, key := range keys {
				if len(dims) == 100 {
					break
				}
				if params := named.GetMap()[key]; params != nil {
					dims = append(dims, key+":"+strconv.FormatUint(params.GetSize(), 10))
				}
			}
			n.Metadata["vector_dimensions"] = boundedNativeString(strings.Join(dims, ","))
		}
	}
	return n, nil
}

func (e *QdrantExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	names, err := e.client.ListCollections(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	out := NativeCatalog{Category: model.CategoryVector, ServerVersion: version}
	for _, name := range names {
		if len(out.Namespaces) == nativeMaxRows {
			break
		}
		if name == "" {
			continue
		}
		info, err := e.client.GetCollectionInfo(ctx, name)
		if err != nil {
			return NativeCatalog{}, err
		}
		n, err := qdrantNamespace(name, info)
		if err != nil {
			return NativeCatalog{}, err
		}
		out.Namespaces = append(out.Namespaces, n)
	}
	return out, nil
}

func qdrantCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "SEARCH", "GET", "DESCRIBE", "SCROLL", "STATUS", "HEALTH":
		return nil
	case "UPSERT":
		if !readOnly {
			return nil
		}
		return fmt.Errorf("qdrant write forbidden in read-only mode")
	default:
		return fmt.Errorf("qdrant command %s is not allowed", command)
	}
}

func qdrantVector(input string) ([]float32, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(input), &raw); err != nil || len(raw) < 1 || len(raw) > 2048 {
		return nil, fmt.Errorf("qdrant vector must contain 1..2048 numbers")
	}
	values := make([]float32, 0, len(raw))
	for _, item := range raw {
		if len(item) == 0 || (item[0] != '-' && (item[0] < '0' || item[0] > '9')) {
			return nil, fmt.Errorf("qdrant vector values must be numbers")
		}
		var value float32
		if err := json.Unmarshal(item, &value); err != nil {
			return nil, fmt.Errorf("qdrant vector values must be numbers")
		}
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("qdrant vector values must be finite")
		}
		values = append(values, value)
	}
	return values, nil
}

func qdrantPointID(input string) (*qdrant.PointId, error) {
	if input == "" || strings.TrimSpace(input) != input {
		return nil, fmt.Errorf("qdrant cursor or point ID is invalid")
	}
	if id, err := strconv.ParseUint(input, 10, 64); err == nil {
		return qdrant.NewIDNum(id), nil
	}
	id, err := uuid.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("qdrant cursor or point ID is invalid")
	}
	return qdrant.NewIDUUID(id.String()), nil
}

func qdrantIDString(id *qdrant.PointId) string {
	if id == nil {
		return "unavailable"
	}
	switch id.GetPointIdOptions().(type) {
	case *qdrant.PointId_Num:
		return strconv.FormatUint(id.GetNum(), 10)
	case *qdrant.PointId_Uuid:
		return boundedNativeString(id.GetUuid())
	default:
		return "unavailable"
	}
}

func qdrantWritePoints(input string) ([]*qdrant.PointStruct, error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &raw); err != nil || len(raw) < 1 || len(raw) > 100 {
		return nil, fmt.Errorf("qdrant upsert requires 1..100 JSON points")
	}
	points := make([]*qdrant.PointStruct, 0, len(raw))
	for _, row := range raw {
		if len(row) != 2 || row["id"] == nil || row["vector"] == nil {
			return nil, fmt.Errorf("qdrant point requires only id and vector")
		}
		for key := range row {
			if key != "id" && key != "vector" {
				return nil, fmt.Errorf("qdrant point field is unsupported")
			}
		}
		var idText string
		if row["id"][0] == '"' {
			if err := json.Unmarshal(row["id"], &idText); err != nil {
				return nil, fmt.Errorf("qdrant point ID is invalid")
			}
		} else {
			idText = string(row["id"])
		}
		id, err := qdrantPointID(idText)
		if err != nil {
			return nil, err
		}
		vector, err := qdrantVector(string(row["vector"]))
		if err != nil {
			return nil, err
		}
		points = append(points, &qdrant.PointStruct{Id: id, Vectors: qdrant.NewVectors(vector...)})
	}
	return points, nil
}

func (e *QdrantExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.id, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if err = qdrantCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if command == "STATUS" || command == "HEALTH" {
		if req.Namespace != "" || len(args) != 0 {
			return result, fmt.Errorf("qdrant health takes no arguments")
		}
		if err = e.Ping(ctx); err != nil {
			return result, err
		}
		return boundedNativeRows([]string{"status"}, [][]string{{"healthy"}}), nil
	}
	if err = vectorCollection(req.Namespace); err != nil {
		return result, err
	}
	switch command {
	case "GET", "DESCRIBE":
		if len(args) != 0 {
			return result, fmt.Errorf("qdrant describe takes no arguments")
		}
		info, callErr := e.client.GetCollectionInfo(ctx, req.Namespace)
		if callErr != nil {
			return result, callErr
		}
		n, parseErr := qdrantNamespace(req.Namespace, info)
		if parseErr != nil {
			return result, parseErr
		}
		data, _ := json.Marshal(n.Metadata)
		return boundedNativeRows([]string{"collection", "points_count", "metadata"}, [][]string{{n.Name, n.Metadata["points_count"], string(data)}}), nil
	case "SEARCH":
		if len(args) != 2 {
			return result, fmt.Errorf("qdrant search requires vector and limit")
		}
		vector, parseErr := qdrantVector(args[0])
		if parseErr != nil {
			return result, parseErr
		}
		limit, parseErr := vectorLimit(args[1])
		if parseErr != nil {
			return result, parseErr
		}
		points, callErr := e.client.Query(ctx, &qdrant.QueryPoints{CollectionName: req.Namespace, Query: qdrant.NewQuery(vector...), Limit: qdrant.PtrOf(uint64(limit))})
		if callErr != nil {
			return result, callErr
		}
		rows := make([][]string, 0, min(len(points), limit))
		for _, point := range points {
			if len(rows) == limit {
				break
			}
			if point == nil {
				return result, fmt.Errorf("qdrant search result is incomplete")
			}
			rows = append(rows, []string{qdrantIDString(point.GetId()), strconv.FormatFloat(float64(point.GetScore()), 'g', -1, 32)})
		}
		return boundedNativeRows([]string{"id", "score"}, rows), nil
	case "SCROLL":
		if len(args) != 2 {
			return result, fmt.Errorf("qdrant scroll requires limit and cursor (START for first page)")
		}
		limit, parseErr := vectorLimit(args[0])
		if parseErr != nil {
			return result, parseErr
		}
		var offset *qdrant.PointId
		if args[1] != "START" {
			offset, parseErr = qdrantPointID(args[1])
			if parseErr != nil {
				return result, parseErr
			}
		}
		points, next, callErr := e.client.ScrollAndOffset(ctx, &qdrant.ScrollPoints{CollectionName: req.Namespace, Limit: qdrant.PtrOf(uint32(limit)), Offset: offset})
		if callErr != nil {
			return result, callErr
		}
		rows := make([][]string, 0, min(len(points), limit))
		for _, point := range points {
			if len(rows) == limit {
				break
			}
			if point == nil {
				return result, fmt.Errorf("qdrant scroll result is incomplete")
			}
			rows = append(rows, []string{qdrantIDString(point.GetId())})
		}
		result = boundedNativeRows([]string{"id"}, rows)
		if next != nil {
			result.Info = map[string]string{"next_cursor": qdrantIDString(next)}
		}
		return result, nil
	case "UPSERT":
		if len(args) != 1 {
			return result, fmt.Errorf("qdrant upsert requires JSON points")
		}
		points, parseErr := qdrantWritePoints(args[0])
		if parseErr != nil {
			return result, parseErr
		}
		wait := true
		if _, err = e.client.Upsert(ctx, &qdrant.UpsertPoints{CollectionName: req.Namespace, Points: points, Wait: &wait}); err != nil {
			return result, err
		}
		return boundedNativeRows([]string{"written"}, [][]string{{strconv.Itoa(len(points))}}), nil
	}
	return result, fmt.Errorf("qdrant command is not allowed")
}
