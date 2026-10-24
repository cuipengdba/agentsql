package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
	milvus "github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// MilvusSearchRequest carries only a single vector and explicitly named scalar
// output fields. The caller cannot supply Milvus expressions or search options.
type MilvusSearchRequest struct {
	Collection  string
	VectorField string
	Vector      []float32
	Fields      []string
	Filter      map[string]string
	TenantID    string // authoritative Agent tenant; never accepted from MCP input
	TopK        int
}

var milvusSafeEqualityValue = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func ValidateMilvusSearch(request MilvusSearchRequest) error {
	if !vectorName.MatchString(request.Collection) || !vectorName.MatchString(request.VectorField) ||
		len(request.Vector) < 1 || len(request.Vector) > 2048 || len(request.Fields) < 1 || len(request.Fields) > 16 ||
		request.TopK < 1 || request.TopK > 20 {
		return errors.New("invalid controlled Milvus search")
	}
	seen := make(map[string]bool, len(request.Fields))
	for _, field := range request.Fields {
		if !vectorName.MatchString(field) || field == request.VectorField || seen[field] {
			return errors.New("invalid controlled Milvus output field")
		}
		seen[field] = true
	}
	for _, value := range request.Vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("Milvus vector must contain finite values")
		}
	}
	if len(request.Filter) > 4 {
		return errors.New("Milvus scalar filter exceeds limit")
	}
	for field, value := range request.Filter {
		if !vectorName.MatchString(field) || !milvusSafeEqualityValue.MatchString(value) {
			return errors.New("Milvus scalar filter is invalid")
		}
	}
	return nil
}

