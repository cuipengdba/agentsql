package model

import "time"

// AuditFilter describes optional immutable audit-log query constraints.
type AuditFilter struct {
	TimeStart    *time.Time
	TimeEnd      *time.Time
	AgentID      *string
	DatasourceID *string
	SessionID    *string
	MCPTool      *string
	Decisions    []string
	StmtTypes    []string
	RiskMin      *int
	RiskMax      *int
	Keyword      string
	ObjectLike   string
}

// IsEmpty reports whether no audit-log constraint is configured.
func (filter AuditFilter) IsEmpty() bool {
	return filter.TimeStart == nil &&
		filter.TimeEnd == nil &&
		filter.AgentID == nil &&
		filter.DatasourceID == nil &&
		filter.SessionID == nil &&
		filter.MCPTool == nil &&
		len(filter.Decisions) == 0 &&
		len(filter.StmtTypes) == 0 &&
		filter.RiskMin == nil &&
		filter.RiskMax == nil &&
		filter.Keyword == "" &&
		filter.ObjectLike == ""
}
