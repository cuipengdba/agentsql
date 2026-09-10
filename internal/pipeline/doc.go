// Package pipeline is the single orchestration entry point for protected SQL.
// It authenticates, loads policy, parses, evaluates two guard gates, executes
// only allowed statements, redacts result sets, and synchronously audits every
// outcome.
package pipeline
