# 金仓 + 瀚高联合案例社区运营口径

> 适用渠道：公众号、技术群、官网 / GitHub 更新说明。
>
> 发布前提：事实状态必须与 [联合案例验证记录](joint-case-kingbase-highgo.md) 一致；未经厂商书面确认，不使用暗示背书、认证或正式合作完成的措辞。

## 1. 必须保持一致的事实

- 首批联合案例验证对象为两家：**中电科金仓 KingbaseES + 瀚高 HighGo**。
- IvorySQL 是瀚高社区产品线，可作为同源社区版工程参考，但不能替代 HighGo 商业版终验，也不另计为首批第三家厂商。
- 当前瀚高结论只覆盖指定第三方 SEE 镜像、合成数据与列出的 PostgreSQL 协议路径用例；不是厂商认证。
- 当前金仓第三方旧镜像因 license 过期未能启动数据库。V9R1C10 目标镜像待厂商提供；连接、发现、查询和安全闭环均不能写成通过。
- 生态矩阵共 11 项：KingbaseES、HighGo、IvorySQL、openGauss、TiDB、OceanBase、TDSQL、OpenTenBase、YashanDB、达梦 DM、PolarDB。矩阵表示实测、适配中或待验证状态，不表示 11 家均已支持。
- 自 v0.5 起，对外许可口径为“Apache License 2.0 开源许可 + 独立商业许可说明”。Apache-2.0 的权利与义务以 `LICENSE` 为准；商标许可、企业能力、SLA 等以 `COMMERCIAL-LICENSE.md` 为准。双许可说明不是数据库厂商授权或背书。

## 2. 用词规范

| 可以使用 | 仅在满足条件后使用 | 不应使用 |
| --- | --- | --- |
| 协议路径实测、适配验证中、待厂商环境终验、首批验证对象、工程参考 | “联合发布”“联合实践”：仅在双方完成目标版本验收并书面确认后 | 官方支持、厂商认证、联合认证、全面兼容、合作完成、生产级兼容 |

对外出现“已实测”时，必须在同一段或紧邻注释中写清版本 / 镜像来源、用例范围和“非厂商认证”。不要只保留“已实测”三个字。

## 3. 公众号建议稿

### 标题

国产数据库适配验证进展：首批聚焦金仓 KingbaseES 与瀚高 HighGo

### 摘要

AgentSQL v0.5 正在推进国产数据库联合案例验证。首批聚焦中电科金仓 KingbaseES 与瀚高 HighGo，并以同属瀚高社区产品线的 IvorySQL 作为独立工程参考。我们同步公开验证矩阵、可复现 SQL 与已知边界，让每一项结论都能回到具体环境和证据。

### 正文短稿

AgentSQL 是面向 AI Agent 的数据库安全网关。国产数据库适配不应只看“能否建立连接”，还需要逐项核验列发现、SQL 解析、执行计划、规则拦截、结果脱敏、错误处理和审计闭环。

本批记录的边界很明确：指定第三方瀚高 SEE 镜像已经完成 PostgreSQL 协议路径的最小闭环实验；HighGo 商业版仍待厂商目标环境终验。金仓现有第三方旧镜像被过期 license 阻断，V9R1C10 目标镜像尚未到位，所以金仓的连接、发现和查询均保持“待厂商环境验证”，没有写成通过。

官网与 GitHub 的国产数据库生态矩阵现包含 11 项，分别标注实测、适配中或待验证。矩阵是公开路线与工程进度，不等于厂商认证或当前版本全面支持。

自 v0.5 起，项目对外采用“Apache License 2.0 开源许可 + 独立商业许可说明”的清晰口径：开源使用遵循 `LICENSE`，商标许可、企业能力与 SLA 等需求参阅商业许可说明。该许可安排与数据库厂商认证相互独立。

验证记录与三段演示 SQL：`docs/joint-case-kingbase-highgo.md`、`examples/joint-case-highgo.sql`。

## 4. 技术群短消息

AgentSQL v0.5 首批国产数据库联合案例验证聚焦中电科金仓 KingbaseES 与瀚高 HighGo（IvorySQL 作为瀚高社区产品线的独立参考）。当前指定第三方瀚高 SEE 镜像的 PostgreSQL 协议路径已完成连接、列发现、只读查询、R006 拦截、脱敏和审计的最小闭环；HighGo 商业版仍待厂商终验。金仓旧镜像因 license 过期未启动，V9R1C10 目标镜像待厂商提供，相关能力均未写成通过。生态矩阵共 11 项，状态不等于厂商认证或全面支持。验证记录和演示 SQL 已在仓库公开。

## 5. English short version

**AgentSQL v0.5 domestic-database validation update**

The first case-validation batch focuses on KingbaseES and HighGo. IvorySQL is tracked separately as a community product from the same vendor line as HighGo; its results do not replace commercial-edition validation.

For the currently available third-party HighGo SEE image, AgentSQL completed a scoped PostgreSQL-protocol-path exercise covering connectivity, column discovery, a read-only query, R006 rule enforcement, masking, and audit records. This is not vendor certification and does not establish support for the HighGo commercial edition. Commercial-edition validation remains pending a vendor-provided target environment.

The available third-party KingbaseES image is blocked by an expired license, and the target V9R1C10 image has not yet been provided. No KingbaseES connectivity, discovery, query, or security-flow result is claimed as passed.

The public ecosystem matrix currently tracks 11 database entries at different stages. A listing means tested, in progress, or pending as explicitly labeled; it does not mean universal support or vendor endorsement. Starting with v0.5, the public licensing wording is “Apache License 2.0 open-source license plus a separate commercial-license notice.” Trademark permission, enterprise features, and SLA terms are separate from the open-source license and from any database-vendor validation.

## 6. 发布前复核

- 对照主验证记录复核镜像、版本、日期、审计 ID 与“已实测 / 待厂商”状态。
- 确认没有口令、API Key、完整 DSN、真实客户数据或未脱敏截图。
- 金仓未完成目标环境验证前，保持所有能力项为“未执行 / 待厂商”。
- HighGo 商业版未完成终验前，不把 SEE / IvorySQL 结果外推。
- 若使用厂商名称、Logo、引语或联合署名，先取得相应书面许可；没有许可时仅作事实性产品名称引用。
- 发布链接指向带版本边界的验证记录，不只链接生态矩阵状态标签。
