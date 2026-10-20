package businessdb

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoFindRequest is deliberately smaller than MongoDB's find command. It
// cannot carry operators, scripts, nested paths, sort or arbitrary options.
type MongoFindRequest struct {
	Collection string
	Fields     []string
	Filter     map[string]string
	Limit      int
}

const mongoFindMaxBytes = 256 * 1024

func validMongoSimpleName(name string) bool {
	if name == "" || len(name) > 120 {
		return false
	}
	for index, char := range name {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func ValidateMongoFind(request MongoFindRequest) error {
	if !validMongoSimpleName(request.Collection) || request.Limit < 1 || request.Limit > 100 ||
		len(request.Fields) < 1 || len(request.Fields) > 32 || len(request.Filter) < 1 || len(request.Filter) > 8 {
		return fmt.Errorf("invalid bounded MongoDB find request")
	}
	seen := make(map[string]bool, len(request.Fields))
	for _, field := range request.Fields {
		if !validMongoSimpleName(field) || seen[field] {
			return fmt.Errorf("invalid MongoDB projection field")
		}
		seen[field] = true
	}
	for field, value := range request.Filter {
		if !validMongoSimpleName(field) || len(value) > nativeMaxValueBytes {
			return fmt.Errorf("invalid MongoDB equality filter")
		}
	}
	return nil
}

func mongoScalar(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", fmt.Errorf("non-finite MongoDB value")
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case primitive.ObjectID:
		return typed.Hex(), nil
	case primitive.DateTime:
		return typed.Time().UTC().Format(time.RFC3339Nano), nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	default:
		return "", fmt.Errorf("MongoDB projection contains a non-scalar value")
	}
}

// FindControlled returns only explicitly projected top-level scalar fields.
// The caller must establish current Agent policy and durable audit barriers.
func (e *MongoExecutor) FindControlled(ctx context.Context, request MongoFindRequest) (model.QueryResult, error) {
	if e == nil || !e.readOnly || e.client == nil || e.database == "admin" ||
		!validMongoSimpleName(e.database) {
		return model.QueryResult{}, fmt.Errorf("MongoDB read capability is unavailable")
	}
	if err := ValidateMongoFind(request); err != nil {
		return model.QueryResult{}, err
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || !strings.HasPrefix(version, "7.0.") {
		return model.QueryResult{}, fmt.Errorf("MongoDB server version is unsupported")
	}
	projection := bson.M{"_id": 0}
	for _, field := range request.Fields {
		projection[field] = 1
	}
	filter := bson.M{}
	for field, value := range request.Filter {
		filter[field] = value
	}
	started := time.Now()
	cursor, err := e.client.Database(e.database).Collection(request.Collection).Find(ctx, filter,
		options.Find().SetProjection(projection).SetLimit(int64(request.Limit)).SetMaxTime(e.statementTimeout))
	if err != nil {
		return model.QueryResult{}, err
	}
	defer cursor.Close(ctx)
	result := model.QueryResult{Columns: append([]string(nil), request.Fields...), Rows: make([][]string, 0, request.Limit)}
	bytesUsed := 0
	for cursor.Next(ctx) {
		if len(result.Rows) >= request.Limit {
			return model.QueryResult{}, fmt.Errorf("MongoDB result exceeds row limit")
		}
		var document bson.M
		if err := cursor.Decode(&document); err != nil {
			return model.QueryResult{}, err
		}
		if len(document) > len(request.Fields) {
			return model.QueryResult{}, fmt.Errorf("MongoDB returned an unrequested field")
		}
		for field := range document {
			if _, requested := projection[field]; !requested || field == "_id" && projection[field] == 0 {
				return model.QueryResult{}, fmt.Errorf("MongoDB returned an unrequested field")
			}
		}
		row := make([]string, len(request.Fields))
		for index, field := range request.Fields {
			value, err := mongoScalar(document[field])
			if err != nil || len(value) > nativeMaxValueBytes {
				return model.QueryResult{}, fmt.Errorf("MongoDB result cell is unsupported or oversized")
			}
			bytesUsed += len(value)
			if bytesUsed > mongoFindMaxBytes {
				return model.QueryResult{}, fmt.Errorf("MongoDB result exceeds byte limit")
			}
			row[index] = value
		}
		result.Rows = append(result.Rows, row)
	}
	if err := cursor.Err(); err != nil {
		return model.QueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	result.LatencyMS = time.Since(started).Milliseconds()
	return result, nil
}
