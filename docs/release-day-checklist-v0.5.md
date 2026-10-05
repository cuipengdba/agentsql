# AgentSQL v0.5.0 发布日最终执行清单

> 闸门时间：2026-10-16 16:00 CST（北京时间，UTC+08:00）
>
> 本清单最近核对批次：批三十三，2026-10-04，基线提交 `e8777079a342dd9d92c29989e0cd37e35846553e`；批三十一发布工程证据继续保留
>
> 批三十三边界：未签名、未建 tag、未推送、未创建 Release、未 push 镜像、未访问发布站点；未执行任何 git 写操作。

## 1. 当前结论（批三十三；批三十一发布工程证据保留）

- 15 个 GitHub Release 资产的唯一可执行定义位于 `scripts/release-dryrun.ps1` 的 `Get-ExpectedAssetNames`；本文和 `docs/release-notes-v0.5.md` 与它对齐。
- `scripts/build-release-linux.sh` 与 `scripts/package-release.sh` 都只接受 `amd64` / `arm64`。前者要求容器原生架构与 `ARCH` 一致，因此 arm64 依赖原生 arm64 runner 或 Docker/QEMU；本机 Docker/QEMU 的禁网探针已分别回报 `x86_64` 和 `aarch64`。
- `go.mod` 已是 Go 1.26；正式脚本现固定 Go 1.26.8 并设置 `GOTOOLCHAIN=local`。`Makefile` 的开发默认值仍是历史 `1.25.14`，本批按改动范围未修改；发布日不得依赖该默认值，如人工调用 make target 必须显式传 `GO_VERSION=1.26.8`。
- 现存 `dist/release-dryrun/v0.5.0` 来自旧提交 `02565f5...`，且两个 tar 均缺 `lib/yashandb/libyascli.so`、`libyas_infra.so`，Go metadata 也缺 `yashandb-go v1.4.4`。`-ValidateOnly` 真实返回 FAIL，旧资产不可复用。
- 本批按任务红线跳过外网。完整当前 HEAD 构建因 `dnf`、go.dev、阿里云 Go 镜像、GitHub Raw 崖山客户端及 BuildKit 基础镜像元数据均需要外网而阻塞；不得写成 PASS。
- 本地缓存的 `golang:1.26-bookworm` 实测为 Go 1.26.8 amd64，但没有 arm64 平台镜像；原生发行脚本发布日必须按固定版本下载并校验两架构 Go tarball，不能用本批缓存替代。
- 旧的三个 OCI 归档确有 amd64/arm64 descriptor，但三者的平台镜像 digest 完全相同；旧 `quickstart` 只是主镜像换 tag，缺 Compose 所需 demo 配置。发布脚本现用同一个 Buildx Bake 图构建 demo base 和叠加两份配置的 quickstart target，并实际解析 OCI descriptor。
- `scripts/build-ghcr-multiarch.ps1 -Push` 现只推精确版本和可选辅助 tag，不再同时移动 `latest`；精确 tag 匿名双架构验收后，单独执行 `-PromoteLatest`。
- `scripts/release-dryrun.ps1 -ProductionPrepare` 提供生产候选的可执行组装路径：要求完全干净工作树并复制授权公钥，只生成 11 个待签名文件，不读取私钥、不生成签名。补齐两份内容签名、`SHA256SUMS` 和其签名后才形成 15 项。
- R005 的发布说明曾滞后于批十七提交 `02565f5`。代码已覆盖普通生产动态 EXPLAIN 告警、通用执行实际截断补告警和 PostgreSQL 列级授权实际截断补告警；结构化命中位于 `assessment.hits`，同一命中持久化到审计 `rule_hits`，可由管理审计 API 查询。批三十三已校正文档，不再把该项列为开放 P0。
- PostgreSQL parser P99 **仍阻断发布**。批三十三在 Go 1.26.8 Linux/amd64、Intel i7-6700、8 CPU、`GOMAXPROCS=8` 下，优化前和优化后均仅 3/5 轮满足 P99 ≤ 5 ms；优化后为 4.456/4.775/6.404/6.554/4.173 ms。不得用平均 benchmark 改善替代尾延迟门，也不得放宽阈值。

