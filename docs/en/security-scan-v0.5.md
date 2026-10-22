# AgentSQL v0.5 read-only reinforcement verification before release (batch twenty-three)

**Language:** [English](security-scan-v0.5.md) | [Simplified Chinese](../security-scan-v0.5.md)

Verification date: 2026-10-03 (Asia/Shanghai)
Branch: `feature/v0.4`
Verification submission: `13f2dbdef115ad1953dea1f4b613c7e13b2082a8`

## 1. Summary of conclusions

This batch only performs scanning, building and temporary demo running, and does not modify dependencies, source code or Git history. The workspace is clean before verification; check again after completing all read-only actions and it is still clean. The SHA-256 of the protected file before the report is released is:

- `go.mod`：`B6329AC7948ABD7B8C34973CFE7CCDBD00AAE205CDEDD6DED853CC21A80A27B8`
- `go.sum`：`7CF79CBE4DFE855D79D1160787EFF1AF1646E00929B5AA592C6A826E652ED97D`
- `web/package-lock.json`：`BEAEED390837CAA7CDD229DAF076CCD2EF48B077713203211D16DD785DFA3F9B`

| Verification items | Conclusion | key evidence |
| --- | --- | --- |
| Go dependency vulnerability | **source code/symbol level PASS, there are 4 items to be processed at the module level** | Callable vulnerabilities 0, import package vulnerabilities 0; dependent module hits 4 items, but the current code does not call the affected symbols/packages |
| Front-end production dependencies | **PASS** | `npm audit --omit=dev`: 0 vulnerabilities, exit code 0 |
| Git full history key mode | **high confidence mode PASS; assignment mode awaits manual review** | AKIA 0, private key header 0; `password=` 157, `secret=` 47, `api[_-]?key=` 4 patch lines hit |
| Demo smoke | **PASS, but there is a risk of first startup time limit** | Build, health, readiness, login, and controlled query all passed under the extended observation window; the standard one-time `up --wait` failed for the first time due to MySQL initialization timeout. |
| PostgreSQL parser P99 | **FAIL** | Only 3 of the 5 rounds satisfy P99 ≤ 5 ms; P99 is 4.365/4.615/6.467/4.235/6.517 ms |

Therefore, this report cannot give a conclusion that "all passes before publication". `parser` The 5 ms gate still fails; in addition, the person in charge should decide whether the 4 module-level vulnerabilities and the Windows Docker first demo startup timeout block release. This batch will not be automatically repaired.

## 2. Phase 0: Tools and Scope Research

### 2.1 Tool availability

| Tools | Host status | Actual version/method used |
| --- | --- | --- |
| Git | Available | `git version 2.55.0.windows.5` |
| Docker | Available, but limited sessions require open Docker API | Client/Engine `29.8.0`，Docker Desktop `4.92.0` |
| Docker Compose | Standalone commands are available | `docker-compose` v5.5.1; The current `docker compose` plug-in entry is unavailable |
| Go | Host is unavailable | Official `golang:1.25-bookworm` (Go 1.25.14) and `golang:1.26-bookworm` (Go 1.26.8) containers |
| govulncheck | Host is unavailable | `go install golang.org/x/vuln/cmd/govulncheck@latest` in the container, get v1.8.0 |
| Node/npm | Host is unavailable | Official `node:24-bookworm-slim`, Node v24.21.0, npm 11.19.0 |
| `rg` | Host is unavailable | Switch to PowerShell `Select-String` for survey and historical results compilation; do not change the Git scanning scope |

`govulncheck@latest` v1.8.0 requires Go 1.26. When loading this project directly with Go 1.26, `vitess.io/vitess@v0.22.4/go/hack/ensure_swiss_map.go` is reported to Go 1.26 compatibility guard `invalid array length -1`. Finally, the scanner was built using Go 1.26.8, and the package loading tool chain for source code scanning was made compatible with the project Go 1.25.14; the scanner was not downgraded, and `go.mod`/`go.sum` was not modified.

### 2.2 Fixed commands and coverage

