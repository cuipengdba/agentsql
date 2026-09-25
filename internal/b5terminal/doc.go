// Package b5terminal contains the feature-off B5 S4a terminal evidence,
// ownership, and PostgreSQL CancelRequest contracts.
//
// It deliberately contains no database driver adapter and is not imported by
// a production entry point. The PostgreSQL wire implementation and real
// backend lifecycle belong to S4b.
package b5terminal

import "github.com/cuipengdba/agentsql/internal/b5"

const (
	TerminalEvidenceSchema       = b5.TerminalEvidenceSchemaID
	TerminalEvidenceVersion      = b5.TerminalEvidenceVersion
	NoCommitEverSentSchema       = b5.NoCommitProofSchemaID
	NoCommitEverSentVersion      = b5.NoCommitProofVersion
	ConnectionDispositionSchema  = b5.DispositionProofSchemaID
	ConnectionDispositionVersion = b5.DispositionProofVersion
)
