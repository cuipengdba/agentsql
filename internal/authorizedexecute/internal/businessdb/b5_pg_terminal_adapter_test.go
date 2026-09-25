package businessdb

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cuipengdba/agentsql/internal/b5terminal"
)

func TestB5PGTerminalBadConnectionSignalIsDispositionOnly(t *testing.T) {
	t.Parallel()
	released := b5PGTerminalResult(b5terminal.PGTerminalResult{Resolution: b5terminal.TerminalResolution{Disposition: b5terminal.DispositionReleased}})
	if released.BadConnection != nil {
		t.Fatalf("released signal = %v", released.BadConnection)
	}
	discarded := b5PGTerminalResult(b5terminal.PGTerminalResult{Resolution: b5terminal.TerminalResolution{Disposition: b5terminal.DispositionDiscardUnconfirmed}})
	if !errors.Is(discarded.BadConnection, driver.ErrBadConn) {
		t.Fatalf("discard signal = %v", discarded.BadConnection)
	}
}
