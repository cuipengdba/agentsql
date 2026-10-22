# 发布后加固 post2 报告

**语言 / Language:** [简体中文](postrelease-hardening-post2-report.md) | [English](en/postrelease-hardening-post2-report.md)

日期：2026-10-11。下列容器时间戳均为 UTC（北京时间加 8 小时）。所有 Go 验收均使用本机已有的 `golang:1.26-bookworm` 镜像，`--network none`，源码根 `D:\ruanjiansheji\agentsql-v04` 只读挂载到 `/workspace`，宿主模块缓存 `C:\Users\Administrator\go\pkg\mod` 只读挂载到 `/go/pkg/mod`，仓库 `.gocache` 挂载到 `/gocache`，并设置 `GOCACHE=/gocache`、`GOPROXY=off`。容器工作目录为 `/workspace`。未删除 `dist/`。

## 修复

| 项目 | 根因 | 改动与结论 |
| --- | --- | --- |
| P0-2 | 两个源码目录遍历测试进入 git 忽略的历史产物 `dist/`，将产物副本误判为源码。 | 仅在 `TestBusinessDriverImportsStayInsideCapabilityDomain` 与 `TestSQLSinkCallsitesMatchManifest` 的 `WalkDir` 回调中对目录名 `dist` 返回 `filepath.SkipDir`；原有跳过名单及判定逻辑保持不变。两个测试在 `dist/` 存在时均 PASS。 |
| P0-3 | 编译器测试在仓库根创建临时目录，只读挂载时报 `read-only file system`。 | 使用 `os.MkdirTemp("", "capabilitycompile")`；每个子测试在系统临时目录写入临时 `go.mod`，以 `replace` 指向仓库根，并从该目录执行 `go test -mod=mod .`。子进程设置 `GOWORK=off`、`GOPROXY=off`、`GOSUMDB=off`，避免离线环境对新临时模块查询校验服务器。增加允许导入的正向用例，原有两种禁止能力继续检查具体编译错误。临时目录由 `t.Cleanup` 删除。只读挂载下三组子测试均 PASS。 |
| P1-1 | `key_registry.go` 文件末尾有多余空行。 | 删除该空行，`gofmt -l` 不再列出此文件。 |
| P1-2 | 单批模式固定指向已失效的版本目录；链式模式先找旧版本，递归查到多个版本时又会报“无法唯一确定”。 | 两种模式共用 `Get-CodexExecutable`：枚举安装根下一层目录中的 `codex.exe`，按可执行文件的 `LastWriteTimeUtc` 降序、完整路径升序确定性选择；无候选时报包含搜索根路径的错误。保留链式模式原有自测覆盖路径限制。实际安装解析、无候选和双候选选择点测均通过。 |

## 验收记录

以下 `go ...` 命令均在上述离线、源码只读容器内实际执行。单项命令使用同一挂载和环境设置；完整验收顺序执行，前一步失败即停止。时间戳为命令开始前打印的实际容器时间。

| 时间（UTC） | 实测命令与条件 | 结果 |
| --- | --- | --- |
| 2026-10-11 15:16:25 | `test -d dist`；`go test -short ./internal/authorizedexecute -run TestBusinessDriverImportsStayInsideCapabilityDomain -count=1 -v` | `dist=present`；目标测试 PASS；容器退出 0。 |
| 2026-10-11 15:23:47 | `go test -short ./internal/authorizedexecute -run TestCompilerRejectsRawBusinessDatabaseCapabilities -count=1 -v` | 允许导入、内部包拒绝、已删 API 拒绝三项均 PASS；容器退出 0。 |
| 2026-10-11 15:26:14 | `test -d dist`；`go test -short ./internal/authorizedexecute -run TestSQLSinkCallsitesMatchManifest -count=1 -v` | `DIST_PRESENT`；目标测试 PASS。 |
| 2026-10-11 15:27:02 | `go build ./...` | `GO_BUILD_PASS`，退出 0。 |
| 2026-10-11 15:28:50 | `go build -tags=yashan ./...` | `GO_BUILD_YASHAN_PASS`，退出 0。 |
| 2026-10-11 15:30:39 | `go test -short ./internal/authorizedexecute ./internal/b5wal -count=1` | 两包均 `ok`；`GO_TEST_PASS`，退出 0。 |
| 2026-10-11 15:33:01 | `go vet ./internal/authorizedexecute ./internal/b5wal` | 无告警；`GO_VET_PASS`，退出 0。 |
| 2026-10-11 15:33:56 | `gofmt -l internal/authorizedexecute/capability_test.go internal/b5wal/key_registry.go` | 输出为空；容器退出 0。 |
| 2026-10-11 23:13:08 +08:00 | PowerShell 解析 runner AST 并单独调用 `Get-CodexExecutable`；再传入不存在的安装根调用 | 脚本解析错误 0；解析到 `C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\2e5e00daee91c61d\codex.exe`；无候选错误包含已搜索的根路径。 |
| 2026-10-11 23:13:57 +08:00 | 在系统临时目录建立两个模拟版本，赋予不同 `LastWriteTimeUtc` 后调用 `Get-CodexExecutable -Bin <临时根>` | 选中时间较新的 `b\codex.exe`；临时目录已清理。 |
| 2026-10-11 23:35:07 +08:00 | `git diff --check`；核对改动文件名及报告尾随空白 | `git diff --check` 输出为空、退出 0；改动范围为下列四个文件；报告无尾随空白。 |

编译器测试首次迁至临时模块时，在 `--network none` 下曾因新模块查询 `sum.golang.org` 失败；添加仅限该测试子进程的 `GOSUMDB=off` 后，按上表重新运行并通过。仓库 `go.mod` 与 `go.sum` 未修改。

## 文件范围与遗留

- `internal/authorizedexecute/capability_test.go`：两个遍历跳过 `dist`；编译测试迁至系统临时目录并覆盖允许和禁止导入。
- `internal/b5wal/key_registry.go`：仅删除文件末尾多余空行。
- `scripts/codex-batch-runner.ps1`：统一两种模式的 Codex 可执行文件解析。
- `docs/postrelease-hardening-post2-report.md`：本报告。

没有新增仓库夹具。运行时的临时 `go.mod` 和测试源文件位于系统临时目录，由 `t.Cleanup` 清理。`dist/` 保留。遗留问题：无本批范围内未通过项。
