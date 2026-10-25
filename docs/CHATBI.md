# Go 原生问数服务

[中文](CHATBI.md) | [English](en/CHATBI.md)

v4.1.0 新增独立 Go 问数服务。它复用 AgentSQL 网关的身份绑定、授权、脱敏和审计；运行时不需要 SQLBot 或 Python。当前提供普通账号登录、单轮 PostgreSQL/MySQL 问数、会话历史、SQL 与表格结果，以及中文/英文和浅色/深色界面。它尚不代表 SQLBot 社区版全部功能已替换；五版目标见 [版本设计](v4.1.0-design.md)。

## 安装与启动

在可信渠道校验发行包签名后，解压对应架构的 Linux 包，在包目录执行：

```sh
sudo sh install.sh install --with-chatbi
```

离线使用本地已校验包，不需要安装 Python、Go 或 Docker。操作系统仍需满足原生安装器的 Linux、glibc、systemd 和基础命令要求。`--no-start` 仅预安装，不代表服务已启动。此包不内置大模型权重和模型服务，首次安装也不会自动授权数据库；完整离线模型套件在后续版本交付。

| 组件 | 默认位置 |
| --- | --- |
| 网关控制台 | `http://127.0.0.1:7780` |
| 问数页面与兼容 API | `http://127.0.0.1:8000` |
| 服务 | `agentsql-chatbi.service` |
| 问数配置 | `/etc/agentsql/chatbi.yaml` |
| 加密密钥和初始管理员密码 | `/etc/agentsql/chatbi.env`，仅 root 可读 |
| 加密存储 | `/var/lib/agentsql/chatbi.db` |
| 独立问数二进制 | `/usr/local/lib/agentsql/chatbi/agentsql` |
| 升级备份 | `/var/backups/agentsql/chatbi-upgrade.*` |

初始账号为 `admin`，工作空间和用户 ID 均为字符串 `"1"`。密码随机生成，不输出到非交互安装日志。远程使用应通过 HTTPS 反向代理或 SSH 隧道；不要把管理凭据或数据库连接凭据放到浏览器端。网关账号与问数账号是不同身份空间。

独立演示实例可用 `agentsql chatbi init --config chatbi.yaml --admin-user demo --standard-user` 初始化普通账号；该选项取消管理员标识和工作空间管理权重，仅适用于全新存储。数据库权限仍须通过个人绑定单独授予。

## 配置模型和个人数据源

先按 [SQLBot 个人绑定说明](SQLBOT.md) 在网关配置数据源、只读 Agent、人员、表列授权及个人绑定。原生问数沿用这些服务端绑定，仍在每次调用时重新校验。填写工作空间 ID、用户 ID 和数据源名称时必须与问数配置一致。示例中的数据源 `10` 是问数侧 ID，不是网关数据源 ID。

将下列私有 JSON 保存为权限 `0600` 的文件，把占位凭据替换成实际值。模型必须提供 OpenAI 兼容的 `/chat/completions` 接口；`api_domain` 填到 `/v1`，不包含 `/chat/completions`。HTTP 远端模型须显式启用 `allow_http`，生产远程连接使用 HTTPS。

```json
{
  "models": [{
    "workspace": "1",
    "model": {
      "id": "1",
      "name": "Local model",
      "base_model": "local-model",
      "api_domain": "http://127.0.0.1:8080/v1",
      "api_key": "",
      "allow_http": true,
      "max_tokens": 2048
    }
  }],
  "bindings": [{
    "datasource": "10",
    "workspace": "1",
    "user": "1",
    "name": "Sales PostgreSQL",
    "dialect": "pg",
    "binding_id": "REPLACE_WITH_GATEWAY_BINDING_ID",
    "connector_token": "REPLACE_WITH_CONNECTOR_SECRET",
    "agent_key": "REPLACE_WITH_AGENT_KEY"
  }]
}
```

通过受控运维终端把 `AGENTSQL_CHATBI_SECRET` 注入命令环境，再运行：

```sh
agentsql chatbi configure --config /etc/agentsql/chatbi.yaml --from connections.private.json
```

配置与本地管理审计一起提交，凭据加密保存，下一次问数读取新配置。配置成功不等于真实查询成功；需要实际验证允许、拒绝、脱敏、审计和故障场景。v4.1.0 使用每个工作空间一个默认模型，个人绑定通过命令行维护。

## 旧身份迁移预演

