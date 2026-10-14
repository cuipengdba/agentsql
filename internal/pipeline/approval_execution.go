package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type approvalExecutionStore interface {
	ClaimApproved(context.Context, string, string, time.Time) (model.Approval, error)
	CompleteClaim(context.Context, string, string, string, *int64, *string, time.Time) (model.Approval, error)
}

type approvalSealPayload struct {
	ID                    string `json:"id"`
	TenantID              string `json:"tenant_id"`
	AgentID               string `json:"agent_id"`
	DatasourceID          string `json:"datasource_id"`
	DatasourceFingerprint string `json:"datasource_fingerprint"`
	SQLRaw                string `json:"sql_raw"`
	SQLNorm               string `json:"sql_norm"`
	Reason                string `json:"reason"`
	RequestReason         string `json:"request_reason"`
	ExpiresAt             string `json:"expires_at"`
}

func approvalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sealApproval(secret []byte, approval model.Approval) (string, error) {
	if len(secret) != 32 || approval.ID == "" || approval.TenantID == "" ||
		approval.AgentID == nil || *approval.AgentID == "" ||
		approval.DatasourceID == nil || *approval.DatasourceID == "" ||
		approval.DatasourceFingerprint == nil || *approval.DatasourceFingerprint == "" ||
		approval.SQLRaw == nil || *approval.SQLRaw == "" ||
		approval.SQLNorm == nil || *approval.SQLNorm == "" || approval.ExpiresAt == nil {
		return "", ErrInvalidRequest
	}
	payload := approvalSealPayload{
		ID: approval.ID, TenantID: approval.TenantID,
		AgentID: *approval.AgentID, DatasourceID: *approval.DatasourceID,
		DatasourceFingerprint: *approval.DatasourceFingerprint,
		SQLRaw:                *approval.SQLRaw, SQLNorm: *approval.SQLNorm,
		Reason: approvalValue(approval.Reason), RequestReason: approvalValue(approval.RequestReason),
		ExpiresAt: approval.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode approval seal: %w", err)
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func datasourceFingerprint(datasource model.Datasource) string {
	// Exclude timestamps and presentation name; include every connection and
	// execution setting. The sealed digest never exposes the password cipher.
	payload := struct {
		ID, TenantID, DBType, Host, Database, Username, PasswordEnc string
		Port, ConnLimit, StmtTimeoutMS, RowLimit                    int
		TLSMode, TLSServerName, TLSCAFile                           string
		TrustServerCertificate                                      bool
	}{
		datasource.ID, datasource.TenantID, datasource.DBType, datasource.Host,
		datasource.Database, datasource.Username, datasource.PasswordEnc,
		datasource.Port, datasource.ConnLimit, datasource.StmtTimeoutMS, datasource.RowLimit,
		datasource.TLSMode, datasource.TLSServerName, datasource.TLSCAFile,
		datasource.TrustServerCertificate,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ExecuteApproved atomically consumes a DBA-approved request before any
// business database access. A crash leaves it claimed for manual review.
func (pipeline *Pipeline) ExecuteApproved(ctx context.Context, apiKey, approvalID string) (Response, error) {
	if pipeline == nil || ctx == nil || apiKey == "" || approvalID == "" {
		return Response{}, ErrInvalidRequest
	}
	workflow, ok := pipeline.ports.Approvals.(approvalExecutionStore)
	if !ok || isNilInterface(workflow) {
		return Response{}, fmt.Errorf("approval execution store unavailable: %w", ErrInvalidPorts)
	}
	agent, err := pipeline.ports.Authenticator.Authenticate(ctx, apiKey)
	if err != nil {
		return Response{}, err
	}
	claimed, err := workflow.ClaimApproved(ctx, approvalID, agent.ID, time.Now().UTC())
	if err != nil {
		return Response{}, err
	}
	finalize := func(status string, auditID *int64, code *string) error {
		finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, finishErr := workflow.CompleteClaim(finishContext, claimed.ID, agent.ID, status, auditID, code, time.Now().UTC())
		return finishErr
	}
	expectedSeal, err := sealApproval(pipeline.secret, claimed)
	if err != nil || claimed.Seal == nil ||
		!hmac.Equal([]byte(expectedSeal), []byte(*claimed.Seal)) ||
		claimed.AgentID == nil || *claimed.AgentID != agent.ID || claimed.TenantID != agent.TenantID ||
		claimed.ExpiresAt == nil || !claimed.ExpiresAt.After(time.Now().UTC()) {
		invalid := "APPROVAL_SEAL_INVALID"
		return Response{}, errors.Join(ErrInvalidRequest, finalize("failed", nil, &invalid))
	}
	claimEvent := model.AuditLog{
		TenantID: claimed.TenantID, TS: time.Now().UTC(),
		AgentID: claimed.AgentID, DatasourceID: claimed.DatasourceID,
		SQLRaw: claimed.SQLRaw, SQLNorm: claimed.SQLNorm,
		Decision: "approve", Action: stringPointer("approval_claim"),
		ActorType: stringPointer("agent"), ActorID: claimed.AgentID,
		DetailsJSON: stringPointer(fmt.Sprintf(`{"approval_id":%q}`, claimed.ID)),
	}
	if _, err := pipeline.ports.Audit.Record(ctx, claimEvent); err != nil {
		code := "AUDIT_UNAVAILABLE"
		return Response{}, errors.Join(ErrAuditUnavailable, err, finalize("failed", nil, &code))
	}
	response, runErr := pipeline.process(ctx, Request{
		APIKey: apiKey, DatasourceID: *claimed.DatasourceID,
		SQL: *claimed.SQLRaw, MCPTool: "execute_approved",
	}, runModeApproved, &claimed)
	status := "completed"
	if runErr != nil || response.Decision == model.DecisionError {
		status = "unknown"
	} else if response.Decision != model.DecisionAllow && response.Decision != model.DecisionWarn {
		status = "failed"
	}
	var auditID *int64
	if response.AuditID > 0 {
		auditID = &response.AuditID
	}
	var outcomeCode *string
	if response.ErrorCode != "" {
		outcomeCode = &response.ErrorCode
	}
	if finishErr := finalize(status, auditID, outcomeCode); finishErr != nil {
		return Response{}, errors.Join(runErr, fmt.Errorf("complete claimed approval: %w", finishErr))
	}
	return response, runErr
}