func MilvusSearchControlled(ctx context.Context, ds model.Datasource, password string, request MilvusSearchRequest) (model.QueryResult, error) {
	if err := ValidateMilvusSearch(request); err != nil {
		return model.QueryResult{}, err
	}
	control, err := model.ParseNativeReadControl(ds.ReadControlJSON, ds.DBType)
	if err != nil {
		return model.QueryResult{}, err
	}
	expression, expected, err := controlledMilvusFilter(request, control.Milvus)
	if err != nil {
		return model.QueryResult{}, err
	}
	client, err := milvusControlledClient(ctx, ds, password)
	if err != nil {
		return model.QueryResult{}, err
	}
	defer client.Close()
	collection, err := client.DescribeCollection(ctx, request.Collection)
	if err != nil || collection == nil || collection.Name != request.Collection || collection.Schema == nil ||
		collection.Schema.EnableDynamicField {
		return model.QueryResult{}, errors.New("Milvus collection schema is unavailable or unsupported")
	}
	fields := make(map[string]*entity.Field, len(collection.Schema.Fields))
	for _, field := range collection.Schema.Fields {
		if field != nil {
			fields[field.Name] = field
		}
	}
	vectorField := fields[request.VectorField]
	if vectorField == nil || vectorField.DataType != entity.FieldTypeFloatVector ||
		vectorField.IsDynamic || vectorField.IsPartitionKey || vectorField.IsClusteringKey {
		return model.QueryResult{}, errors.New("Milvus vector field is unsupported")
	}
	dim, err := strconv.Atoi(vectorField.TypeParams[entity.TypeParamDim])
	if err != nil || dim != len(request.Vector) {
		return model.QueryResult{}, errors.New("Milvus vector dimension mismatch")
	}
	for _, name := range request.Fields {
		field := fields[name]
		if field == nil || field.IsDynamic || !milvusScalarOutputType(field.DataType) {
			return model.QueryResult{}, errors.New("Milvus output field is unsupported")
		}
	}
	for name := range expected {
		field := fields[name]
		if field == nil || field.IsDynamic || field.IsPartitionKey || field.IsClusteringKey ||
			(field.DataType != entity.FieldTypeString && field.DataType != entity.FieldTypeVarChar) {
			return model.QueryResult{}, errors.New("Milvus filter field is not a static string scalar")
		}
	}
	indexes, err := client.DescribeIndex(ctx, request.Collection, request.VectorField)
	if err != nil || len(indexes) != 1 {
		return model.QueryResult{}, errors.New("Milvus vector index is unavailable")
	}
	if indexes[0] == nil || indexes[0].Params()["metric_type"] != "COSINE" {
		return model.QueryResult{}, errors.New("Milvus vector index metric is unsupported")
	}
	sp, err := entity.NewIndexFlatSearchParam()
	if err != nil {
		return model.QueryResult{}, err
	}
	started := time.Now()
	searchFields := append([]string(nil), request.Fields...)
	for name := range expected {
		if !containsMilvusField(searchFields, name) {
			searchFields = append(searchFields, name)
		}
	}
	hits, err := client.Search(ctx, request.Collection, nil, expression, searchFields,
		[]entity.Vector{entity.FloatVector(request.Vector)}, request.VectorField, entity.COSINE, request.TopK, sp)
	if err != nil || len(hits) != 1 {
		return model.QueryResult{}, errors.New("Milvus search failed")
	}
	hit := hits[0]
	// SDK v2.4.2 reports a decoder error for a valid zero-hit response from
	// Milvus 2.6.25 because the server omits empty field data. Accept only this
	// exact empty shape; all other decoding failures remain closed.
	if hit.ResultCount == 0 && hit.Err != nil && hit.Err.Error() == "FieldData type not matched" &&
		hit.IDs != nil && hit.IDs.Len() == 0 && len(hit.Scores) == 0 && len(hit.Fields) == 0 {
		if ctx.Err() != nil {
			return model.QueryResult{}, ctx.Err()
		}
		return model.QueryResult{Columns: append([]string(nil), request.Fields...), Rows: [][]string{},
			LatencyMS: time.Since(started).Milliseconds()}, nil
	}
	if hit.Err != nil || hit.ResultCount < 0 || hit.ResultCount > request.TopK ||
		hit.IDs == nil || hit.IDs.Len() != hit.ResultCount ||
		len(hit.Scores) != hit.ResultCount {
		return model.QueryResult{}, errors.New("Milvus search result is incomplete")
	}
	result := model.QueryResult{Columns: append([]string(nil), request.Fields...), Rows: make([][]string, 0, hit.ResultCount)}
	columns := make([][]string, len(request.Fields))
	totalBytes := 0
	for name, want := range expected {
		column := hit.Fields.GetColumn(name)
		if hit.ResultCount == 0 && column == nil {
			continue
		}
		if column == nil || column.Len() != hit.ResultCount {
			return model.QueryResult{}, errors.New("Milvus filter result column is missing")
		}
		for row := 0; row < hit.ResultCount; row++ {
			value, err := column.Get(row)
			if err != nil || value == nil || fmt.Sprint(value) != want {
				return model.QueryResult{}, errors.New("Milvus filter result does not match authoritative scope")
			}
		}
	}
	for i, name := range request.Fields {
		column := hit.Fields.GetColumn(name)
		if hit.ResultCount == 0 && column == nil {
			columns[i] = []string{}
			continue
		}
		if column == nil || column.Len() != hit.ResultCount {
			return model.QueryResult{}, errors.New("Milvus output column is missing")
		}
		columns[i] = make([]string, hit.ResultCount)
		for row := 0; row < hit.ResultCount; row++ {
			value, err := column.Get(row)
			if err != nil || value == nil {
				return model.QueryResult{}, errors.New("Milvus output value is unavailable")
			}
			text := fmt.Sprint(value)
			totalBytes += len(text)
			if len(text) > 64*1024 || totalBytes > 1024*1024 || !utf8.ValidString(text) {
				return model.QueryResult{}, errors.New("Milvus output exceeds byte limit")
			}
			columns[i][row] = text
		}
	}
	for row := 0; row < hit.ResultCount; row++ {
		line := make([]string, len(request.Fields))
		for i := range request.Fields {
			line[i] = columns[i][row]
		}
		result.Rows = append(result.Rows, line)
	}
	result.RowCount = len(result.Rows)
	result.LatencyMS = time.Since(started).Milliseconds()
	if ctx.Err() != nil {
		return model.QueryResult{}, ctx.Err()
	}
	return result, nil
}

