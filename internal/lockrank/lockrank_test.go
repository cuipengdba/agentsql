package lockrank

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlBusinessOrderAndReverseRejection(t *testing.T) {
	ctx := WithTracker(context.Background())
	control, err := Acquire(ctx, Control)
	require.NoError(t, err)
	business, err := Acquire(ctx, Business)
	require.NoError(t, err)
	_, err = Acquire(ctx, Control)
	require.ErrorIs(t, err, ErrReverseOrder)
	business.Release()
	secondControl, err := Acquire(ctx, Control)
	require.NoError(t, err)
	secondControl.Release()
	control.Release()
}
