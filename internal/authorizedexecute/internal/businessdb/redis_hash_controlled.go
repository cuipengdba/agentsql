package businessdb

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	controlledHashFieldLimit = 16
	controlledHashValueLimit = 16 * 1024
	controlledHashTotalLimit = 64 * 1024
)

// The fixed read-only script checks sizes before producing a network reply.
// Separate HSTRLEN and HMGET requests would permit a concurrent writer to
// enlarge a field between the checks and send an unbounded value to the client.
const controlledHashScript = `
if #KEYS ~= 1 or #ARGV < 1 or #ARGV > 16 then
  return redis.error_reply('invalid controlled Hash request')
end
local kind = redis.call('TYPE', KEYS[1]).ok
if kind == 'none' then return {} end
if kind ~= 'hash' then return redis.error_reply('controlled key is not a Hash') end
local result = {}
local total = 0
for i = 1, #ARGV do
  local value = redis.call('HGET', KEYS[1], ARGV[i])
  if not value then return redis.error_reply('controlled Hash field is missing') end
  if #value > 16384 then return redis.error_reply('controlled Hash field is oversized') end
  total = total + #value
  if total > 65536 then return redis.error_reply('controlled Hash result is oversized') end
  result[i] = value
end
return result
`

type RedisHashReadRequest struct {
	Key    string
	Fields []string
}

func validControlledHashField(field string) bool {
	if len(field) < 1 || len(field) > 64 {
		return false
	}
	for index, char := range field {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			index > 0 && char >= '0' && char <= '9' || index > 0 && char == '_' {
			continue
		}
		return false
	}
	return true
}

func ValidateRedisHashRead(request RedisHashReadRequest) error {
	if !validControlledKey(request.Key) || len(request.Fields) < 1 || len(request.Fields) > controlledHashFieldLimit {
		return fmt.Errorf("invalid controlled Hash request")
	}
	seen := make(map[string]bool, len(request.Fields))
	for _, field := range request.Fields {
		if !validControlledHashField(field) || seen[field] {
			return fmt.Errorf("invalid controlled Hash field")
		}
		seen[field] = true
	}
	return nil
}

func (e *RedisExecutor) ReadHashControlled(ctx context.Context, request RedisHashReadRequest) (model.QueryResult, error) {
	if e == nil || !e.readOnly || e.client == nil || ctx == nil {
		return model.QueryResult{}, fmt.Errorf("controlled Hash read is unavailable")
	}
	if err := ValidateRedisHashRead(request); err != nil {
		return model.QueryResult{}, err
	}
	version, err := e.ServerVersion(ctx)
	if err != nil || (e.dialect == "redis" && version != "8.2.9") ||
		(e.dialect == "valkey" && version != "8.1.10") {
		return model.QueryResult{}, fmt.Errorf("key server version is unsupported")
	}
	args := make([]interface{}, len(request.Fields))
	for index, field := range request.Fields {
		args[index] = field
	}
	started := time.Now()
	raw, err := e.client.EvalRO(ctx, controlledHashScript, []string{request.Key}, args...).Result()
	if err != nil {
		return model.QueryResult{}, err
	}
	values, ok := raw.([]interface{})
	if !ok || len(values) != 0 && len(values) != len(request.Fields) {
		return model.QueryResult{}, fmt.Errorf("malformed controlled Hash response")
	}
	result := model.QueryResult{Columns: append([]string(nil), request.Fields...), LatencyMS: time.Since(started).Milliseconds()}
	if len(values) == 0 {
		return result, nil
	}
	row := make([]string, len(values))
	total := 0
	for index, value := range values {
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) || len(text) > controlledHashValueLimit {
			return model.QueryResult{}, fmt.Errorf("malformed controlled Hash value")
		}
		total += len(text)
		if total > controlledHashTotalLimit {
			return model.QueryResult{}, fmt.Errorf("controlled Hash result is oversized")
		}
		row[index] = text
	}
	result.Rows = [][]string{row}
	result.RowCount = 1
	return result, nil
}
