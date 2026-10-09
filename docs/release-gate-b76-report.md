# v0.5.0 发布闸门：batch76 联网补依赖与全量验证

验证日期：2026-10-09（Asia/Shanghai）；基线提交：`81c40aa`。**总判定：FAIL，不满足本批发布 B 闸门。** 两种全仓构建均通过，依赖与许可证核查通过；parser P99 五轮中三轮超过 5 ms，全仓 short tests 有 7 个失败包。本批没有 tag、merge、push、Release、镜像推送、官网部署或 PVR 操作。

## 环境与证据

所有 Go 容器均使用 `golang:1.26-bookworm`（实际 `go1.26.8 linux/amd64`）、`GOTOOLCHAIN=local`、`GOMAXPROCS=8`，挂载可写主机缓存 `C:\Users\Administrator\go\pkg\mod:/go/pkg/mod` 及可写源码 `${PWD}:/src`。依赖下载使用 `GOPROXY=https://goproxy.cn,direct` 和 bridge 网络；构建与测试使用 `--network none`。测试容器另设 `GOCACHE=/src/.gocache` 复用编译缓存，不改变测试命令。挂载实测见 [mount-check.log](../dist/release-gate-b76/mount-check.log)，所有日志行包含本地时间戳、命令及退出码。

环境复验把 `git archive HEAD` 的已跟踪文件解压到容器原生可写目录 `/tmp/b76-native`，覆盖当时下载阶段的 `go.sum` 后运行单包测试。所需主机缓存和源码挂载仍然保留；源码快照的创建和清理记录见 [native-workspace.log](../dist/release-gate-b76/native-workspace.log)。这也避开了用户要求不得触碰的两个未跟踪路径。

| 闸门 | 结论 | 主要证据 |
| --- | --- | --- |
| `go mod download -json all` | **PASS** | [download.log](../dist/release-gate-b76/download.log) 退出 0；[dependency-summary.log](../dist/release-gate-b76/dependency-summary.log) 与 [新增模块版本清单](../dist/release-gate-b76/modules-added.txt) |
| 许可证一致性 | **PASS** | [license-audit.log](../dist/release-gate-b76/license-audit.log)、[源码文件头扫描](../dist/release-gate-b76/source-license-headers.log) |
| `go build ./...` | **PASS** | [首次 build.log](../dist/release-gate-b76/build.log) 与 [最终 build-final.log](../dist/release-gate-b76/build-final.log) 均退出 0 |
| `go build -tags=yashan ./...` | **PASS** | [首次 build-yashan.log](../dist/release-gate-b76/build-yashan.log) 与 [最终 build-yashan-final.log](../dist/release-gate-b76/build-yashan-final.log) 均退出 0 |
| postgres parser P99 连续五轮 | **FAIL** | [p99.log](../dist/release-gate-b76/p99.log) 退出 1；[五轮数据 CSV](../dist/release-gate-b76/p99-summary.csv) |
| `go test -short ./... -count=1` | **FAIL** | [short-all.log](../dist/release-gate-b76/short-all.log) 退出 1；[逐包 CSV](../dist/release-gate-b76/short-all-packages.csv) |
| rules/pipeline 定向 short tests | **PASS** | [short-targeted.log](../dist/release-gate-b76/short-targeted.log) 两包 `ok`，退出 0 |
| 最终 `go.sum` 全仓测试编译检查 | **PASS（仅编译）** | [test-compile-final.log](../dist/release-gate-b76/test-compile-final.log) 47/47 包有结果，退出 0；不作为 short tests PASS |

## 依赖变更

完整模块图下载前后，主机缓存中真正的 `.zip` 文件从 306 个增至 734 个，新增 **428 个模块版本**；其中有 `github.com/moby/sys/sequential v0.7.0`。[modules-added.txt](../dist/release-gate-b76/modules-added.txt) 逐项列出路径与版本，[下载前](../dist/release-gate-b76/module-zips-before.txt)和[下载后](../dist/release-gate-b76/module-zips-after.txt)保留原始缓存快照。这里的 428 是执行 `go mod download -json all` 后新增的整个模块图缓存版本数，不等同于构建单独缺失 428 项。

