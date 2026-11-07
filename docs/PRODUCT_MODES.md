# AgentSQL 的三种使用方式

[中文](PRODUCT_MODES.md) | [English](en/PRODUCT_MODES.md)

AgentSQL v4.6.0 是**独立 AI 问数 Agent + 独立 MCP Server 数据库安全网关**。问数页面和 Go 服务由 AgentSQL 自己提供，运行时不需要 SQLBot。MCP Server 可以单独为其他 AI 客户端提供受控数据库工具。两种入口也可以同时使用同一个网关。以下步骤以自托管、运营方预先登记的 PostgreSQL 网关数据源和本地模型为例；其他数据库或客户端要按各自的验收范围配置。

## 开始前：安装与授权

1. 从 [v4.6.0 Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.6.0) 获取对应架构的正式包，按发布页校验签名和哈希。在包根目录安装网关；需要自有问数时加 `--with-chatbi`。安装器的其他选项见[部署指南](DEPLOY.md)。
2. 在网关控制台 `http://127.0.0.1:7780` 创建仅有需要权限的数据库账号、数据源、Agent 和对象/列授权。为用户问数建立个人绑定；为外部 MCP 客户端单独创建 Agent API Key。不要把管理员登录令牌当成 MCP Key。
3. 在私有环境提供 OpenAI 兼容的本地文本模型服务。v4.6.0 包含固定的本地表结构向量模型，但**不内置文本大模型权重**。问数的模型和个人绑定配置见[原生问数配置](CHATBI.md)。两个本地监听端口默认只供回环访问；远程访问使用 SSH 隧道或受控 HTTPS 反向代理。

```sh
sudo sh install.sh install --with-chatbi
systemctl is-active agentsql.service agentsql-chatbi.service
```

## 方式一：只用 AgentSQL 独立问数

1. 在网关批准用户、数据源、表列和适用的行规则，再按[原生问数配置](CHATBI.md)把当前个人绑定与本地模型写入问数服务。配置成功不代表查询成功。
2. 打开 `http://127.0.0.1:8000`，以 AgentSQL 问数账号登录，选择已绑定的数据源，输入自然语言问题，例如“按地区统计订单数”。
3. AgentSQL 从当前受权结构选择表列，调用配置的模型生成候选 SQL；网关检查并执行，页面展示回答、SQL、结果行数与审计编号。核对 SQL 和业务含义后再使用答案。
4. 重新打开历史或导出时复核当前权限。撤销绑定或表列授权后，旧结果不可继续作为授权依据；用同一问题做一次允许、一次拒绝，并在网关查询审计中确认决策。

**适合：** 员工直接用 AgentSQL 自有页面问数。SQLBot 无需部署。v4.6.0 的实测支持范围是发布说明列明的本地模型、登记的 PostgreSQL 网关别名和有限权限形态；复杂活动角色/多用户/非空白名单规则不列为本版可执行能力。

## 方式二：只用 AgentSQL MCP Server 安全网关

1. 在网关创建专用 Agent、关联数据源和最小授权，并保存只显示一次的 `asql_...` API Key。问数服务无需启动。
2. 将支持 Streamable HTTP 的 MCP Client 指向本机 `http://127.0.0.1:7780/mcp`，通过安全的客户端密钥配置注入 Bearer Key。服务端的其他传输方式和协议差异见[MCP 接入指南](INTEGRATIONS.md)。
3. 先调用 `list_datasources` 和 `list_schema` 核对可见范围，再调用已授权的 `query`；用未授权表或列做拒绝验证，并核对允许和拒绝的网关审计。客户端不得自行声称用户身份或绕过 Agent 绑定。

```json
{
  "mcpServers": {
    "agentsql": {
      "url": "http://127.0.0.1:7780/mcp",
      "headers": {"Authorization": "Bearer <AGENT_API_KEY>"}
    }
  }
}
```

**适合：** 为自有 AI Agent 或已验证的 MCP 客户端集中控制数据库访问。SQLBot 可以通过[固定版本适配器](SQLBOT.md)成为其中一个可选调用方；这不表示任意 SQLBot 部署都能零改动接入。MCP 网关自身没有自然语言问数页面，文本模型由外部客户端管理。

## 方式三：独立问数与 MCP Server 一起使用

1. 在同一部署中运行 `agentsql.service` 和 `agentsql-chatbi.service`。为问数用户建立个人绑定，为外部 MCP Client 建立独立 Agent Key；按各自身份分别授权，不共享管理员令牌。
2. 用户通过 AgentSQL 页面问同一份受权数据；外部 Agent 通过 MCP 工具查询其获准的数据。两条入口都进入网关的 SQL 检查、限额、脱敏和持久查询审计。
3. 分别执行允许和拒绝案例，检查审计中的 Agent、用户或绑定、数据源、SQL、决策与时间。撤销一条入口的凭据或绑定后复测该入口及另一入口，确认授权边界符合预期。

```text
AgentSQL 自有页面 → Go 问数 Agent ─────┐
                                    ├→ AgentSQL 安全网关 → 当前受权数据库
外部 AI Agent → AgentSQL MCP Server ─┘
```

**适合：** 同时服务员工问数与企业 AI Agent。两种入口共用安全内核，但各自的身份、凭据与权限需要单独配置和验收。公网 [Demo](https://agentsql.cn/demo/) 的问数模型仅为模拟展示，不能代替以上真模型和真库验收。

## 当前已验边界与更多资料

- 已验路径和性能数字以 [v4.8.1 Release](https://github.com/cuipengdba/agentsql/releases/tag/v4.8.1) 及各功能的原始验收证据为准，不承诺所有自然语言问题都正确，也不把固定示例外推到所有数据库。
- v4.6.0 增加固定 SQLBot Community v1.10.3 的 Type 1 高级助手与第三方目录限定接入。先按[Type 1 配置步骤](v4.6.0-design.md)登记真实非管理员用户、既有受权网关别名、私有目录与宿主证书，再启用独立助手服务；目录不能提供数据库连接凭据或绕过当前授权。
- v4.7.0 已交付限定形态的角色、多用户及非空白名单规则；v4.8.0/1 已交付限定路径与并发索引修复。原计划的全部社区版页面/API 操作、广覆盖连接兼容、真实云模型推理和生产规模 p95 仍未完成逐项验收；未支持入口保持拒绝或仅配置。
- 用户真实业务系统尚未验证；不宣称 100% 兼容或零改动切换。生产环境还需数据库最小权限、网络隔离、备份和本地验收。
- 详细操作：[原生问数配置](CHATBI.md) · [MCP 接入指南](INTEGRATIONS.md) · [网关使用手册](USER_GUIDE.md) · [安全边界](../SECURITY.md)。
