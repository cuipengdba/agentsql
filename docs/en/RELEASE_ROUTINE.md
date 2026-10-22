# AgentSQL each version delivery process

**Language:** [English](RELEASE_ROUTINE.md) | [Simplified Chinese](../RELEASE_ROUTINE.md)

This process applies to functional versions and repaired versions. The version leader is responsible for the complete closed loop of design, implementation, verification, release, official website and Live Demo; the creation of GitHub Release cannot be regarded as the end of delivery. All results must indicate the commit SHA, environment, and untested boundaries.

v1.1.0–v1.4.0 Create a formal GitHub Release version by version according to the phased rhythm determined by the product leader. The official website, Live Demo and GHCR `latest` remain v1.0.0; the acceptance of each version still requires the completion of independent upgrades, client, real database and accurate version asset verification. After v1.5.0 is completed, update these three default entrances at once and perform re-verification on the public network.

## 1. Design and Scope

1. Compare the previous official version, real user feedback, competing products and existing code to describe user issues, non-targets, compatibility impacts, database support boundaries and acceptance use cases.
2. For changes involving identity, permissions, execution, masking, auditing, and fault closure, draw the end-to-end call chain and list negative use cases. Abilities that cannot be proven are simply marked as unverified.
3. Version numbers are selected based on actual compatibility; database protocol compatibility and single-instance synthetic data pass do not equal full production support.

## 2. Development and regression

1. Implement backend, console, configuration, documentation and necessary migrations in isolated branches. All officially released code is built from the same commit.
2. Running Go, front-end type checking and testing, website validation, SQL safe negative sets. Real client and database testing only records actual completed versions, protocols, modes and environments.
3. Audit failures, token revocation, cross-tenant, permission changes, disconnection retries, and rollbacks are release blocking items for security features. If it fails, fix and rerun the affected use case.
4. Use the installation package and data backup drill of the previous official version to upgrade; when there are schema changes, drill the complete backup and recovery at the same time. New code must not covertly migrate production data.

## 3. Release and Delivery

1. Review diffs, dependencies, sensitive information, and release notes before merging. After merging, the green CI of the target SHA is used as the basis for release.
2. Create official tags and GitHub Releases from this SHA, build and verify dual-schema assets, SHA256 manifests, and signatures; verify multi-schema digests and anonymous pulls of GHCR accurate tags.
3. The official website copy, compatibility matrix, download command and PDF are aligned with the official Release; after deployment, read and check the version and resource links from the public network.
4. Live Demo first backs up and completes migration, startup, synthetic scenarios and safety negative smoke testing in the isolated candidate environment, and then switches to the public network. Check `/healthz` version, login, allow and deny scenarios, masking and persistent auditing.
5. After both the official website and demo are passed, `latest` is promoted to the accepted master image summary and pulled anonymously again; the Release text and acceptance record are completed with CI, assets/signatures, image summary, official website, demo, client and database evidence, and any unfinished items. Matters without evidence cannot be written as approved.

If any of the release blocking items fails, keep the completed code and test records, and stop announcing that the version has been released; first restore the official website and demo to the previous verified version, and then fix the problem.
