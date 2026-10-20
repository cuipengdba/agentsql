// Package policybundle defines a deterministic, tenant-scoped snapshot of
// Agent object policies. It contains no datasource credentials or user secrets.
package policybundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/policy"
)

const SchemaVersion = "agentsql.policy-bundle/v1"
const MaxPolicies = 128

type Bundle struct {
	SchemaVersion string  `json:"schema_version"`
	TenantID      string  `json:"tenant_id"`
	Policies      []Entry `json:"policies"`
}

type Entry struct {
	ID                    string             `json:"id"`
	AgentID               string             `json:"agent_id"`
	DatasourceID          string             `json:"datasource_id"`
	ObjectType            string             `json:"object_type"`
	ObjectName            string             `json:"object_name"`
	Columns               *string            `json:"columns,omitempty"`
	RowFilter             *string            `json:"row_filter,omitempty"`
	Action                string             `json:"action"`
	Revision              int64              `json:"revision"`
	LegacyUnrepresentable bool               `json:"legacy_unrepresentable,omitempty"`
	RelationBindingID     *string            `json:"relation_binding_id,omitempty"`
	RelationBinding       *Binding           `json:"relation_binding,omitempty"`
	ColumnPermissions     []ColumnPermission `json:"column_permissions,omitempty"`
	ColumnStaging         []Staging          `json:"column_staging,omitempty"`
}

type Binding struct {
	ID                 string  `json:"id"`
	DatasourceID       string  `json:"datasource_id"`
	SchemaName         string  `json:"schema_name"`
	RelationName       string  `json:"relation_name"`
	StableObjectID     *string `json:"stable_object_id,omitempty"`
	CatalogFingerprint *string `json:"catalog_fingerprint,omitempty"`
	Status             string  `json:"status"`
	Revision           int64   `json:"revision"`
}

type ColumnPermission struct {
	RelationEnrollmentID string `json:"relation_enrollment_id"`
	ColumnOrdinal        int    `json:"column_ordinal"`
	ColumnName           string `json:"column_name"`
	ColumnTypeDigest     string `json:"column_type_digest"`
	Usage                string `json:"usage"`
}

type Staging struct {
	TokenOrdinal   int     `json:"token_ordinal"`
	LegacyToken    string  `json:"legacy_token"`
	RequestedUsage string  `json:"requested_usage"`
	SourceCSVHash  string  `json:"source_csv_hash"`
	BindStatus     string  `json:"bind_status"`
	ErrorCode      *string `json:"error_code,omitempty"`
}

type Change struct {
	ID            string `json:"id"`
	Operation     string `json:"operation"`
	BroadensGrant bool   `json:"broadens_grant"`
	FromRevision  int64  `json:"from_revision,omitempty"`
	ToRevision    int64  `json:"to_revision,omitempty"`
}

// ReplayCase probes one exact table for one Agent and datasource. It is a
// metadata-only comparison and is never evidence that business SQL can run.
type ReplayCase struct {
	AgentID      string `json:"agent_id"`
	DatasourceID string `json:"datasource_id"`
	ObjectName   string `json:"object_name"`
}

type ReplayResult struct {
	ReplayCase
	Before string `json:"before"`
	After  string `json:"after"`
	Reason string `json:"reason"`
}

// Replay returns a conservative table-policy comparison for bounded exact
// object probes. Anything requiring column, binding, SQL, datasource or
// runtime state is unknown.
func Replay(current, desired Bundle, cases []ReplayCase) ([]ReplayResult, error) {
	if len(cases) > 32 || current.TenantID != desired.TenantID {
		return nil, fmt.Errorf("invalid replay scope")
	}
	if _, err := Canonical(current); err != nil {
		return nil, err
	}
	if _, err := Canonical(desired); err != nil {
		return nil, err
	}
	result := make([]ReplayResult, 0, len(cases))
	for _, probe := range cases {
		object, err := exactObject(probe)
		if err != nil {
			return nil, err
		}
		before := replayDecision(current, probe, object)
		after := replayDecision(desired, probe, object)
		result = append(result, ReplayResult{ReplayCase: probe, Before: before, After: after,
			Reason: "table policy only; SQL parser, column grants, dynamic rules, datasource and audit remain unchecked"})
	}
	return result, nil
}

