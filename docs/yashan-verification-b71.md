# 崖山 YashanDB 批七十一接入验证

验证日期：2026-10-08（Asia/Shanghai）。本报告只针对本机
`yashandb:yashandb-image-23.4.1.109-linux-x86_64`、容器 `yashan-v06`、
崖山模式及列出的合成 SQL。**已获厂家口头授权（C 客户端再分发）**；仓库
没有书面授权文件或授权编号，本报告不虚构这些文件。

## 镜像、客户端与连接

| 检查项 | 批七十一结果 |
| --- | --- |
| 输入镜像 | 本地 `D:\ruanjiansheji\db-images\yashandb-image-23.4.1.109-linux-x86_64.tar.gz` 存在（500650748 字节），对应镜像已在本地 Docker 加载；未联网拉取 |
| 实例 | `yashan-v06` 正在运行，主机端口 1688；历史服务端记录为 Enterprise Edition Release 23.4.1.109、`COMPAT_VECTOR=yashan`、当前用户 `SYS` |
| 服务端工具 | 镜像内 `yasql`、`include/yacli.h`、`lib/libyascli.so` 均存在；探查仅检查文件存在，不读取 license 或凭据 |
| 服务端 C 库 | 历史真库探针使用服务端库与 yashandb-go v1.4.4 时返回 `YAS-02143`；不能可靠作为 Go 驱动运行库，本轮没有从镜像复制或打包它 |
| 独立客户端 | 本机旧的、未发布 dry-run tarball 缓存独立客户端 23.4.7.100；仅将其中 `lib/yashandb` 的 24 个库文件提取至系统临时目录用于本轮测试，没有复制 license。此旧产物不能充当当前发行验收 |

默认连接探查采用容器已有 `SYS_PASSWD` 环境变量的值，仅在进程内传给
测试；终端、transcript、仓库均不记录该值。`SYS` 只用于创建和清理
本轮临时用户/表；业务查询探针使用新建的最小权限只读用户。账号/
当前模式可用 `SELECT USER FROM DUAL` 探查；这里的 `SYS` 是当前模式，
不可按 PostgreSQL 的 database/schema 含义套用。兼容模式以历史实例
参数 `COMPAT_VECTOR=yashan` 为准，本轮未切换兼容模式。

## 接线与真库证据

`internal/parser/parser.go` 的标准 `NewParser(model.DialectYashan)`
已接通 batch66 的崖山 parser。流水线、规则、引擎的方言门已最小接线。
`YashanExecutor` 与物理会话在提交 SQL 前再次运行窄 SELECT parser 和
执行器的受控 SELECT 校验；不支持的 SQL 在驱动前失败关闭。写入、事务
与 EXPLAIN 保持关闭。缺 Go 驱动时给出 `CGO_ENABLED=1`、`-tags yashan`
提示；缺独立 C 客户端时提示设置 `LD_LIBRARY_PATH`。

本轮 `scripts/yashan-verify.ps1 -RequireLive` 的原生驱动探针结果：

| 探针 | 结果 |
| --- | --- |
| Go 原生驱动 Ping | PASS |
| `SELECT ? FROM DUAL` 参数绑定 | PASS |
| 临时只读账号通过 `?` 读取合成表 | PASS |
| 同账号对该表 INSERT | 被服务器拒绝，错误码 `YAS-02213`；仅记录错误码，不记录原始诊断 |
| 清理 | 临时只读用户与合成表删除 PASS |

这证明指定独立 C 客户端与 Go 驱动能连接本机真库，且本轮最小权限
账号写入被数据库拒绝。随后带 `yashan` 标签的
`TestYashanDiscoveryE2E` 通过真实 `YashanExecutor` 的连接/Ping、
物理会话 SELECT、连接池 SELECT、绑定参数的 `ALL_TAB_COLUMNS` 列发现，
并验证执行器写入与 EXPLAIN 继续拒绝。随后
`TestYashanPipelineRealE2E` 用真实崖山连接和两张合成表，验证 SELECT
allow、`SYS.表.PHONE` 投影血缘、表列定向手机号脱敏、另一张可由数据库
账号读取的表被 R010 拒绝，以及 allow/deny/注释语法错误三条审计记录。
注释在 parser 阶段被拒绝，**不算 R006 命中**。该流水线测试使用真实
数据库执行器、真实脱敏器及测试夹具的认证/策略读取/审计记录端口；
业务库账号是 `SYS`，最小权限账号另由上述原生探针验证。因此它证明
流水线端口闭环，尚不证明 HTTP/MCP 接口、持久化审计库或生产账号
配置。历史连接/元数据发现证据见
[方言边界记录](yashan-dialect.md)，不能用历史普通 Query 拒绝测试
代替新路径的通过证据。

## 构建、测试与 transcript

脚本使用本地 `golang:1.26-bookworm`、只读挂载的本机 Go module cache、
`--network none` 和 `GOPROXY=off` 执行构建及可运行的短测试。本轮结果：