`go.mod` 哈希未变。全模块图下载曾使 `go.sum` 临时新增 878 行、删除 125 行；完整临时差异见 [go-mod-sum.diff](../dist/release-gate-b76/go-mod-sum.diff)，[下载后哈希](../dist/release-gate-b76/go-hashes-after-download.txt)已留档。原 `go.sum` 已含 `sequential v0.7.0` 的校验和，故将临时重排恢复；[最终哈希](../dist/release-gate-b76/go-hashes-final.txt)与批次开始时完全相同，`go.mod`/`go.sum` 最终均无 Git 差异。恢复后的两种断网全仓构建及 47 包测试编译检查全部退出 0，说明缓存补全不要求持久修改模块清单或校验和。没有修改业务逻辑代码。

## 许可证核查

根目录 `LICENSE` 包含 Apache License 2.0 第 1–9 节、条款结束段和附录。与 [Apache 官方文本](https://www.apache.org/licenses/LICENSE-2.0.txt) 比较，原始文件因换行排版不同而字节不等；归一化空白后 **10,221 字节完全一致**，可确认全文内容完整。[核查日志](../dist/release-gate-b76/license-audit.log)只记录哈希、结构和比较结论，不输出许可证内容。

`NOTICE` 与 `COMMERCIAL-LICENSE.md` 都明确 Apache-2.0、独立商业授权及 2026 年崔鹏版权，商标保留口径一致。对已跟踪 `.md`、`.go` 文件的 AGPL 搜索仅命中 `CHANGELOG.md` 的许可证迁移记录、v0.4/v0.2 历史资料，以及带日期的 batch75 旧报告；没有把 AGPLv3 描述为 v0.5.0 当前许可证的命中。`docs/release-notes-v0.2.0.md` 的旧许可段落已有后续版本切换说明；batch75 报告的“待决定”状态是历史快照，不可复用为当前发布文案。扫描 638 个已跟踪 Go 文件的前 15 行，未见需要本批统一的旧许可证文件头横幅。

## parser P99 五轮

标准命令：`go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v`。此命令独占本批验证负载；宿主机原有服务容器未停机，因此不能宣称全主机零负载。五轮来自一次真实运行，未为取得通过结果追加重跑。

| 轮次 | P50 | P90 | P99 | ≤ 5 ms |
| ---: | ---: | ---: | ---: | :---: |
| 1 | 1.033803 ms | 2.061843 ms | **6.218622 ms** | 否 |
| 2 | 853.486 µs | 1.730673 ms | 2.493049 ms | 是 |
| 3 | 1.037639 ms | 2.107479 ms | **5.367101 ms** | 否 |
| 4 | 918.835 µs | 1.781067 ms | 2.879308 ms | 是 |
| 5 | 1.070586 ms | 2.228379 ms | **7.893833 ms** | 否 |

结论：**2/5 轮过线，连续五轮门槛 FAIL**。这些数据表明尾延迟抖动；原因尚未证实，不能把超限归因于血缘语义或任意抹掉失败轮次。全仓 short test 还在并行负载下测得 mysql P99 6.65963 ms、postgres P99 12.825661 ms，见其原始日志；这不替代上述独占五轮结论。

## 全仓 short tests：47 包真实结果

命令退出 1。47/47 包均有 Go 原始结果：**32 个测试包 PASS，7 个测试包 FAIL，8 个包无测试文件**；测试包通过率 **32/39 = 82.05%**，若把无测试文件包计入命令成功状态，则为 **40/47 = 85.11%**。无测试文件不称作测试 PASS。以下是原命令逐包状态，单包复验不会改写这些结果。

| 包 | 原命令状态 | 时长/说明 |
| --- | --- | --- |
| `cmd/agentsql` | PASS | 101.265s |
| `cmd/agentsql-b2-fallback` | 无测试 | no test files |
| `cmd/agentsql-b2-gate` | 无测试 | no test files |
| `cmd/agentsqlctl` | PASS | 214.241s |
| `internal/adminapi` | **FAIL** | 600.156s |
| `internal/audit` | PASS | 0.424s |
| `internal/auditchain` | PASS | 0.015s |
| `internal/auditrelay` | PASS | 0.034s |
| `internal/auth` | PASS | 3.746s |
| `internal/authorizedexecute` | **FAIL** | 127.690s |
| `internal/authorizedexecute/internal/businessdb` | PASS | 1.816s |
| `internal/b2release` | PASS | 0.029s |
| `internal/b5` | PASS | 0.016s |
| `internal/b5coordinator` | PASS | 0.942s |
| `internal/b5dml` | **FAIL** | 170.036s |
| `internal/b5session` | **FAIL** | 168.487s |
| `internal/b5terminal` | **FAIL** | 263.589s |
| `internal/b5wal` | PASS | 10.354s |
| `internal/bootstrap` | PASS | 166.588s |
| `internal/columnauth` | PASS | 0.016s |
| `internal/compliance` | PASS | 0.023s |
| `internal/config` | PASS | 1.330s |
| `internal/controlledread` | PASS | 0.145s |
| `internal/demoseed` | PASS | 23.714s |
| `internal/discovery` | PASS | 0.018s |
| `internal/engine` | PASS | 0.012s |
| `internal/eventbus` | PASS | 0.016s |
| `internal/lockrank` | PASS | 0.012s |
| `internal/mask` | PASS | 183.201s |
| `internal/mcpserver` | **FAIL** | 323.692s |
| `internal/metrics` | PASS | 0.031s |
| `internal/model` | PASS | 0.012s |
| `internal/notify` | PASS | 0.221s |
| `internal/parser` | **FAIL** | 9.159s |
| `internal/pipeline` | PASS | 0.669s |
| `internal/policy` | PASS | 0.023s |
| `internal/rbac` | PASS | 10.470s |
| `internal/redaction` | PASS | 0.009s |
| `internal/rules` | PASS | 0.063s |
| `internal/server` | 无测试 | no test files |
| `internal/store` | PASS | 556.314s |
| `internal/version` | 无测试 | no test files |
| `internal/webui` | PASS | 0.014s |
| `scripts` | 无测试 | no test files |
| `scripts/highgo` | 无测试 | no test files |
| `scripts/highgo/pgx-probe` | 无测试 | no test files |
| `scripts/releasesign` | 无测试 | no test files |

### 失败包定性与正常磁盘复验

| 原全仓失败包 | 原始失败 | 单包/串行复验 | 定性 |
| --- | --- | --- | --- |
| `internal/adminapi` | Go 默认 10 分钟超时；栈停在 modernc SQLite WAL `fsync` | [原生磁盘单包](../dist/release-gate-b76/native-adminapi.log) **PASS**，318.568s | 绑定挂载/全仓并发下的磁盘环境敏感；原全仓仍 FAIL |
| `internal/authorizedexecute` | 两项能力清单检查失败 | [原生磁盘单包](../dist/release-gate-b76/native-authorizedexecute.log) **FAIL**，同两项 | 已跟踪 `scripts/highgo/pgx-probe/main.go` 导入 `pgx/v5`，第 32 行 `Exec` 未列入受审 SQL sink 清单；需后续批次处理 |
| `internal/b5dml`、`internal/b5session`、`internal/b5terminal` | 三项隔离检查均报 `/src/capabilitycompile...` 临时目录不存在 | [原生磁盘 `-p 1` 串行复验](../dist/release-gate-b76/native-b5-serial.log) **三包 PASS** | 全仓并行时共享临时目录发生竞争；原全仓仍 FAIL |
| `internal/mcpserver` | 50 个同步请求隔离断言出现 `error`，期望 `allow` | [原生磁盘单包](../dist/release-gate-b76/native-mcpserver.log) **PASS**，153.062s | 全仓并行下未复现的环境/时序敏感失败；根因未证实，原全仓仍 FAIL |
| `internal/parser` | mysql、postgres 的 P99 分别为 6.65963、12.825661 ms | 独占 postgres 五轮见上表，仍 **FAIL** | 性能门槛未达成 |

## 遗留与边界

1. **发布阻塞**：独占 parser P99 未达到连续五轮 ≤5 ms；标准全仓 short tests 仍有 7 个原始 FAIL 包。`authorizedexecute` 的能力/SQL sink 清单失败在正常磁盘上稳定复现。
2. **环境/测试并发问题**：`adminapi`、三个 b5 包和 `mcpserver` 的单包或串行正常磁盘复验通过；标准全仓命令仍会受挂载盘 `fsync`、共享临时目录及并发时序影响。后续须修复或调整这些测试环境后重新跑原标准命令，不能用单包 PASS 代替全仓 PASS。
3. 本批仅新增报告并生成忽略目录下的日志/缓存；`go.mod`、`go.sum`、业务逻辑和许可证文件最终均未修改。两个预存的未跟踪路径 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 未触碰。