func exactObject(probe ReplayCase) (model.ObjectRef, error) {
	if probe.AgentID == "" || probe.DatasourceID == "" || probe.ObjectName == "" || strings.TrimSpace(probe.ObjectName) != probe.ObjectName || strings.Contains(probe.ObjectName, "*") {
		return model.ObjectRef{}, fmt.Errorf("invalid replay case")
	}
	parts := strings.Split(probe.ObjectName, ".")
	if len(parts) == 1 && parts[0] != "" {
		return model.ObjectRef{Table: parts[0]}, nil
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return model.ObjectRef{Schema: parts[0], Table: parts[1]}, nil
	}
	return model.ObjectRef{}, fmt.Errorf("invalid replay object")
}

func replayDecision(bundle Bundle, probe ReplayCase, object model.ObjectRef) string {
	allow, deny := false, false
	for _, row := range bundle.Policies {
		if row.AgentID != probe.AgentID || row.DatasourceID != probe.DatasourceID {
			continue
		}
		if row.ObjectType != "table" || row.RelationBindingID != nil || row.RelationBinding != nil || row.Columns != nil || row.RowFilter != nil || len(row.ColumnPermissions) != 0 || len(row.ColumnStaging) != 0 || row.LegacyUnrepresentable {
			return "unknown"
		}
		if !policy.MatchesAnyTable([]string{row.ObjectName}, object) {
			continue
		}
		if row.Action == "deny" {
			deny = true
		} else if row.Action == "allow" {
			allow = true
		} else {
			return "unknown"
		}
	}
	if deny {
		return "deny"
	}
	if allow {
		return "candidate"
	}
	return "deny"
}

// FromModels builds a snapshot from tenant-filtered repository rows.
func FromModels(tenantID string, policies []model.Policy) Bundle {
	bundle := Bundle{SchemaVersion: SchemaVersion, TenantID: tenantID, Policies: make([]Entry, 0, len(policies))}
	for _, row := range policies {
		entry := Entry{
			ID: row.ID, AgentID: row.AgentID, DatasourceID: row.DatasourceID,
			ObjectType: row.ObjectType, ObjectName: row.ObjectName, Columns: row.Columns,
			RowFilter: row.RowFilter, Action: row.Action, Revision: row.Revision,
			LegacyUnrepresentable: row.LegacyUnrepresentable, RelationBindingID: row.RelationBindingID,
		}
		if row.RelationBinding != nil {
			v := row.RelationBinding
			entry.RelationBinding = &Binding{
				ID: v.ID, DatasourceID: v.DatasourceID, SchemaName: v.SchemaName,
				RelationName: v.RelationName, StableObjectID: v.StableObjectID,
				CatalogFingerprint: v.CatalogFingerprint, Status: v.Status, Revision: v.Revision,
			}
		}
		for _, v := range row.ColumnPermissions {
			entry.ColumnPermissions = append(entry.ColumnPermissions, ColumnPermission{
				RelationEnrollmentID: v.RelationEnrollmentID, ColumnOrdinal: v.ColumnOrdinal,
				ColumnName: v.ColumnName, ColumnTypeDigest: v.ColumnTypeDigest, Usage: v.Usage,
			})
		}
		for _, v := range row.ColumnStaging {
			entry.ColumnStaging = append(entry.ColumnStaging, Staging{
				TokenOrdinal: v.TokenOrdinal, LegacyToken: v.LegacyToken,
				RequestedUsage: v.RequestedUsage, SourceCSVHash: v.SourceCSVHash,
				BindStatus: v.BindStatus, ErrorCode: v.ErrorCode,
			})
		}
		bundle.Policies = append(bundle.Policies, entry)
	}
	return bundle
}

