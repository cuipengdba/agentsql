# AgentSQL v0.5 发布前只读加固核验（批二十三）

核验日期：2026-10-03（Asia/Shanghai）
分支：`feature/v0.4`
核验提交：`13f2dbdef115ad1953dea1f4b613c7e13b2082a8`

## 1. 结论摘要

本批仅执行扫描、构建与临时 demo 运行，不修改依赖、源码或 Git 历史。核验前工作区干净；完成全部只读动作后再次检查仍干净。报告落盘前受保护文件 SHA-256 为：

- `go.mod`：`B6329AC7948ABD7B8C34973CFE7CCDBD00AAE205CDEDD6DED853CC21A80A27B8`
- `go.sum`：`7CF79CBE4DFE855D79D1160787EFF1AF1646E00929B5AA592C6A826E652ED97D`
- `web/package-lock.json`：`BEAEED390837CAA7CDD229DAF076CCD2EF48B077713203211D16DD785DFA3F9B`

| 核验项 | 结论 | 关键证据 |
| --- | --- | --- |
| Go 依赖漏洞 | **源码/符号级 PASS，模块级有 4 项待处置** | 可调用漏洞 0、导入包漏洞 0；依赖模块命中 4 项，但当前代码没有调用受影响符号/包 |
| 前端生产依赖 | **PASS** | `npm audit --omit=dev`：0 vulnerabilities，退出码 0 |
| Git 全历史密钥模式 | **高置信模式 PASS；赋值模式待人工复核** | AKIA 0、私钥头 0；`password=` 157、`secret=` 47、`api[_-]?key=` 4 个补丁行命中 |
| Demo smoke | **PASS，但有首次启动时限风险** | 扩展观察窗下构建、健康、就绪、登录、受控查询均通过；标准一次性 `up --wait` 首次因 MySQL 初始化超时失败 |
| PostgreSQL parser P99 | **FAIL** | 5 轮仅 3 轮满足 P99 ≤ 5 ms；P99 为 4.365/4.615/6.467/4.235/6.517 ms |

因此本报告不能给出“发布前全部通过”的结论。`parser` 5 ms 闸门仍失败；此外应由负责人决定 4 个模块级漏洞与 Windows Docker 首次 demo 启动超时是否阻断发布。本批不自动修复。

## 2. 阶段 0：工具与范围调研

### 2.1 工具可用性

| 工具 | 宿主状态 | 实际使用版本/方式 |
| --- | --- | --- |
| Git | 可用 | `git version 2.55.0.windows.5` |
| Docker | 可用，但受限会话需开放 Docker API | Client/Engine `29.8.0`，Docker Desktop `4.92.0` |
| Docker Compose | 独立命令可用 | `docker-compose` v5.5.1；当前 `docker compose` 插件入口不可用 |
| Go | 宿主不可用 | 官方 `golang:1.25-bookworm`（Go 1.25.14）与 `golang:1.26-bookworm`（Go 1.26.8）容器 |
| govulncheck | 宿主不可用 | 容器内 `go install golang.org/x/vuln/cmd/govulncheck@latest`，得到 v1.8.0 |
| Node/npm | 宿主不可用 | 官方 `node:24-bookworm-slim`，Node v24.21.0、npm 11.19.0 |
| `rg` | 宿主不可用 | 调研与历史结果整理改用 PowerShell `Select-String`；不改变 Git 扫描范围 |

`govulncheck@latest` v1.8.0 要求 Go 1.26。直接用 Go 1.26 加载本项目时，`vitess.io/vitess@v0.22.4/go/hack/ensure_swiss_map.go` 对 Go 1.26 的兼容性守卫报 `invalid array length -1`。最终使用 Go 1.26.8 构建扫描器，并令源码扫描的包加载工具链为项目兼容的 Go 1.25.14；没有降级扫描器，也没有修改 `go.mod`/`go.sum`。

### 2.2 固定命令与覆盖范围

