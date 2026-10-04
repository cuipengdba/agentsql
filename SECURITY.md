# AgentSQL 安全政策

## 支持版本

AgentSQL 仅为下表列出的维护系列评估和提供安全修复；是否为某个问题提供补丁，还会结合影响范围、修复风险和发布状态判断。

| 版本 | 状态 | 安全修复支持截止 |
| --- | --- | --- |
| v0.5.x（当前） | 支持 | 至下一主版本（v0.6.0）发布后 6 个月，且不早于 2027-10-31 |
| v0.4.x（上一版，已发布） | 支持 | 2027-04-30 |
| 0.3.x 及更早 | EOL | 不再提供安全修复 |

请先在可控环境复现，并尽量使用最新的受支持版本确认问题仍然存在。

## 私密报告漏洞

不要在公开 Issue、Discussion、Pull Request、日志网站或社交媒体中披露未修复漏洞、利用代码、真实凭据或敏感数据。

请使用以下任一私密渠道：

- 发送邮件至 [87326549@qq.com](mailto:87326549@qq.com)；
- 使用仓库的 [GitHub Security Advisories 私密报告](https://github.com/cuipengdba/agentsql/security/advisories/new)。

报告中请尽量包含：

- 受影响的 AgentSQL 版本、提交号及部署方式；
- 操作系统、数据库类型与版本，以及 MCP 接入方式；
- 问题描述、可复现步骤和最小化配置；
- 实际影响、攻击前提、所需权限及是否可绕过网关；
- 已观察到的日志或错误信息，并移除密钥、密码、DSN 和业务数据；
- 建议的修复或缓解措施（如有）；
- PoC 的安全获取方式。请优先提供最小、无害的复现；不要使用公开链接传播利用代码，也不要在未经确认前发送恶意载荷或真实数据。

目前不提供 PGP 公钥。如材料不适合直接通过邮件发送，请先发送不含敏感细节的说明，协商后续传输方式。

## 响应与披露流程

收到报告后，我们通常按以下流程处理：

1. 确认收到并检查报告是否完整。我们会尽力在 3 个工作日内给出初步响应。
2. 复现和评估影响，确认受影响版本、严重程度及临时缓解措施。我们会尽力在 7 个工作日内提供初步研判；复杂问题可能需要更长时间。
3. 在私密渠道中协调修复、回归验证、发布计划和披露时间。修复时间取决于问题复杂度与发布风险，不构成服务等级承诺。
4. 修复可用后协调公开披露；必要时先给用户合理的升级窗口，再发布安全公告与修复说明。

在不影响调查和用户安全的前提下，我们欢迎报告者参与验证。经报告者同意，公开公告中可以致谢；如希望匿名，请在报告中说明。请在双方协调披露前保持漏洞细节私密。

## 安全模型与边界

AgentSQL 是面向 AI Agent 的数据库安全网关和 MCP Server。它对经过网关的请求执行身份认证、SQL 解析、授权与规则评估、受控执行、结果脱敏和应用层审计，用于降低 Agent 误操作、越权访问和危险 SQL 的风险。

部署和风险评估时必须同时考虑以下边界：

- 保护范围只覆盖经过 AgentSQL 网关、并使用受控运行账号访问数据库的请求。能够绕过网关直连数据库的 owner、superuser、DBA 或其他高权限账号，不受 AgentSQL 规则保护。
- 数据库运行账号仍是最终权限边界。应使用最小权限、按用途隔离的账号；AgentSQL 不能补偿过度授权，也不能阻止数据库管理员在数据库侧直接操作。
- 结果脱敏是在结果层按列规则打码，用于降低特定字段直接暴露的风险，不是完整 DLP。复杂表达式、重命名、聚合、视图或其他数据变换可能超出列匹配能力，不能据此声称敏感数据已被全面发现或阻断。
- 审计记录属于应用层审计，不是不可篡改账本，也不是法规级 WORM、保留锁或合规存证。拥有底层存储或数据库高权限的人员可能修改或删除记录；有合规要求时应另行使用受控归档、外部 SIEM 或合规存储。
- 规则、审批和 fail-closed 处理用于降低已知风险，但不能保证识别所有 SQL 语义、业务逻辑滥用、数据库漏洞或未知攻击。
- Streamable HTTP 的远程部署需要由运维方配置 HTTPS、网络访问控制和凭据管理；AgentSQL 的本机 HTTP 示例不代表公网安全部署方案。

发现可突破上述预期边界、造成权限提升、认证绕过、敏感信息泄露或审计规避的问题时，请按本政策私密报告。

## SQL Server 2025 安全边界

`db_type=sqlserver` 是独立的 SQL Server 2025（主版本 17）受限只读能力，不代表完整 T-SQL、DML、DDL、存储过程、SQL Agent、CLR、链接服务器或 B2/B5 支持。

- 传输必须加密。默认 `tls_mode=strict`，对应 TDS 8.0 严格加密与证书校验；`verify-full` 对应 `encrypt=true` 的兼容路径。没有关闭 TLS 的配置。`trust_server_certificate=true` 会跳过服务端身份验证，只能在隔离开发环境中与 `verify-full` 显式组合，生产环境禁止使用。
- 数据库账号必须遵循最小权限：仅授予目标库/模式/表的 `SELECT`、受控元数据可见性及估算计划所需 `SHOWPLAN`。不要授予 `sysadmin`、`CONTROL SERVER`、`db_owner`、写权限、DDL、存储过程执行、impersonation、外部数据源或文件访问权限。
- 网关只接受一条窄只读 `SELECT`。写操作（包括有 `WHERE` 的写操作）、过程调用、批处理、动态数据源、`xp_cmdshell`、OLE 自动化、`BULK`、`WAITFOR`、`DBCC`、会话级 `SET/USE` 和未证明安全的语法均失败关闭。解析失败时不会把原 SQL 交给驱动尝试。
- discovery 使用固定、参数化的 `sys.tables/sys.schemas/sys.columns/sys.types` 查询。SQL Server 的 metadata visibility 会按数据库权限隐藏对象；权限不足、目录结果超限或结果形状异常都作为失败处理，不扩大权限或回退到调用者提供的 SQL。
- 动态扫描规则使用 `SHOWPLAN_XML` 的估算计划；目标 `SELECT` 不在该阶段执行。计划 XML 解析有大小、深度和节点限制，无法验证的计划拒绝继续。`SHOWPLAN` 本身可能泄露对象与计划信息，因此只应授予专用网关账号，不应向终端用户暴露原始计划。
- 审计中的 `db_type` 会记录为 `sqlserver`，但仍属于应用层审计，保留与防篡改边界与本政策其他数据库相同。驱动原始错误、DSN、密码、证书路径和业务对象名不得写入面向客户端的错误或普通审计详情。
- SQL Server 路径不使用 PostgreSQL B2/B5 native binder，不提供 PostgreSQL 的列级证明。结果脱敏也不是数据库原生 RLS、Dynamic Data Masking 或完整 DLP 的替代品。

部署配置与驱动行为应以 Microsoft 的 [Go 驱动加密与证书](https://learn.microsoft.com/en-us/sql/connect/golang/encryption-certificates?view=sql-server-ver17)、[Go 驱动安全最佳实践](https://learn.microsoft.com/en-us/sql/connect/golang/security-best-practices?view=sql-server-ver17)及 [`SET SHOWPLAN_XML`](https://learn.microsoft.com/en-us/sql/t-sql/statements/set-showplan-xml-transact-sql?view=sql-server-ver17) 文档为准。
