package adminapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskGrantSQLCiphertextRequiresMatchingSecret(t *testing.T) {
	key := DeriveTokenKey([]byte(adminTestSecret))
	sqlText := "SELECT id FROM public.customers WHERE id = 708140123 LIMIT 1"
	encoded, err := sealTaskGrantSQL(key, sqlText)
	require.NoError(t, err)
	require.NotContains(t, encoded, sqlText)
	plain, err := openTaskGrantSQL(key, encoded)
	require.NoError(t, err)
	require.Equal(t, sqlText, plain)
	_, err = openTaskGrantSQL(DeriveTokenKey([]byte("another-secret-of-sufficient-length")), encoded)
	require.Error(t, err)
	_, err = openTaskGrantSQL(key, encoded[:len(encoded)-2]+"AA")
	require.Error(t, err)
}