| 项目 | 命令/等价命令 | 范围 |
| --- | --- | --- |
| Go 源码漏洞 | `GOTOOLCHAIN=go1.25.14 govulncheck ./...` | Linux/amd64、默认 build tags 下的全仓 Go 包 |
| Go 模块补扫 | 在 `cmd/agentsql` 下执行 `govulncheck -scan module -show verbose` | 主入口解析到的 49 个模块；补充不可达模块漏洞明细 |
| 引入链 | `go mod why -m vitess.io/vitess golang.org/x/crypto` | 当前模块图 |
| 前端审计 | `npm audit --omit=dev --registry=https://registry.npmjs.org/` | `web/package-lock.json` 中 production dependencies；明确不含 devDependencies |
| Git 历史扫描 | 对每个高危 regex 执行 `git log --all --format=... -p --unified=0 --no-ext-diff --no-textconv -G <regex>` 并只匹配增删补丁行 | 所有 refs 的完整补丁历史；输出仅保留模式、commit、文件和计数，不回显候选值 |
| Demo | `docker-compose --project-name agentsql-v05-preflight --file docker-compose.demo.yml ...` | 独立 project、回环端口、随机临时凭据、合成数据；结束后 `down -v --remove-orphans` |
| Parser P99 | `go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v` | Go 1.25.14、Linux/amd64、8 CPU、`GOMAXPROCS=8`，串行 5 轮 |

## 3. Go 依赖漏洞

### 3.1 执行证据

- 源码扫描开始：2026-10-03 07:25:39 CST。扫描器输出 `GOVULNCHECK_SCAN_EXIT=0`；外层 Docker/PowerShell 清理等待未正常回传结束时间，确认扫描容器已经退出后中止了外层等待。有效结果为：`No vulnerabilities found`、代码可调用漏洞 0、导入包漏洞 0、仅依赖模块层面 4 项。
- 模块明细扫描：2026-10-03 07:36:47–07:39:49 CST，182.38 秒。模块级命中按 govulncheck 约定退出码 3。
- 引入链：2026-10-03 08:19:44–08:22:21 CST，157.122 秒，退出码 0。
- 扫描器：govulncheck v1.8.0；漏洞库 `https://vuln.go.dev`，库更新时间 2026-10-01 20:24:15 UTC。

### 3.2 模块级发现

govulncheck 本身不输出 CVSS；“上游严重度”取对应 CVE/GHSA 公告，“本项目判定”结合符号可达性。不得把模块级命中 4 项表述为“完全无漏洞”。

