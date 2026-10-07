package businessdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	milvus "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

type MilvusExecutor struct {
	client   milvus.Client
	id       string
	readOnly bool
}

var vectorName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

func milvusConfig(ds model.Datasource, password string) (milvus.Config, error) {
	if ds.DBType != "milvus" || strings.TrimSpace(ds.Host) == "" {
		return milvus.Config{}, fmt.Errorf("milvus host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 19530
	}
	if port < 1 || port > 65535 {
		return milvus.Config{}, fmt.Errorf("milvus port is invalid")
	}
	if ds.TrustServerCertificate || ds.TLSServerName != "" || ds.TLSCAFile != "" || ds.TLSMode != "" {
		return milvus.Config{}, fmt.Errorf("milvus TLS settings are unsupported")
	}
	if ds.Username != "" && password == "" {
		return milvus.Config{}, fmt.Errorf("milvus username requires password")
	}
	if ds.Database != "" && !vectorName.MatchString(ds.Database) {
		return milvus.Config{}, fmt.Errorf("milvus database is invalid")
	}
	cfg := milvus.Config{Address: net.JoinHostPort(ds.Host, strconv.Itoa(port)), DBName: ds.Database}
	if ds.Username == "" {
		cfg.APIKey = password
	} else {
		cfg.Username, cfg.Password = ds.Username, password
	}
	return cfg, nil
}

func NewMilvusExecutor(ds model.Datasource, password string, readOnly bool) (*MilvusExecutor, error) {
	cfg, err := milvusConfig(ds, password)
	if err != nil {
		return nil, err
	}
	ms, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ms)*time.Millisecond)
	defer cancel()
	c, err := milvus.NewClient(ctx, cfg)
	if err != nil {
		return nil, err
	}
	e := &MilvusExecutor{client: c, id: ds.ID, readOnly: readOnly}
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (*MilvusExecutor) Category() model.DatasourceCategory { return model.CategoryVector }
func (*MilvusExecutor) Dialect() model.DBDialect           { return "milvus" }
func (e *MilvusExecutor) Close() error                     { return e.client.Close() }
func (e *MilvusExecutor) Ping(ctx context.Context) error {
	state, err := e.client.CheckHealth(ctx)
	if err != nil {
		return err
	}
	if state == nil || !state.IsHealthy {
		return fmt.Errorf("milvus is unhealthy")
	}
	return nil
}
func (e *MilvusExecutor) ServerVersion(ctx context.Context) (string, error) {
	v, err := e.client.GetVersion(ctx)
	return boundedNativeString(v), err
}

