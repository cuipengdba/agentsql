# AgentSQL v4.0.0 release notes

**Language:** [Simplified Chinese](../release-notes-v4.0.0.md) | [English](release-notes-v4.0.0.md)

This release improves isolated-network delivery, cross-version upgrades and maintenance diagnostics, completing the authorized ten-release v3.1.0–v4.0.0 plan. Formal publication status is recorded in [release evidence](v4.0.0-release-evidence.md).

| Change | User-visible result |
| --- | --- |
| Local release verification | Specify a version, independently trusted key and complete asset directory to verify 15 assets, three Ed25519 signatures, SBOM identity, provenance and hashes. |
| Offline transport archive | Copy into private staging, verify and archive; reverify at the destination and reuse existing `install.sh --from`. |
| Minimal diagnostic export | Fixed-field JSON, create-only output and read-only store checks, without SQL, data, logs, connection details or credentials. |
| Upgrade maintenance | Upgrade/rollback rehearsals for three official baselines and both store layouts; console and bilingual manuals provide operating instructions. |

## Compatibility and scope

No business database is added and no authorization, execution, masking or audit contract is loosened. Oracle, SQL Server, relational and native-read capabilities retain their versioned evidence boundaries. The public Demo continues to use synthetic PostgreSQL/MySQL data. There is no claim of customer production certification, vendor certification, complete production support for arbitrary databases or a fixed LTS term.

## Usage and acceptance

See the [delivery guide](v4.0.0-delivery.md), [upgrade and rollback](v4.0.0-upgrade.md), [acceptance record](v4.0.0-acceptance.md) and [design](v4.0.0-design.md).
