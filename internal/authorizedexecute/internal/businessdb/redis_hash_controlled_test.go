package businessdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateRedisHashReadRejectsUnboundedOrAmbiguousRequests(t *testing.T) {
	require.NoError(t, ValidateRedisHashRead(RedisHashReadRequest{Key: "customers:1", Fields: []string{"phone", "region_1"}}))
	for _, request := range []RedisHashReadRequest{
		{Key: "customers:*", Fields: []string{"phone"}},
		{Key: "customers:1", Fields: nil},
		{Key: "customers:1", Fields: []string{"phone", "phone"}},
		{Key: "customers:1", Fields: []string{"phone.secret"}},
		{Key: "customers:1", Fields: []string{"1phone"}},
	} {
		require.Error(t, ValidateRedisHashRead(request))
	}
	fields := make([]string, 17)
	for index := range fields {
		fields[index] = "field" + string(rune('A'+index))
	}
	require.Error(t, ValidateRedisHashRead(RedisHashReadRequest{Key: "customers:1", Fields: fields}))
}
