package businessdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateClickHouseReadRejectsUnboundedOrInjectedInput(t *testing.T) {
	valid := ClickHouseReadRequest{Table: "events", Fields: []string{"name", "phone"}, Filter: map[string]string{"region": "east"}, Limit: 10}
	require.NoError(t, ValidateClickHouseRead(valid))
	cases := []ClickHouseReadRequest{
		{Table: "events; DROP TABLE x", Fields: valid.Fields, Filter: valid.Filter, Limit: 10},
		{Table: "events", Fields: []string{"*"}, Filter: valid.Filter, Limit: 10},
		{Table: "events", Fields: []string{"name", "name"}, Filter: valid.Filter, Limit: 10},
		{Table: "events", Fields: valid.Fields, Filter: map[string]string{}, Limit: 10},
		{Table: "events", Fields: valid.Fields, Filter: map[string]string{"region OR 1=1": "east"}, Limit: 10},
		{Table: "events", Fields: valid.Fields, Filter: valid.Filter, Limit: 101},
	}
	for _, request := range cases {
		require.Error(t, ValidateClickHouseRead(request))
	}
}