func milvusNamespace(c *entity.Collection) NativeNamespace {
	if c == nil {
		return NativeNamespace{Kind: "collection", Metadata: map[string]string{"vector_dimensions": "unavailable"}}
	}
	n := NativeNamespace{Name: boundedNativeString(c.Name), Kind: "collection", Metadata: map[string]string{"vector_dimensions": "unavailable"}}
	if c.Schema == nil {
		return n
	}
	fields := make([]string, 0, min(len(c.Schema.Fields), 100))
	dims := make([]string, 0)
	for _, f := range c.Schema.Fields {
		if f == nil {
			continue
		}
		if len(fields) < 100 {
			fields = append(fields, f.Name+":"+f.DataType.Name())
		}
		if f.PrimaryKey {
			n.Metadata["primary_key"] = boundedNativeString(f.Name)
		}
		if f.DataType == entity.FieldTypeFloatVector || f.DataType == entity.FieldTypeBinaryVector {
			if dim := f.TypeParams[entity.TypeParamDim]; dim != "" && len(dims) < 100 {
				dims = append(dims, f.Name+":"+dim)
			}
		}
	}
	n.Metadata["fields"] = boundedNativeString(strings.Join(fields, ","))
	if len(dims) > 0 {
		n.Metadata["vector_dimensions"] = boundedNativeString(strings.Join(dims, ","))
	}
	return n
}
func (e *MilvusExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	cs, err := e.client.ListCollections(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	out := NativeCatalog{Category: model.CategoryVector, ServerVersion: v}
	for _, c := range cs {
		if len(out.Namespaces) >= nativeMaxRows {
			break
		}
		if c == nil || c.Name == "" {
			continue
		}
		full, err := e.client.DescribeCollection(ctx, c.Name)
		if err != nil {
			return NativeCatalog{}, err
		}
		if full == nil {
			return NativeCatalog{}, fmt.Errorf("milvus collection metadata unavailable")
		}
		out.Namespaces = append(out.Namespaces, milvusNamespace(full))
	}
	return out, nil
}

func milvusCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "SEARCH", "QUERY", "GET", "DESCRIBE", "STATUS", "HEALTH":
		return nil
	case "INSERT", "UPSERT":
		if !readOnly {
			return nil
		}
		return fmt.Errorf("milvus write forbidden in read-only mode")
	default:
		return fmt.Errorf("milvus command %s is not allowed", command)
	}
}
func vectorLimit(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 100 {
		return 0, fmt.Errorf("vector result limit must be 1..100")
	}
	return n, nil
}
func vectorCollection(s string) error {
	if !vectorName.MatchString(s) {
		return fmt.Errorf("vector collection or class is invalid")
	}
	return nil
}
func vectorExpression(s string) error {
	if strings.TrimSpace(s) == "" || strings.ContainsAny(s, ";\r\n") {
		return fmt.Errorf("vector filter expression is invalid")
	}
	return nil
}
func parseFloatVector(s string) (entity.FloatVector, error) {
	var values []float32
	if err := json.Unmarshal([]byte(s), &values); err != nil || len(values) == 0 || len(values) > 2048 {
		return nil, fmt.Errorf("vector must be a JSON numeric array with 1..2048 dimensions")
	}
	for _, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("vector values must be finite")
		}
	}
	return entity.FloatVector(values), nil
}
func milvusMetric(indexes []entity.Index) (entity.MetricType, error) {
	for _, index := range indexes {
		if index == nil {
			continue
		}
		switch index.Params()["metric_type"] {
		case "IP":
			return entity.IP, nil
		case "L2":
			return entity.L2, nil
		case "COSINE":
			return entity.COSINE, nil
		}
	}
	return "", fmt.Errorf("milvus vector index metric is unavailable or unsupported")
}
func milvusResultSet(rs milvus.ResultSet) NativeQueryResult {
	cols := make([]string, 0, len(rs))
	rows := make([][]string, 0, min(rs.Len(), 100))
	for _, c := range rs {
		cols = append(cols, boundedNativeString(c.Name()))
	}
	for i := 0; i < rs.Len() && i < 100; i++ {
		row := make([]string, 0, len(rs))
		for _, c := range rs {
			v, err := c.Get(i)
			if err != nil {
				row = append(row, "unavailable")
			} else {
				row = append(row, fmt.Sprint(v))
			}
		}
		rows = append(rows, row)
	}
	return boundedNativeRows(cols, rows)
}
func milvusWriteColumns(schema *entity.Schema, input string, upsert bool) ([]entity.Column, int, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &rows); err != nil || len(rows) == 0 || len(rows) > 100 {
		return nil, 0, fmt.Errorf("milvus write requires 1..100 JSON rows")
	}
	if schema == nil {
		return nil, 0, fmt.Errorf("milvus schema is unavailable")
	}
	var pk, vec *entity.Field
	for _, f := range schema.Fields {
		if f == nil {
			continue
		}
		if f.PrimaryKey {
			pk = f
		}
		if f.DataType == entity.FieldTypeFloatVector {
			if vec != nil {
				return nil, 0, fmt.Errorf("milvus multi-vector writes are unsupported")
			}
			vec = f
		}
	}
	if pk == nil || vec == nil {
		return nil, 0, fmt.Errorf("milvus write requires primary key and float vector fields")
	}
	dim, err := strconv.Atoi(vec.TypeParams[entity.TypeParamDim])
	if err != nil || dim < 1 || dim > 2048 {
		return nil, 0, fmt.Errorf("milvus vector dimension is unavailable or invalid")
	}
	idsInt := []int64{}
	idsString := []string{}
	vectors := [][]float32{}
	for _, row := range rows {
		if len(row) > 2 || len(row) < 1 {
			return nil, 0, fmt.Errorf("milvus write row has unsupported fields")
		}
		for name := range row {
			if name != pk.Name && name != vec.Name {
				return nil, 0, fmt.Errorf("milvus write row has unsupported field")
			}
		}
		var arr []float32
		if json.Unmarshal(row[vec.Name], &arr) != nil || len(arr) != dim {
			return nil, 0, fmt.Errorf("milvus vector dimension mismatch")
		}
		for _, v := range arr {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, 0, fmt.Errorf("milvus vector value is invalid")
			}
		}
		vectors = append(vectors, arr)
		if upsert || !pk.AutoID {
			raw, ok := row[pk.Name]
			if !ok {
				return nil, 0, fmt.Errorf("milvus primary key is required")
			}
			switch pk.DataType {
			case entity.FieldTypeInt64:
				var n int64
				if json.Unmarshal(raw, &n) != nil {
					return nil, 0, fmt.Errorf("milvus integer primary key is invalid")
				}
				idsInt = append(idsInt, n)
			case entity.FieldTypeVarChar:
				var s string
				if json.Unmarshal(raw, &s) != nil || s == "" {
					return nil, 0, fmt.Errorf("milvus string primary key is invalid")
				}
				idsString = append(idsString, s)
			default:
				return nil, 0, fmt.Errorf("milvus primary key type is unsupported")
			}
		} else if _, ok := row[pk.Name]; ok {
			return nil, 0, fmt.Errorf("milvus auto-ID primary key must be omitted")
		}
	}
	columns := []entity.Column{entity.NewColumnFloatVector(vec.Name, dim, vectors)}
	if len(idsInt) > 0 {
		columns = append(columns, entity.NewColumnInt64(pk.Name, idsInt))
	}
	if len(idsString) > 0 {
		columns = append(columns, entity.NewColumnVarChar(pk.Name, idsString))
	}
	return columns, len(rows), nil
}
func (e *MilvusExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.id, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if err = milvusCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if command == "STATUS" || command == "HEALTH" {
		if req.Namespace != "" || len(args) != 0 {
			return result, fmt.Errorf("milvus health takes no arguments")
		}
		err = e.Ping(ctx)
		if err != nil {
			return result, err
		}
		return boundedNativeRows([]string{"status"}, [][]string{{"healthy"}}), nil
	}
	if err = vectorCollection(req.Namespace); err != nil {
		return result, err
	}
	switch command {
	case "DESCRIBE", "GET":
		if len(args) != 0 {
			return result, fmt.Errorf("milvus describe takes no arguments")
		}
		var c *entity.Collection
		c, err = e.client.DescribeCollection(ctx, req.Namespace)
		if err != nil {
			return result, err
		}
		if c == nil {
			return result, fmt.Errorf("milvus collection metadata unavailable")
		}
		n := milvusNamespace(c)
		if command == "GET" {
			var stats map[string]string
			stats, err = e.client.GetCollectionStatistics(ctx, req.Namespace)
			if err != nil {
				return result, err
			}
			for k, v := range stats {
				if len(n.Metadata) >= 100 {
					break
				}
				n.Metadata[boundedNativeString(k)] = boundedNativeString(v)
			}
		}
		b, _ := json.Marshal(n.Metadata)
		return boundedNativeRows([]string{"collection", "metadata"}, [][]string{{n.Name, string(b)}}), nil
	case "QUERY":
		if len(args) != 2 {
			return result, fmt.Errorf("milvus query requires filter and limit")
		}
		if err = vectorExpression(args[0]); err != nil {
			return result, err
		}
		var limit int
		limit, err = vectorLimit(args[1])
		if err != nil {
			return result, err
		}
		var rs milvus.ResultSet
		rs, err = e.client.Query(ctx, req.Namespace, nil, args[0], nil, milvus.WithLimit(int64(limit)))
		if err != nil {
			return result, err
		}
		return milvusResultSet(rs), nil
	case "SEARCH":
		if len(args) != 3 && len(args) != 4 {
			return result, fmt.Errorf("milvus search requires field, vector, topK and optional filter")
		}
		if !vectorName.MatchString(args[0]) {
			return result, fmt.Errorf("milvus vector field is invalid")
		}
		var vec entity.FloatVector
		vec, err = parseFloatVector(args[1])
		if err != nil {
			return result, err
		}
		var topK int
		topK, err = vectorLimit(args[2])
		if err != nil {
			return result, err
		}
		filter := ""
		if len(args) == 4 {
			filter = args[3]
			if err = vectorExpression(filter); err != nil {
				return result, err
			}
		}
		sp, x := entity.NewIndexFlatSearchParam()
		if x != nil {
			return result, x
		}
		indexes, x := e.client.DescribeIndex(ctx, req.Namespace, args[0])
		if x != nil {
			return result, x
		}
		metric, x := milvusMetric(indexes)
		if x != nil {
			return result, x
		}
		var hits []milvus.SearchResult
		hits, err = e.client.Search(ctx, req.Namespace, nil, filter, nil, []entity.Vector{vec}, args[0], metric, topK, sp)
		if err != nil {
			return result, err
		}
		rows := [][]string{}
		for _, h := range hits {
			if h.Err != nil {
				return result, h.Err
			}
			if h.IDs == nil || h.ResultCount > len(h.Scores) {
				return result, fmt.Errorf("milvus search result is incomplete")
			}
			for i := 0; i < h.ResultCount && len(rows) < 100; i++ {
				id, x := h.IDs.Get(i)
				if x != nil {
					return result, x
				}
				rows = append(rows, []string{fmt.Sprint(id), fmt.Sprint(h.Scores[i])})
			}
		}
		return boundedNativeRows([]string{"id", "score"}, rows), nil
	case "INSERT", "UPSERT":
		if len(args) != 1 {
			return result, fmt.Errorf("milvus write requires JSON rows")
		}
		var c *entity.Collection
		c, err = e.client.DescribeCollection(ctx, req.Namespace)
		if err != nil {
			return result, err
		}
		if c == nil {
			return result, fmt.Errorf("milvus collection metadata unavailable")
		}
		var columns []entity.Column
		var count int
		columns, count, err = milvusWriteColumns(c.Schema, args[0], command == "UPSERT")
		if err != nil {
			return result, err
		}
		var ids entity.Column
		if command == "INSERT" {
			ids, err = e.client.Insert(ctx, req.Namespace, "", columns...)
		} else {
			ids, err = e.client.Upsert(ctx, req.Namespace, "", columns...)
		}
		if err != nil {
			return result, err
		}
		_ = ids
		return boundedNativeRows([]string{"written"}, [][]string{{strconv.Itoa(count)}}), nil
	}
	return result, fmt.Errorf("milvus command is not allowed")
}
