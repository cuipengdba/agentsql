// Package b5session implements the feature-off B5 S3 coordination contracts:
// a strongly-consistent session directory, continuation capabilities, shared
// connection and plan admission, durable dial identities, quarantine, and the
// owner-crash reaper.
//
// The package is deliberately not imported by a production MCP/HTTP handler.
// It owns metadata only and never receives a business SQL executor.
package b5session
