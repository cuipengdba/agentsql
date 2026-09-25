package businessdb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type BinderCapabilityAuditRecord struct {
	At                 time.Time
	Stage              string
	Outcome            string
	Reason             string
	Provider           string
	ServerMajor        int
	DatabaseOID        uint32
	ExtensionAvailable bool
	ExtensionInstalled bool
	ExtensionVersion   string
	ABI                string
	BuildHash          string
	ExtensionHash      string
	NodeManifestHash   string
	AllowlistHash      string
	CapabilityDigest   string
	RuntimeRoleOID     uint32
}

type BinderCapabilityAuditSink interface {
	RecordBinderCapabilityAudit(context.Context, BinderCapabilityAuditRecord) error
}

type BinderCapabilityAuditFunc func(context.Context, BinderCapabilityAuditRecord) error

func (fn BinderCapabilityAuditFunc) RecordBinderCapabilityAudit(ctx context.Context, record BinderCapabilityAuditRecord) error {
	return fn(ctx, record)
}

type EasyDeployBinderBootstrapOptions struct {
	CreateIfAvailable bool
	RuntimeRole       string
	Audit             BinderCapabilityAuditSink
}

const (
	postgresProviderSelfManaged = "self_managed"
	postgresProviderAWSManaged  = "aws_rds_or_aurora"
	postgresProviderAzure       = "azure_flexible_server"
	postgresProviderCloudSQL    = "gcp_cloud_sql"
)