## 2. 15 资产核对表

`SHA256SUMS` 覆盖除自身和 `SHA256SUMS.sig.json` 外的另外 13 个资产；两个 tar 内另有独立的 payload `SHA256SUMS`。

| # | 资产 | 来源 | 发布日校验 | 批三十一状态 |
| ---: | --- | --- | --- | --- |
| 1 | `agentsql-v0.5.0-linux-amd64.tar.gz` | Rocky 8 amd64：`build-release-linux.sh` → `package-release.sh` | 外层 sidecar；包内全文件 SHA-256；ELF x86-64；GLIBC ≤ 2.28；二进制版本；Yashan 两库及 `ldd` | **阻塞**：旧包缺 Yashan；当前构建需外网 |
| 2 | `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | `package-release.sh` | 严格 `64hex␠␠filename`，重算匹配 | **阻塞**：随 #1 重建 |
| 3 | `agentsql-v0.5.0-linux-arm64.tar.gz` | Rocky 8 arm64/QEMU：同上 | 外层/包内 SHA-256；ELF AArch64；GLIBC ≤ 2.28；版本；Yashan 两库及 arm64 `ldd` | **阻塞**：旧包缺 Yashan；当前构建需外网 |
| 4 | `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | `package-release.sh` | 同 #2 | **阻塞**：随 #3 重建 |
| 5 | `agentsql-v0.5.0.spdx.json` | 固定 digest Syft 对 sealed modules + 四个二进制生成 | SPDX JSON 可解析；namespace 非 dry-run；包/关系数记录 | **阻塞**：等待当前双架构二进制 |
| 6 | `agentsql-v0.5.0.spdx.json.sig.json` | `scripts/releasesign/main.go -mode sign` | `-mode verify` 对 SBOM 原始字节通过 | **本批禁止签名** |
| 7 | `ed25519-release-public-key.json` | 正式 Ed25519 release key 导出 | schema/algorithm/keyClass/keyId；三份签名均用同一 key ID | **本批禁止生成正式密钥** |
| 8 | `go-version-metadata.txt` | `go version -m`，四个 Linux 二进制合并 | 两架构均存在；`agentsql` 含 `yashandb-go v1.4.4` | **阻塞**：旧文件缺 Yashan |
| 9 | `install.sh` | sealed commit 的 `scripts/install.sh` | 与源码 SHA-256 一致；`sh -n`；离线 `--from` 安装 | 源文件 amd64/arm64 `sh -n` **PASS**；实际安装等待新包 |
| 10 | `provenance.json` | `-ProductionPrepare` | version/commit/source epoch/输入哈希正确；非 dry-run；`dirtyAtBuild=false` | 命令已补；未执行生产准备 |
| 11 | `provenance.json.sig.json` | 正式签名工具 | `-mode verify` 对 provenance 原始字节通过 | **本批禁止签名** |
| 12 | `SBOM-GENERATION.txt` | `-ProductionPrepare` | 固定 Syft digest、epoch、package/relationship 数与实际一致；不得写 dry-run | 命令已补；未生成 |
| 13 | `SHA256SUMS` | 签完 #6/#11 后对另外 13 项生成 | 恰好 13 个唯一 basename；逐项重算通过 | 等待 #1–#12/#15 |
| 14 | `SHA256SUMS.sig.json` | 正式签名工具，最后签 | `-mode verify` 对最终 manifest 原始字节通过 | **本批禁止签名** |
| 15 | `VERIFYING-SIGNATURES.md` | `-ProductionPrepare` | 命令指向 v0.5.0 三个真实签名；不得带 dry-run 声明 | 命令已补；未生成 |

## 3. 批三十一真实命令与输出摘要

以下只记录影响发布判定的命令；调研用的 `Get-Content` / `git grep` 等只读命令不作为闸门证据。

