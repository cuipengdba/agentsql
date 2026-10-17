package pipeline

import (
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/stretchr/testify/require"
)

func TestApprovalSealBindsImmutableRequestFields(t *testing.T) {
	agent, datasource, fingerprint := "agent-1", "ds-1", "fingerprint"
	sqlRaw, sqlNorm, reason, requestReason := "SELECT id FROM public.t WHERE id=1 LIMIT 1", "SELECT id FROM public.t WHERE id = 1 LIMIT 1", "manual review", "incident"
	expires := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	base := model.Approval{
		ID: "approval-1", TenantID: "tenant-1", AgentID: &agent, DatasourceID: &datasource,
		DatasourceFingerprint: &fingerprint, SQLRaw: &sqlRaw, SQLNorm: &sqlNorm,
		Reason: &reason, RequestReason: &requestReason, ExpiresAt: &expires,
	}
	secret := make([]byte, 32)
	seal, err := sealApproval(secret, base)
	require.NoError(t, err)
	for name, modify := range map[string]func(*model.Approval){
		"tenant":         func(a *model.Approval) { a.TenantID = "tenant-2" },
		"agent":          func(a *model.Approval) { other := "agent-2"; a.AgentID = &other },
		"datasource":     func(a *model.Approval) { other := "ds-2"; a.DatasourceID = &other },
		"configuration":  func(a *model.Approval) { other := "changed"; a.DatasourceFingerprint = &other },
		"raw_sql":        func(a *model.Approval) { other := "SELECT 2"; a.SQLRaw = &other },
		"normalized_sql": func(a *model.Approval) { other := "SELECT 2"; a.SQLNorm = &other },
		"reason":         func(a *model.Approval) { other := "changed"; a.Reason = &other },
		"request_reason": func(a *model.Approval) { other := "changed"; a.RequestReason = &other },
		"requester_user": func(a *model.Approval) { other := "user-1"; a.RequesterUserID = &other },
		"expiry":         func(a *model.Approval) { other := expires.Add(time.Hour); a.ExpiresAt = &other },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			modify(&changed)
			other, sealErr := sealApproval(secret, changed)
			require.NoError(t, sealErr)
			require.NotEqual(t, seal, other)
		})
	}
}
