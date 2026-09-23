package mask

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeyCommitmentGolden(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	require.Equal(t, "a31f889589ef261522a3524cc34fe61c37533d31714954a22138fc8f4d340981", KeyCommitment(key))
	require.Equal(t, "0123456789abcdef0123456789abcdef", string(key), "commitment must not mutate material")
}
