# 批次七十五：v0.5.0 发布闸门离线验证报告

验证日期：2026-10-09（CST，UTC+08:00）。基线：`bfc743ad6f8ab3241b6051248e5d530ca630c4a0`，分支 `feature/v0.4`。本批只验证，未修改业务代码、未执行 git 写操作、未联网、未签名。所有容器运行均使用 `--network none --pull never`，Go 命令使用 `GOPROXY=off` 与 `GOTOOLCHAIN=local`。原始日志均有开始/结束时间和退出码，位于 [`dist/release-gate-b75/`](../dist/release-gate-b75/)。

**总判定：发布 B 闸门未通过。** 当前离线环境缺 Go 模块缓存，P99 五轮、两次全仓构建和定向三包测试均未完成；全仓 short tests 命令退出 1。15 资产 dry-run 在本批禁网及文件范围约束下未执行。以下 PASS 仅代表对应命令实际通过，不代表发布资产可用。

## 1–7 项闸门

| 项目 | 结论 | 真实证据与原因 |
| --- | --- | --- |
| 1. parser P99 连续五轮 | **NOT RUN** | [`p99.log`](../dist/release-gate-b75/p99.log)：禁网标准测试命令实际退出 1，`pg_query_go/v5`、`vitess` 缓存缺失，在 parser setup 阶段停止。五轮均未开始；P50/P90/P99 的第 1–5 轮全部为 **N/A**，不能判定“五轮 P99 均 ≤5ms”。 |
| 2a. `go build ./...` | **NOT RUN（未完成）** | [`build-all.log`](../dist/release-gate-b75/build-all.log)：实际退出 1，`GOPROXY=off` 下多处模块查找失败。 |
| 2b. `go build -tags=yashan ./...` | **NOT RUN（未完成）** | [`build-yashan.log`](../dist/release-gate-b75/build-yashan.log)：实际退出 1，同为模块缓存缺失；不能判作含崖山标签构建 PASS。 |
| 3. parser/rules/pipeline 定向 short tests | **NOT RUN（未完成）** | [`short-targeted.log`](../dist/release-gate-b75/short-targeted.log)：实际退出 1，三个包均在 setup 阶段失败，没有测试断言结果。 |
| 4. 全仓 short tests | **FAIL（命令退出 1）** | [`short-all.log`](../dist/release-gate-b75/short-all.log)、[逐包结果](../dist/release-gate-b75/short-all-packages.log)、[包枚举](../dist/release-gate-b75/package-enumeration.log)：47/47 包有结果，6 包实际测试 PASS，6 包无测试文件，35 包因缺模块缓存 setup failed、测试 **NOT RUN**；没有已执行测试的断言失败。包级成功 12/47（25.53%，含 6 个无测试文件包）；有测试包实际通过 6/41（14.63%）。`internal/b5dml` 用时 495.220 秒且 PASS，并非超时。 |
| 5. 15 资产 dry-run | **NOT RUN** | [`dryrun.log`](../dist/release-gate-b75/dryrun.log)、[脚本前置条件](../dist/release-gate-b75/offline-prerequisites.log)：`pwsh` 不在 PATH；原脚本直接调用未加禁网参数的 Docker、在线 `dnf`/`curl`/Go 代理，并写入指定输出目录之外，故不能按本批红线执行。`RELEASE_DRYRUN_ASSET_COUNT`、`SHA256SUMS`、Yashan 再分发标识及 `IMAGES` 均无真实运行结果；未产出本批 15 资产。 |
| 6. Rocky 8 双架构可运行、shell 语法 | **PASS（仅这两项）** | [`dual-arch.log`](../dist/release-gate-b75/dual-arch.log)：amd64 为 `x86_64`、arm64 为 `aarch64`，两次退出 0；两架构的 `build-release-linux.sh`、`package-release.sh` 语法检查退出 0。正式构建与打包 **NOT RUN**，所需下载及前置条件见下文。 |
| 7. 许可证文件存在性 | **PASS（存在性核查）** | [`license.log`](../dist/release-gate-b75/license.log)：仓库根跟踪的 `LICENSE`、`NOTICE` 均存在，`LICENSE.md` 不存在。这与任务背景“许可证文件不存在”的摸底不一致。未读取文件内容，因此许可证类型未核定，仍待用户拍板。 |

[环境镜像证据](../dist/release-gate-b75/environment.log)显示本机已缓存脚本指定 digest 的 Rocky 8 与 Syft，Buildx v0.37.1 可用。[签名工具编译日志](../dist/release-gate-b75/releasesign-build.log)显示 Go 1.26.8 Linux/amd64 和 `scripts/releasesign` 编译退出 0；这不代表签名验证通过。`build-release-linux.sh` 会运行 `dnf`、获取 go.dev 发布清单和 Go tarball、`go mod download`、获取双架构崖山客户端；`package-release.sh` 还要求正式二进制、Go 工具链、崖山库及根 `LICENSE`。这些正式构建前置条件未离线完成。

## 全仓 short tests 逐包结果

表中 **NOT RUN** 对应 Go 原始输出的 `[setup failed]`，原因是离线模块缓存缺失；**PASS（无测试）** 只表示包命令成功且 `[no test files]`，不计作测试执行通过。完整错误上下文见 [`short-all.log`](../dist/release-gate-b75/short-all.log)。

