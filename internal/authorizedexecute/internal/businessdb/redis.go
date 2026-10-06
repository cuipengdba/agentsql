package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	redis "github.com/redis/go-redis/v9"
)

type RedisExecutor struct {
	client       *redis.Client
	dialect      model.DBDialect
	datasourceID string
	database     int
	readOnly     bool
}

func NewRedisExecutor(datasource model.Datasource, password string, readOnly bool) (*RedisExecutor, error) {
	if datasource.DBType != "redis" && datasource.DBType != "valkey" {
		return nil, fmt.Errorf("invalid Redis dialect")
	}
	if strings.TrimSpace(datasource.Host) == "" {
		return nil, fmt.Errorf("redis host is required")
	}
	port := datasource.Port
	if port == 0 {
		port = 6379
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("redis port is invalid")
	}
	db := 0
	if datasource.Database != "" {
		var err error
		db, err = strconv.Atoi(datasource.Database)
		if err != nil || db < 0 || db > 15 {
			return nil, fmt.Errorf("redis database must be 0 to 15")
		}
	}
	timeoutMS, err := normalizedStatementTimeout(datasource.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	limit, err := normalizedConnectionLimit(datasource.ConnLimit)
	if err != nil {
		return nil, err
	}
	options := &redis.Options{Addr: net.JoinHostPort(datasource.Host, strconv.Itoa(port)), Username: datasource.Username, Password: password, DB: db, PoolSize: limit, DialTimeout: time.Duration(timeoutMS) * time.Millisecond, ReadTimeout: time.Duration(timeoutMS) * time.Millisecond, WriteTimeout: time.Duration(timeoutMS) * time.Millisecond}
	if datasource.TrustServerCertificate {
		return nil, fmt.Errorf("unverified Redis TLS is unsupported")
	}
	if datasource.TLSMode != "" {
		if datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full" {
			return nil, fmt.Errorf("invalid Redis TLS mode")
		}
		config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: datasource.TLSServerName}
		if datasource.TLSCAFile != "" {
			pem, err := os.ReadFile(datasource.TLSCAFile)
			if err != nil {
				return nil, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid Redis TLS CA")
			}
			config.RootCAs = roots
		}
		options.TLSConfig = config
	} else if datasource.TLSServerName != "" || datasource.TLSCAFile != "" {
		return nil, fmt.Errorf("Redis TLS settings require tls_mode")
	}
	client := redis.NewClient(options)
	executor := &RedisExecutor{client: client, dialect: model.DBDialect(datasource.DBType), datasourceID: datasource.ID, database: db, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	if err := executor.Ping(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return executor, nil
}

func (executor *RedisExecutor) Category() model.DatasourceCategory { return model.CategoryKeyValue }
func (executor *RedisExecutor) Dialect() model.DBDialect           { return executor.dialect }
func (executor *RedisExecutor) Ping(ctx context.Context) error {
	return executor.client.Ping(ctx).Err()
}
func (executor *RedisExecutor) Close() error { return executor.client.Close() }
func (executor *RedisExecutor) ServerVersion(ctx context.Context) (string, error) {
	info, err := executor.client.Info(ctx, "server").Result()
	if err != nil {
		return "", err
	}
	for _, key := range []string{"valkey_version", "redis_version"} {
		for _, line := range strings.Split(info, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
				return boundedNativeString(value), nil
			}
		}
	}
	return "", fmt.Errorf("server version is unavailable")
}
func parseRedisKeyspace(info string) []NativeNamespace {
	var result []NativeNamespace
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		name, body, ok := strings.Cut(line, ":")
		if !ok || !strings.HasPrefix(name, "db") {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(name, "db")); err != nil {
			continue
		}
		for _, part := range strings.Split(body, ",") {
			if count, found := strings.CutPrefix(part, "keys="); found {
				value, err := strconv.ParseInt(count, 10, 64)
				if err == nil {
					result = append(result, NativeNamespace{Name: name, Kind: "database", ItemCount: value})
				}
				break
			}
		}
		if len(result) >= nativeMaxRows {
			break
		}
	}
	return result
}
func (executor *RedisExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, err := executor.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	info, err := executor.client.Info(ctx, "keyspace").Result()
	if err != nil {
		return NativeCatalog{}, err
	}
	namespaces := parseRedisKeyspace(info)
	count, err := executor.client.DBSize(ctx).Result()
	if err != nil {
		return NativeCatalog{}, err
	}
	current := fmt.Sprintf("db%d", executor.database)
	found := false
	for i := range namespaces {
		if namespaces[i].Name == current {
			namespaces[i].ItemCount = count
			found = true
		}
	}
	if !found {
		namespaces = append(namespaces, NativeNamespace{Name: current, Kind: "database", ItemCount: count})
	}
	return NativeCatalog{Category: model.CategoryKeyValue, ServerVersion: version, Namespaces: namespaces}, nil
}