| ID | 上游严重度 | 命中版本 / 修复版本 | 引入链与本项目可达性 | 本项目判定 |
| --- | --- | --- | --- | --- |
| [GO-2026-6356](https://pkg.go.dev/vuln/GO-2026-6356) / CVE-2026-65959 | Medium，CVSS 3.1 6.5（[GHSA](https://github.com/vitessio/vitess/security/advisories/GHSA-mhc4-g3wh-cw7m)） | `vitess.io/vitess@v0.22.4`；修复于 v0.23.6 | `agentsql/internal/parser -> vitess.io/vitess/go/vt/sqlparser`；漏洞位于 vttablet VReplication `/debug/vrlog`，源码扫描未发现调用 | 中：模块命中，当前产品调用链不可达；后续依赖升级批次评估 |
| [GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355) / CVE-2026-56855 | High，CNA CVSS 3.1 7.5 | `golang.org/x/crypto@v0.55.0`；修复于 v0.56.0 | `agentsql/internal/rbac -> golang.org/x/crypto/bcrypt`；漏洞在 `x/crypto/ssh`，当前未导入/调用受影响包与符号 | 中：上游高危但当前不可达；后续依赖升级批次评估 |
| [GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354) / CVE-2026-78662 | High，CNA CVSS 3.1 7.5 | `golang.org/x/crypto@v0.55.0`；修复于 v0.56.0 | 同上；漏洞在 `x/crypto/ssh`，当前只因 `bcrypt` 引入模块 | 中：上游高危但当前不可达；后续依赖升级批次评估 |
| [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) | Unscored；OpenPGP 包不再维护且设计不安全 | `golang.org/x/crypto@v0.55.0`；无修复版本 | 同上；漏洞仅指 `x/crypto/openpgp*`，当前未导入这些包 | 低：模块级泛化命中；保持禁止新增 OpenPGP 调用 |

建议：另起依赖修复批次评估 Vitess v0.23.6 与 x/crypto v0.56.0 的兼容性并跑全量回归。`GO-2026-5932` 没有可直接升级到的修复版本，应通过源码/依赖门禁持续确认未导入 `openpgp*`。本批不改依赖。

## 4. 前端生产依赖审计

- 时间：2026-10-03 07:24:19–07:25:19 CST，60.433 秒。
- 命令：`npm audit --omit=dev --registry=https://registry.npmjs.org/`（`web/`，仓库只读挂载）。
- 环境：Node v24.21.0、npm 11.19.0。
- 结果：`found 0 vulnerabilities`，`NPM_AUDIT_EXIT=0`，Docker 退出码 0。
- 结论：在官方 registry 返回的当前 advisory 集合下，`package-lock.json` 的生产依赖未发现漏洞。devDependencies 按任务口径未覆盖，不能外推为全部前端依赖 0 漏洞。

## 5. Git 全历史密钥模式扫描

### 5.1 执行证据与结论

- 时间：2026-10-03 07:14:46–07:14:59 CST；5 个 regex 并行只读检索，每项耗时 7.658–11.218 秒。
- 高置信模式：AWS `AKIA[0-9A-Z]{16}` 0 行；`-----BEGIN (RSA|OPENSSH|EC) PRIVATE KEY-----` 0 行。
- 低置信赋值模式：`password=` 157 个补丁行、64 个路径；`secret=` 47 个补丁行、20 个路径；`api[_-]?key=` 4 个补丁行、3 个路径。
- 计数口径：同一文本的引入与删除分别计一行，因此不能把 208 个补丁行解释为 208 个唯一密钥。
- 初步复核：命中集中于公开 example/文档、测试夹具、安装/冒烟脚本的临时变量、历史构建后的前端资源，以及 JSX `apiKey=` 属性；未看到 AKIA 或私钥头。由于本批不回显候选值且没有做凭据有效性验证，赋值类命中仍保留为低严重度人工复核项，不能宣称“历史中不存在任何秘密”。

### 5.2 人工复核定位清单

以下 commit 为 12 位短哈希；必要时可用 `git show <commit> -- <path>` 只读复核。

#### `password=`

| 路径 | 补丁行 | commit |
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

| 路径 | 补丁行 | commit |
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

| 路径 | 补丁行 | commit |
| --- | ---: | --- |
| `docs/en/INTEGRATIONS.md` | 1 | `641366f26376` |
| `docs/INTEGRATIONS.md` | 1 | `857db5baf750` |
| `web/src/pages/Agents.tsx` | 2 | `111cfbf4b2b7` |

`Agents.tsx` 两处为 React JSX `apiKey=` 属性传值，不是硬编码密钥。文档命中仍保留在清单中供负责人复核。

## 6. Demo smoke

### 6.1 标准一次性启动：FAIL

- 时间：2026-10-03 07:44:35–08:04:59 CST，1224.35 秒（包含首次镜像拉取与构建）。
- 命令：`docker-compose --project-name agentsql-v05-preflight --file docker-compose.demo.yml up -d --build --wait --wait-timeout 900`。
- 构建：`agentsql:v0.5.0` 与 `agentsql/postgres-binder-demo:16-0.5` 构建成功。
- 失败点：`demo-mysql` 在 Compose 自带的 30 次健康重试内仍处于初始化，Compose 报 `dependency demo-mysql failed to start`；`demo-postgres` 已健康。日志显示 MySQL 8.4.11 正在初始化，没有 SQL 初始化报错，但尚未到 seed、网关或 API 阶段。
- 清理：独立 project 的容器、网络、3 个测试卷均删除成功，`CLEANUP_EXIT=0`。

### 6.2 分阶段扩展观察：PASS

- 时间：2026-10-03 08:06:21–08:12:29 CST，367.938 秒。
- 方法：复用已构建镜像，先启动 PostgreSQL/MySQL；不修改健康检查定义，只在外部最多观察 10 分钟。MySQL 曾转为 `unhealthy`，约第 55 次 5 秒观察时恢复为 `healthy`；随后启动 seed 与 gateway。
- 结果：seed 退出码 0；gateway 容器健康。
- `/healthz`：`status=ok`、`demo.enabled=true`、banner 存在。
- `/readyz`：`status=ready`。
- `POST /api/v1/auth/login`：`code=0`、token 存在。
- `POST /api/v1/playground/run`：固定 `ds-demo-pg`/`ro` 身份执行一条受控 JOIN 点查；`code=0`、`decision=allow`、1 行、2 个脱敏单元格、`audit_id` 存在。
- 清理：容器/网络/卷删除成功，最终按 Compose project label 查询无残留。

结论：功能链路 PASS，但默认一次性首次启动在本机 Windows Docker 持久卷上不能稳定通过既有启动时限。严重度判为**中**：不影响已就绪环境的功能结果，但影响首次 demo 交付体验。建议负责人决定是否在单独批次调整初始化/等待策略；不得用本次扩展观察结果掩盖标准命令的首次 FAIL。

## 7. PostgreSQL parser P99 复测

- 时间：2026-10-03 08:13:59–08:18:02 CST，243.764 秒（含首次下载/编译依赖；计时样本在测试内部，不含下载编译）。
- 环境：`golang:1.25-bookworm`，Go 1.25.14，Linux/amd64，8 CPU，`GOMAXPROCS=8`。
- 命令：`go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v`。
- 门槛：测试源码中的 `typicalP99Budget = 5ms`，未修改。

| 轮次 | P50 | P90 | P99 | 5 ms 闸门 |
| ---: | ---: | ---: | ---: | --- |
| 1 | 1.672222 ms | 3.029721 ms | 4.364518 ms | PASS |
| 2 | 1.834034 ms | 3.208935 ms | 4.615138 ms | PASS |
| 3 | 1.936738 ms | 3.480248 ms | 6.466874 ms | FAIL |
| 4 | 1.709622 ms | 3.047517 ms | 4.235102 ms | PASS |
| 5 | 1.904345 ms | 3.314153 ms | 6.517398 ms | FAIL |

整体 `go test` 退出码 1，仅 3/5 轮通过。相较批十九，本次中位与 P90 仍较稳定，P99 尾部继续跨越 5 ms，因此结论保持**高严重度发布闸门失败**，不放宽门槛、不标记达标。

“独占”指本批没有并发执行其他 Codex 扫描、构建或测试，并限制测试容器使用 8 CPU；未停止用户先前已运行的其他 Docker 容器，因此不能声称是裸机级绝对无后台负载。若负责人要求严格发布判定，应在固定 Linux runner、清空无关负载后再独占复跑 5 轮，但本次真实 FAIL 不能因此抹除。

## 8. 发现项、建议与未覆盖项

| 编号 | 严重度 | 发现 | 建议（本批不执行） |
| --- | --- | --- | --- |
| F-01 | 高 / 发布闸门 | PostgreSQL parser P99 仅 3/5 轮 ≤ 5 ms | 固定 Linux runner 再复测并采集 CPU/alloc/GC 证据；未稳定通过前保持发布闸门失败 |
| F-02 | 中 | 4 个模块级 Go 漏洞：2 个上游 High、1 个 Medium、1 个 Unscored；当前符号/包不可达 | 独立依赖升级批次评估 Vitess v0.23.6、x/crypto v0.56.0；保持 OpenPGP 禁用 |
| F-03 | 中 | Windows Docker 首次 demo 一次性启动超过 MySQL 健康重试窗口 | 单独评估健康重试、初始化说明或预热策略；不得降低数据库健康判定 |
| F-04 | 低 / 人工复核 | 历史赋值类 regex 命中 208 个补丁行 | 负责人按附录复核 example/测试/历史 dist；若发现真实凭据，先轮换再另批处理历史，禁止在本批改写历史 |

未覆盖：

- KingbaseES V9 仍待 2026-10-08 厂商镜像与 license；DM8 真库仍待用户凭据。本批没有把缺少环境表述为通过。
- Git 扫描是指定 regex 的全历史补丁扫描，不是熵扫描、凭据在线有效性验证或第三方 secret scanner；未覆盖 Git LFS/外部制品库/已删除远端 refs。
- npm 审计按任务要求排除 devDependencies；没有覆盖容器 OS 包、浏览器运行时或供应链签名。
- govulncheck 为 Linux/amd64、默认 build tags；可选 build tags、未构建平台和外部插件需各自扫描。
- Demo 仅覆盖本地合成数据、健康/就绪、管理登录与一次受控 PostgreSQL 查询；未覆盖公网 TLS、反向代理、HA、故障切换、完整 PostgreSQL/MySQL 权限矩阵或生产数据。
- 本批未修复任何发现项，未执行 `git add`、commit、merge、push、tag 或历史改写。