| 命令 | 真实输出 / 判定 |
| --- | --- |
| `git branch --show-current`；`git show -s 33f2d56` | `feature/v0.4`；HEAD/前置均为 `33f2d568...` |
| `docker version`；`docker buildx version` | client/server `29.8.0`；Buildx `v0.37.1` |
| `docker run --rm --pull never --network none --platform linux/{amd64,arm64} rockylinux:8 ... uname -m` | `x86_64` / `aarch64`，两架构运行能力存在 |
| `./scripts/release-dryrun.ps1 -ValidateOnly -AssetsDirectory dist/release-dryrun/v0.5.0/assets` | **FAIL**：`go-version-metadata.txt does not include yashandb-go v1.4.4.` |
| `tar -tzf` 检查旧 amd64/arm64 tar | 两包都没有 `lib/yashandb/` 条目 |
| 禁网运行旧本地 `agentsql:v0.5.0` 并检查 Yashan 路径 | 版本为 `v0.5.0`，但两库均 `No such file or directory`，不可作当前候选 |
| 解析三个旧 OCI 的 `index.json` 与嵌套 index | 每份都有 `linux/amd64`、`linux/arm64` 和两份 attestation；三个归档的平台 manifest digest 都是 amd64 `774548d1...`、arm64 `97cb882b...`，证明旧辅助 tag 未形成 quickstart 层 |
| 当前工作树 `docker buildx build --network none --pull=false --target quickstart ...` | **未完成/不合规**：仍出现 `resolve image config` / `load metadata for docker.io/...`；客户端 PID 经命令行核对后终止，临时目录已清理 |
| 两架构 Rocky 禁网容器执行 `sh -n scripts/{build-release-linux.sh,package-release.sh,install.sh,push-release-image.sh}` | amd64 `SHELL_SYNTAX=PASS`；arm64 `SHELL_SYNTAX=PASS` |
| Go 1.26.8 禁网容器 `GOTOOLCHAIN=local GOPROXY=off go build ./scripts/releasesign` | exit 0；只编译，未生成密钥、未签名 |
| PowerShell AST 解析两份 `.ps1`；`git diff --check` | PASS |

## 4. 发布日串行执行清单

所有命令都在 sealed commit 的全新工作目录执行。遇到未知输出、名称不一致、dirty worktree、摘要不一致、单架构缺失或任何命令非零，立即停止；不得继续 tag/push。

### A. 凭据与环境（只检查，不构建）

1. 负责人确认：GitHub `repo` 与 `write:packages` 权限、GHCR package 管理权、正式 Ed25519 私钥落盘策略、官网部署凭据、PVR 设置访问权。
2. 准备 amd64 + arm64/QEMU、Docker/Buildx、PowerShell、Go 1.26.8、Node + Edge（PDF）及出站 HTTPS。`go.mod` 是 `go 1.26.0`；发布脚本固定 `GOTOOLCHAIN=local`，禁止悄悄下载另一工具链。
3. 使用全新 clone，核对并封存提交：

```powershell
git switch feature/v0.4
git rev-parse HEAD
git status --porcelain
git diff --check
```

验收：`status` 无任何输出；HEAD 是最终批准提交，不一定仍是本批基线 `33f2d56`。

### B. 代码与双架构构建闸门

```powershell
docker run --rm --platform linux/amd64 -e GOMAXPROCS=8 -e GOTOOLCHAIN=local -v "${PWD}:/src:ro" -w /src golang:1.26.8-bookworm /usr/local/go/bin/go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v
docker run --rm --platform linux/amd64 -e GOTOOLCHAIN=local -v "${PWD}:/src:ro" -w /src golang:1.26.8-bookworm /usr/local/go/bin/go test -short ./internal/rules ./internal/pipeline -count=1
docker run --rm --platform linux/amd64 -v "${PWD}:/src" -w /src golang:1.26.8-bookworm sh -c 'GOTOOLCHAIN=local go build ./... && GOTOOLCHAIN=local go build -tags=yashan ./...'
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -OutputDirectory dist/release-dryrun/v0.5.0-final
```

