# AgentSQL v3.0.2 发布说明

**语言 / Language:** [简体中文](release-notes-v3.0.2.md) | [English](en/release-notes-v3.0.2.md)

## 变更

1. Web 控制台的左侧导航随浅色和深色主题切换；菜单继续独立滚动，底部条目可达。
2. GitHub README、文档和官网提供完整英文对应内容；保留章节、表格与可执行示例。新增当前文档索引和运维手册，历史记录保留当时版本事实。
3. 提供固定上游版本与源码哈希的 SQLBot v1.10.2 PostgreSQL 适配器。表字段发现及 SQL 执行走 AgentSQL MCP，字段类型仅在授权裁剪后输出。
4. SQLBot 映射绑定工作空间与数据源。拒绝、需审批、未知决策、无审计 ID、缺失映射及网关不可用均不交付查询结果；阻断底层业务直连，不自动重试 SQL。
5. GitHub 与官网移除微信群二维码，保留作者个人微信和微信公众号二维码。

6. 新增统一 AgentSQL Logo：安全盾牌、数据库与 Agent 连接点，官网、GitHub README 和 Web 共用 SVG 母版。

## 兼容和升级

元数据合库 schema 23、分库 metadata schema 22、audit schema 6 保持不变。升级前备份配置、密钥和状态；从上一正式版升级及恢复的实际证据见本版验收。数据库支持范围沿用原版本证据，不因文档和客户端接入扩大。

SQLBot 首期验证 PostgreSQL 和指定简单标识符；部署须使用专用 Agent Key、服务端静态映射和有效 HTTPS。SQLBot 自身元数据存储不经过业务适配器。模型生成、全部自然语言场景和全部图表类型不包含在执行层验收中。

见 [SQLBot 接入指南](SQLBOT.md)、[当前运维手册](OPERATIONS.md)、[验收记录](v3.0.2-acceptance.md)。
