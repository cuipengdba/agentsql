// Package b5terminal contains the feature-off B5 terminal evidence,
// ownership, PostgreSQL wire adapter, and CancelRequest contracts.
//
// It is deliberately not imported by a production entry point. In particular,
// adding the PostgreSQL adapter here does not activate a flag or replace the
// legacy businessdb transaction interface.
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