// controlledMilvusFilter constructs a deliberately tiny expression language:
// equality on administrator-approved static string fields. Values use a strict
// literal alphabet so no escaping or caller-provided expression is possible.
func controlledMilvusFilter(request MilvusSearchRequest, profile *model.MilvusReadControl) (string, map[string]string, error) {
	if profile == nil {
		if len(request.Filter) != 0 {
			return "", nil, errors.New("Milvus scalar filter requires tenant scope")
		}
		return "", nil, nil
	}
	if !milvusSafeEqualityValue.MatchString(request.TenantID) {
		return "", nil, errors.New("Milvus authoritative tenant ID is unsupported")
	}
	approved := make(map[string]bool, len(profile.FilterFields))
	for _, field := range profile.FilterFields {
		approved[field] = true
	}
	expected := map[string]string{profile.TenantField: request.TenantID}
	for field, value := range request.Filter {
		if !approved[field] || field == profile.TenantField || !milvusSafeEqualityValue.MatchString(value) {
			return "", nil, errors.New("Milvus scalar filter is not approved")
		}
		expected[field] = value
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	clauses := make([]string, 0, len(names))
	for _, name := range names {
		clauses = append(clauses, name+` == "`+expected[name]+`"`)
	}
	return strings.Join(clauses, " && "), expected, nil
}

func containsMilvusField(fields []string, target string) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}

func MilvusPingControlled(ctx context.Context, ds model.Datasource, password string) error {
	client, err := milvusControlledClient(ctx, ds, password)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}

func milvusControlledClient(ctx context.Context, ds model.Datasource, password string) (milvus.Client, error) {
	if ds.DBType != "milvus" || ds.Database != "default" || ds.Username == "" || ds.Username == "root" ||
		password == "" || (ds.TLSMode != "strict" && ds.TLSMode != "verify-full") ||
		ds.TrustServerCertificate || ds.TLSCAFile == "" || !filepath.IsAbs(ds.TLSCAFile) ||
		strings.TrimSpace(ds.Host) == "" || ds.Port < 1 || ds.Port > 65535 {
		return nil, errors.New("controlled Milvus connection is unavailable")
	}
	ca, err := os.ReadFile(ds.TLSCAFile)
	if err != nil || len(ca) == 0 || len(ca) > 64*1024 {
		return nil, errors.New("Milvus CA is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("Milvus CA is invalid")
	}
	serverName := ds.TLSServerName
	if serverName == "" {
		serverName = ds.Host
	}
	config := milvus.Config{Address: net.JoinHostPort(ds.Host, strconv.Itoa(ds.Port)),
		Username: ds.Username, Password: password, DBName: ds.Database, EnableTLSAuth: true,
		DialOptions: append(append([]grpc.DialOption{}, milvus.DefaultGrpcOpts...),
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
				RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12,
			})), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2*1024*1024))),
	}
	client, err := milvus.NewClient(ctx, config)
	if err != nil {
		return nil, err
	}
	version, err := client.GetVersion(ctx)
	if err != nil || version != "2.6.25" {
		_ = client.Close()
		return nil, errors.New("unverified Milvus server version")
	}
	return client, nil
}

func milvusScalarOutputType(dataType entity.FieldType) bool {
	switch dataType {
	case entity.FieldTypeBool, entity.FieldTypeInt8, entity.FieldTypeInt16, entity.FieldTypeInt32,
		entity.FieldTypeInt64, entity.FieldTypeFloat, entity.FieldTypeDouble, entity.FieldTypeString, entity.FieldTypeVarChar:
		return true
	default:
		return false
	}
}
