# Batch 83b（batch831）：v0.5.0 正式发布与发布后烟测

日期：2026-10-10（Asia/Shanghai）。最终烟测记录：`dist/release-b83b/post-release-smoke.log`；首次脚本错误的记录保留在 `dist/release-b83b/post-release-smoke-attempt1.log`。

## Release 转正式

- 发布前只读核对：`gh release view v0.5.0 --json isDraft,isPrerelease,tagName -q .` 返回 `isDraft=true`、`isPrerelease=false`、`tagName=v0.5.0`。
- 唯一远端写操作：`gh release edit v0.5.0 --draft=false --latest`，于北京时间 09:35:46 开始、09:35:54 返回成功。GitHub 返回正式入口：<https://github.com/cuipengdba/agentsql/releases/tag/v0.5.0>。
- `gh release view v0.5.0 --json isDraft,isPrerelease,tagName,publishedAt,url -q .` 复核：`isDraft=false`、`isPrerelease=false`、`tagName=v0.5.0`、`publishedAt=2026-10-10T01:35:51Z`。一次包含 `isLatest` 的 CLI 查询被拒绝（该字段无效）；改用匿名 GitHub `/releases/latest` API，返回 `tag_name=v0.5.0`、`draft=false`、`prerelease=false`。**RELEASE_PUBLISHED**。

## 发布后烟测

最终完整重跑按请求顺序执行，日志结尾为 **`SMOKE_SUMMARY PASS=9 FAIL=0`**，达到 ≥8 PASS、0 FAIL 闸门。

| 项目 | 结果 | 观测 |
| --- | --- | --- |
| RELEASE_PAGE | PASS | 无认证访问正式 Release 页 HTTP 200；公开 API 为 v0.5.0、非 draft、非 prerelease。 |
| RELEASE_ASSETS | PASS | Release 恰好 15 项，名称与 `dist/release-prepare/v0.5.0-final/assets/` 权威目录一致。 |
| GHCR_LATEST | PASS | `docker buildx imagetools inspect` 顶层摘要为 `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278`。任务文字中的 `...bf41f1ad...` 多一个十六进制字符；比较采用 batch82c 已验收的有效 64 位摘要。 |
| ANON_PULL_LATEST | PASS | 使用新建空 `DOCKER_CONFIG` 执行 `docker pull ghcr.io/cuipengdba/agentsql:latest` 成功，临时配置已清理。 |
| DEMO_HEALTHZ | PASS | `/healthz` 的 `status=ok`、`version=v0.5.0`。未输出响应中的演示凭据。 |
| DEMO_READYZ | PASS | `/readyz` 的 `status=ready`。 |
| SITE_HOME | PASS | HTTPS 首页 GET 返回 HTTP 200。 |
| SITE_PDFS | PASS | 三份 `agentsql-*-v0.5.0.pdf` 均返回 HTTP 200、`application/pdf`。 |
| DEMO_LOGIN | PASS | 使用 Demo `/healthz` 提供的演示账号信息登录，返回成功及会话令牌；未打印或记录密码、令牌。 |
| SIGNATURE（可选） | 引用既有结论 | batch81 已对回下载的 15 项逐一比对，并验证 SBOM、provenance、SHA256SUMS 三份签名；本轮未重跑。 |

首次尝试在 SITE_HOME 项因烟测脚本误将 PowerShell 只读自动变量 `$HOME` 用作响应变量而停止，留下 `PASS=6 FAIL=1` 的原始日志。随后只针对官网诊断，HEAD 与 GET 均为 HTTP 200；修正变量名并从第一项重新执行，获得上表 9 项 PASS。首次记录未删除，也未并入最终重跑的计数。

## 结论与入口

`v0.5.0` 已正式发布并成为 GitHub 最新 Release；修正烟测脚本后完整重跑获得 9 PASS、0 FAIL，发布后闸门通过。

- Release：<https://github.com/cuipengdba/agentsql/releases/tag/v0.5.0>
- GHCR latest：`ghcr.io/cuipengdba/agentsql:latest`
- 官网：<https://agentsql.cn>
- Live Demo：<https://demo.agentsql.cn>

未执行 git 写操作或提交，未触碰受保护的两处未跟踪路径。
