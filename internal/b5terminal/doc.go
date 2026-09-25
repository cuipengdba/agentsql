// Package b5terminal contains the feature-off B5 S4a terminal evidence,
// ownership, and PostgreSQL CancelRequest contracts.
//
// It deliberately contains no database driver adapter and is not imported by
// a production entry point. The PostgreSQL wire implementation and real
// backend lifecycle belong to S4b.
package b5terminal

const (
	TerminalEvidenceSchema      = "agentsql.b5.terminal-evidence.v2"
	NoCommitEverSentSchema      = "agentsql.b5.no-commit-ever-sent.v1"
	ConnectionDispositionSchema = "agentsql.b5.connection-disposition-proof.v1"
)