// ProbeEasyDeployBinderCapabilitiesAudited records the filesystem inventory
// result separately from attestation validation. An audit failure is fail
// closed; raw SQL and object names are deliberately absent from the record.
func (executor *PostgresExecutor) ProbeEasyDeployBinderCapabilitiesAudited(ctx context.Context, expectation NativeCapabilityExpectation, budget PostgresCatalogBudget, audit BinderCapabilityAuditSink) (BinderCapabilityHandshake, error) {
	if audit == nil {
		return BinderCapabilityHandshake{}, NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	handshake, err := executor.ProbeEasyDeployBinderCapabilities(ctx, expectation, budget)
	if err != nil {
		_ = recordBinderAudit(ctx, audit, BinderCapabilityAuditRecord{Stage: "probe", Outcome: "error", Reason: binderAuditReason(err)})
		return BinderCapabilityHandshake{}, err
	}
	if err := recordBinderAudit(ctx, audit, BinderCapabilityAuditRecord{Stage: "probe", Outcome: "complete",
		Reason: handshake.NativeHealth, ServerMajor: handshake.Closed.ServerMajor, DatabaseOID: handshake.Closed.DatabaseOID,
		ExtensionAvailable: handshake.NativeFilesAvailable, ExtensionInstalled: handshake.NativeInstalled}); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	outcome := "fallback"
	if handshake.Native.Available {
		outcome = "accepted"
	}
	if err := recordBinderAudit(ctx, audit, BinderCapabilityAuditRecord{Stage: "validate", Outcome: outcome,
		Reason: handshake.NativeHealth, ServerMajor: handshake.Closed.ServerMajor, DatabaseOID: handshake.Closed.DatabaseOID,
		ExtensionAvailable: handshake.NativeFilesAvailable, ExtensionInstalled: handshake.NativeInstalled,
		ExtensionVersion: handshake.Native.ExtensionVersion, ABI: handshake.Native.ABI,
		BuildHash: handshake.Native.BuildHash, ExtensionHash: handshake.Native.ExtensionHash,
		NodeManifestHash: handshake.Native.NodeManifestHash, AllowlistHash: handshake.Native.AllowlistHash,
		CapabilityDigest: handshake.Native.Digest}); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	return handshake, nil
}

// BootstrapEasyDeployBinder may create only an extension whose supporting
// files are already visible through pg_available_extension_versions. It never
// transfers a shared library and refuses CREATE on recognized managed services.
// The method is not wired to a production flag in v0.4.
func (executor *PostgresExecutor) BootstrapEasyDeployBinder(ctx context.Context, options EasyDeployBinderBootstrapOptions, budget PostgresCatalogBudget) (BinderCapabilityHandshake, error) {
	if executor == nil || executor.pool == nil || ctx == nil || budget == nil || options.Audit == nil {
		return BinderCapabilityHandshake{}, NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	connection, err := executor.pool.Acquire(ctx)
	if err != nil {
		return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageAcquire, "acquire PostgreSQL binder bootstrap connection", err)
	}
	defer connection.Release()
	tx, err := connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageBeginTx, "begin PostgreSQL binder bootstrap", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := setPostgresCatalogTimeout(ctx, tx, executor.timeout); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	var serverVersion int
	var databaseOID, runtimeRoleOID uint32
	var versionText string
	var hasRDSSetting, hasAzureSetting, hasCloudSQLSetting bool
	const environmentSQL = `SELECT current_setting('server_version_num')::integer,
  (SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())::oid,
  version(),
  EXISTS(SELECT 1 FROM pg_catalog.pg_settings WHERE name LIKE 'rds.%'),
  EXISTS(SELECT 1 FROM pg_catalog.pg_settings WHERE name LIKE 'azure.%'),
  EXISTS(SELECT 1 FROM pg_catalog.pg_settings WHERE name LIKE 'cloudsql.%')`
	if err := tx.QueryRow(ctx, environmentSQL).Scan(&serverVersion, &databaseOID, &versionText, &hasRDSSetting, &hasAzureSetting, &hasCloudSQLSetting); err != nil {
		return BinderCapabilityHandshake{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	major := serverVersion / 10000
	expectation, supported := PostgresBinderNativeExpectation(major)
	if !supported {
		return BinderCapabilityHandshake{}, NewCapabilityFailure(BinderCodeModeUnsupported)
	}
	provider := classifyPostgresProvider(versionText, hasRDSSetting, hasAzureSetting, hasCloudSQLSetting)
	var available, exactVersion, installed, installedExactVersion, schemaSafe bool
	const inventorySQL = `SELECT
  EXISTS(SELECT 1 FROM pg_catalog.pg_available_extensions WHERE name='agentsql_binder'),
  EXISTS(SELECT 1 FROM pg_catalog.pg_available_extension_versions WHERE name='agentsql_binder' AND version='0.4'),
  EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder'),
  EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder' AND extversion='0.4'),
  NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='agentsql_catalog') OR
    EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid=n.nspowner
           WHERE n.nspname='agentsql_catalog' AND r.rolname=current_user) OR
    EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_extension e ON e.extnamespace=n.oid
           WHERE n.nspname='agentsql_catalog' AND e.extname='agentsql_binder' AND e.extversion='0.4')`
	if err := tx.QueryRow(ctx, inventorySQL).Scan(&available, &exactVersion, &installed, &installedExactVersion, &schemaSafe); err != nil {
		return BinderCapabilityHandshake{}, NewCatalogFailure("AUTH_CATALOG_INCOMPLETE")
	}
	inventoryVersion := ""
	if exactVersion {
		inventoryVersion = "0.4"
	}
	if err := recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "bootstrap_inventory", Outcome: "complete",
		Provider: provider, ServerMajor: major, DatabaseOID: databaseOID, ExtensionAvailable: available,
		ExtensionInstalled: installed, ExtensionVersion: inventoryVersion}); err != nil {
		return BinderCapabilityHandshake{}, err
	}
	configure := options.CreateIfAvailable && available && exactVersion && (!installed || installedExactVersion) && provider == postgresProviderSelfManaged
	if configure {
		if !schemaSafe {
			_ = recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "create", Outcome: "denied", Reason: "AUTH_BINDER_SCHEMA_OWNERSHIP_REQUIRED", Provider: provider, ServerMajor: major, DatabaseOID: databaseOID})
			return BinderCapabilityHandshake{}, NewCapabilityFailure("AUTH_BINDER_SCHEMA_OWNERSHIP_REQUIRED")
		}
		if strings.TrimSpace(options.RuntimeRole) == "" {
			return BinderCapabilityHandshake{}, NewCapabilityFailure("AUTH_BINDER_RUNTIME_ROLE_REQUIRED")
		}
		var bootstrapSuper, runtimeSuper, runtimeCreateDB, runtimeCreateRole bool
		const roleSQL = `SELECT
  (SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname=current_user),
  oid::oid, rolsuper, rolcreatedb, rolcreaterole
FROM pg_catalog.pg_roles WHERE rolname=$1`
		if err := tx.QueryRow(ctx, roleSQL, options.RuntimeRole).Scan(&bootstrapSuper, &runtimeRoleOID, &runtimeSuper, &runtimeCreateDB, &runtimeCreateRole); err != nil || !bootstrapSuper || runtimeSuper || runtimeCreateDB || runtimeCreateRole {
			_ = recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "create", Outcome: "denied", Reason: "AUTH_BINDER_PRIVILEGE_SEPARATION_REQUIRED", Provider: provider, ServerMajor: major, DatabaseOID: databaseOID, RuntimeRoleOID: runtimeRoleOID})
			return BinderCapabilityHandshake{}, NewCapabilityFailure("AUTH_BINDER_PRIVILEGE_SEPARATION_REQUIRED")
		}
		role := pgx.Identifier{options.RuntimeRole}.Sanitize()
		statements := []string{
			`CREATE SCHEMA IF NOT EXISTS agentsql_catalog`,
		}
		if !installed {
			statements = append(statements, `CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog VERSION '0.4'`)
		}
		statements = append(statements,
			`REVOKE ALL ON SCHEMA agentsql_catalog FROM PUBLIC`,
			`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA agentsql_catalog FROM PUBLIC`,
			`GRANT USAGE ON SCHEMA agentsql_catalog TO `+role,
			`GRANT EXECUTE ON FUNCTION agentsql_catalog.capabilities(), agentsql_catalog.dml_capabilities(), agentsql_catalog.prepare(text,text), agentsql_catalog.prepare_dml(text,text), agentsql_catalog.seal_prepared(text), agentsql_catalog.prepared_manifest(text), agentsql_catalog.prepared_dml_manifest(text), agentsql_catalog.prepared_relations(text), agentsql_catalog.prepared_vars(text), agentsql_catalog.prepared_objects(text) TO `+role)
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement); err != nil {
				_ = recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "create", Outcome: "error", Reason: "AUTH_BINDER_CREATE_FAILED", Provider: provider, ServerMajor: major, DatabaseOID: databaseOID, RuntimeRoleOID: runtimeRoleOID})
				return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageMetadata, "bootstrap PostgreSQL binder extension", err)
			}
		}
		outcome := "configured"
		if !installed {
			installed = true
			outcome = "created"
		}
		if err := recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "create", Outcome: outcome, Provider: provider,
			ServerMajor: major, DatabaseOID: databaseOID, ExtensionAvailable: true, ExtensionInstalled: true,
			ExtensionVersion: "0.4", RuntimeRoleOID: runtimeRoleOID}); err != nil {
			return BinderCapabilityHandshake{}, err
		}
	} else if options.CreateIfAvailable && (!installed || !installedExactVersion) {
		reason := "AUTH_BINDER_FILES_UNAVAILABLE"
		if provider != postgresProviderSelfManaged {
			reason = "AUTH_BINDER_MANAGED_SERVICE_UNSUPPORTED"
		} else if (available && !exactVersion) || (installed && !installedExactVersion) {
			reason = "AUTH_BINDER_VERSION_MISMATCH"
		}
		if err := recordBinderAudit(ctx, options.Audit, BinderCapabilityAuditRecord{Stage: "create", Outcome: "skipped", Reason: reason,
			Provider: provider, ServerMajor: major, DatabaseOID: databaseOID, ExtensionAvailable: available, ExtensionInstalled: false}); err != nil {
			return BinderCapabilityHandshake{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return BinderCapabilityHandshake{}, postgresDatabaseError(ctx, DBStageCommit, "commit PostgreSQL binder bootstrap", err)
	}
	return executor.ProbeEasyDeployBinderCapabilitiesAudited(ctx, expectation, budget, options.Audit)
}

func classifyPostgresProvider(version string, rds, azure, cloudSQL bool) string {
	lower := strings.ToLower(version)
	switch {
	case rds || strings.Contains(lower, "amazon rds") || strings.Contains(lower, "aurora"):
		return postgresProviderAWSManaged
	case azure || strings.Contains(lower, "azure"):
		return postgresProviderAzure
	case cloudSQL || strings.Contains(lower, "cloud sql") || strings.Contains(lower, "cloudsql"):
		return postgresProviderCloudSQL
	default:
		return postgresProviderSelfManaged
	}
}

func recordBinderAudit(ctx context.Context, audit BinderCapabilityAuditSink, record BinderCapabilityAuditRecord) error {
	if audit == nil {
		return NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	record.At = time.Now().UTC()
	if err := audit.RecordBinderCapabilityAudit(ctx, record); err != nil {
		return NewCapabilityFailure("AUTH_AUDIT_UNAVAILABLE")
	}
	return nil
}

func binderAuditReason(err error) string {
	var reasoned interface{ AuthorizationReason() string }
	if errors.As(err, &reasoned) {
		return reasoned.AuthorizationReason()
	}
	return "AUTH_DATABASE_ERROR"
}