// Canonical sorts every collection, rejects ambiguous identities and returns
// the exact bytes covered by Digest. The input is never modified.
func Canonical(input Bundle) ([]byte, error) {
	if input.SchemaVersion != SchemaVersion || strings.TrimSpace(input.TenantID) == "" || len(input.Policies) > MaxPolicies {
		return nil, fmt.Errorf("unsupported policy bundle version, tenant or size")
	}
	bundle := Bundle{SchemaVersion: SchemaVersion, TenantID: input.TenantID, Policies: make([]Entry, len(input.Policies))}
	copy(bundle.Policies, input.Policies)
	sort.Slice(bundle.Policies, func(i, j int) bool { return bundle.Policies[i].ID < bundle.Policies[j].ID })
	for i := range bundle.Policies {
		v := &bundle.Policies[i]
		if v.ID == "" || v.AgentID == "" || v.DatasourceID == "" || v.ObjectName == "" || v.Revision < 0 ||
			(i > 0 && bundle.Policies[i-1].ID == v.ID) {
			return nil, fmt.Errorf("invalid or duplicate policy identity")
		}
		v.ColumnPermissions = append([]ColumnPermission{}, v.ColumnPermissions...)
		sort.Slice(v.ColumnPermissions, func(a, b int) bool {
			x, y := v.ColumnPermissions[a], v.ColumnPermissions[b]
			if x.RelationEnrollmentID != y.RelationEnrollmentID {
				return x.RelationEnrollmentID < y.RelationEnrollmentID
			}
			if x.ColumnOrdinal != y.ColumnOrdinal {
				return x.ColumnOrdinal < y.ColumnOrdinal
			}
			return x.Usage < y.Usage
		})
		v.ColumnStaging = append([]Staging{}, v.ColumnStaging...)
		sort.Slice(v.ColumnStaging, func(a, b int) bool {
			x, y := v.ColumnStaging[a], v.ColumnStaging[b]
			if x.TokenOrdinal != y.TokenOrdinal {
				return x.TokenOrdinal < y.TokenOrdinal
			}
			return x.RequestedUsage < y.RequestedUsage
		})
	}
	return json.Marshal(bundle)
}

func Digest(input Bundle) (string, error) {
	data, err := Canonical(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Diff is a metadata-only plan. It never claims that a business SQL statement
// will be authorized; that still requires the production execution pipeline.
func Diff(current, desired Bundle) ([]Change, error) {
	if current.TenantID != desired.TenantID {
		return nil, fmt.Errorf("bundle tenant mismatch")
	}
	currentBytes, err := Canonical(current)
	if err != nil {
		return nil, err
	}
	desiredBytes, err := Canonical(desired)
	if err != nil {
		return nil, err
	}
	var normalizedCurrent, normalizedDesired Bundle
	if err := json.Unmarshal(currentBytes, &normalizedCurrent); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(desiredBytes, &normalizedDesired); err != nil {
		return nil, err
	}
	current, desired = normalizedCurrent, normalizedDesired
	before := make(map[string]Entry, len(current.Policies))
	for _, v := range current.Policies {
		before[v.ID] = v
	}
	after := make(map[string]Entry, len(desired.Policies))
	for _, v := range desired.Policies {
		after[v.ID] = v
	}
	changes := make([]Change, 0)
	for id, newRow := range after {
		oldRow, exists := before[id]
		if !exists {
			changes = append(changes, Change{ID: id, Operation: "create", BroadensGrant: newRow.Action == "allow", ToRevision: newRow.Revision})
			continue
		}
		oldData, _ := json.Marshal(oldRow)
		newData, _ := json.Marshal(newRow)
		if string(oldData) != string(newData) {
			// A changed allow entry is conservatively marked for extra review.
			changes = append(changes, Change{ID: id, Operation: "update", BroadensGrant: newRow.Action == "allow", FromRevision: oldRow.Revision, ToRevision: newRow.Revision})
		}
	}
	for id, oldRow := range before {
		if _, exists := after[id]; !exists {
			changes = append(changes, Change{ID: id, Operation: "delete", BroadensGrant: oldRow.Action == "deny", FromRevision: oldRow.Revision})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes, nil
}
