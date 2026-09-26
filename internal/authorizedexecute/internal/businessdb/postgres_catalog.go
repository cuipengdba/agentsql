package businessdb

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const postgresCatalogFingerprintVersion = "agentsql-pg-catalog-v1"

// PostgresCatalogBudget is implemented by authorizedexecute.Budget. Keeping
// the interface here prevents the database capability package from importing
// its parent while ensuring the binder and catalog share one request counter.
type PostgresCatalogBudget interface {
	ChargeRelations(int) error
	CheckViewDepth(int) error
	ChargeCatalogRoundTrips(int) error
	ChargeDefinitionBytes(int) error
	ChargeBinderBytes(int) error
	ChargeCatalogBytes(int) error
	ChargeCatalogRows(int) error
	ChargeColumnMetadata(int) error
	ChargeNodes(int) error
	ChargeEdges(int) error
	ChargePaths(int) error
	ChargeWork(int) error
}

type PostgresRelationIdentity struct {
	DatabaseOID  uint32
	OID          uint32
	NamespaceOID uint32
	Schema       string
	Name         string
	Kind         byte
	Persistence  byte
	RelationAM   uint32
	OfType       uint32
	IsPartition  bool
	ViewDepth    int
	Qualified    string
	Definition   string
}

type PostgresColumnIdentity struct {
	RelationOID uint32
	Attnum      int16
	Name        string
	TypeOID     uint32
	Typmod      int32
	Collation   uint32
	NotNull     bool
	Identity    byte
	Generated   byte
}

type PostgresDependency struct {
	ClassID, ObjectID, ObjectSubID          uint32
	RefClassID, RefObjectID, RefObjectSubID uint32
	Type                                    byte
}

type PostgresObjectUse struct {
	Kind string `json:"kind"`
	OID  uint32 `json:"oid"`
}

type PostgresOIDAllowlist struct {
	Types            []uint32 `json:"types"`
	Functions        []uint32 `json:"functions"`
	Operators        []uint32 `json:"operators"`
	Casts            []uint32 `json:"casts"`
	RelationAMs      []uint32 `json:"relation_ams"`
	IndexAMs         []uint32 `json:"index_ams"`
	Opclasses        []uint32 `json:"opclasses"`
	Opfamilies       []uint32 `json:"opfamilies"`
	AMOperators      []uint32 `json:"am_operators"`
	AMProcedures     []uint32 `json:"am_procedures"`
	TypeIOFunctions  []uint32 `json:"type_io_functions"`
	Collations       []uint32 `json:"collations"`
	AggregateSupport []uint32 `json:"aggregate_support"`
	WindowSupport    []uint32 `json:"window_support"`
}

func validatePostgresObjectAllowlist(objects []PostgresObjectUse, allow PostgresOIDAllowlist) error {
	sets := map[string]map[uint32]struct{}{
		"type":      oidSet(allow.Types),
		"function":  oidSet(allow.Functions),
		"operator":  oidSet(allow.Operators),
		"cast":      oidSet(allow.Casts),
		"collation": oidSet(allow.Collations),
		"aggregate": oidSet(allow.Functions),
		"window":    oidSet(allow.Functions),
	}
	for _, object := range objects {
		allowed, known := sets[object.Kind]
		if !known || object.OID == 0 {
			return catalogAuthError("AUTH_EXPRESSION_IDENTITY_UNSUPPORTED")
		}
		if _, ok := allowed[object.OID]; !ok {
			return catalogAuthError("AUTH_EXPRESSION_IDENTITY_UNSUPPORTED")
		}
	}
	return nil
}

