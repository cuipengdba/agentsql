# AgentSQL

**语言 / Language:** [简体中文](README.md) | [English](README.en.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.5.0](https://img.shields.io/badge/Version-v4.5.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

生态收录：[Glama](https://glama.ai/mcp/servers?query=AgentSQL)

已收录于 MCP 官方 Registry，可直接在 MCP Registry 搜索 AgentSQL 接入。

AgentSQL（智盾）是**独立 AI 问数 Agent + MCP Server 数据库安全网关**二合一产品。自有 Go 问数服务和 Web 页面不依赖 SQLBot 运行；MCP Server 也可独立为外部 AI Agent 提供受控数据库工具。两条入口共用身份、授权、SQL 检查、受控执行、结果脱敏和持久审计。

### 独立 AI 问数 Agent

用户在 AgentSQL 自有页面提问；服务按当前身份和数据源授权选取表列结构，由配置的模型理解问题、生成 SQL，再交给 AgentSQL 网关验证与执行。授权不符或依赖故障时拒绝交付数据；历史和导出继续核对当前权限。这是 AgentSQL 自己的问数链路，不需要运行 SQLBot。v4.5.0 已在限定的本地模型、登记的 PostgreSQL 网关别名和原版客户端指定路径上验收；模型生成的结论仍需人核对业务含义。

### 独立 MCP Server 安全网关

外部 MCP Client 可以直接连接 AgentSQL 的 stdio 或 HTTP MCP Server。已有 SQLBot 问数执行路径可**经固定版本适配器**把数据库调用交给 AgentSQL MCP 网关，保留其前端流程，同时由 AgentSQL 逐次执行身份映射、最小权限、脱敏与审计。SQLBot 是可选接入方，不是 AgentSQL 问数功能的运行依赖；接入范围与固定版本见 [SQLBot 固定适配指南](docs/SQLBOT.md)。

```text
AgentSQL 自有页面 / API → 自有 AI 问数 Agent ─┐
                                                ├→ AgentSQL 安全执行内核 → 已授权数据库
SQLBot（经适配器）/ MCP Client → AgentSQL MCP Server ┘
```

**当前范围：** [v4.5.0 正式发布说明](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0)列明已验的普通问数、原版 Python MCP SDK、基本助手和 Type 4 页面嵌入路径。高级助手、活动复杂权限目标及广覆盖社区版页面/API 分别规划到 v4.6.0、v4.7.0、v4.8.0；不能据此推定完整 SQLBot 兼容。尚未在用户真实业务系统验证，也不宣称零改动切换。[详细历史版本](https://github.com/cuipengdba/agentsql/releases)保留在 Release 与 [CHANGELOG](CHANGELOG.md)。

## 核心特性

- **自有问数：** 独立 Go 服务、Web 页面和模型编排；按当前身份检索受权结构、生成 SQL，并通过同一安全网关执行。当前授权历史、导出及嵌入边界见 [问数指南](docs/CHATBI.md)和正式发布说明。
- **MCP Server：** stdio、Streamable HTTP 和独立无状态 HTTP 入口；工具须绑定 Agent、人员或任务授权，不因接入 MCP 自动获得数据库权限。
- **默认拒绝的安全执行：** 表列权限、受限行规则、SQL 解析与风险规则、四态决策、只读边界、限时限量及必要时审批；无法证明安全的请求在执行前拒绝。
- **数据与审计：** 结果层按来源列脱敏，控制面与审计可使用 SQLite 或 PostgreSQL；管理审计、查询审计、健康探针及可选通知用于复核运行状态。
- **部署与兼容：** 双架构签名离线包和 GHCR 镜像；数据库能力按下方矩阵的指定版本、模式与测试证据分级，不由注册数量推断生产支持。

## 生态与兼容

数据库能力按下方[兼容矩阵](#数据库兼容矩阵)的指定版本、部署模式与验收证据分级。连接成功或进入注册表不等于能安全执行问数；国产数据库、NoSQL 和向量库的限定路径也不等于厂商认证或完整生产支持。需要目标环境验证可通过 [Discussions](https://github.com/cuipengdba/agentsql/discussions) 联系。

## 5 分钟快速开始

以下三条路径都默认只在宿主机回环地址监听。自动安装器生成的是持久使用的**管理员密码**，不是一次性口令；远程访问请使用 SSH 本地转发，例如 `ssh -L 7780:127.0.0.1:7780 user@server`，不要直接向公网暴露 7780。

### 1. Linux 裸机一行安装（推荐）

v0.4.0 起提供 `linux/amd64` 与 `linux/arm64` 两种原生 glibc 包：`uname -m` 为 `x86_64` / `amd64` 时使用 amd64 包，为 `aarch64` / `arm64` 时使用 arm64 包。两者均要求 glibc 2.28+ 且 systemd 为 PID 1；安装器会自动选择本机架构、校验发布包外层与包内哈希，并安装为 systemd 服务。musl / Alpine 与 CentOS 7 仍不支持：

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.5.0/install.sh | sudo sh -s -- install --version v4.5.0
```

管道和 CI 的非 TTY 输出不会显示自动生成的管理员密码；root 可在 `/etc/agentsql/agentsql.env` 查看。交互终端会在首次创建凭据时显示一次，也可显式加 `--show-password`。离线环境同时取得 tarball 与同名 `.sha256` 后，校验、解压并从包根安装：

以下是 v4.5.0 的 amd64 资产名示例；v0.4.0 起 arm64 主机使用同版本的 `-linux-arm64` 资产名。

```bash
sha256sum -c agentsql-v4.5.0-linux-amd64.tar.gz.sha256
tar -xzf agentsql-v4.5.0-linux-amd64.tar.gz
cd agentsql-v4.5.0-linux-amd64
sudo ./install.sh install
```

### 2. Docker 一行启动

脚本默认拉取 GitHub 最新稳定 Release 对应的精确 GHCR tag；该 tag 是 `linux/amd64` + `linux/arm64` 多架构镜像，Docker 会自动匹配运行架构。脚本生成权限为 `0600` 的 `.env`，使用 `agentsql-data` 命名卷，并固定绑定回环地址：

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.5.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.5.0
```

GHCR 包必须由发布者设为 public，以上命令才能在未登录环境匿名拉取。先核对 `v4.5.0` 精确 tag 的公开状态与双架构 manifest；Docker 会从 manifest list 自动选择 `linux/amd64` 或 `linux/arm64`：

```bash
docker pull ghcr.io/cuipengdba/agentsql:v4.5.0
```

等价的单条 `docker run` 如下；随机值只通过当前 shell 环境传入，不写进命令行参数：

```bash
export AGENTSQL_SECRET="$(openssl rand -base64 24)" AGENTSQL_ADMIN_USER=admin AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker run -d --name agentsql --restart unless-stopped --security-opt no-new-privileges:true -p 127.0.0.1:7780:7780 -e AGENTSQL_SECRET -e AGENTSQL_ADMIN_USER -e AGENTSQL_ADMIN_PASSWORD -v agentsql-data:/var/lib/agentsql ghcr.io/cuipengdba/agentsql:v4.5.0
```

### 3. 本地 Live Demo（一条命令，无需源码）

这套自包含环境只连接合成的 PostgreSQL/MySQL 演示数据，不连接真实数据库。无需克隆源码、无需编辑配置或构建镜像，只需下载一个 compose 文件并启动：

```bash
mkdir agentsql-demo && cd agentsql-demo
curl -fsSL -o docker-compose.yml https://raw.githubusercontent.com/cuipengdba/agentsql/v4.5.0/deploy/quickstart/docker-compose.yml
docker compose up -d
```

等待约 1–2 分钟，四个容器就绪后打开 <http://127.0.0.1:17880>：登录页已自动填好演示账号，直接点「登录」即可。「演示台」内置 5 个真实剧本：列授权 + 脱敏、列授权缺失、对象越权、DDL 越权、写操作转人工。彻底清理：

```bash
docker compose down -v
```

Demo 凭据均为固定演示值、写在 compose 中，仅用于本地体验，切勿连接真实数据。完整说明见 [5 分钟快速上手](docs/GETTING_STARTED.md) 与 [Live Demo 指南](docs/DEMO.md)。

## 其他安装方式（源码构建、离线与审计环境）

需要审计每一步、修改 Compose 配置或从源码构建时，可继续使用原有流程。Docker Compose 默认只启动 AgentSQL，元数据和审计写入命名卷 `agentsql-data` 中的 `/var/lib/agentsql/agentsql.db`：

```bash
cp examples/docker/.env.example .env
openssl rand -base64 24   # 填入 AGENTSQL_SECRET，输出恰好 32 个 ASCII 字节
openssl rand -base64 24   # 填入 AGENTSQL_ADMIN_PASSWORD
docker compose up -d --build
docker compose ps
```

PowerShell 可分别执行两次以下命令并填写两个变量：

```powershell
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); [Convert]::ToBase64String($bytes); $rng.Dispose()
```

访问 <http://127.0.0.1:7780>。Compose 也只在宿主机回环地址暴露端口。需要可观测性栈时，先在环境中设置自行生成并保存的随机值，再运行：

```bash
docker compose --profile observability up -d --build
```

完整说明见 [可观测性示例](examples/observability/README.md)。`AGENTSQL_SECRET` 必须与 `agentsql.db` 成对备份；直接更换 SECRET 会让既有数据源口令无法解密。生产控制面可将 metadata 与 audit 分别放入独立的 PostgreSQL 15+ 数据库，完整迁移、最小权限和回滚流程见 [部署指南](docs/DEPLOY.md#postgresql-控制面部署)。

源码二进制构建按 `go.mod` 需要 Go 1.26、cgo、C 编译器和 glibc 兼容环境；本地 `make build` 使用 `PATH` 中的 Go，须先确认版本。当前 Makefile 与正式发布脚本默认使用 Go 1.26.10；普通构建直接使用仓库已有的内嵌控制台产物，不需要 Node.js。

以下源码构建示例仅生成本机可执行文件。自行构建的文件没有经过正式发布资产的签名与分发验收：

```bash
make build VERSION=v4.5.0
./bin/agentsqlctl init-config -o config.yaml
export AGENTSQL_SECRET="$(openssl rand -base64 24)"
export AGENTSQL_ADMIN_USER='admin'
export AGENTSQL_ADMIN_PASSWORD="$(openssl rand -base64 24)"
./bin/agentsql serve -c config.yaml
```

Windows PowerShell：

```powershell
./bin/agentsqlctl.exe init-config -o config.yaml
$bytes = [byte[]]::new(24); $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $rng.GetBytes($bytes); $env:AGENTSQL_SECRET = [Convert]::ToBase64String($bytes); $rng.Dispose()
$env:AGENTSQL_ADMIN_USER = 'admin'
$passwordBytes = [byte[]]::new(24); $passwordRng = [System.Security.Cryptography.RandomNumberGenerator]::Create(); $passwordRng.GetBytes($passwordBytes); $env:AGENTSQL_ADMIN_PASSWORD = [Convert]::ToBase64String($passwordBytes); $passwordRng.Dispose()
./bin/agentsql.exe serve -c config.yaml
```

`agentsqlctl check-config -c config.yaml` 只校验配置，不要求凭据；`agentsqlctl migrate -c config.yaml` 会校验 SECRET 后执行元数据库迁移。

v0.5 审计报表支持 CSV、JSONL、纯 Go 生成的 PDF，以及包含四类载荷和逐文件 SHA-256 `manifest.json` 的 ZIP：

```powershell
./bin/agentsqlctl audit report -c config.yaml --format archive --out audit-compliance.zip
./bin/agentsqlctl audit verify-archive --in audit-compliance.zip
```

归档校验只证明包内一致性，不替代数字签名、外部时间戳、WORM 或 SIEM 保留。

## MCP 接入

先在控制台创建 Agent、配置数据源和授权策略，并保存只显示一次的 `asql_...` API Key。

### stdio

```json
{
  "mcpServers": {
    "agentsql": {
      "command": "/absolute/path/to/agentsql",
      "args": ["mcp", "--config", "/absolute/path/to/config.yaml"],
      "env": {
        "AGENTSQL_SECRET": "<YOUR_32_BYTE_SECRET>",
        "AGENTSQL_API_KEY": "<YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

### Streamable HTTP

兼容 MCP 本地端点为 `http://127.0.0.1:7780/mcp`；非本机部署必须在前面配置 HTTPS 反向代理。

```json
{
  "mcpServers": {
    "agentsql-http": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {
        "Authorization": "Bearer <YOUR_AGENTSQL_API_KEY>"
      }
    }
  }
}
```

网关始终提供 12 个基础 MCP 工具：`list_datasources`、`list_schema`、`explain_query`、`query`、`find_documents`、`read_key`、`query_analytics`、`search_vectors`、`execute_write`、`request_approval`、`get_approval_result`、`execute_approved`。`find_documents` 仅面向具备精确集合授权、严格 TLS、认证账号的 MongoDB 指定版本受控只读路径。`read_key` 仅面向 Redis/Valkey 指定 TLS/ACL 单实例、独立键策略和 UTF-8 字符串。`query_analytics` 仅面向 ClickHouse 指定 TLS 单节点、MergeTree 单表及精确表列授权。`search_vectors` 仅面向 Milvus 2.6.25 指定 TLS 单机、精确 collection/字段授权和单向量受控检索。v0.4.0 默认开启的 PostgreSQL B5 会话/计划事务还会注册 `open_session`、`close_session`、`get_session_status`、`begin_transaction`、`execute_transaction_statement`、`commit_transaction`、`rollback_transaction`、`get_transaction_status` 8 个工具；显式关闭 `mcp.sessions.enabled` 后只保留基础工具。

### 无状态 MCP 与任务授权

- `/mcp/2026-07-28` 对每个请求重新验证 Agent Key、租户和协议元数据；它不提供跨请求 B5 工具。原有 `/mcp` 与 stdio 继续可用，示例见 [MCP 接入指南](docs/INTEGRATIONS.md)。
- 临时任务 Key 只针对一个 Agent、数据源与精确 SQL，经异人审批后由申请人一次性领取，可撤销且最长 24 小时；原权限、脱敏与审计仍生效，见 [任务授权说明](docs/task-read-grants.md)。
- 接入向导和 `agentsqlctl migrate --check` 只做预检，不代表查询获准，也不替代数据库与配对密钥备份。

## 能力矩阵

| 产品能力 | v4.5.0 状态 | 使用与边界 |
| --- | --- | --- |
| 独立 AI 问数 Agent | 已验限定路径 | 自有 Go/Web 问数，不依赖 SQLBot；已登记 PostgreSQL 网关别名、本地模型、普通问数和当前授权历史按 [发布说明](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0)执行 |
| MCP Server 安全网关 | 可独立使用 | stdio 与 HTTP MCP；身份、对象/列授权、规则、受控执行、脱敏与持久审计按当前策略逐次生效 |
| SQLBot 接入 | 可选、固定基线 | 经已验证适配器把 SQLBot 数据库执行导入 AgentSQL MCP；固定版本和接入步骤见 [SQLBot 指南](docs/SQLBOT.md)，不代表任意 SQLBot 部署直接兼容 |
| 基本助手与页面嵌入 | 已验限定路径 | 单数据源基本游客助手与 Type 4 页面嵌入；来源、身份和撤权仍须核验 |
| 高级助手与广覆盖兼容 | 后续规划 | Type 1/第三方目录、复杂活动权限目标、其余社区版页面/API 分别纳入 v4.6.0–v4.8.0，不作为 v4.5.0 已支持项 |

## 数据库兼容矩阵

v3.0.0 代码的注册表覆盖 **8 大类、30 款数据库**（9 款关系型 + 21 款 NoSQL / 向量）。openGauss 与瀚高 HighGo 均作为独立的限定只读类型计入；金仓 KingbaseES V9 另列为指定实例 PG 协议路径，不计入 30 款；分类表共 31 行。🟢 表示解析、授权、受控执行、脱敏、审计链路在所述范围通过；🔵 受控只读＝指定实例上的受限查询、脱敏与持久审计链路；🔷 连接级＝ping、版本、Schema、只读预览；🟡 待验证＝等厂商环境。指定实例闭环不代表完整生产支持。

| 类别 | 数据库 | 适配状态 | 边界说明 |
| --- | --- | --- | --- |
| 关系型 | PostgreSQL 14–18 | 🟢 已适配 · 完整防护 | 全链路实测通过 |
|  | MySQL 8.0 | 🟢 已适配 · 完整防护 | 全链路实测通过 |
|  | Oracle AI Database 26ai Free | 🔵 指定版本受控只读 | `FREEPDB1`、服务端 `23.26.3.0.0`：最小权限 HTTP MCP、R010、手机号脱敏、双客户端、权限撤销、独立审计故障及业务库断线恢复通过；`EXPLAIN PLAN` 不在网关账号路径，商业版及完整生产支持未验，见[验收记录](docs/v2.2.0-acceptance.md) |
|  | 达梦 DM8 | 🟢 指定场景闭环 | Pack3、COMPATIBLE_MODE=0：v1.5.0 完成客户端与权限撤销；v1.6.0 补齐审计故障、锁等待超时、取消与恢复复验；见[验收记录](docs/v1.6.0-acceptance.md) |
|  | 崖山 YashanDB | 🔵 已适配 · 指定版本受控只读 | 23.4.1.109 指定实例的最小权限 HTTP MCP、R010、脱敏、持久审计和故障关闭通过；其他版本及生产拓扑未验证 |
|  | SQL Server 2025 Developer | 🔵 指定版本受控只读 | Linux x86_64 单实例合成数据：严格 TLS、最小权限 HTTP MCP、R010、脱敏、双客户端、SELECT/SHOWPLAN 撤权及故障关闭通过；其他版本、Windows/Always On、生产证书轮换和完整生产支持未验证，见[验收记录](docs/v2.3.0-acceptance.md) |
|  | openGauss 社区版 7.0.0 LTS | 🔵 指定版本受控只读 | 独立 `opengauss` 类型，指定 build 2397a86f：`verify-full`、只读物理单表窄 SELECT、真实 MCP、R010、脱敏、双客户端、权限撤销与两类故障关闭通过；GaussDB 商业版、主备及完整生产支持未验证，见[验收记录](docs/v2.4.0-acceptance.md) |
|  | Vastbase G100 | 🔵 指定版本受控只读 | V3.0 Build 9 PG 模式的有界 SELECT、表授权、脱敏、持久审计、客户端、权限撤销与停库恢复实测；v1.6.0 补齐故障关闭复验，v1.8.0 验证 `verify-full`、证书及凭据轮换和双客户端真调用；正式主备、其他 Build 与生产负载未测，见[验收记录](docs/v1.8.0-acceptance.md)。 |
|  | 瀚高 HighGo | 🔵 指定版本受控只读 | 独立 `highgo` 类型：9.0.10 企业版 PG 模式、单实例合成表的有界 SELECT、客户端、权限撤销、审计与故障关闭；见[验收记录](docs/v1.7.0-acceptance.md) |
|  | 金仓 KingbaseES V9（另列） | 🟢 指定场景闭环 | V9R1C10 PG 模式：v1.5.0 完成客户端、权限撤销与网络隔离恢复；v1.6.0 补齐故障关闭复验，不计入 30 |
| 键值 | Redis | 🔵 指定版本受控只读 | 8.2.9 TLS/ACL 单实例：精确键策略、`read_key` UTF-8 字符串、脱敏、持久审计、双客户端与故障关闭通过；Cluster、Sentinel、写入及完整生产支持未验证，见[验收记录](docs/v2.6.0-acceptance.md) |
|  | Valkey | 🔵 指定版本受控只读 | 8.1.10 TLS/ACL 单实例与 Redis 同一窄读合同，独立真库及客户端验收；其他拓扑和完整生产支持未验证，见[验收记录](docs/v2.6.0-acceptance.md) |
|  | Memcached | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
| 文档 | MongoDB | 🔵 指定版本受控只读 | 7.0.43 单实例：精确集合与可选字段授权、严格 TLS、最小权限 `find`、显式投影和有界等值过滤、脱敏、持久审计、双客户端及故障关闭实测；副本集、分片、写入及完整生产支持未验证，见[验收记录](docs/v2.5.0-acceptance.md) |
|  | Couchbase | 🔷 已适配 · 连接级 | 同口径 |
|  | CouchDB | 🔷 已适配 · 连接级 | 同口径 |
| 宽列 | Cassandra | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | ScyllaDB | 🔷 已适配 · 连接级 | 同口径 |
|  | HBase | 🔷 已适配 · 连接级 | 同口径 |
| 图 | Neo4j | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | JanusGraph | 🔷 已适配 · 连接级 | 同口径 |
|  | NebulaGraph | 🔷 已适配 · 连接级 | 同口径 |
| 时序 | InfluxDB | 🔷 已适配 · 连接级 | ping / 版本 / Schema / 只读预览 |
|  | Prometheus | 🔷 已适配 · 连接级 | 同口径 |
|  | TimescaleDB | 🔷 已适配 · 连接级 | 同口径 |
| OLAP | ClickHouse | 🔵 指定版本受控只读 | 26.8.31.2 严格 TLS 单节点：`query_analytics` 的 MergeTree 单表显式列、String 等值过滤、精确表列授权、只读角色、资源限制、脱敏、持久审计、双客户端与断库恢复通过；其他版本、集群及完整生产支持未验证，见[验收记录](docs/v2.7.0-acceptance.md) |
|  | Apache Doris | 🔷 已适配 · 连接级 | 同口径 |
|  | StarRocks | 🔷 已适配 · 连接级 | 同口径 |
| 向量 | Milvus | 🔵 指定版本受控只读 | 2.6.25 TLS 单机 `default` 数据库：`search_vectors` 的精确 collection/字段授权、有限 FloatVector、COSINE 索引、脱敏、持久审计和双客户端在合成数据上通过；集群、其他版本及完整生产支持未验证，见[候选验收](docs/v2.8.0-acceptance.md) |
|  | Qdrant | 🔷 已适配 · 连接级 | 同口径 |
|  | Weaviate | 🔷 已适配 · 连接级 | 同口径 |

除矩阵中列明的指定版本受控读取外，其他 NoSQL/向量类型仅有连接级能力；只读预览不代表完整查询或服务端安全能力。所有实测都以具体实例、合成用例和文档中的限制为准，非数据库厂商认证。业务请求须经 AgentSQL 网关，并使用专用最小权限账户；解析、授权或能力事实不明时故障关闭。详见 [NoSQL 范围](docs/nosql-support.md)和[安全边界](SECURITY.md)。

控制面存储另计：SQLite 默认零配置，可 combined 存储；PostgreSQL 15+ 可 combined 或独立 metadata / audit 库（PG18 为基准）。使用 `agentsqlctl migrate-sqlite-to-postgres` 可从 SQLite 迁至单库或独立双库。

## 已知限制与安全边界

- 脱敏按全局列、`表.列`、`模式.表.列`三档作用域在结果层匹配：能唯一归属到物理表的顶层直接投影列按来源表/来源列精确命中；PostgreSQL/MySQL 已验证的函数/表达式、聚合、`CAST`、`UNION`、CTE 与视图引用等派生输出，在能证明同源时按规则处理，来源不明而可能涉及受保护列时固定阻断为 `***`。这不是完整 DLP；数据库函数与视图内部语义、统计侧信道及绕过网关的直连仍需数据库最小权限、列级授权、安全视图与审批共同约束。
- `hash` 是不可逆指纹，不是加密，也不能解密还原。它在业务数据库返回结果后计算，不下推到数据库内的 JOIN/WHERE；确定性会暴露相等关系和频率，共享同一 key 还会带来跨库关联风险。
- `block` 不输出原值字符、长度或等值关系，但仍保留结果集行列形状、行数、列名、是否有结果，并因空值原样返回而暴露该格为空/NULL；它不是匿名化。`block` 在数据库执行后处理，不减少数据库读取，也不阻止数据库侧使用原值做 WHERE/JOIN/GROUP BY，只阻止结果单元对外返回。其固定 `***` 是不透明字符串，不保证数值、日期或 JSON 的 schema 兼容。
- PostgreSQL 有列策略的 SELECT（含多表 JOIN、自连接、谓词和通配投影）必须走 B2 列绑定路径；B2 不可用时执行前失败关闭。MySQL 8.0/8.4 只在显式开启并完成双账号登记的单表 InnoDB SELECT 子集进入独立列授权路径；其余有列策略的 MySQL 查询执行前拒绝。Vastbase 等仍无等价列级绑定证明。没有列策略时仍由表策略约束。
- `AllowedTables` 中的 `*` 或 `schema.*` 表示管理员显式授予匹配表的全部列；此时精确列白名单不再收紧。单表使用精确列白名单时，应显式列出投影列。
- PostgreSQL 的 B2 列级授权出厂默认开启；普通 schema-qualified 基表使用免扩展的 `CATALOG_CLOSED_V1`，原生 binder 仅用于可选增强。MySQL 不进入 B2 PostgreSQL 路径。
- B5 跨请求逻辑会话与 PostgreSQL 计划事务出厂默认开启；MySQL 跨请求事务不支持，配置 `mcp.transactions.mysql: true` 会拒绝启动。每个 operation 仍只允许一条顶层 SQL，并受预检计划和会话安全边界约束。

## 本地测试模式

`AGENTSQL_INSECURE=1` 永久只表示“允许公开测试凭据”：它可在本地开发/测试中放行长度正确的公开示例 SECRET 和非空弱管理员口令，但仍拒绝空 SECRET、非 32 字节 SECRET 与空管理员口令。它不会关闭认证、安全规则或 fail-closed 行为，生产环境不得设置。

## 文档与社区

- [五分钟快速上手](docs/GETTING_STARTED.md)
- [使用手册](docs/USER_GUIDE.md)
- [MCP 接入指南](docs/INTEGRATIONS.md)
- [当前版本文档索引](docs/README.md)
- [任务级临时只读授权](docs/task-read-grants.md)
- [v4.5.0 正式发布说明](https://github.com/cuipengdba/agentsql/releases/tag/v4.5.0)
- [当前版本运维手册](docs/OPERATIONS.md)
- [敏感列发现与脱敏草稿](docs/DISCOVERY.md)
- [Webhook / Syslog 通知外发与安全配置](docs/NOTIFICATIONS.md)
- [部署、PostgreSQL 控制面、升级、备份与 systemd](docs/DEPLOY.md)
- [本地 Live Demo、定期重置与公开部署安全清单](docs/DEMO.md)
- [产品与工程规范](docs/SPEC.md)
- [安全策略与私密漏洞报告](SECURITY.md)
- [变更记录](CHANGELOG.md)
- [Issue 反馈](https://github.com/cuipengdba/agentsql/issues)

请不要在公开 Issue 中提交真实密钥、口令、连接串或未修复漏洞细节；未修复漏洞请按 [SECURITY.md](SECURITY.md) 走私密渠道。

### 加入社区（微信）

| 公众号 | 个人微信 |
| --- | --- |
| ![CP 的 PostgreSQL 厨房](website/public/assets/community/wechat-official-account.png) | ![崔鹏个人微信](website/public/assets/community/wechat-personal.png) |
| 扫码关注 | 扫码添加作者，备注「AgentSQL」 |

## 构建与贡献

```bash
go vet ./...
go test -race -count=1 ./...
make build VERSION=v4.5.0
```

`pg_query_go` 要求 cgo；不要使用 `CGO_ENABLED=0` 或 Alpine/musl 构建。提交改动前请阅读 [SPEC](docs/SPEC.md)，为行为变化补测试，并保持 `tests/corpus` 决策语料不被无意改写。

## 开源许可与商标

当前源码使用 [Apache License 2.0](LICENSE)，并附有 [NOTICE](NOTICE)。使用、修改和分发请遵守该许可。AgentSQL（含中文名“智盾”）名称与 Logo 的商标权保留；Apache-2.0 不授予商标使用权。历史 tag 和发布包仍适用其发布时的原许可。

v0.5 人员认证（MFA / OIDC / LDAP）的配置、威胁边界和迁移说明见 [身份认证指南](docs/AUTHENTICATION.md)。
