# AgentSQL（智盾）

**语言 / Language:** [简体中文](README.md) | [English](README.en.md)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/agentsql-wordmark-dark.svg">
  <img src="brand/agentsql-wordmark-light.svg" width="360" height="80" alt="AgentSQL">
</picture>

![Version v4.7.0](https://img.shields.io/badge/Version-v4.7.0-blue)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue)](LICENSE)

**AgentSQL = 独立 AI 问数 Agent + MCP Server 数据库安全网关。** 用户可直接在 AgentSQL 自有页面用自然语言问数；其他 AI Agent 可把 AgentSQL 当作独立 MCP Server 使用。两条入口共用当前身份与授权、受控 SQL 执行、结果脱敏和持久审计。AgentSQL 自有问数不依赖 SQLBot；SQLBot 是可经固定版本适配器接入的可选客户端。

[官网](https://agentsql.cn/) · [中文问数界面与功能示意](https://agentsql.cn/#qa-gallery) · [v4.7.0 下载与发布边界](https://github.com/cuipengdba/agentsql/releases/tag/v4.7.0) · [三种使用方式](docs/PRODUCT_MODES.md) · [文档索引](docs/README.md)

## 选一种使用方式

| 方式 | 你来做什么 | 从哪里开始 |
| --- | --- | --- |
| **独立问数** | 登录 AgentSQL 自有页面，选择已绑定数据源，提问并核对生成的 SQL、结果和审计 | [问数操作与配置](docs/PRODUCT_MODES.md#方式一只用-agentsql-独立问数) |
| **独立 MCP 安全网关** | 给外部 AI Agent 配置 Agent API Key，让 MCP 工具在授权范围内查询 | [MCP Server 操作](docs/PRODUCT_MODES.md#方式二只用-agentsql-mcp-server-安全网关) |
| **两者一起** | 员工用 AgentSQL 页面问数，外部 Agent 通过 MCP 查询；分别授权，共用安全审计 | [联合使用](docs/PRODUCT_MODES.md#方式三独立问数与-mcp-server-一起使用) |

```text
AgentSQL 自有页面 → Go 问数 Agent ─────┐
                                    ├→ AgentSQL 安全网关 → 当前受权数据库
外部 AI Agent → AgentSQL MCP Server ─┘
```

## v4.7.0 能力与边界

| 能力 | 当前状态 | 关键边界 |
| --- | --- | --- |
| 自有 AI 问数 | **限定路径已验** | 自有 Go/Web 问数；本地模型、运营方登记的 PostgreSQL 网关别名和当前权限，**无需 SQLBot 运行** |
| MCP Server 安全网关 | **可独立使用** | stdio、Streamable HTTP 和独立无状态 HTTP；Agent Key 不等于管理员令牌，每次请求按当前授权执行 |
| 基本助手与 Type 4 页面嵌入 | **限定路径已验** | 单数据源基本游客助手与指定页面嵌入，来源、身份及撤权仍受控 |
| SQLBot 接入 | **可选固定基线** | 已验适配器可把指定 SQLBot 执行路径交给 AgentSQL MCP；不承诺任意部署零改动接入 |
| Type 1 高级助手 | **限定路径已验** | 未修改 SQLBot Community v1.10.3 助手 SDK；运营方登记第三方目录，将外部 ID 映射到当前受权的网关别名，独立助手监听器不提供管理 API；只支持发布说明列明的场景 |
| 活动数据权限 | **限定形态已验** | 明确列出的同工作空间用户；内建管理员/普通成员角色限定；非空白名单只作元数据，不豁免行列限制 |
| 更广兼容 | **后续规划** | 任意角色扩展、原版权限库直接迁移与余下社区版页面/API 规划至 v4.8.0 |

自然语言结果需要核对生成的 SQL 和业务含义。v4.7.0 **未在用户真实业务系统验证**，不宣称 100% 兼容或零改动切换；[正式发布说明](https://github.com/cuipengdba/agentsql/releases/tag/v4.7.0)给出精确提交、验收场景、性能数字与限制。公网 Demo 的问数模型仅作**模拟展示**。

## 快速开始

Linux `amd64` / `arm64` 原生包要求 glibc ≥ 2.28 和 PID 1 的 systemd。下载正式包后先按[发布说明](https://github.com/cuipengdba/agentsql/releases/tag/v4.7.0)核对签名和哈希。以下命令安装网关；自有问数需要按[三种使用方式](docs/PRODUCT_MODES.md)安装问数组件并配置本地文本模型与个人数据源绑定。

```bash
curl -fsSL https://github.com/cuipengdba/agentsql/releases/download/v4.7.0/install.sh | sudo sh -s -- install --version v4.7.0
```

Docker 使用多架构正式镜像；一键脚本仅绑定本机回环地址并创建持久卷：

```bash
curl -fsSL https://raw.githubusercontent.com/cuipengdba/agentsql/v4.7.0/scripts/quickstart.sh -o quickstart.sh && sh quickstart.sh --version v4.7.0
```

网关控制台默认 `http://127.0.0.1:7780`，安装了问数组件后自有页面默认 `http://127.0.0.1:8000`。安装器生成持久管理员密码；非交互日志不输出，可由 root 在 `/etc/agentsql/agentsql.env` 查看。远程访问应使用 SSH 本地转发或受控 HTTPS 反向代理，不直接暴露管理端口。离线安装、密钥、备份和升级步骤见[部署指南](docs/DEPLOY.md)。

## 按任务查文档

| 任务 | 文档 |
| --- | --- |
| 三种产品形态与验收步骤 | [三种使用方式](docs/PRODUCT_MODES.md) |
| 自有问数模型与个人绑定 | [原生问数配置](docs/CHATBI.md) |
| MCP 客户端、协议与工具 | [MCP 接入指南](docs/INTEGRATIONS.md) |
| 控制台权限、规则、脱敏与审计 | [使用手册](docs/USER_GUIDE.md) · [安全边界](SECURITY.md) |
| SQLBot 固定版本适配 | [SQLBot 接入](docs/SQLBOT.md) · [Type 1 高级助手部署](docs/v4.6.0-design.md#生产部署边界) |
| 数据库适配分级与证据 | [官网兼容矩阵](https://agentsql.cn/#compatibility) · [数据库研究与验收](docs/ecosystem-db-compat-research.md) |
| 离线安装、升级、备份与恢复 | [部署指南](docs/DEPLOY.md) · [运维手册](docs/OPERATIONS.md) |

数据库“已注册”“连接级”“指定版本受控只读”“指定场景完整防护”是不同等级。官网矩阵保留逐库版本、拓扑与验收边界；连接成功不等于可安全问数，也不等于厂商认证。

## 社区与项目

[历史 Release](https://github.com/cuipengdba/agentsql/releases) · [CHANGELOG](CHANGELOG.md) · [路线图](ROADMAP.md) · [Issues](https://github.com/cuipengdba/agentsql/issues) · [Discussions](https://github.com/cuipengdba/agentsql/discussions) · [贡献指南](CONTRIBUTING.md)

当前源码按 [Apache-2.0](LICENSE) 发布，并附 [NOTICE](NOTICE)；AgentSQL 名称和标识的商标权不由该许可证授予。未修复漏洞请按 [SECURITY.md](SECURITY.md) 私下报告，不在公开 Issue 中提交真实凭据或连接串。
