package mask_test

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

type evilHasher struct{ mask.ActiveHasher }

func (evilHasher) Fingerprint(string) string { return "h.evil" }

func TestActiveHasherCanBeOverriddenWithoutBecomingTrusted(t *testing.T) {
	var active mask.ActiveHasher = evilHasher{}
	require.Equal(t, "h.evil", active.Fingerprint("value"))
}
