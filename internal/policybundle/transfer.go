package policybundle

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const TransferSchemaVersion = "agentsql.policy-transfer/v1"

const maxTransferLifetime = 7 * 24 * time.Hour
const maxTransferBytes = 256 << 10

// TransferPayload is signed by a source-environment key held outside AgentSQL.
// Its bundle is limited to the simple table rules supported by atomic apply.
type TransferPayload struct {
	SchemaVersion     string    `json:"schema_version"`
	SourceEnvironment string    `json:"source_environment"`
	SourceTenant      string    `json:"source_tenant"`
	ExportedAt        time.Time `json:"exported_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	BundleDigest      string    `json:"bundle_digest"`
	Bundle            Bundle    `json:"bundle"`
}

type SignedTransfer struct {
	Payload   TransferPayload `json:"payload"`
	Signature string          `json:"signature"`
}

// TransferMapping binds every source identity to an explicitly selected target
// identity. A policy maps to either an existing simple table rule or a new ID.
type TransferMapping struct {
	TargetTenant string            `json:"target_tenant"`
	Agents       map[string]string `json:"agents"`
	Datasources  map[string]string `json:"datasources"`
	Policies     map[string]string `json:"policies"`
	Objects      map[string]string `json:"objects"`
}

func SignTransfer(bundle Bundle, sourceEnvironment string, exportedAt time.Time, key ed25519.PrivateKey) (SignedTransfer, error) {
	if len(key) != ed25519.PrivateKeySize {
		return SignedTransfer{}, errors.New("invalid policy transfer signing key")
	}
	digest, err := Digest(bundle)
	if err != nil {
		return SignedTransfer{}, err
	}
	payload := TransferPayload{
		SchemaVersion: TransferSchemaVersion, SourceEnvironment: sourceEnvironment,
		SourceTenant: bundle.TenantID, ExportedAt: exportedAt.UTC(),
		ExpiresAt: exportedAt.UTC().Add(maxTransferLifetime), BundleDigest: digest, Bundle: bundle,
	}
	message, err := transferMessage(payload)
	if err != nil {
		return SignedTransfer{}, err
	}
	return SignedTransfer{Payload: payload, Signature: hex.EncodeToString(ed25519.Sign(key, message))}, nil
}

func VerifyTransfer(transfer SignedTransfer, key ed25519.PublicKey, now time.Time) error {
	if len(key) != ed25519.PublicKeySize || now.IsZero() {
		return errors.New("invalid policy transfer verification input")
	}
	message, err := transferMessage(transfer.Payload)
	if err != nil {
		return err
	}
	signature, err := hex.DecodeString(transfer.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, message, signature) {
		return errors.New("policy transfer signature is invalid")
	}
	if now.Before(transfer.Payload.ExportedAt.Add(-5*time.Minute)) || !now.Before(transfer.Payload.ExpiresAt) {
		return errors.New("policy transfer is outside its validity window")
	}
	return nil
}

func transferMessage(payload TransferPayload) ([]byte, error) {
	if payload.SchemaVersion != TransferSchemaVersion || payload.SourceEnvironment == "" ||
		strings.TrimSpace(payload.SourceEnvironment) != payload.SourceEnvironment || len(payload.SourceEnvironment) > 128 ||
		payload.SourceTenant != payload.Bundle.TenantID || payload.ExportedAt.IsZero() || payload.ExpiresAt.IsZero() ||
		!payload.ExpiresAt.After(payload.ExportedAt) || payload.ExpiresAt.Sub(payload.ExportedAt) > maxTransferLifetime {
		return nil, errors.New("policy transfer metadata is invalid")
	}
	digest, err := Digest(payload.Bundle)
	if err != nil {
		return nil, err
	}
	if digest != payload.BundleDigest {
		return nil, errors.New("policy transfer bundle digest differs")
	}
	for _, entry := range payload.Bundle.Policies {
		if !transferableTableRule(entry) {
			return nil, fmt.Errorf("policy %q is not a transferable simple table rule", entry.ID)
		}
		if _, err := exactObject(ReplayCase{AgentID: entry.AgentID, DatasourceID: entry.DatasourceID, ObjectName: entry.ObjectName}); err != nil {
			return nil, fmt.Errorf("policy %q has a non-exact source table: %w", entry.ID, err)
		}
	}
	message, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(message) > maxTransferBytes {
		return nil, errors.New("policy transfer exceeds size limit")
	}
	return message, nil
}

func transferableTableRule(entry Entry) bool {
	return entry.ObjectType == "table" && (entry.Action == "allow" || entry.Action == "deny") &&
		entry.Columns == nil && entry.RowFilter == nil && entry.RelationBindingID == nil && entry.RelationBinding == nil &&
		len(entry.ColumnPermissions) == 0 && len(entry.ColumnStaging) == 0 && !entry.LegacyUnrepresentable
}

// PlanTransfer creates a candidate for the existing review and apply flow.
// It never deletes unrelated target policies or persists anything.
func PlanTransfer(transfer SignedTransfer, key ed25519.PublicKey, now time.Time, current Bundle, mapping TransferMapping) (Bundle, []Change, error) {
	if err := VerifyTransfer(transfer, key, now); err != nil {
		return Bundle{}, nil, err
	}
	if mapping.TargetTenant == "" || mapping.TargetTenant != current.TenantID {
		return Bundle{}, nil, errors.New("policy transfer target tenant mismatch")
	}
	if len(mapping.Policies) != len(transfer.Payload.Bundle.Policies) || len(mapping.Objects) != len(transfer.Payload.Bundle.Policies) {
		return Bundle{}, nil, errors.New("policy transfer mappings must cover exactly the source policies")
	}
	currentBytes, err := Canonical(current)
	if err != nil {
		return Bundle{}, nil, err
	}
	var desired Bundle
	if err := json.Unmarshal(currentBytes, &desired); err != nil {
		return Bundle{}, nil, err
	}
	indices := make(map[string]int, len(desired.Policies))
	for index, entry := range desired.Policies {
		indices[entry.ID] = index
	}
	used := make(map[string]bool, len(transfer.Payload.Bundle.Policies))
	for _, source := range transfer.Payload.Bundle.Policies {
		targetID, ok := mapping.Policies[source.ID]
		if !ok || targetID == "" || used[targetID] {
			return Bundle{}, nil, fmt.Errorf("policy %q has no unique target identity", source.ID)
		}
		used[targetID] = true
		targetAgent, okAgent := mapping.Agents[source.AgentID]
		targetDatasource, okDatasource := mapping.Datasources[source.DatasourceID]
		targetObject, okObject := mapping.Objects[source.ID]
		if !okAgent || !okDatasource || !okObject || targetAgent == "" || targetDatasource == "" || targetObject == "" {
			return Bundle{}, nil, fmt.Errorf("policy %q has an incomplete resource mapping", source.ID)
		}
		if _, err := exactObject(ReplayCase{AgentID: targetAgent, DatasourceID: targetDatasource, ObjectName: targetObject}); err != nil {
			return Bundle{}, nil, fmt.Errorf("policy %q has an invalid target table: %w", source.ID, err)
		}
		entry := source
		entry.ID, entry.AgentID, entry.DatasourceID, entry.ObjectName, entry.Revision = targetID, targetAgent, targetDatasource, targetObject, 0
		if index, exists := indices[targetID]; exists {
			if !transferableTableRule(desired.Policies[index]) {
				return Bundle{}, nil, fmt.Errorf("target policy %q cannot be replaced by transfer", targetID)
			}
			entry.Revision = desired.Policies[index].Revision
			desired.Policies[index] = entry
		} else {
			indices[targetID] = len(desired.Policies)
			desired.Policies = append(desired.Policies, entry)
		}
	}
	changes, err := Diff(current, desired)
	if err != nil {
		return Bundle{}, nil, err
	}
	return desired, changes, nil
}
