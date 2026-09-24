package model

// AuditPage is one immutable audit-log result page.
type AuditPage struct {
	Total    int64
	Page     int
	PageSize int
	List     []AuditLog
}
