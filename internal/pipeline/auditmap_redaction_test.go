package pipeline

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestMapAuditLogMergesAuditPhaseAndHashKeyVersion(t *testing.T) {
	version := 1
	log, err := mapAuditLog(Request{}, nil, nil, nil, Response{
		Redact: mask.RedactReport{HashKeyVersion: &version}, auditPhase: auditPhaseOutcome, relatedAuditID: 42,
	}, nil, "allow", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, log.DetailsJSON)
	var details map[string]any
	require.NoError(t, json.Unmarshal([]byte(*log.DetailsJSON), &details))
	require.Equal(t, "outcome", details["audit_phase"])
	require.Equal(t, float64(42), details["related_audit_id"])
	require.Equal(t, float64(1), details["key_version"])
	require.Len(t, details, 3)
}

func TestMapAuditLogSingleDetailsSemantics(t *testing.T) {
	log, err := mapAuditLog(Request{}, nil, nil, nil, Response{}, nil, "allow", nil, time.Now())
	require.NoError(t, err)
	require.Nil(t, log.DetailsJSON)

	version := 1
	log, err = mapAuditLog(Request{}, nil, nil, nil, Response{Redact: mask.RedactReport{HashKeyVersion: &version}}, nil, "allow", nil, time.Now())
	require.NoError(t, err)
	require.JSONEq(t, `{"key_version":1}`, *log.DetailsJSON)
}
