# Batch 82c Acceptance Report

**Language:** [English](release-b82c-report.md) | [Simplified Chinese](../release-b82c-report.md)

Status: **VERIFIED — latest has pointed to v0.5.0**. The following times are all 2026-10-10 (UTC+08:00).

## Pre-checking and anonymous pulling

- `gh api --method GET /user/packages/container/agentsql-mysql-demo --jq .visibility` returns `public`.
- `docker logout ghcr.io` has been executed, and the three quickstart images are pulled under the newly created `DOCKER_CONFIG` without credentials.

| mirror | Anonymous pull completion time | result |
| --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart` | 08:25:42 | PASS |
| `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5-quickstart` | 08:25:58 | PASS |
| `ghcr.io/cuipengdba/agentsql-mysql-demo:8` | 08:27:11 | PASS |
| `ghcr.io/cuipengdba/agentsql:latest` | 08:42:39 | PASS (this time, pull it from the empty `DOCKER_CONFIG`) |

## Anonymous quickstart

`docker-compose -p agentsql-b82c -f deploy/quickstart/docker-compose.yml up -d` first returned failure at 08:28:17; the database was still being initialized at that time. After PostgreSQL and MySQL were turned healthy, the same project successfully retried at 08:29:53, still within the five-minute window after initial startup. seed completes with exit code 0 and gateway is healthy.

| assert | Observations | result |
| --- | --- | --- |
| `/healthz` status | `ok` | `QUICKSTART_HEALTHZ_PASS` |
| `/readyz` status | `ready` | `QUICKSTART_READYZ_PASS` |
| `/healthz` version | `v0.5.0` | `QUICKSTART_VERSION_PASS` |

Assertion complete time: 08:29:59. 08:30:22 After executing `down -v`, the container, network, and volume counts of `agentsql-b82c` are all 0; the temporary anonymous configuration has been removed.

## PromoteLatest and anonymous latest verification

| label | Promote top-level digest | The top-level digest after this upgrade |
| --- | --- | --- |
| `latest` | `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` |
| `v0.5.0` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` |

First ran `scripts/build-ghcr-multiarch.ps1 -PromoteLatest -Version v0.5.0` under temporary `DOCKER_CONFIG` without credentials at 08:33:07. `imagetools create` will be written to GHCR and cannot be executed anonymously; GHCR refuses to write at 08:33:26, and the script exit code is 1. When checked again at 08:33:51, both digests have not changed.

This time, `gh auth token` is first used to complete the GHCR login of the default Docker configuration through standard input, and then `powershell -ExecutionPolicy Bypass -File ./scripts/build-ghcr-multiarch.ps1 -PromoteLatest -Version v0.5.0` is run with the default configuration at 08:40:38. The script exited successfully at 08:41:00; 08:41:11 read-only `imagetools inspect` reviewed the top-level digest of latest and v0.5.0, which is recorded as `LATEST_PROMOTED`.

The expected digest given by the task text is in`bf41`One more after`1`, a total of 65 hexadecimal characters. The above table uses the 64-bit digest actually returned by GHCR and in a valid format. This value is also consistent with the first report record.v0.5.0digest is consistent.

08:42:28 Executing `docker logout ghcr.io` for default configuration. Then a new empty `DOCKER_CONFIG` was created, and `latest` was successfully pulled anonymously at 08:42:39. The digest of the pull result is consistent with the above table and is recorded as `LATEST_ANON_PULL_PASS`. 08:42:43 Running `--version` of this image outputs `v0.5.0`, recorded as `LATEST_VERSION_PASS`; the temporary anonymous configuration has been deleted.

Conclusion: `QUICKSTART_ALL_PASS`, `LATEST_PROMOTED`, `LATEST_ANON_PULL_PASS`, `LATEST_VERSION_PASS`.

Full timestamp evidence is found at `anonymous-pull.log`, `quickstart-verify.log`, `cleanup.log`, `promote-latest.log`, `latest-verify.log`, `promote-latest-2.log`, and `latest-verify-2.log` under `dist/release-b82c/`.