func oidSet(values []uint32) map[uint32]struct{} {
	result := make(map[uint32]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

type PostgresCatalogFrame struct {
	ServerVersion int
	DatabaseOID   uint32
	Relations     []PostgresRelationIdentity
	Columns       []PostgresColumnIdentity
	Dependencies  []PostgresDependency
	Fingerprint   string
}

type postgresCatalogFinding struct {
	OwnerOID  uint32
	Kind      string
	ObjectOID uint32
}

func scanPostgresCatalog(
	ctx context.Context,
	tx pgx.Tx,
	relationOIDs []uint32,
	viewDepth map[uint32]int,
	objects []PostgresObjectUse,
	allow PostgresOIDAllowlist,
	budget PostgresCatalogBudget,
) (PostgresCatalogFrame, error) {
	if ctx == nil || tx == nil || budget == nil {
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	oids := uniqueSortedOIDs(relationOIDs)
	if err := budget.ChargeRelations(len(oids)); err != nil {
		return PostgresCatalogFrame{}, err
	}
	for _, depth := range viewDepth {
		if err := budget.CheckViewDepth(depth); err != nil {
			return PostgresCatalogFrame{}, err
		}
	}

	var frame PostgresCatalogFrame
	var typeOIDs, collationOIDs []uint32
	var err error
	if len(oids) == 0 {
		frame, err = readPostgresSourceFreeFrame(ctx, tx, budget)
	} else {
		frame, typeOIDs, collationOIDs, err = readPostgresIdentities(ctx, tx, oids, viewDepth, budget)
	}
	if err != nil {
		return PostgresCatalogFrame{}, err
	}
	objectKinds := make(map[string][]uint32)
	for _, object := range objects {
		if object.OID != 0 {
			objectKinds[object.Kind] = append(objectKinds[object.Kind], object.OID)
		}
	}
	typeOIDs = append(typeOIDs, objectKinds["type"]...)
	collationOIDs = append(collationOIDs, objectKinds["collation"]...)

	findings, err := scanPostgresImplicitObjects(ctx, tx, oids, uniqueSortedOIDs(typeOIDs), uniqueSortedOIDs(collationOIDs), objectKinds, allow, budget)
	if err != nil {
		return PostgresCatalogFrame{}, err
	}
	if len(findings) != 0 {
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_IMPLICIT_OBJECT_UNSUPPORTED")
	}
	if len(oids) != 0 {
		if err := scanPostgresRelationShapes(ctx, tx, oids, allow.RelationAMs, budget); err != nil {
			return PostgresCatalogFrame{}, err
		}
	}
	frame.Fingerprint = fingerprintPostgresCatalog(frame)
	return frame, nil
}

func readPostgresSourceFreeFrame(ctx context.Context, tx pgx.Tx, budget PostgresCatalogBudget) (PostgresCatalogFrame, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, err
	}
	var frame PostgresCatalogFrame
	if err := tx.QueryRow(ctx, `SELECT current_setting('server_version_num')::int,oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&frame.ServerVersion, &frame.DatabaseOID); err != nil || frame.ServerVersion == 0 || frame.DatabaseOID == 0 {
		return PostgresCatalogFrame{}, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}
	if err := budget.ChargeCatalogRows(1); err != nil {
		return PostgresCatalogFrame{}, err
	}
	if err := budget.ChargeCatalogBytes(8); err != nil {
		return PostgresCatalogFrame{}, err
	}
	return frame, nil
}

func readPostgresIdentities(ctx context.Context, tx pgx.Tx, oids []uint32, depths map[uint32]int, budget PostgresCatalogBudget) (PostgresCatalogFrame, []uint32, []uint32, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, nil, nil, err
	}
	const relationSQL = `SELECT current_setting('server_version_num')::int,
  d.oid, c.oid, n.oid, n.nspname, c.relname, c.relkind::text,
  c.relpersistence::text, c.relam, c.reloftype, c.relispartition,
  pg_catalog.format('%I.%I',n.nspname,c.relname),
  CASE WHEN c.relkind IN ('v','m') THEN r.ev_action::text ELSE NULL END
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
CROSS JOIN pg_catalog.pg_database d
LEFT JOIN pg_catalog.pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN' AND r.ev_type='1'
WHERE d.datname=current_database() AND c.oid=ANY($1::oid[])
ORDER BY c.oid,r.oid`
	rows, err := tx.Query(ctx, relationSQL, oidArrayLiteral(oids))
	if err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL relation identities", err)
	}
	defer rows.Close()
	frame := PostgresCatalogFrame{Relations: make([]PostgresRelationIdentity, 0, len(oids))}
	seen := make(map[uint32]struct{}, len(oids))
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		var relation PostgresRelationIdentity
		var kind, persistence string
		var definition *string
		if err := rows.Scan(&frame.ServerVersion, &frame.DatabaseOID, &relation.OID, &relation.NamespaceOID,
			&relation.Schema, &relation.Name, &kind, &persistence, &relation.RelationAM,
			&relation.OfType, &relation.IsPartition, &relation.Qualified, &definition); err != nil {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		if _, duplicate := seen[relation.OID]; duplicate || len(kind) != 1 || len(persistence) != 1 {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		seen[relation.OID] = struct{}{}
		relation.DatabaseOID = frame.DatabaseOID
		relation.Kind, relation.Persistence = kind[0], persistence[0]
		relation.ViewDepth = depths[relation.OID]
		if (relation.Kind == 'v' || relation.Kind == 'm') && (definition == nil || *definition == "") {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		if definition != nil {
			relation.Definition = *definition
			if err := budget.ChargeDefinitionBytes(len(relation.Definition)); err != nil {
				return PostgresCatalogFrame{}, nil, nil, err
			}
		}
		if err := budget.ChargeCatalogBytes(relationRecordBytes(relation)); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		frame.Relations = append(frame.Relations, relation)
	}
	if err := rows.Err(); err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL relation identities", err)
	}
	if len(frame.Relations) != len(oids) {
		return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
	}

	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, nil, nil, err
	}
	const columnSQL = `SELECT a.attrelid,a.attnum,a.attname,a.atttypid,a.atttypmod,
  a.attcollation,a.attnotnull,a.attidentity::text,a.attgenerated::text
FROM pg_catalog.pg_attribute a
WHERE a.attrelid=ANY($1::oid[]) AND a.attnum>0 AND NOT a.attisdropped
ORDER BY a.attrelid,a.attnum`
	rows, err = tx.Query(ctx, columnSQL, oidArrayLiteral(oids))
	if err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL column identities", err)
	}
	defer rows.Close()
	var typeOIDs, collationOIDs []uint32
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		if err := budget.ChargeColumnMetadata(1); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		var column PostgresColumnIdentity
		var identity, generated string
		if err := rows.Scan(&column.RelationOID, &column.Attnum, &column.Name, &column.TypeOID,
			&column.Typmod, &column.Collation, &column.NotNull, &identity, &generated); err != nil {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		if column.Attnum <= 0 || len(identity) > 1 || len(generated) > 1 {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		if identity != "" {
			column.Identity = identity[0]
		}
		if generated != "" {
			column.Generated = generated[0]
		}
		if err := budget.ChargeCatalogBytes(columnRecordBytes(column)); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		frame.Columns = append(frame.Columns, column)
		typeOIDs = append(typeOIDs, column.TypeOID)
		if column.Collation != 0 {
			collationOIDs = append(collationOIDs, column.Collation)
		}
	}
	if err := rows.Err(); err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL column identities", err)
	}

	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return PostgresCatalogFrame{}, nil, nil, err
	}
	const dependencySQL = `SELECT classid,objid,objsubid,refclassid,refobjid,refobjsubid,deptype::text
FROM pg_catalog.pg_depend
WHERE objid=ANY($1::oid[]) OR refobjid=ANY($1::oid[])
ORDER BY classid,objid,objsubid,refclassid,refobjid,refobjsubid,deptype`
	rows, err = tx.Query(ctx, dependencySQL, oidArrayLiteral(oids))
	if err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL dependencies", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		if err := budget.ChargeEdges(1); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		var dependency PostgresDependency
		var dependencyType string
		if err := rows.Scan(&dependency.ClassID, &dependency.ObjectID, &dependency.ObjectSubID,
			&dependency.RefClassID, &dependency.RefObjectID, &dependency.RefObjectSubID, &dependencyType); err != nil || len(dependencyType) != 1 {
			return PostgresCatalogFrame{}, nil, nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		dependency.Type = dependencyType[0]
		if err := budget.ChargeCatalogBytes(29); err != nil {
			return PostgresCatalogFrame{}, nil, nil, err
		}
		frame.Dependencies = append(frame.Dependencies, dependency)
	}
	if err := rows.Err(); err != nil {
		return PostgresCatalogFrame{}, nil, nil, postgresDatabaseError(ctx, DBStageMetadata, "read PostgreSQL dependencies", err)
	}
	return frame, typeOIDs, collationOIDs, nil
}

func scanPostgresImplicitObjects(ctx context.Context, tx pgx.Tx, relationOIDs, typeOIDs, collationOIDs []uint32, objectKinds map[string][]uint32, allow PostgresOIDAllowlist, budget PostgresCatalogBudget) ([]postgresCatalogFinding, error) {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return nil, err
	}
	const query = `WITH target(relid) AS (SELECT unnest($1::oid[])), idx AS (
 SELECT i.indexrelid,i.indrelid,i.indclass FROM pg_catalog.pg_index i JOIN target t ON t.relid=i.indrelid
), findings(owner_oid,kind,object_oid) AS (
 SELECT t.tgrelid,'trigger',t.oid FROM pg_catalog.pg_trigger t JOIN target x ON x.relid=t.tgrelid WHERE NOT t.tgisinternal
 UNION ALL SELECT d.adrelid,CASE WHEN a.attgenerated<>'' THEN 'generated' ELSE 'column_default' END,d.oid
 FROM pg_catalog.pg_attrdef d JOIN pg_catalog.pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum JOIN target x ON x.relid=d.adrelid
 UNION ALL SELECT c.oid,'rls',COALESCE(p.oid,c.oid) FROM pg_catalog.pg_class c JOIN target x ON x.relid=c.oid LEFT JOIN pg_catalog.pg_policy p ON p.polrelid=c.oid
 WHERE c.relrowsecurity OR c.relforcerowsecurity OR p.oid IS NOT NULL
 UNION ALL SELECT r.ev_class,'rule',r.oid FROM pg_catalog.pg_rewrite r JOIN pg_catalog.pg_class c ON c.oid=r.ev_class JOIN target x ON x.relid=r.ev_class
	WHERE NOT (r.rulename='_RETURN' AND c.relkind IN ('v','m') AND r.ev_type='1')
 UNION ALL SELECT q.conrelid,'constraint:'::text||q.contype::text,q.oid FROM pg_catalog.pg_constraint q JOIN target x ON x.relid=q.conrelid WHERE q.contype::text IN ('c'::text,'x'::text)
 UNION ALL SELECT q.conrelid,'foreign_key',q.oid FROM pg_catalog.pg_constraint q JOIN target x ON q.conrelid=x.relid OR q.confrelid=x.relid WHERE q.contype='f'
 UNION ALL SELECT i.indrelid,CASE WHEN i.indexprs IS NOT NULL THEN 'expression_index' ELSE 'partial_index' END,i.indexrelid
 FROM pg_catalog.pg_index i JOIN target x ON x.relid=i.indrelid WHERE i.indexprs IS NOT NULL OR i.indpred IS NOT NULL
 UNION ALL SELECT c.oid,'relation_am',c.relam FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.relam<>0 AND NOT (c.relam=ANY($2::oid[]))
 UNION ALL SELECT x.indrelid,'index_am',ic.relam FROM idx x JOIN pg_catalog.pg_class ic ON ic.oid=x.indexrelid WHERE NOT (ic.relam=ANY($3::oid[]))
 UNION ALL SELECT x.indrelid,'opclass',oc.oid FROM idx x CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid) JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid WHERE NOT (oc.oid=ANY($4::oid[]))
 UNION ALL SELECT x.indrelid,'opfamily',oc.opcfamily FROM idx x CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid) JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid WHERE NOT (oc.opcfamily=ANY($5::oid[]))
 UNION ALL SELECT x.indrelid,'amop',o.oid FROM idx x CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid) JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid JOIN pg_catalog.pg_amop o ON o.amopfamily=oc.opcfamily WHERE NOT (o.oid=ANY($6::oid[]))
 UNION ALL SELECT x.indrelid,'amproc',p.oid FROM idx x CROSS JOIN LATERAL unnest(x.indclass::oid[]) v(opclass_oid) JOIN pg_catalog.pg_opclass oc ON oc.oid=v.opclass_oid JOIN pg_catalog.pg_amproc p ON p.amprocfamily=oc.opcfamily WHERE NOT (p.oid=ANY($7::oid[]))
 UNION ALL SELECT 0::oid,'type_io'::text,f::oid FROM pg_catalog.pg_type t CROSS JOIN LATERAL unnest(ARRAY[t.typinput,t.typoutput,t.typreceive,t.typsend]) f WHERE t.oid=ANY($8::oid[]) AND f::oid<>0::oid AND NOT (f::oid=ANY($9::oid[]))
 UNION ALL SELECT 0::oid,'collation'::text,c.oid FROM pg_catalog.pg_collation c WHERE c.oid=ANY($10::oid[]) AND (c.collprovider::text NOT IN ('c'::text,'d'::text) OR NOT (c.oid=ANY($11::oid[])))
 UNION ALL SELECT 0::oid,'aggregate_support'::text,f::oid FROM pg_catalog.pg_aggregate a CROSS JOIN LATERAL unnest(ARRAY[a.aggtransfn,a.aggfinalfn,a.aggcombinefn,a.aggserialfn,a.aggdeserialfn,a.aggmtransfn,a.aggminvtransfn,a.aggmfinalfn]) f WHERE a.aggfnoid=ANY($12::oid[]) AND f::oid<>0::oid AND NOT (f::oid=ANY($13::oid[]))
 UNION ALL SELECT 0::oid,'window_support'::text,p.prosupport::oid FROM pg_catalog.pg_proc p WHERE p.oid=ANY($14::oid[]) AND p.prosupport::oid<>0::oid AND NOT (p.prosupport::oid=ANY($15::oid[]))
) SELECT owner_oid,kind,object_oid FROM findings ORDER BY 1,2,3`
	args := []any{oidArrayLiteral(relationOIDs), oidArrayLiteral(allow.RelationAMs), oidArrayLiteral(allow.IndexAMs), oidArrayLiteral(allow.Opclasses), oidArrayLiteral(allow.Opfamilies), oidArrayLiteral(allow.AMOperators), oidArrayLiteral(allow.AMProcedures), oidArrayLiteral(typeOIDs), oidArrayLiteral(allow.TypeIOFunctions), oidArrayLiteral(collationOIDs), oidArrayLiteral(allow.Collations), oidArrayLiteral(uniqueSortedOIDs(objectKinds["aggregate"])), oidArrayLiteral(allow.AggregateSupport), oidArrayLiteral(uniqueSortedOIDs(objectKinds["window"])), oidArrayLiteral(allow.WindowSupport)}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "scan PostgreSQL implicit objects", err)
	}
	defer rows.Close()
	var findings []postgresCatalogFinding
	for rows.Next() {
		if err := budget.ChargeCatalogRows(1); err != nil {
			return nil, err
		}
		var finding postgresCatalogFinding
		if err := rows.Scan(&finding.OwnerOID, &finding.Kind, &finding.ObjectOID); err != nil {
			return nil, catalogAuthError("AUTH_CATALOG_INCOMPLETE")
		}
		if err := budget.ChargeCatalogBytes(8 + len(finding.Kind)); err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresDatabaseError(ctx, DBStageMetadata, "scan PostgreSQL implicit objects", err)
	}
	return findings, nil
}

func scanPostgresRelationShapes(ctx context.Context, tx pgx.Tx, relationOIDs, relationAMs []uint32, budget PostgresCatalogBudget) error {
	if err := budget.ChargeCatalogRoundTrips(1); err != nil {
		return err
	}
	const query = `WITH target(relid) AS (SELECT unnest($1::oid[]))
SELECT c.oid,'relispartition',c.oid FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.relispartition
UNION ALL SELECT p.partrelid,'partitioned_table',p.partrelid FROM pg_catalog.pg_partitioned_table p JOIN target t ON t.relid=p.partrelid
UNION ALL SELECT i.inhrelid,'inherits_parent',i.inhparent FROM pg_catalog.pg_inherits i JOIN target t ON t.relid=i.inhrelid
UNION ALL SELECT i.inhparent,'inherits_child',i.inhrelid FROM pg_catalog.pg_inherits i JOIN target t ON t.relid=i.inhparent
UNION ALL SELECT c.oid,'typed_table',c.reloftype FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.reloftype<>0
UNION ALL SELECT c.oid,'relation_am',c.relam FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.relam<>0 AND NOT(c.relam=ANY($2::oid[]))
UNION ALL SELECT c.oid,'relation_kind',c.oid FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid WHERE c.relkind::text NOT IN ('r'::text,'v'::text,'m'::text) OR c.relpersistence::text<>'p'::text
UNION ALL SELECT c.oid,'system_schema',c.relnamespace FROM pg_catalog.pg_class c JOIN target t ON t.relid=c.oid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('pg_catalog','information_schema') OR n.nspname LIKE 'pg_toast%'
ORDER BY 1,2,3`
	rows, err := tx.Query(ctx, query, oidArrayLiteral(relationOIDs), oidArrayLiteral(relationAMs))
	if err != nil {
		return postgresDatabaseError(ctx, DBStageMetadata, "scan PostgreSQL relation shapes", err)
	}
	defer rows.Close()
	if rows.Next() {
		return catalogAuthError("AUTH_RELATION_SHAPE_UNSUPPORTED")
	}
	if err := rows.Err(); err != nil {
		return postgresDatabaseError(ctx, DBStageMetadata, "scan PostgreSQL relation shapes", err)
	}
	return nil
}

type catalogReasonError struct{ reason string }

func (err *catalogReasonError) Error() string               { return err.reason }
func (err *catalogReasonError) AuthorizationReason() string { return err.reason }
func catalogAuthError(reason string) error                  { return &catalogReasonError{reason: reason} }

func oidArrayLiteral(oids []uint32) string {
	if len(oids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, oid := range oids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(oid), 10))
	}
	b.WriteByte('}')
	return b.String()
}

func uniqueSortedOIDs(values []uint32) []uint32 {
	result := append([]uint32(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	write := 0
	for _, value := range result {
		if value == 0 || (write > 0 && result[write-1] == value) {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func fingerprintPostgresCatalog(frame PostgresCatalogFrame) string {
	h := sha256.New()
	writeCanonicalString(h, postgresCatalogFingerprintVersion)
	writeCanonicalUint(h, uint64(frame.ServerVersion))
	writeCanonicalUint(h, uint64(frame.DatabaseOID))
	for _, r := range frame.Relations {
		writeCanonicalUint(h, uint64(r.OID))
		writeCanonicalUint(h, uint64(r.NamespaceOID))
		writeCanonicalString(h, r.Schema)
		writeCanonicalString(h, r.Name)
		h.Write([]byte{r.Kind, r.Persistence})
		writeCanonicalUint(h, uint64(r.RelationAM))
		writeCanonicalUint(h, uint64(r.OfType))
		writeCanonicalBool(h, r.IsPartition)
		writeCanonicalString(h, r.Definition)
	}
	for _, c := range frame.Columns {
		writeCanonicalUint(h, uint64(c.RelationOID))
		writeCanonicalUint(h, uint64(c.Attnum))
		writeCanonicalString(h, c.Name)
		writeCanonicalUint(h, uint64(c.TypeOID))
		writeCanonicalUint(h, uint64(uint32(c.Typmod)))
		writeCanonicalUint(h, uint64(c.Collation))
		writeCanonicalBool(h, c.NotNull)
		h.Write([]byte{c.Identity, c.Generated})
	}
	for _, d := range frame.Dependencies {
		writeCanonicalUint(h, uint64(d.ClassID))
		writeCanonicalUint(h, uint64(d.ObjectID))
		writeCanonicalUint(h, uint64(d.ObjectSubID))
		writeCanonicalUint(h, uint64(d.RefClassID))
		writeCanonicalUint(h, uint64(d.RefObjectID))
		writeCanonicalUint(h, uint64(d.RefObjectSubID))
		h.Write([]byte{d.Type})
	}
	return postgresCatalogFingerprintVersion + ":" + hex.EncodeToString(h.Sum(nil))
}

type canonicalWriter interface{ Write([]byte) (int, error) }

func writeCanonicalUint(w canonicalWriter, value uint64) {
	var b [10]byte
	n := binary.PutUvarint(b[:], value)
	_, _ = w.Write([]byte{1, 1})
	_, _ = w.Write(b[:n])
}
func writeCanonicalString(w canonicalWriter, value string) {
	var b [10]byte
	n := binary.PutUvarint(b[:], uint64(len(value)))
	_, _ = w.Write([]byte{2, 1})
	_, _ = w.Write(b[:n])
	_, _ = w.Write([]byte(value))
}
func writeCanonicalBool(w canonicalWriter, value bool) {
	b := byte(0)
	if value {
		b = 1
	}
	_, _ = w.Write([]byte{3, 1, b})
}
func relationRecordBytes(r PostgresRelationIdentity) int {
	return 32 + len(r.Schema) + len(r.Name) + len(r.Qualified) + len(r.Definition)
}
func columnRecordBytes(c PostgresColumnIdentity) int { return 32 + len(c.Name) }

var _ interface{ AuthorizationReason() string } = (*catalogReasonError)(nil)