| 命令或检查 | 结果 |
| --- | --- |
| 离线 `go build ./...` | PASS，退出 0 |
| 离线 `CGO_ENABLED=1 go build -tags yashan ./...` | PASS，退出 0 |
| 离线 `go test -short ./internal/parser ./internal/engine ./internal/rules` | PASS，退出 0 |
| 离线 `go test -short` 加入 `pipeline` 与 `businessdb` | **未通过**：本机缓存缺 `github.com/moby/sys/sequential v0.7.0`；按禁网约束未下载 |
| 临时 Go overlay 定向 `TestYashan*` | `pipeline` 与 `businessdb` PASS；仅排除无关 Testcontainers 测试文件，不能替代完整包短测 |
| 崖山 parser 标准入口 | `yashan_parser_test.go` 定向断言在上述 parser 包测试中 PASS |
| 真库原生驱动探针 | PASS，脚本退出 0；该探针不依赖缺失的 Testcontainers 测试依赖 |
| 带 `yashan` 标签的真库 `TestYashanDiscoveryE2E` | PASS；使用临时 overlay 排除无关 Testcontainers 测试文件，验证真实执行器的连接/会话 SELECT/元数据/写入与 EXPLAIN 边界 |
| 带 `yashan` 标签的真库 `TestYashanPipelineRealE2E` | PASS；真实崖山表/执行器、真实解析/规则/脱敏和测试夹具审计端口，覆盖 SELECT、血缘、R010、脱敏、三条审计 |

验证脚本非零退出代表执行的检查失败；缺完整包测试依赖被明确记录
为 NOT RUN，不会伪装成通过。定向测试由脚本临时生成 overlay，在不
改变仓库测试文件的前提下运行本轮崖山测试。脚本固定覆盖写入
`C:\Users\Administrator\AppData\Local\Temp\agentsql-yashan-b71-verify.log`，
成功时只含固定 PASS/NOT RUN 文本，失败时另记固定 FAIL 阶段信息；不含密码或 license 内容。真库探针的
`YASHAN_PASSWORD` 由调用进程内存传入，脚本和日志均不回显。

## 运行路径与边界

本轮没有从服务端镜像打包驱动或客户端，也没有重建当前 Linux/GHCR
发行物。Go 驱动需在构建时通过 `CGO_ENABLED=1 -tags yashan` 静态注册；
它不能在没有该构建标签的现成二进制中事后动态注册。C 客户端是
运行时动态加载：用户自行取得独立 23.4.7.100 客户端，将其 `lib`
目录放入进程启动前的 `LD_LIBRARY_PATH`。缺组件时失败关闭，不回退到
Oracle、PostgreSQL 或服务器镜像的旧库。现有打包脚本中的客户端捆绑
方案仍需单独的发行构建、架构与依赖验收。

| 未测或保持关闭的项目 | 当前边界 |
| --- | --- |
| HTTP/MCP 接口与持久化审计库 | 真库流水线测试采用测试夹具的认证、策略和审计端口；接口调用和持久化审计待单独 E2E |
| 生产最小权限账号的流水线组合 | 原生驱动只读账号探针与流水线真库测试分别通过；后者使用 `SYS`，组合尚未验证 |
| R006 注释探针 | parser 在规则前拒绝注释；不算 R006 真库命中 |
| 序列伪列 `CURRVAL` | 崖山执行器的只读 SQL 校验明确拒绝，定向离线回归覆盖 |
| R004 与 R005 计划估算 | 无已验证的崖山 EXPLAIN 解码器，跳过计划探针；执行行数上限和截断后的 R005 告警仍保留 |
| 多行分页、复杂 JOIN、函数、系统目录、TLS、取消、连接复用与更多错误码 | 未做本轮目标真库验证；已知 `YAS-02213` 只证明本次写入拒绝 |
| INSERT/UPDATE/DELETE/DDL、事务、EXPLAIN | AgentSQL 路径仍失败关闭 |

本轮没有启动 AgentSQL 容器；因此没有本轮 AgentSQL 容器待停。
原生探针创建的临时用户/表及流水线测试创建的两张表均已删除。
`yashan-v06` 保留运行供后续
真库流水线验证；临时客户端库目录仅作本机测试材料，不属于仓库或
发行物。

## 批七十一改动文件

- 矩阵与首页：`README.md`、`README.en.md`、`SECURITY.md`、
  `CHANGELOG.md`、`website/public/index.html`。
- 文档：`docs/dm8-verification.md`、`docs/dm-oracle-dialect.md`、
  `docs/SPEC.md`、`docs/release-notes-v0.5.md`、`docs/DEPLOY.md`、
  `docs/USER_GUIDE.md`、`docs/RELEASE_ASSETS.md`、`docs/yashan-dialect.md`、
  `docs/yashan-verification-b71.md`。
- 探查与验证脚本：`scripts/yashan-verify.ps1`、
  `scripts/yashan-native-probe.go`。
- 最小接线与测试：`internal/parser/parser.go`、
  `internal/parser/yashan_parser_test.go`、`internal/engine/engine.go`、
  `internal/rules/generic.go`、`internal/pipeline/pipeline.go`、
  `internal/pipeline/rules.go`、`internal/pipeline/yashan_test.go`、
  `internal/pipeline/yashan_live_test.go`、
  `internal/authorizedexecute/internal/businessdb/yashan.go`、
  `internal/authorizedexecute/internal/businessdb/yashan_test.go`、
  `internal/authorizedexecute/internal/businessdb/sql_dialect.go`。
