package businessdb

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestControlledMilvusFilterBindsAuthoritativeTenant(t *testing.T) {
	profile := &model.MilvusReadControl{TenantField: "tenant_id", FilterFields: []string{"region", "status"}}
	request := MilvusSearchRequest{Collection: "events", VectorField: "embedding", Vector: []float32{1, 0},
		Fields: []string{"name"}, Filter: map[string]string{"status": "active", "region": "east"},
		TenantID: "tenant-a", TopK: 2}
	require.NoError(t, ValidateMilvusSearch(request))
	expression, expected, err := controlledMilvusFilter(request, profile)
	require.NoError(t, err)
	require.Equal(t, `region == "east" && status == "active" && tenant_id == "tenant-a"`, expression)
	require.Equal(t, map[string]string{"region": "east", "status": "active", "tenant_id": "tenant-a"}, expected)
	for _, filter := range []map[string]string{
		{"tenant_id": "tenant-b"}, {"private": "x"}, {"status": `active" || tenant_id == "tenant-b`},
	} {
		request.Filter = filter
		_, _, err := controlledMilvusFilter(request, profile)
		require.Error(t, err)
	}
	request.Filter = nil
	request.TenantID = ""
	_, _, err = controlledMilvusFilter(request, profile)
	require.Error(t, err)
	request.Filter = map[string]string{"status": "active"}
	_, _, err = controlledMilvusFilter(request, nil)
	require.Error(t, err)
}
