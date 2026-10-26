package pipeline

import (
	"context"
	"strings"

	executor "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/store"
)

// checkManagedFileOwnership is an additional gate for the reserved file
// schema. Agent-level policies alone cannot identify the human who imported a
// file. Missing delegation, a pending import, and a revoked binding all deny.
func (run *pipelineRun) checkManagedFileOwnership(ctx context.Context) error {
	if run.ast == nil || run.agent == nil || run.datasource == nil {
		return nil
	}
	deny := func() error { return &executor.AuthError{Reason: executor.ReasonAgentDenied} }
	seen := false
	for _, table := range run.ast.Tables {
		managedSchema := strings.EqualFold(table.Schema, "agentsql_files")
		managedName := strings.HasPrefix(strings.ToLower(table.Table), "agentsql_file_")
		if !managedSchema && !managedName {
			continue
		}
		seen = true
		if table.Schema != "agentsql_files" || !managedName || run.datasource.DBType != "postgres" ||
			run.request.DelegatedUserID == "" || run.request.DelegatedBindingID == "" ||
			run.request.DelegatedTenantID != run.agent.TenantID ||
			isNilInterface(run.pipeline.managedFile) {
			return deny()
		}
		tenantCtx, err := store.WithTenant(ctx, run.agent.TenantID)
		if err != nil {
			return deny()
		}
		allowed, err := run.pipeline.managedFile.PublishedTableOwner(tenantCtx, run.request.DelegatedBindingID, run.datasource.ID,
			run.agent.ID, run.request.DelegatedUserID, table.Table)
		if err != nil || !allowed {
			return deny()
		}
	}
	// A parser that did not report a syntactic reference must not silently
	// permit a reserved-schema identifier through an unusual SQL construct.
	if !seen {
		lower := strings.ToLower(run.request.SQL)
		if strings.Contains(lower, "agentsql_files") || strings.Contains(lower, "agentsql_file_") {
			return deny()
		}
	}
	return nil
}
