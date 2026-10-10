# Batch 82c 验收报告

状态：**VERIFIED — latest 已指向 v0.5.0**。以下时间均为 2026-10-10（UTC+08:00）。

## 前置检查与匿名拉取

- `gh api --method GET /user/packages/container/agentsql-mysql-demo --jq .visibility` 返回 `public`。
- 已执行 `docker logout ghcr.io`，在新建且无凭据的 `DOCKER_CONFIG` 下拉取 quickstart 的三个镜像。

| 镜像 | 匿名拉取完成时间 | 结果 |
| --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql:v0.5.0-quickstart` | 08:25:42 | PASS |
| `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5-quickstart` | 08:25:58 | PASS |
| `ghcr.io/cuipengdba/agentsql-mysql-demo:8` | 08:27:11 | PASS |
| `ghcr.io/cuipengdba/agentsql:latest` | 08:42:39 | PASS（本次在空 `DOCKER_CONFIG` 下拉取） |

## 匿名 quickstart

`docker-compose -p agentsql-b82c -f deploy/quickstart/docker-compose.yml up -d` 首次于 08:28:17 返回失败；当时数据库仍在初始化。PostgreSQL 和 MySQL 转为 healthy 后，同一项目在 08:29:53 重试成功，仍在首次启动后的五分钟窗口内。seed 以退出码 0 完成，gateway 为 healthy。

| 断言 | 观测值 | 结果 |
| --- | --- | --- |
| `/healthz` status | `ok` | `QUICKSTART_HEALTHZ_PASS` |
| `/readyz` status | `ready` | `QUICKSTART_READYZ_PASS` |
| `/healthz` version | `v0.5.0` | `QUICKSTART_VERSION_PASS` |

断言完成时间：08:29:59。08:30:22 执行 `down -v` 后，`agentsql-b82c` 的容器、网络和卷计数均为 0；临时匿名配置已删除。

## PromoteLatest 与匿名 latest 核验

| 标签 | 提升前顶层 digest | 本次提升后顶层 digest |
| --- | --- | --- |
| `latest` | `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` |
| `v0.5.0` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` |

首次于 08:33:07 在无凭据的临时 `DOCKER_CONFIG` 下运行 `scripts/build-ghcr-multiarch.ps1 -PromoteLatest -Version v0.5.0`。`imagetools create` 会写入 GHCR，不能匿名执行；GHCR 于 08:33:26 拒绝写入，脚本退出码为 1。08:33:51 再次检查时两个 digest 均未变化。

本次先用 `gh auth token` 经标准输入完成默认 Docker 配置的 GHCR 登录，再于 08:40:38 用默认配置运行 `powershell -ExecutionPolicy Bypass -File ./scripts/build-ghcr-multiarch.ps1 -PromoteLatest -Version v0.5.0`。脚本于 08:41:00 成功退出；08:41:11 只读 `imagetools inspect` 复核 latest 与 v0.5.0 的顶层 digest 相等，记为 `LATEST_PROMOTED`。

任务文字给出的预期 digest 在 `bf41` 后多了一个 `1`，总计 65 位十六进制字符。上表采用 GHCR 实际返回且格式有效的 64 位 digest，该值也与首次报告记录的 v0.5.0 digest 一致。

08:42:28 对默认配置执行 `docker logout ghcr.io`。随后新建空 `DOCKER_CONFIG`，于 08:42:39 匿名拉取 `latest` 成功，拉取结果的 digest 与上表一致，记为 `LATEST_ANON_PULL_PASS`。08:42:43 运行该镜像的 `--version` 输出 `v0.5.0`，记为 `LATEST_VERSION_PASS`；临时匿名配置已删除。

结论：`QUICKSTART_ALL_PASS`、`LATEST_PROMOTED`、`LATEST_ANON_PULL_PASS`、`LATEST_VERSION_PASS`。

完整时间戳证据见 `dist/release-b82c/` 下的 `anonymous-pull.log`、`quickstart-verify.log`、`cleanup.log`、`promote-latest.log`、`latest-verify.log`、`promote-latest-2.log` 和 `latest-verify-2.log`。