| 包（省略 `github.com/cuipengdba/agentsql/` 前缀） | 状态 | 说明 |
| --- | --- | --- |
| `cmd/agentsql` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `cmd/agentsql-b2-fallback` | **PASS** | 无测试文件 |
| `cmd/agentsql-b2-gate` | **PASS** | 无测试文件 |
| `cmd/agentsqlctl` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/adminapi` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/audit` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/auditchain` | **PASS** | 测试通过，0.031s |
| `internal/auditrelay` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/auth` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/authorizedexecute` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/authorizedexecute/internal/businessdb` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/b2release` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/b5` | **PASS** | 测试通过，0.095s |
| `internal/b5coordinator` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/b5dml` | **PASS** | 测试通过，495.220s |
| `internal/b5session` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/b5terminal` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/b5wal` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/bootstrap` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/columnauth` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/compliance` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/config` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/controlledread` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/demoseed` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/discovery` | **PASS** | 测试通过，0.029s |
| `internal/engine` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/eventbus` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/lockrank` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/mask` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/mcpserver` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/metrics` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/model` | **PASS** | 测试通过，0.030s |
| `internal/notify` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/parser` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/pipeline` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/policy` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/rbac` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/redaction` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/rules` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/server` | **PASS** | 无测试文件 |
| `internal/store` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `internal/version` | **PASS** | 无测试文件 |
| `internal/webui` | **PASS** | 测试通过，0.009s |
| `scripts` | **PASS** | 无测试文件 |
| `scripts/highgo` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `scripts/highgo/pgx-probe` | **NOT RUN** | setup failed；离线模块缓存缺失 |
| `scripts/releasesign` | **PASS** | 无测试文件 |

## 缺口清单

以下按当前机器的实际证据计数：**A 5 项、B 4 项、C 3 项、本地环境 2 项，共 14 项**。Rocky 8 与 Syft 的指定 digest 已缓存，故不虚列为本机下载缺口；若换全新发布 runner，仍须联网获取并校验它们。

### A. 必须联网获取或恢复出站访问（5 项）

1. **Go 模块依赖**：parser 的 `pg_query_go/v5`、`vitess`，以及全仓的 testcontainers、`modernc.org/sqlite` 等多项缺缓存；35 包在 setup 阶段停止。需按锁定版本预取并校验完整缓存后重跑全部 Go 门。
2. **正式 Go 1.26.8 双架构 tarball 与 go.dev 校验清单**：发布脚本固定下载、核验 amd64 与 arm64 文件；本机 amd64 Go 容器不能替代正式脚本的下载校验。
3. **崖山客户端**：发布脚本要求固定版本的 x86_64、aarch64 客户端及两个共享库，需要获取并核对脚本内 SHA-256。
4. **Rocky 构建依赖包**：脚本在容器内执行 `dnf install`，完全禁网时未预置这些 rpm 及仓库元数据，正式构建不能照原脚本完成。
5. **正式发布服务出站连接**：GitHub tag/Release 与回下载、GHCR 推送和匿名拉取、官网部署与验证、PVR 设置都要求网络。本批全部未执行。多架构 Buildx 图的基础镜像元数据也需在正式构建时验证可离线解析，当前未证明。

### B. 必须凭据或管理权限（4 项）

1. GitHub 仓库 tag/Release 写权限及 `write:packages` scope。
2. GHCR package 管理权，含公开可见性设置。
3. 官网 ECS 部署凭据，以及相关 DNS/证书权限。
4. GitHub PVR 设置访问权限。

### C. 必须用户拍板（3 项）

1. **许可证类型**：Apache-2.0 与 AGPLv3 的取舍仍需用户决定。当前 `LICENSE`、`NOTICE` 已存在，但本批禁止读取其内容，不能据此认定类型；不能写成“许可证文件缺失”。
2. 正式 Ed25519 私钥的落盘、保管与销毁策略。本批未生成密钥或签名。
3. 2026-10-09 今天的具体发布时间窗。现有清单页首仍写 2026-10-16 16:00 CST，与本次用户决定不一致；本批没有改动清单。

### 本地环境与工作树（2 项）

1. 当前 Windows 会话没有 `pwsh`，原 15 资产脚本无法按指定命令启动；需在发布环境提供 PowerShell 7，且先解决脚本禁网执行约束。
2. [git 状态日志](../dist/release-gate-b75/git-state.log)显示基线工作树已有两处位于用户禁止触碰路径的未跟踪项；本批未读取或清理。正式清单要求干净 sealed checkout，须在另一个合规干净工作树重验。`git diff --check` 退出 0 仅表示已跟踪差异没有空白错误。

## 发布判定

截至本批结束，**v0.5.0 不满足发布日 B 闸门**：P99 连续五轮无数据，两种全仓 build 无 PASS，定向 short tests 无 PASS，全仓 short tests 退出 1，15 资产 dry-run 无产物。双架构运行与脚本语法、许可证存在性、签名工具编译的局部 PASS 不能替代这些必需闸门。恢复依赖与合规环境后须按相同固定命令重新运行，并保留新的完整日志。