| Project | Command/equivalent command | range |
| --- | --- | --- |
| Go source code vulnerability | `GOTOOLCHAIN=go1.25.14 govulncheck ./...` | Linux/amd64, full warehouse Go package under default build tags |
| Go module re-scan | Execute `govulncheck -scan module -show verbose` under `cmd/agentsql` | 49 modules parsed by the main entrance; added details of unreachable module vulnerabilities |
| Introduction chain | `go mod why -m vitess.io/vitess golang.org/x/crypto` | Current module diagram |
| Front-end audit | `npm audit --omit=dev --registry=https://registry.npmjs.org/` | Production dependencies in `web/package-lock.json`; devDependencies are explicitly excluded |
| Git history scan | Execute `git log --all --format=... -p --unified=0 --no-ext-diff --no-textconv -G <regex>` for each high-risk regex and match only added and deleted patch lines | Full patch history for all refs; output only retains schema, commits, files, and counts, does not echo candidate values |
| Demo | `docker-compose --project-name agentsql-v05-preflight --file docker-compose.demo.yml ...` | Independent project, loopback port, random temporary credentials, synthetic data; after completion `down -v --remove-orphans` |
| Parser P99 | `go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v` | Go 1.25.14, Linux/amd64, 8 CPU, `GOMAXPROCS=8`, serial 5 rounds |

## 3. Go dependency vulnerability

### 3.1 Evidence of execution

- Source code scanning starts: 2026-10-03 07:25:39 CST. The scanner outputs `GOVULNCHECK_SCAN_EXIT=0`; the outer Docker/PowerShell cleanup wait does not return the end time normally, and the outer wait is terminated after confirming that the scanning container has exited. The valid results are: `No vulnerabilities found`, 0 code callable vulnerabilities, 0 import package vulnerabilities, and only 4 dependencies on the module level.
- Module details scan: 2026-10-03 07:36:47–07:39:49 CST, 182.38 seconds. Module-level hits follow the govulncheck convention with exit code 3.
- Introduction chain: 2026-10-03 08:19:44–08:22:21 CST, 157.122 seconds, exit code 0.
- Scanner: govulncheck v1.8.0; vulnerability library `https://vuln.go.dev`, library update time 2026-10-01 20:24:15 UTC.

### 3.2 Module-level discovery

govulncheck itself does not output CVSS; "upstream severity" is taken from the corresponding CVE/GHSA announcement, and "this project judgment" is combined with symbolic reachability. A module-level hit of 4 should not be expressed as "completely bug-free".

