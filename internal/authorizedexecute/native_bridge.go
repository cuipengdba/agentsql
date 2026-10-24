package authorizedexecute

import (
	"context"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
)

type NativeQueryRequest = businessdb.NativeQueryRequest
type NativeQueryResult = businessdb.NativeQueryResult
type NativeCatalog = businessdb.NativeCatalog
type NativeNamespace = businessdb.NativeNamespace
type MongoFindRequest = businessdb.MongoFindRequest
type RedisReadRequest = businessdb.RedisReadRequest
type RedisHashReadRequest = businessdb.RedisHashReadRequest
type ClickHouseReadRequest = businessdb.ClickHouseReadRequest
type MilvusSearchRequest = businessdb.MilvusSearchRequest

var ErrNativeUnsupported = errors.New("native capability unsupported")

func ValidateMongoFind(request MongoFindRequest) error {
	return businessdb.ValidateMongoFind(request)
}

func ValidateClickHouseRead(request ClickHouseReadRequest) error {
	return businessdb.ValidateClickHouseRead(request)
}

func ValidateMilvusSearch(request MilvusSearchRequest) error {
	return businessdb.ValidateMilvusSearch(request)
}

func (gateway *Gateway) MilvusSearchControlled(ctx context.Context, datasource model.Datasource, secret []byte, request MilvusSearchRequest) (model.QueryResult, error) {
	if gateway == nil || !gateway.readOnly {
		return model.QueryResult{}, ErrNativeUnsupported
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil || password == "" {
		return model.QueryResult{}, fixedExecutionError(fmt.Errorf("controlled Milvus credentials unavailable"))
	}
	result, err := businessdb.MilvusSearchControlled(ctx, datasource, password, request)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

func (gateway *Gateway) MilvusPingControlled(ctx context.Context, datasource model.Datasource, secret []byte) error {
	if gateway == nil {
		return ErrNativeUnsupported
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return fixedExecutionError(err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil || password == "" {
		return fixedExecutionError(fmt.Errorf("controlled Milvus credentials unavailable"))
	}
	return fixedExecutionError(businessdb.MilvusPingControlled(ctx, datasource, password))
}

func (gateway *Gateway) ClickHouseReadControlled(ctx context.Context, datasource model.Datasource, secret []byte, request ClickHouseReadRequest) (model.QueryResult, error) {
	if gateway == nil || !gateway.readOnly {
		return model.QueryResult{}, ErrNativeUnsupported
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil || password == "" {
		return model.QueryResult{}, fixedExecutionError(fmt.Errorf("controlled ClickHouse credentials unavailable"))
	}
	result, err := businessdb.ClickHouseReadControlled(ctx, datasource, password, request)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

func RedisDatabaseNumber(datasource model.Datasource) (int, error) {
	return businessdb.RedisDatabaseNumber(datasource)
}

func ValidateRedisHashRead(request RedisHashReadRequest) error {
	return businessdb.ValidateRedisHashRead(request)
}

func (gateway *Gateway) RedisHashReadControlled(ctx context.Context, datasource model.Datasource, secret []byte, request RedisHashReadRequest) (model.QueryResult, error) {
	if gateway == nil || !gateway.readOnly || (datasource.DBType != "redis" && datasource.DBType != "valkey") ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" || datasource.Username == "" || datasource.PasswordEnc == "" {
		return model.QueryResult{}, ErrNativeUnsupported
	}
	if _, err := RedisDatabaseNumber(datasource); err != nil {
		return model.QueryResult{}, err
	}
	if err := ValidateRedisHashRead(request); err != nil {
		return model.QueryResult{}, err
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil || password == "" {
		return model.QueryResult{}, fixedExecutionError(fmt.Errorf("controlled Hash credentials unavailable"))
	}
	opened, err := businessdb.NewRedisExecutorWithContext(ctx, datasource, password, true)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	defer opened.Close()
	result, err := opened.ReadHashControlled(ctx, request)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

func (gateway *Gateway) RedisReadControlled(ctx context.Context, datasource model.Datasource, secret []byte, request RedisReadRequest) (model.QueryResult, error) {
	if gateway == nil || !gateway.readOnly || (datasource.DBType != "redis" && datasource.DBType != "valkey") ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.TLSCAFile == "" || datasource.Username == "" || datasource.PasswordEnc == "" {
		return model.QueryResult{}, ErrNativeUnsupported
	}
	if _, err := RedisDatabaseNumber(datasource); err != nil {
		return model.QueryResult{}, err
	}
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	password, err := cipher.Decrypt(datasource.PasswordEnc)
	if err != nil || password == "" {
		return model.QueryResult{}, fixedExecutionError(fmt.Errorf("controlled key credentials unavailable"))
	}
	opened, err := businessdb.NewRedisExecutorWithContext(ctx, datasource, password, true)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	defer opened.Close()
	result, err := opened.ReadKeyControlled(ctx, request)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

// MongoFindControlled is an exact MongoDB read capability. It never accepts a
// generic native command or a caller-controlled namespace.
func (gateway *Gateway) MongoFindControlled(ctx context.Context, datasource model.Datasource, secret []byte, request MongoFindRequest) (model.QueryResult, error) {
	if gateway == nil || !gateway.readOnly || datasource.DBType != "mongodb" ||
		(datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full") ||
		datasource.TrustServerCertificate || datasource.Username == "" || datasource.Database == "" {
		return model.QueryResult{}, ErrNativeUnsupported
	}
	if err := ValidateMongoFind(request); err != nil {
		return model.QueryResult{}, err
	}
	// The general native manager caches by datasource ID. A controlled read must
	// bind to the current host, database, certificate and credentials on every
	// request, including immediately after an administrator edits the source.
	cipher, err := store.NewPasswordCipher(secret)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	password := ""
	if datasource.PasswordEnc != "" {
		password, err = cipher.Decrypt(datasource.PasswordEnc)
		if err != nil {
			return model.QueryResult{}, fixedExecutionError(err)
		}
	}
	mongo, err := businessdb.NewMongoExecutorWithContext(ctx, datasource, password, true)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	defer mongo.Close()
	result, err := mongo.FindControlled(ctx, request)
	if err != nil {
		return model.QueryResult{}, fixedExecutionError(err)
	}
	return result, nil
}

func ClassifyNativeCommand(dbType string, request NativeQueryRequest) (string, error) {
	return businessdb.ClassifyNativeCommand(dbType, request)
}

func (gateway *Gateway) NativeQueryRead(ctx context.Context, datasource model.Datasource, secret []byte, request NativeQueryRequest) (NativeQueryResult, error) {
	if gateway == nil || !gateway.readOnly {
		return NativeQueryResult{}, fmt.Errorf("read-only native gateway is required")
	}
	access, err := ClassifyNativeCommand(datasource.DBType, request)
	if err != nil || access != "read" {
		return NativeQueryResult{}, fmt.Errorf("native read command is not allowed")
	}
	opened, err := gateway.openNative(ctx, datasource, secret)
	if err != nil {
		return NativeQueryResult{}, fixedExecutionError(err)
	}
	return opened.NativeQuery(ctx, request)
}

func (gateway *Gateway) NativeDiscover(ctx context.Context, datasource model.Datasource, secret []byte) (NativeCatalog, error) {
	if gateway == nil || !gateway.readOnly {
		return NativeCatalog{}, fmt.Errorf("read-only native gateway is required")
	}
	opened, err := gateway.openNative(ctx, datasource, secret)
	if err != nil {
		return NativeCatalog{}, fixedExecutionError(err)
	}
	return opened.Discover(ctx)
}

func (gateway *Gateway) NativeServerVersion(ctx context.Context, datasource model.Datasource, secret []byte) (string, error) {
	if gateway == nil || !gateway.readOnly {
		return "", fmt.Errorf("read-only native gateway is required")
	}
	opened, err := gateway.openNative(ctx, datasource, secret)
	if err != nil {
		return "", fixedExecutionError(err)
	}
	return opened.ServerVersion(ctx)
}