var redisReadCommands = map[string]bool{"GET": true, "MGET": true, "STRLEN": true, "EXISTS": true, "TYPE": true, "TTL": true, "PTTL": true, "HGET": true, "HMGET": true, "HGETALL": true, "HKEYS": true, "HVALS": true, "HLEN": true, "LRANGE": true, "LLEN": true, "SMEMBERS": true, "SCARD": true, "ZRANGE": true, "ZRANGEBYSCORE": true, "ZCARD": true, "ZSCORE": true, "SCAN": true, "DBSIZE": true, "INFO": true, "PING": true, "ECHO": true, "SELECT": true, "OBJECT ENCODING": true}
var redisWriteCommands = map[string]bool{"SET": true, "MSET": true, "DEL": true, "HSET": true, "SADD": true, "ZADD": true, "LPUSH": true, "RPUSH": true, "EXPIRE": true, "INCR": true, "DECR": true}

func redisCommandAllowed(command string, readOnly bool) error {
	if redisReadCommands[command] {
		return nil
	}
	if redisWriteCommands[command] {
		if readOnly {
			return fmt.Errorf("native write command %s is forbidden in read-only mode", command)
		}
		return nil
	}
	return fmt.Errorf("native command %s is not allowed", command)
}
func redisValueRows(value any, rows *[][]string) {
	if len(*rows) >= nativeMaxRows {
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			redisValueRows(child, rows)
		}
	case map[any]any:
		for key, child := range typed {
			if len(*rows) >= nativeMaxRows {
				break
			}
			*rows = append(*rows, []string{fmt.Sprint(key), fmt.Sprint(child)})
		}
	case map[string]any:
		for key, child := range typed {
			if len(*rows) >= nativeMaxRows {
				break
			}
			*rows = append(*rows, []string{key, fmt.Sprint(child)})
		}
	case nil:
		*rows = append(*rows, []string{""})
	default:
		*rows = append(*rows, []string{fmt.Sprint(value)})
	}
}
func (executor *RedisExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(executor.dialect, executor.datasourceID, command, executor.readOnly, err) }()
	var args []string
	command, args, err = normalizedNativeCommand(request)
	if err != nil {
		return NativeQueryResult{}, err
	}
	if err := redisCommandAllowed(command, executor.readOnly); err != nil {
		return NativeQueryResult{}, err
	}
	if request.Namespace != "" && request.Namespace != fmt.Sprintf("db%d", executor.database) {
		return NativeQueryResult{}, fmt.Errorf("native namespace differs from connected database")
	}
	if command == "SELECT" {
		if len(args) != 1 || args[0] != strconv.Itoa(executor.database) {
			return NativeQueryResult{}, fmt.Errorf("SELECT may only target the connected database")
		}
		return NativeQueryResult{Raw: "OK"}, nil
	}
	if command == "SCAN" && len(args) > 0 {
		for _, arg := range args {
			if strings.EqualFold(arg, "COUNT") {
				return NativeQueryResult{}, fmt.Errorf("SCAN count option is unsupported")
			}
		}
	}
	parts := strings.Fields(command)
	values := make([]any, 0, len(parts)+len(args))
	for _, part := range parts {
		values = append(values, part)
	}
	for _, arg := range args {
		values = append(values, arg)
	}
	value, err := executor.client.Do(ctx, values...).Result()
	if err != nil {
		return NativeQueryResult{}, err
	}
	rows := make([][]string, 0)
	redisValueRows(value, &rows)
	return boundedNativeRows([]string{"value"}, rows), nil
}
