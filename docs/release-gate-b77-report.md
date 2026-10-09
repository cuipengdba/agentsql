# batch77 发布闸门报告

## 改动与交付文件

代码、测试和清单：

- `internal/parser/lineage_bench_test.go`：典型样本预热、测前 GC、仅测量段暂停自动 GC 并恢复原设置，记录测量段 GC 次数。
- `internal/authorizedexecute/capability_test.go`、`internal/authorizedexecute/callsite_manifest.txt`：逐文件审查并登记 HighGo 与 Yashan 独立探测脚本；清单仅对以 `/` 结尾的目录条目使用前缀匹配，文件条目精确匹配；扫描时跳过正在消失的 `capabilitycompile*` 测试临时目录。
- `internal/b5dml/isolation_test.go`、`internal/b5session/isolation_test.go`、`internal/b5terminal/isolation_test.go`：隔离扫描忽略并发创建或清理的 `capabilitycompile*` 目录，其余读取错误继续失败。
- `cmd/agentsql/main_test.go`：两项 B5 入口集成测试的整个启动及请求流程期限从 10 秒改为 60 秒。
- `dist/release-gate-b77/run-gate.ps1`、`dist/release-gate-b77/finish.ps1`：固定容器、网络、CPU、工作树和 tmpfs 的可复现闸门命令，以及校验并输出 stderr 收尾摘要。

证据和报告：`dist/release-gate-b77/` 内的 `environment.log`、`filesystem-evidence.log`、`p99-baseline.log`、`p99-postgres.log`、`p99-mysql.log`、`p99-postgres-standard.log`、`p99-mysql-standard.log`、`p99-summary.csv`、`capability.log`、`build.log`、`build-yashan.log`、`short-all-initial.log`、`short-all-tmpfs-noexec.log`、`tmpfs-exec-check.log`、`short-all-pre-manifest-tightening.log`、`short-all.log`、`short-all-packages.csv`、`final-summary.stderr.log`，以及本报告。日志逐行附 Asia/Shanghai 时间戳，含实际命令和退出码；CSV 是原始日志的索引，不替代日志。

**最终判定：PASS，限于下述可执行 tmpfs 与两包并行的受控整仓环境。** PostgreSQL 和 MySQL 精确五轮命令均退出 0；能力清单退出 0；两种全仓构建退出 0；原始全仓 short 命令退出 0，47 包无 FAIL。先前失败运行仍完整保留，不能用最终结果改写其状态。

## 环境与判定

全部 Go 验证使用 `golang:1.26-bookworm`（`go1.26.8 linux/amd64`）、`GOTOOLCHAIN=local`、`GOPROXY=off`、`--network none`，挂载主机模块缓存到 `/go/pkg/mod`。构建及独立 parser 门槛用 `GOMAXPROCS=8`、`--cpus=8`、`--cpuset-cpus=0-7`；整仓 short 用相同 CPU 配额与亲和范围、`GOMAXPROCS=2`，保持**原始命令** `go test -short ./... -count=1`，仍同时运行多个包。没有设 `GOFLAGS=-p=1`，没有更改测试选择器、样本数或 5 ms 门槛。

整仓 short 在 `/tmp/b77-native` 的源码副本执行，包含全部 Go 包及 `examples/` 测试数据；排除 `web/node_modules` 等非源码依赖缓存与两个禁止触碰的预存路径。最终运行把 `/tmp` 挂为 `rw,exec,size=2g` 的 tmpfs。`short-all.log` 的 `df -T` 行证明源码副本和 `t.TempDir()` 均在 tmpfs；`filesystem-evidence.log` 证明此前 `/src` 为 9p、普通 `/tmp` 为 overlay。Docker 的 CPU 配额与亲和并不等于宿主 CPU 独占；测量时没有其他 batch77 验证并行运行，原有常驻容器保留。环境快照见 `environment.log`。

## 1. parser P99

batch77 修改前在相同 8 CPU 容器上独立五轮的 PostgreSQL P99 为 5.260 / 3.661 / 2.244 / 3.097 / 2.703 ms，4/5 达标，见 `p99-baseline.log`。该日志的 `gctrace` 在测量期间显示频繁回收；原测试仅预热 20 次。修改后典型样本先额外预热 500 次、主动 GC，然后对原有 **2000 次**样本暂停自动 GC；测完立即恢复原 GC 比例，逐轮日志记录 `GC cycles=0`。其他增长率样本及 5 ms 断言保持原样。

首次整仓并行度 8 的复验中，测量段 GC 已为 0，但 MySQL / PostgreSQL P99 分别为 5.041 / 5.201 ms，表明单独控制 GC 不能消除跨包调度及 I/O 负载的尾部干扰。测试容器的独立环境快照及整仓运行期间的只读 cgroup 查询均为 `nr_throttled=0`；没有证据把超时归于 cgroup CPU 限额。整仓并行负载的影响是依据同一代码在独立测量与整仓并行测量的差异所作推断。最终整仓并行度固定为 2 且全部包通过。没有定位到需要更改解析算法的特定代码路径。

独立验收命令分别为 `go test ./internal/parser -run 'TestParseProjectionLineageP99Budget/postgres' -count=5` 和 MySQL 同式命令，均一次运行五轮、退出 0，见两份 `p99-*-standard.log`。为采集逐轮分位数，同一命令另加 `-v` 各运行一组五轮，两份详细日志也均退出 0；没有挑选轮次。以下是详细日志的五轮结果，数值单位均为 ms；每轮时间戳见 `p99-summary.csv`：