验收：parser 命令连续 5 轮均为 PASS 且每轮 P99 ≤ 5 ms，任一轮失败立即停止；R005 所在 rules/pipeline short tests PASS；两次 Go build PASS；dry-run 输出 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_IMAGES=PASS`、`PASS_UNSIGNED_NOT_FOR_RELEASE`。逐项保存完整日志。dry-run 占位签名禁止上传。

### C. 正式公钥、生产候选与 15 资产

在受控目录生成本版本正式密钥；私钥路径必须在仓库和输出目录之外，并按负责人策略保管或销毁：

```powershell
$Key = 'X:\secure\agentsql-v0.5.0-ed25519.seed'
$Public = 'X:\secure\ed25519-release-public-key-v0.5.0.json'
go run ./scripts/releasesign/main.go -mode generate -key $Key -public $Public
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -ProductionPrepare -PublicKeyPath $Public -OutputDirectory dist/release-prepare/v0.5.0-final
$Assets = (Resolve-Path 'dist/release-prepare/v0.5.0-final/assets').Path
```

验收：脚本输出 `RELEASE_PREPARE_UNSIGNED_ASSET_COUNT=11`、`PASS_UNSIGNED_REQUIRES_SIGNING`，provenance commit 等于 sealed HEAD、`dirtyAtBuild=false`，所有说明均不含 dry-run 文案。

签两份内容文件，再生成覆盖其余 13 项的 manifest，最后签 manifest：

```powershell
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/agentsql-v0.5.0.spdx.json" -signature "$Assets/agentsql-v0.5.0.spdx.json.sig.json"
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/provenance.json" -signature "$Assets/provenance.json.sig.json"
$ManifestNames = @(
  'agentsql-v0.5.0-linux-amd64.tar.gz','agentsql-v0.5.0-linux-amd64.tar.gz.sha256',
  'agentsql-v0.5.0-linux-arm64.tar.gz','agentsql-v0.5.0-linux-arm64.tar.gz.sha256',
  'agentsql-v0.5.0.spdx.json','agentsql-v0.5.0.spdx.json.sig.json',
  'ed25519-release-public-key.json','go-version-metadata.txt','install.sh',
  'provenance.json','provenance.json.sig.json','SBOM-GENERATION.txt','VERIFYING-SIGNATURES.md'
) | Sort-Object
$Lines = foreach ($Name in $ManifestNames) {
  $Hash = (Get-FileHash -LiteralPath (Join-Path $Assets $Name) -Algorithm SHA256).Hash.ToLowerInvariant()
  "$Hash  $Name"
}
[IO.File]::WriteAllText((Join-Path $Assets 'SHA256SUMS'), (($Lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding($false)))
go run ./scripts/releasesign/main.go -mode sign -key $Key -input "$Assets/SHA256SUMS" -signature "$Assets/SHA256SUMS.sig.json"
```

最终验签和拓扑校验：

```powershell
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/agentsql-v0.5.0.spdx.json" -signature "$Assets/agentsql-v0.5.0.spdx.json.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/provenance.json" -signature "$Assets/provenance.json.sig.json"
go run ./scripts/releasesign/main.go -mode verify -public "$Assets/ed25519-release-public-key.json" -input "$Assets/SHA256SUMS" -signature "$Assets/SHA256SUMS.sig.json"
pwsh ./scripts/release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory $Assets
```

验收：三次 `verified`；`RELEASE_DRYRUN_ASSET_COUNT=15`；最终目录恰好 15 个文件。`-ValidateOnly` 只做签名结构检查，不能替代前三条密码学验签。

### D. tag、Release draft 与回下载

本仓库 v0.4.0 使用 annotated tag；v0.5.0 沿用该类型。只有 A–C 全绿后才能执行：

```powershell
$Commit = git rev-parse HEAD
git tag -a v0.5.0 $Commit -m 'AgentSQL v0.5.0'
git show -s --format='%H %s' 'v0.5.0^{}'
git push origin refs/tags/v0.5.0
$AssetFiles = @(Get-ChildItem -LiteralPath $Assets -File | Sort-Object Name)
if ($AssetFiles.Count -ne 15) { throw "expected 15 assets, got $($AssetFiles.Count)" }
$AssetPaths = @($AssetFiles.FullName)
gh release create v0.5.0 --draft --verify-tag --title 'AgentSQL v0.5.0' --notes-file docs/release-notes-v0.5.md $AssetPaths
```

凭据/外网：Git push 权限、GitHub Release 写权限、GitHub 外网。验收：tag 指向 sealed commit；draft 未公开；draft 恰好 15 项。随后下载到全新目录，重复 C 的三次验签和 `-ValidateOnly`。回下载任何字节差异都阻断。

### E. GHCR 精确 tag、辅助镜像与 latest

先授权并只推精确 tag；这一步不会移动 `latest`：

```powershell
gh auth refresh -h github.com -s write:packages
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags -Push -DigestOutput dist/release-prepare/v0.5.0-final/image-digests-pushed.json
```

验收：`v0.5.0`、`v0.5.0-demo`、`v0.5.0-quickstart` 均为 public，均含 linux/amd64 + linux/arm64；digest JSON 顶层 `pushed=true` 且有三个 tag 记录。在两台无 GHCR 登录的干净 amd64/arm64 环境分别 pull 精确 tag。主镜像必须启动并使 `/healthz` 回报 `v0.5.0`、`/readyz` 就绪；quickstart tag 还必须通过：

```bash
docker run --rm --entrypoint sh ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart -c 'test -f /etc/agentsql/config.demo.yaml && test -f /etc/agentsql/demo-seed.yaml && test "$(agentsql --version)" = v0.5.0'
```

再按 `deploy/quickstart/docker-compose.yml` 完成 seed → gateway 健康链路与 `pwsh ./demo/reset.ps1`。所有精确 tag 闸门通过后才移动 `latest`：

```powershell
pwsh ./scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -PromoteLatest
```

验收：脚本确认 `latest` 与 `v0.5.0` 顶层 digest 相同；再次匿名 pull `latest` 并做版本/健康检查。

### F. 官网、正式发布与发布后

1. `node website/tools/build-docs-pdf.mjs` 重导三份 v0.5.0 PDF，核对网站版本、Release 链接、robots 与 `/demo/` noindex 边界。
2. 按 `website/deploy/README-DEPLOY.md` 执行 stage A；运行 `bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a`。备案、DNS、证书和隔离就绪后再 stage B，并运行 `bash ./deploy/verify-static.sh https://agentsql.cn stage-b`。
3. 仓库管理员在 GitHub Security settings 人工确认 PVR 已开启。
4. 以上全部绿后把 GitHub Release draft 转正式；随后从无登录干净环境复验 `releases/latest`、15 资产下载/验签/离线安装、三个 GHCR tag、官网、Demo 允许/拒绝/审批链路。

## 5. 禁止与阻塞

- 禁止上传 dry-run 目录；其 `dryRun=true` / `signed=false` 占位文档、dry-run SBOM namespace 和说明不是发布资产。
- 禁止在 `v0.5.0` 精确镜像匿名双架构验收前移动 `latest`。
- 禁止把本批旧资产、旧 OCI descriptor 或脚本语法 PASS 写成当前 HEAD 构建 PASS。
- 发布日仍需外网：Rocky/Syft/Go/崖山客户端获取与校验、GitHub/GHCR、PVR、官网部署与公网验收。
- 发布日仍需凭据：正式 Ed25519 密钥落盘策略、Git tag/push、GitHub Release、`write:packages`、GHCR visibility、官网主机/DNS/证书权限。
- `Makefile` 的 `GO_VERSION ?= 1.25.14` 不是正式发布值；使用 `scripts/release-dryrun.ps1` / `-ProductionPrepare`，或对 make 显式覆盖为 `1.26.8`，否则 `GOTOOLCHAIN=local` 会 fail-closed。
