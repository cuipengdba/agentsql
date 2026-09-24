package businessdb

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type cancelTrace struct{ events []string }

type fakeCancelRows struct{ trace *cancelTrace }

func (rows *fakeCancelRows) Next() bool {
	rows.trace.events = append(rows.trace.events, "drain")
	return false
}
func (rows *fakeCancelRows) Err() error {
	rows.trace.events = append(rows.trace.events, "rows_err")
	return errors.New("stream canceled")
}
func (rows *fakeCancelRows) Close() error {
	rows.trace.events = append(rows.trace.events, "rows_close")
	return nil
}

type fakeCancelTarget struct {
	trace *cancelTrace
	rows  mysqlCancelRows
}

func (*fakeCancelTarget) ThreadID() uint64             { return 42 }
func (target *fakeCancelTarget) Rows() mysqlCancelRows { return target.rows }
func (target *fakeCancelTarget) Rollback(context.Context) error {
	target.trace.events = append(target.trace.events, "rollback")
	return nil
}
func (target *fakeCancelTarget) Discard() error {
	target.trace.events = append(target.trace.events, "discard")
	return nil
}

type fakeCancelControl struct{ trace *cancelTrace }

func (control fakeCancelControl) KillQuery(context.Context, uint64) error {
	control.trace.events = append(control.trace.events, "kill")
	return nil
}

func TestMySQLAuxiliaryCancelDrainsRollsBackAndDiscards(t *testing.T) {
	trace := &cancelTrace{}
	target := &fakeCancelTarget{trace: trace, rows: &fakeCancelRows{trace: trace}}
	err := (mysqlAuxiliaryCanceller{enabled: true, control: fakeCancelControl{trace: trace}}).cancel(context.Background(), target)
	require.Error(t, err)
	require.Equal(t, []string{"kill", "drain", "rows_err", "rows_close", "rollback", "discard"}, trace.events)
}

func TestMySQLAuxiliaryCancelFeatureOffTouchesNothing(t *testing.T) {
	trace := &cancelTrace{}
	err := (mysqlAuxiliaryCanceller{}).cancel(context.Background(), &fakeCancelTarget{trace: trace})
	require.ErrorIs(t, err, errMySQLAuxiliaryCancelDisabled)
	require.Empty(t, trace.events)
}
