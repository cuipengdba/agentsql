package businessdb

import (
	"context"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestValidateMongoFindClosedShape(t *testing.T) {
	valid := MongoFindRequest{Collection: "customers", Fields: []string{"name", "phone"}, Filter: map[string]string{"name": "test"}, Limit: 10}
	require.NoError(t, ValidateMongoFind(valid))
	for _, change := range []func(*MongoFindRequest){
		func(r *MongoFindRequest) { r.Collection = "$cmd" },
		func(r *MongoFindRequest) { r.Collection = "other.customers" },
		func(r *MongoFindRequest) { r.Fields = []string{"phone", "phone"} },
		func(r *MongoFindRequest) { r.Fields = []string{"nested.phone"} },
		func(r *MongoFindRequest) { r.Filter = map[string]string{"$where": "true"} },
		func(r *MongoFindRequest) { r.Filter = nil },
		func(r *MongoFindRequest) { r.Limit = 101 },
	} {
		request := valid
		change(&request)
		require.Error(t, ValidateMongoFind(request))
	}
	for _, value := range []any{map[string]any{"secret": "x"}, []string{"x"}} {
		_, err := mongoScalar(value)
		require.Error(t, err)
	}
}

func TestMongoControlledOpenHonorsCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewMongoExecutorWithContext(ctx, model.Datasource{DBType: "mongodb"}, "", true)
	require.ErrorIs(t, err, context.Canceled)
}
