package authorizedexecute

import (
	"context"
	"errors"
	"fmt"

	"github.com/cuipengdba/agentsql/internal/authorizedexecute/internal/businessdb"
	"github.com/cuipengdba/agentsql/internal/model"
)

type NativeQueryRequest = businessdb.NativeQueryRequest
type NativeQueryResult = businessdb.NativeQueryResult
type NativeCatalog = businessdb.NativeCatalog
type NativeNamespace = businessdb.NativeNamespace

var ErrNativeUnsupported = errors.New("native capability unsupported")

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