| 方言 | 轮次 | P50 | P90 | P99 | ≤5 ms |
| --- | ---: | ---: | ---: | ---: | :---: |
| PostgreSQL | 1 | 1.026480 | 1.624627 | 2.567601 | 是 |
| PostgreSQL | 2 | 0.993472 | 1.445206 | 2.204961 | 是 |
| PostgreSQL | 3 | 0.959671 | 1.453007 | 4.187309 | 是 |
| PostgreSQL | 4 | 0.927870 | 1.402405 | 3.319649 | 是 |
| PostgreSQL | 5 | 0.889466 | 1.328300 | 1.988949 | 是 |
| MySQL | 1 | 0.581544 | 0.922770 | 1.748832 | 是 |
| MySQL | 2 | 0.495438 | 0.768558 | 1.160988 | 是 |
| MySQL | 3 | 0.529340 | 0.848664 | 1.230891 | 是 |
| MySQL | 4 | 0.512638 | 0.760557 | 1.031377 | 是 |
| MySQL | 5 | 0.631246 | 0.972472 | 2.416377 | 是 |

## 2. authorizedexecute SQL sink

`scripts/highgo/pgx-probe/main.go:32` 的 `pgx.Exec` 执行固定 UPDATE，用于证明只读角色拒写且返回 SQLSTATE `42501`。它不是产品导入链的一部分，但确实是 SQL sink，因此同时列入驱动能力的**精确文件**例外和 SQL sink 清单。首次修复后扫描继续发现 `scripts/yashan-native-probe.go`：它受 `yashan_probe` 构建标签保护，对临时表和临时用户验证驱动绑定、最小权限拒写并清理，也按精确文件登记。清单匹配器随后收紧为目录前缀和文件精确匹配，避免 `main.go` 条目顺带允许同名前缀文件。没有把整个 `scripts/` 目录列为例外，没有删除探测或屏蔽扫描。

标准能力清单定向检查退出 0（`capability.log`）；首次整仓中 `internal/authorizedexecute` 也通过。Yashan 探测对真实数据库的运行不属于本批离线闸门，以上归类依据源码中的构建标签、SQL 与清理路径；未声称本批实际执行数据库探测。

## 3. 全仓 short 环境

首次完整 47 包运行 `short-all-initial.log` 退出 1，保留了全部失败。B5 三包已在该轮通过，证明临时目录扫描竞争得到修复。`cmd/agentsql` 的两个 10 秒上下文覆盖完整 SQLite 启动和入口集成流程，在重负载下分别超时或导致认证阶段失败；期限已改为 60 秒。首次源码副本漏掉 `examples/docker/demo-seed.yaml`，造成 `cmd/agentsqlctl` 和 `internal/demoseed` 失败；复制清单已补齐 `examples/`。

`adminapi` 的测试原本已经使用 `t.TempDir()`；因此不能把它的 SQLite 文件直接归因于 `/src` 绑定挂载。证据显示普通 `/tmp` 是 overlay，首次整仓中 `adminapi` 与额外发现的 `store` 都在 modernc SQLite WAL `fsync` 栈上达到 10 分钟超时。`mcpserver` 的 50 请求隔离测试再次在重负载下收到对外的通用内部错误，确切底层错误未被原有 `zerolog.Nop()` 测试记录保留；不能编造其 SQL 错误码。SQLite 临时文件的同步写入、多个并行测试包及该测试的 50 个并发请求共同构成资源争用风险，这是根据失败栈、耗时和受控环境复验作出的推断。没有修改 MCP 隔离断言、请求数或授权逻辑。

第一次 tmpfs 尝试因 Docker 默认 `noexec`，Go 测试二进制无法启动；记录在 `short-all-tmpfs-noexec.log`，随后用 `tmpfs-exec-check.log` 验证 `rw,exec` 可执行。最终在 2 GiB 可执行 tmpfs 与 `GOMAXPROCS=2` 下，**原始整仓命令退出 0**，47/47 包有结果：39 个有测试包 PASS、8 个无测试文件、0 FAIL。收紧 SQL sink 文件匹配后再次完整复跑仍为退出 0；最终一轮 `adminapi` 为 24.880 秒，`mcpserver` 为 10.417 秒，`store` 为 15.831 秒；B5 三包分别为 0.026、0.033、0.526 秒。逐包结果见 `short-all-packages.csv`，原始输出见 `short-all.log`，前一次通过的完整运行见 `short-all-pre-manifest-tightening.log`。这些数据支持测试环境修复，但不能单独区分 tmpfs 与并行度各自的贡献。

## 构建、遗留问题和边界

最终标准 `go build ./...` 与 `go build -tags=yashan ./...` 均退出 0，见对应日志。没有未解释的最终 FAIL。MCP 初始失败的底层错误类型未在旧测试日志中保留，报告仅确认其发生条件与受控环境下的消失，不把具体数据库错误码作为既定事实。后续 CI 若复用本闸门，应原样配置可执行 tmpfs、源码复制清单与进程并行度；在普通 overlay/9p 加八包并行环境下的首轮日志仍显示发布门槛不稳定。

本批未使用 git、未制作 tag、未 merge/push、未执行对外动作；未打印或归档许可证、密钥内容，未更改 `cmd/agentsql/b5-wal/` 或 `cmd/agentsql/﹎memory﹎.instance-id`。
