package businessdb

import (
	"context"
	"errors"
	"testing"
)

func TestPostgresManagedProviderDetection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		version              string
		rds, azure, cloudSQL bool
		want                 string
	}{
		{"self-managed", "PostgreSQL 16.4 on Linux", false, false, false, postgresProviderSelfManaged},
		{"rds-setting", "PostgreSQL 16.4", true, false, false, postgresProviderAWSManaged},
		{"aurora-version", "Aurora PostgreSQL 16.4", false, false, false, postgresProviderAWSManaged},
		{"azure-setting", "PostgreSQL 16.4", false, true, false, postgresProviderAzure},
		{"cloudsql-version", "PostgreSQL 16.4 on Cloud SQL", false, false, false, postgresProviderCloudSQL},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyPostgresProvider(test.version, test.rds, test.azure, test.cloudSQL); got != test.want {
				t.Fatalf("provider = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBinderAuditIsFailClosed(t *testing.T) {
	t.Parallel()
	err := recordBinderAudit(context.Background(), BinderCapabilityAuditFunc(func(context.Context, BinderCapabilityAuditRecord) error {
		return errors.New("durable audit unavailable")
	}), BinderCapabilityAuditRecord{Stage: "probe"})
	requireEasyDeployReason(t, err, "AUTH_AUDIT_UNAVAILABLE")
}

func TestNativeExpectationsCoverFiveMajorsAndRejectTamper(t *testing.T) {
	t.Parallel()
	for major := 14; major <= 18; major++ {
		expected, ok := PostgresBinderNativeExpectation(major)
		if !ok || expected.BuildHash == "" || expected.ExtensionHash == "" || expected.NodeManifestHash == "" || expected.AllowlistHash == "" {
			t.Fatalf("incomplete expectation for PG%d: %#v", major, expected)
		}
		value := PostgresBinderCapability{ABI: expected.ABI, ServerMajor: major, ExtensionVersion: expected.ExtensionVersion,
			BuildHash: expected.BuildHash, ExtensionHash: expected.ExtensionHash, NodeManifestHash: expected.NodeManifestHash, AllowlistHash: expected.AllowlistHash, Matview: true}
		if !nativeCapabilityMatches(value, expected) {
			t.Fatalf("PG%d exact capability rejected", major)
		}
		value.BuildHash = "tampered"
		if nativeCapabilityMatches(value, expected) {
			t.Fatalf("PG%d tampered build accepted", major)
		}
	}
	if _, ok := PostgresBinderNativeExpectation(19); ok {
		t.Fatal("unknown PostgreSQL major accepted")
	}
}
