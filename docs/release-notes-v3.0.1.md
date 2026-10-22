# AgentSQL v3.0.1 发布说明

**语言 / Language:** [简体中文](release-notes-v3.0.1.md) | [English](en/release-notes-v3.0.1.md)

状态：已发布（2026-10-22）。[GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.0.1)、[官网](https://agentsql.cn/)和[Live Demo](https://demo.agentsql.cn/)已更新。验收细节见[发布记录](v3.0.1-release-evidence.md)。

## 本版变化

- 官网新增中文和英文页面及语言切换，Live Demo 安全入口有对应英文版。
- GitHub 仓库保留完整中文 README，英文 README 更新到当前能力范围，两份互相链接。GitHub 网站自身的界面语言由 GitHub 用户设置。
- Web 控制台可在登录页和登录后切换中英文；浏览器保存选择，英文官网的 Demo 深链可直接打开英文控制台。现有页面静态文案、Ant Design 控件及日期数字格式跟随语言；SQL 和结果数据保持原文。
- 左侧导航拥有独立滚动区域，解决短有效视口下底部选项不可达的问题。

## 升级和边界

本版不改变后端 API、MCP 协议、数据库适配、权限、执行、脱敏、审计或 schema。v3.0.0 的兼容范围与各数据库原版证据仍适用。安装前按[部署指南](DEPLOY.md)备份控制库及密钥并验证恢复。英文详细手册及 PDF 尚未提供，中文版可继续使用。现有页面由受检静态文案目录翻译，少量动态业务数据若与目录键完全相同可能发生文案匹配；查询结果单元格已排除。验收状态见[验收记录](v3.0.1-acceptance.md)。
