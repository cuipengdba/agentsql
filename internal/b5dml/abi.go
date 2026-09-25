package b5dml

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/b5"
)

type Dialect uint8

const (
	DialectUnknown Dialect = iota
	DialectPostgreSQL
	DialectMySQL
)

type Action = b5.DMLAction

const (
	ActionUnknown = b5.ActionUnknown
	ActionInsert  = b5.ActionInsert
	ActionUpdate  = b5.ActionUpdate
	ActionDelete  = b5.ActionDelete
)

func validAction(action Action) bool {
	return action == ActionInsert || action == ActionUpdate || action == ActionDelete
}

// RelationIdentity is authority-bearing only as a complete catalog identity.
// Schema and Name are included for audit readability but never replace OIDs
// and CatalogFingerprint.
type RelationIdentity struct {
	DatasourceID       string
	DatabaseOID        uint32
	RelationOID        uint32
	RelationKind       byte
	Schema             string
	Name               string
	CatalogFingerprint string
}

func (relation RelationIdentity) complete() bool {
	return relation.DatasourceID != "" && relation.DatabaseOID != 0 &&
		relation.RelationOID != 0 && relation.RelationKind != 0 &&
		relation.Schema != "" && relation.Name != "" &&
		relation.CatalogFingerprint != ""
}

func (relation RelationIdentity) key() string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s",
		relation.DatasourceID, relation.DatabaseOID, relation.RelationOID,
		relation.RelationKind, relation.Schema, relation.Name,
		relation.CatalogFingerprint)
}

// ColumnIdentity binds an attnum to its relation and type identity. Positive
// attnums are ordinary user columns, zero is PostgreSQL's whole-row Var, and
// negative attnums are system columns. The latter two are representable as
// references but are rejected by the v0.4 contract.
type ColumnIdentity struct {
	Relation     RelationIdentity
	Attnum       int16
	Name         string
	TypeOID      uint32
	TypeModifier int32
	CollationOID uint32
}

func (column ColumnIdentity) completeUserColumn() bool {
	return column.Relation.complete() && column.Attnum > 0 &&
		column.Name != "" && column.TypeOID != 0
}

func (column ColumnIdentity) key() string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%d\x00%d", column.Relation.key(),
		column.Attnum, column.Name, column.TypeOID, column.TypeModifier,
		column.CollationOID)
}

type WriteTargetKind uint8

const (
	WriteTargetUnknown WriteTargetKind = iota
	WriteTargetColumn
	// WriteTargetRow is the single relation-level target required by DELETE.
	// DELETE does not manufacture a write to every column.
	WriteTargetRow
)

type WriteSource uint8

const (
	WriteSourceUnknown WriteSource = iota
	WriteSourceExplicit
	WriteSourceImplicitNull
)

type WriteTarget struct {
	Kind     WriteTargetKind
	Relation RelationIdentity
	Column   ColumnIdentity
	Source   WriteSource
}

func ColumnWrite(column ColumnIdentity, source WriteSource) WriteTarget {
	return WriteTarget{Kind: WriteTargetColumn, Relation: column.Relation, Column: column, Source: source}
}

func RowDelete(relation RelationIdentity) WriteTarget {
	return WriteTarget{Kind: WriteTargetRow, Relation: relation, Source: WriteSourceExplicit}
}

func (target WriteTarget) key() string {
	if target.Kind == WriteTargetRow {
		return fmt.Sprintf("row\x00%s", target.Relation.key())
	}
	return fmt.Sprintf("column\x00%s", target.Column.key())
}

type ReferenceKind uint8

const (
	ReferenceUnknown ReferenceKind = iota
	ReferenceColumn
	ReferenceWholeRow
	// ReferenceRowCount represents count(table_alias). count(*) does not carry
	// a relation composite value and is not represented by this kind.
	ReferenceRowCount
	ReferenceRecord
	ReferenceComposite
	ReferenceSystemColumn
)

type ReferenceSite uint8

const (
	ReferenceSiteUnknown ReferenceSite = iota
	ReferenceWhere
	ReferenceJoin
	ReferenceUsing
	ReferenceExpression
	ReferenceSubquery
	ReferenceForeignKey
	ReferenceConstraint
	ReferenceInternalRead
	ReferenceConflictCheck
	ReferenceReturning
)

type Reference struct {
	Kind       ReferenceKind
	Site       ReferenceSite
	Relation   RelationIdentity
	Column     ColumnIdentity
	SystemName string
}

func ColumnReference(column ColumnIdentity, site ReferenceSite) Reference {
	return Reference{Kind: ReferenceColumn, Site: site, Relation: column.Relation, Column: column}
}

func (reference Reference) key() string {
	return fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s", reference.Kind,
		reference.Site, reference.Relation.key(), reference.Column.key(),
		reference.SystemName)
}

func supportedReference(reference Reference) bool {
	return reference.Kind == ReferenceColumn && reference.Column.completeUserColumn() &&
		reference.Site >= ReferenceWhere && reference.Site <= ReferenceReturning
}

// BinderAttestation is independently built and hashed for one PostgreSQL
// major. All five supported majors enter the proof even though one server
// major is selected for a request.
type BinderAttestation struct {
	ServerMajor      int
	ABI              string
	BuildHash        string
	ExtensionHash    string
	NodeManifestHash string
	AllowlistHash    string
}

func (attestation BinderAttestation) complete() bool {
	return attestation.ServerMajor >= 14 && attestation.ServerMajor <= 18 &&
		attestation.ABI == BinderABI && attestation.BuildHash != "" &&
		attestation.ExtensionHash != "" && attestation.NodeManifestHash != "" &&
		attestation.AllowlistHash != ""
}

func (attestation BinderAttestation) key() string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		attestation.ServerMajor, attestation.ABI, attestation.BuildHash,
		attestation.ExtensionHash, attestation.NodeManifestHash,
		attestation.AllowlistHash)
}
