// Package b5dml contains the feature-off B5 S5a DML authorization, statement
// semantics, closure, and native-BEGIN cleanup contracts.
//
// This package is intentionally independent from columnauth. columnauth is a
// SELECT-only output/reference and mask contract; a write path must never
// consume its ProtectionPlan. b5dml also has no database driver, migration,
// feature flag, or production entry-point integration. The PostgreSQL binder
// and persisted policy schema belong to S5b and S1b respectively.
package b5dml

import "github.com/cuipengdba/agentsql/internal/b5"

const (
	// BinderABI is the reviewed name reserved for the future PostgreSQL DML
	// binder implemented by the isolated, feature-off S5b businessdb adapter.
	BinderABI = "agentsql-binder-dml-1"

	// AuthorizationContractSchema identifies this non-persisted S5a canonical
	// contract. S1b must assign the formal proof and storage schema versions.
	AuthorizationContractSchema = "agentsql.b5.dml-authorization-contract.s5a.v1"
	BeginCleanupProofSchema     = b5.BeginCleanupProofSchemaID
	BeginCleanupProofVersion    = b5.BeginCleanupProofVersion

	// GrantSchemaDecision records the reviewed conclusion without pretending
	// that S5a has created the S1b persistence schema.
	GrantSchemaDecision = b5.DMLGrantProofSchemaID
)
