package businessdb

import (
	"context"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

const controlledKeyValueLimit = 64 * 1024

type RedisReadRequest struct {
	Key string
}

func (e *RedisExecutor) ReadKeyControlled(ctx context.Context, request RedisReadRequest) (model.QueryResult, error) {
	if e == nil || !e.readOnly || ctx == nil || !validControlledKey(request.Key) {
		return model.QueryResult{}, fmt.Errorf("invalid controlled key request")
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || (e.dialect == "redis" && version != "8.2.9") ||
		(e.dialect == "valkey" && version != "8.1.10") {
		return model.QueryResult{}, fmt.Errorf("key server version is unsupported")
	}
	keyType, err := e.client.Type(ctx, request.Key).Result()
	if err != nil {
		return model.QueryResult{}, err
	}
	if keyType == "none" {
		return model.QueryResult{Columns: []string{"value"}}, nil
	}
	if keyType != "string" {
		return model.QueryResult{}, fmt.Errorf("controlled key is not a string")
	}
	// GETRANGE bounds the network response. One extra byte detects truncation.
	value, err := e.client.GetRange(ctx, request.Key, 0, controlledKeyValueLimit).Result()
	if err != nil {
		return model.QueryResult{}, err
	}
	if len(value) > controlledKeyValueLimit || !utf8.ValidString(value) {
		return model.QueryResult{}, fmt.Errorf("controlled key value is oversized or not UTF-8")
	}
	return model.QueryResult{Columns: []string{"value"}, Rows: [][]string{{value}}, RowCount: 1}, nil
}

func validControlledKey(key string) bool {
	if len(key) < 3 || len(key) > 128 {
		return false
	}
	for _, char := range key {
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			char >= '0' && char <= '9' || char == ':' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func RedisDatabaseNumber(datasource model.Datasource) (int, error) {
	if datasource.Database == "" {
		return 0, nil
	}
	database, err := strconv.Atoi(datasource.Database)
	if err != nil || database < 0 || database > 15 || strconv.Itoa(database) != datasource.Database {
		return 0, fmt.Errorf("invalid controlled key database")
	}
	return database, nil
}
