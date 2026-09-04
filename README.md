# AgentSQL（智盾）

AI-Native Database Security Gateway —— AI Agent 访问关系型数据库的安全网关（生产级数据库 MCP Server）。

每条 SQL 走六段流水线：Parse → Auth → Guard → Decide → Execute → Redact/Audit。

## 开发须知（给 Codex / 贡献者）

- **唯一技术权威是 [`docs/SPEC.md`](docs/SPEC.md)**，开工前必须完整阅读，契约（第 3 章）冻结，不得擅改。
- 一次只实现一个任务单（T01→T25），严格按《逐次投喂与验收手册》的顺序。
- 技术栈、目录结构、安全铁律（fail-closed、审计先于响应、deny 不触达真实库）以 SPEC 为准。

## 快速开始（开发中）

```bash
cp examples/config.example.yaml config.yaml
export AGENTSQL_SECRET="<32字节随机字符串>"
make build
```