| ID | upstream severity | Hit version/fixed version | Introduction chain and accessibility of this project | Judgment of this project |
| --- | --- | --- | --- | --- |
| [GO-2026-6356](https://pkg.go.dev/vuln/GO-2026-6356) / CVE-2026-65959 | Medium，CVSS 3.1 6.5（[GHSA](https://github.com/vitessio/vitess/security/advisories/GHSA-mhc4-g3wh-cw7m)） | `vitess.io/vitess@v0.22.4`; fixed in v0.23.6 | `agentsql/internal/parser -> vitess.io/vitess/go/vt/sqlparser`; The vulnerability is located in vttablet VReplication `/debug/vrlog`, and the source code scan did not find the call. | Medium: Module hit, current product call chain is unreachable; subsequent dependency upgrade batch evaluation |
| [GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355) / CVE-2026-56855 | High，CNA CVSS 3.1 7.5 | `golang.org/x/crypto@v0.55.0`; fixed in v0.56.0 | `agentsql/internal/rbac -> golang.org/x/crypto/bcrypt`; The vulnerability is in `x/crypto/ssh`, and the affected packages and symbols are not currently imported/called. | Medium: The upstream is high-risk but currently unreachable; subsequent upgrade batch evaluation is required. |
| [GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354) / CVE-2026-78662 | High，CNA CVSS 3.1 7.5 | `golang.org/x/crypto@v0.55.0`; fixed in v0.56.0 | Same as above; the vulnerability is in `x/crypto/ssh`, currently only due to the introduction of the module `bcrypt` | Medium: The upstream is high-risk but currently unreachable; subsequent upgrade batch evaluation is required. |
| [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) | Unscored; the OpenPGP package is no longer maintained and is not secure by design | `golang.org/x/crypto@v0.55.0`; no fixed version | Same as above; the vulnerability only refers to `x/crypto/openpgp*`, and these packages are not currently imported. | Low: Module-level generalization hit; new OpenPGP calls remain prohibited |

Suggestion: Start a separate dependency repair batch to evaluate the compatibility of Vitess v0.23.6 and x/crypto v0.56.0 and run full regression. There is no fixed version for `GO-2026-5932` to which you can directly upgrade, and you should continue to confirm that `openpgp*` is not imported via source/dependency gate. No dependencies will be changed in this batch.

## 4. Front-end production dependency audit

- Time: 2026-10-03 07:24:19–07:25:19 CST, 60.433 seconds.
- Command: `npm audit --omit=dev --registry=https://registry.npmjs.org/` (`web/`, repository read-only mount).
- Environment: Node v24.21.0, npm 11.19.0.
- Result: `found 0 vulnerabilities`, `NPM_AUDIT_EXIT=0`, Docker exit code 0.
- Conclusion: Under the current advisory set returned by the official registry, no vulnerabilities were found in the production dependencies of `package-lock.json`. devDependencies are not covered by task caliber and cannot be extrapolated to 0 vulnerabilities in all front-end dependencies.

## 5. Git full history key mode scan

### 5.1 Implementation evidence and conclusion

- Time: 2026-10-03 07:14:46–07:14:59 CST; 5 regex parallel read-only searches, each taking 7.658–11.218 seconds.
- High confidence mode: AWS `AKIA[0-9A-Z]{16}` 0 rows; `-----BEGIN (RSA|OPENSSH|EC) PRIVATE KEY-----` 0 rows.
- Low confidence assignment mode: `password=` 157 patch rows, 64 paths; `secret=` 47 patch rows, 20 paths; `api[_-]?key=` 4 patch rows, 3 paths.
- Counting caliber: The introduction and deletion of the same text are counted as one line each, so 208 patch lines cannot be interpreted as 208 unique keys.
- Initial review: hits focused on public example/documentation, test fixtures, temporary variables for install/smoke scripts, front-end resources after historical builds, and the JSX `apiKey=` attribute; no AKIA or private key headers were seen. Since this batch does not echo candidate values ​​and does not verify the validity of credentials, assignment hits are still retained as low-severity manual review items, and it cannot be claimed that "there are no secrets in history."

### 5.2 Manual review of positioning list

The following commits are 12-bit short hashes; use `git show <commit> -- <path>` read-only review if necessary.

#### `password=`

| path | patch line | commit |
| --- | ---: | --- |
| `.design/s0/spike.go` | 2 | `02a2de4e7fca`, `a9cc6b6f3bcf` |
| `dbext/packaging/test-images.ps1` | 1 | `577d26a81eb8` |
| `demo/mysql/02_readonly.sh` | 1 | `b15463a49450` |
| `demo/mysql/03_data.sh` | 1 | `b15463a49450` |
| `demo/postgres/02_readonly.sh` | 1 | `b15463a49450` |
| `deploy/ecs-demo/.env.example` | 5 | `83c19c5f9008` |
| `deploy/quickstart/docker-compose.yml` | 1 | `b6ebf5f0d3e2` |
| `deploy/quickstart/mysql/02_readonly.sh` | 1 | `b6ebf5f0d3e2` |
| `deploy/quickstart/mysql/03_data.sh` | 1 | `b6ebf5f0d3e2` |
| `deploy/quickstart/postgres/02_readonly.sh` | 1 | `b6ebf5f0d3e2` |
| `docker-compose.demo.yml` | 1 | `b15463a49450` |
| `docs/DEPLOY.md` | 7 | `7972bc0be415`, `e55d08131f79`, `fb3ea5187ad7` |
| `docs/en/QUICKSTART.md` | 1 | `6c5e9d27cc6d` |
| `docs/en/README.md` | 1 | `641366f26376` |
| `docs/yashan-dialect.md` | 1 | `7c6cb3432dce` |
| `examples/docker/.env.example` | 2 | `e55d08131f79`, `fb3ea5187ad7` |
| `examples/docker/demo.env.example` | 5 | `b15463a49450` |
| `examples/observability/README.md` | 1 | `7972bc0be415` |
| `internal/authorizedexecute/b2_s7_attack_matrix_test.go` | 1 | `db87dab20c7c` |
| `internal/authorizedexecute/limits_test.go` | 2 | `ac8718aba4f5` |
| `internal/executor/coverage_executor_test.go` | 12 | `df9a91b03a36` |
| `internal/executor/errors_classify_test.go` | 1 | `d114e258344c` |
| `internal/executor/executor_test.go` | 1 | `f44ab8adb732` |
| `internal/store/redaction_storage_test.go` | 1 | `9cd64b2a8c23` |
| `internal/webui/dist/assets/antd-vendor-DC_jP0dn.js` | 1 | `b596953e2e38` |
| `internal/webui/dist/assets/antd-vendor-kvRpkDHe.js` | 1 | `8726e3e84c7f` |
| `internal/webui/dist/assets/antd-vendor-vOTpU9ge.js` | 1 | `8726e3e84c7f` |
| `internal/webui/dist/assets/Datasources-a_Y5IvWC.js` | 1 | `d2f41cd653d0` |
| `internal/webui/dist/assets/Datasources-BAfVUgKG.js` | 3 | `781a8e0d5000`, `8726e3e84c7f` |
| `internal/webui/dist/assets/Datasources-BDT8TNIg.js` | 2 | `3613fee0fda9` |
| `internal/webui/dist/assets/Datasources-BEyn3i1y.js` | 3 | `77051bd9e4c6`, `d63c335cd5f1` |
| `internal/webui/dist/assets/Datasources-BLCdblJe.js` | 1 | `9d34117d7783` |
| `internal/webui/dist/assets/Datasources-BWntakh7.js` | 2 | `b486a4be1e45` |
| `internal/webui/dist/assets/Datasources-C_1V_GWQ.js` | 2 | `fad9cdd9230a` |
| `internal/webui/dist/assets/Datasources-C9a-jcIO.js` | 1 | `f82698e64ebe` |
| `internal/webui/dist/assets/Datasources-CaHYg1c3.js` | 3 | `1c64a8164212`, `d2f41cd653d0` |
| `internal/webui/dist/assets/Datasources-ChKhQEnq.js` | 3 | `383e0d5a44f8`, `9d34117d7783` |
| `internal/webui/dist/assets/Datasources-CwC6DRQM.js` | 2 | `c3ff4f33f1f8` |
| `internal/webui/dist/assets/Datasources-D_2UxqM7.js` | 2 | `21991ddd194e` |
| `internal/webui/dist/assets/Datasources-D0uyGC-Z.js` | 2 | `e75b72816cf5` |
| `internal/webui/dist/assets/Datasources-DDKwCrkr.js` | 1 | `77051bd9e4c6` |
| `internal/webui/dist/assets/Datasources-Dt1p9UJd.js` | 1 | `8726e3e84c7f` |
| `internal/webui/dist/assets/Datasources-DVH72tr0.js` | 2 | `10944325836e` |
| `internal/webui/dist/assets/Datasources-tkf0nVX-.js` | 2 | `b596953e2e38`, `f82698e64ebe` |
| `internal/webui/dist/assets/Datasources-Vr0odOFO.js` | 2 | `d197464858d8` |
| `internal/webui/dist/assets/Datasources-xzNrmexx.js` | 2 | `6285fe61f139` |
| `internal/webui/dist/assets/index-7CipxkwR.js` | 1 | `839392ffde21` |
| `internal/webui/dist/assets/index-BcEQaP38.js` | 2 | `669ee4fb6b92`, `f9ebfd5bc36e` |
| `internal/webui/dist/assets/index-BFk6MBX-.js` | 4 | `679429a74fb0` |
| `internal/webui/dist/assets/index-B-v30rQ8.js` | 6 | `791a75ad9dd0`, `bdc1492eb35f` |
| `internal/webui/dist/assets/index-BW3tkdAm.js` | 4 | `4b5b09de0be9`, `b596953e2e38` |
| `internal/webui/dist/assets/index-BZ-EYGJs.js` | 2 | `791a75ad9dd0` |
| `internal/webui/dist/assets/index-Cb9DW9_X.js` | 4 | `6fa6333fd7e6` |
| `internal/webui/dist/assets/index-Ck0vRBFJ.js` | 6 | `2ad530e41ad9`, `4b5b09de0be9` |
| `internal/webui/dist/assets/index-DISf5Uly.js` | 2 | `111cfbf4b2b7` |
| `internal/webui/dist/assets/index-DWGkvbx9.js` | 3 | `0d482588d138`, `111cfbf4b2b7` |
| `internal/webui/dist/assets/index-Dyc8mxQg.js` | 2 | `1afb2cd36caa`, `f9ebfd5bc36e` |
| `internal/webui/dist/assets/index-tEkR_XKd.js` | 2 | `1afb2cd36caa`, `839392ffde21` |
| `internal/webui/dist/assets/utility-vendor-DIQSX3FC.js` | 2 | `781a8e0d5000`, `8726e3e84c7f` |
| `internal/webui/dist/assets/utility-vendor-Djxirr9a.js` | 1 | `8726e3e84c7f` |
| `README.md` | 6 | `7972bc0be415`, `d864d916dd68`, `e55d08131f79`, `fb3ea5187ad7` |
| `scripts/compose-smoke.sh` | 10 | `a59a5b461fc1` |
| `scripts/install.sh` | 4 | `d864d916dd68` |
| `scripts/quickstart.sh` | 4 | `d864d916dd68` |

#### `secret=`

| path | patch line | commit |
| --- | ---: | --- |
| `.design/b2-column-auth-design-v1.md` | 4 | `02a2de4e7fca`, `a9cc6b6f3bcf` |
| `deploy/ecs-demo/.env.example` | 1 | `83c19c5f9008` |
| `docs/DEPLOY.md` | 8 | `7972bc0be415`, `a59a5b461fc1`, `e55d08131f79`, `fb3ea5187ad7` |
| `docs/en/INTEGRATIONS.md` | 3 | `641366f26376`, `6c5e9d27cc6d` |
| `docs/en/QUICKSTART.md` | 1 | `6c5e9d27cc6d` |
| `docs/en/README.md` | 1 | `641366f26376` |
| `docs/INTEGRATIONS.md` | 1 | `857db5baf750` |
| `docs/NOTIFICATIONS.md` | 1 | `d001394b4e44` |
| `examples/docker/.env.example` | 2 | `e55d08131f79`, `fb3ea5187ad7` |
| `examples/docker/demo.env.example` | 1 | `b15463a49450` |
| `examples/observability/README.md` | 1 | `7972bc0be415` |
| `internal/authorizedexecute/b2_s7_attack_matrix_test.go` | 1 | `db87dab20c7c` |
| `internal/authorizedexecute/internal/businessdb/b2_s7_attack_matrix_e2e_test.go` | 3 | `db87dab20c7c` |
| `internal/authorizedexecute/internal/businessdb/easy_deploy_closed_select_e2e_test.go` | 1 | `9625092131f6` |
| `internal/authorizedexecute/internal/businessdb/postgres_binder_e2e_test.go` | 1 | `814dae863256` |
| `internal/parser/mysql_lineage_test.go` | 1 | `91696926e347` |
| `README.md` | 8 | `75fc960aa199`, `7972bc0be415`, `d864d916dd68`, `e55d08131f79`, `fb3ea5187ad7` |
| `scripts/compose-smoke.sh` | 4 | `a59a5b461fc1` |
| `scripts/install.sh` | 2 | `d864d916dd68` |
| `scripts/quickstart.sh` | 2 | `d864d916dd68` |

#### `api[_-]?key=`

| path | patch line | commit |
| --- | ---: | --- |
| `docs/en/INTEGRATIONS.md` | 1 | `641366f26376` |
| `docs/INTEGRATIONS.md` | 1 | `857db5baf750` |
| `web/src/pages/Agents.tsx` | 2 | `111cfbf4b2b7` |

`Agents.tsx` The two places pass the value for the React JSX `apiKey=` attribute, not the hardcoded key. Document hits remain in the inventory for review by the responsible person.

## 6. Demo smoke

### 6.1 Standard one-time startup: FAIL

- Time: 2026-10-03 07:44:35–08:04:59 CST, 1224.35 seconds (including the first image pull and build).
- Command: `docker-compose --project-name agentsql-v05-preflight --file docker-compose.demo.yml up -d --build --wait --wait-timeout 900`.
- Build: `agentsql:v0.5.0` and `agentsql/postgres-binder-demo:16-0.5` were built successfully.
- Failure point: `demo-mysql` is still being initialized within the 30 health retries provided by Compose. Compose reports `dependency demo-mysql failed to start`; `demo-postgres` is healthy. The log shows that MySQL 8.4.11 is initializing, no SQL initialization error is reported, but it has not yet reached the seed, gateway or API stage.
- Cleanup: The container, network, and three test volumes of the independent project were all deleted successfully, `CLEANUP_EXIT=0`.

### 6.2 Phased expanded observation: PASS

- Time: 2026-10-03 08:06:21–08:12:29 CST, 367.938 seconds.
- Method: Reuse the built image and start PostgreSQL/MySQL first; do not modify the health check definition and only observe externally for up to 10 minutes. MySQL once converted to `unhealthy`, and returned to `healthy` at about the 55th 5-second observation; then the seed and gateway were started.
- Result: seed exit code 0; gateway container healthy.
- `/healthz`: `status=ok`, `demo.enabled=true`, banner exists.
- `/readyz`：`status=ready`。
- `POST /api/v1/auth/login`: `code=0`, token exists.
- `POST /api/v1/playground/run`: Fixed the identity of `ds-demo-pg`/`ro` to perform a controlled JOIN check; `code=0`, `decision=allow`, 1 row, 2 masked cells, `audit_id` exist.
- Cleanup: Containers/networks/volumes are deleted successfully, and finally there are no residues according to the Compose project label query.

Conclusion: Functional link PASS, but default one-time first boot on native Windows Docker persistent volumes does not reliably pass existing boot time limits. The severity is judged as **or**: it does not affect the functional results of the ready environment, but it affects the first demo delivery experience. It is recommended that the person in charge decides whether to adjust the initialization/wait strategy in a separate batch; the results of this extended observation must not be used to cover up the first FAIL of the standard command.

## 7. PostgreSQL parser P99 retest

- Time: 2026-10-03 08:13:59–08:18:02 CST, 243.764 seconds (including the first download/compile dependency; the timing sample is inside the test and does not include download and compilation).
- Environment: `golang:1.25-bookworm`, Go 1.25.14, Linux/amd64, 8 CPU, `GOMAXPROCS=8`.
- Command: `go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v`.
- Threshold: `typicalP99Budget = 5ms` in the test source code, unmodified.

| rounds | P50 | P90 | P99 | 5 ms gate |
| ---: | ---: | ---: | ---: | --- |
| 1 | 1.672222 ms | 3.029721 ms | 4.364518 ms | PASS |
| 2 | 1.834034 ms | 3.208935 ms | 4.615138 ms | PASS |
| 3 | 1.936738 ms | 3.480248 ms | 6.466874 ms | FAIL |
| 4 | 1.709622 ms | 3.047517 ms | 4.235102 ms | PASS |
| 5 | 1.904345 ms | 3.314153 ms | 6.517398 ms | FAIL |

Overall `go test` exit code 1, only 3/5 rounds passed. Compared with batch 19, the median and P90 this time are still relatively stable, and the tail of P99 continues to span 5 ms. Therefore, the conclusion remains that **high-severity gate failure** will be released, and the threshold will not be relaxed and the standard will not be marked.

"Exclusive" means that no other Codex scans, builds, or tests are executed concurrently in this batch, and the test container is limited to using 8 CPUs; other Docker containers that the user has previously run are not stopped, so it cannot be claimed to be bare metal with absolutely no background load. If the person in charge requires strict release judgment, the Linux runner should be fixed and irrelevant loads should be cleared before running again for 5 rounds exclusively. However, this real FAIL cannot be erased because of this.

## 8. Discovered items, suggestions and uncovered items

| No. | Severity | discover | Suggestion (not implemented in this batch) |
| --- | --- | --- | --- |
| F-01 | High/Release Gate | PostgreSQL parser P99 only 3/5 rounds ≤ 5 ms | Fixed Linux runner retesting and collecting CPU/alloc/GC evidence; maintaining release gate failure until stable pass |
| F-02 | in | 4 module-level Go vulnerabilities: 2 upstream High, 1 Medium, 1 Unscored; current symbol/package is unreachable | Independent dependency upgrade batch evaluation Vitess v0.23.6, x/crypto v0.56.0; keep OpenPGP disabled |
| F-03 | in | Windows Docker's first demo starts at once and exceeds the MySQL healthy retry window | Evaluate health retries, initialization instructions, or warm-up strategies independently; database health determinations must not be degraded |
| F-04 | Low / manual review | History assignment class regex hits 208 patch lines | The person in charge will review the example/test/history dist according to the appendix; if real credentials are found, the history will be rotated first and then processed in another batch. It is prohibited to rewrite the history in this batch. |

**user review conclusion (2026-10-03 user decision):**F-04's 208 assignment class regular hits are all test data, examples or non-sensitive assignments. Press "No Disposal" to close; no code modifications, no batch replacement of strings, and no rotation of any keys.

Not covered:

- KingbaseES V9 is still waiting for the 2026-10-08 manufacturer image and license; the DM8 real database is still waiting for user credentials. This batch did not express the lack of environment as a pass.
- Git scan is a full history patch scan of the specified regex, not an entropy scan, online credential validity verification, or a third-party secret scanner; does not cover Git LFS/external artifact repositories/deleted remote refs.
- npm audit excludes devDependencies as required by the task; there is no coverage of container OS packages, browser runtimes, or supply chain signatures.
- govulncheck is Linux/amd64, default build tags; optional build tags, unbuilt platforms and external plug-ins need to be scanned separately.
- The demo only covers local synthetic data, health/readiness, administrative logins, and a controlled PostgreSQL query; it does not cover public network TLS, reverse proxy, HA, failover, full PostgreSQL/MySQL permission matrix, or production data.
- No findings have been fixed in this batch, and no `git add`, commit, merge, push, tag, or history rewriting has been performed.