导出工具 `scripts/sqlbot-export-identities.py` 只支持 SQLBot 社区版 v1.10.2 的已核对身份源码与 `sqlbot-xpack 0.0.5.37`。它在原实例环境读取用户、工作空间、密码摘要、JWT 密钥与登录 RSA 密钥。导出文件含高敏感凭据，应限制权限、加密传输并按运维制度保管；不得进入 Git、工单或 Release。

```sh
python scripts/sqlbot-export-identities.py --output identities.private.json
agentsql chatbi preflight --from identities.private.json
agentsql chatbi import --config chatbi.yaml --from identities.private.json
agentsql chatbi serve --config chatbi.yaml
```

`preflight` 不修改源文件；`import` 只允许创建全新目标。需要先在环境提供独立的 32 字节 `AGENTSQL_CHATBI_SECRET`，并在配置中选择隔离存储与监听地址。导出文件最初不含模型和个人数据源映射，导入后通过 `configure` 补齐。导入保留原账号、十进制大整数 ID、登录加密密钥与已允许的旧 JWT；弱密码摘要在成功登录后升级为 bcrypt。停用、移除成员关系、撤销令牌或网关绑定仍会拒绝访问。

该工具不导出历史会话、API Key、助手密钥、文件、仪表板或全部权限。`complete_migration` 固定为 `false`，不能用于宣称生产零改动替换已经完成。保留原 SQLBot 服务，先在隔离环境验证。

## 历史问题元数据迁移预演

`scripts/sqlbot-export-history.py` 在固定的 SQLBot 社区版 v1.10.2 环境中以只读事务导出会话 ID、所属用户/工作空间/数据源、标题和问题 ID/时间。它不导出旧 SQL、查询结果、图表、模型提示词或凭据。问题文本可能含业务敏感信息，导出文件必须按私有数据管理。先完成身份迁移和个人数据源绑定，再在隔离的 AgentSQL 环境执行：

```sh
python scripts/sqlbot-export-history.py --output history.private.json --limit 1000
agentsql chatbi history-preflight --from history.private.json
agentsql chatbi import-history --config chatbi.yaml --from history.private.json
```

导出文件采用仅创建写入和 `0600` 权限。每批最多 1000 个会话，每会话最多 200 个有问题文本的记录；超过限制、缺少目标身份或绑定、ID 冲突、审计故障都会拒绝导入，单批事务回滚。原记录 ID 保留，旧记录仅用于展示历史问题，不会重放旧 SQL、返回旧结果或进入新一轮模型上下文。SQLBot 的无时区时间戳暂按 UTC 解释，迁移前须与原系统时区核对。导出报告会列出跳过的记录及下一批起点；`complete_migration` 仍为 `false`，不等于完整历史或零改动替换验收。

## 升级、恢复与边界

已安装问数组件的受管安装，在后续 `install.sh upgrade --from PATH` 时自动包含该组件。安装器停止问数服务后备份数据库、WAL、配置、密钥、二进制与服务单元；启动或版本健康检查失败时恢复该组件原有数据和服务状态。网关与问数使用各自备份，不能把两次组件安装解释为跨数据库分布式事务。

保留备份中的数据库、加密密钥与对应版本二进制作为同一恢复集。恢复前停止服务，不要把运行中 SQLite 的单独主文件当作完整备份。自定义存储路径、密钥变量或监听地址的实例不走默认路径自动升级，应使用单独审查的备份与恢复流程。卸载默认保留配置和数据；`--purge` 才删除默认配置与数据目录。

v4.1.0 的历史数据请求会按当前授权重新执行只读 SQL，避免旧结果绕过权限撤销，因此历史值可能随业务数据变化。这与 SQLBot 缓存结果语义存在差异，后续兼容版本必须收敛。单会话最多 200 条问答，耗时数据请求共享并发槽位和 4 分钟超时；配置项 `max_concurrent_data_requests` 默认为 16，允许设置为 1–16，小规格服务器建议设为 1。过大的列表会拒绝返回。网关拒绝或审计失败时不回传结果，不自动改写 SQL 重试。

尚未交付的范围包括多轮语义、图表与导出、嵌入和助手 SDK、SQLBot MCP、完整管理与数据库矩阵，以及完整数据迁移。现有 AgentSQL 网关 MCP 保持可用，但不等同于 SQLBot MCP 接口兼容。所有模型生成的 SQL 与结果都需要按业务含义核实；受控执行不保证模型理解正确。
