# post4 文档发布后措辞更新报告

**语言 / Language:** [简体中文](postrelease-post4-report.md) | [English](en/postrelease-post4-report.md)

核对日期：2026-10-12。按任务给定事实，v0.5.0 已于 2026-10-10 正式发布（Release 非 draft、GHCR 全 public、官网已切 v0.5.0）。本批只调整文档中的当前发布状态和时态；未执行发布资产、镜像或官网的外部复验。

## 逐文件改动

任务正文称「12 个文件」，逐项清单实际列出以下 13 个文档；本批按逐项路径执行，另新增本报告。

| 文件 | 状态与时态改动 |
| --- | --- |
| `README.md` | 将徽标、版本概述、amd64 资产示例和精确镜像 tag 的发布前措辞改为已发布口径；兼容矩阵的「待发布代码」改为「v0.5.0 代码」。 |
| `CHANGELOG.md` | 将 v0.5.0 标题日期改为 2026-10-10，并将引言改为正式发布状态；其余正文未改。 |
| `docs/release-notes-v0.5.md` | 仅将状态头改为「已发布 / Released」。 |
| `docs/SPEC.md` | 将版本头和「当前待发布代码」改为已发布措辞。 |
| `docs/USER_GUIDE.md` | 将发布状态头改为已发布，说明安装资产和 v0.5.0 镜像可用，保留源码仓库地址。 |
| `docs/test-coverage-notes.md` | 将标题中的「待发布」改为「已发布」。 |
| `docs/en/ADMIN.md` | 将当前代码及安装资产的发布状态改为已发布；CLI wording debt 原样保留。 |
| `docs/en/COVERAGE.md` | 将 v0.5.0 源码及分发资产的发布状态改为已发布；历史许可和 CLI wording debt 原样保留。 |
| `docs/en/GETTING_STARTED.md` | 将状态头和镜像拉取句改为已发布时态。 |
| `docs/en/INTEGRATIONS.md` | 将状态头改为已发布时态。 |
| `docs/en/QUICKSTART.md` | 将状态头改为已发布、安装资产和镜像可用。 |
| `docs/en/README.md` | 将徽标文字和 URL 改为 `Release v0.5.0`。 |
| `docs/en/SECURITY.md` | 仅将兼容性证据对象从 pending v0.5.0 code 改为 released v0.5.0 code。 |

## 保留项核对

- 未修改禁止范围内的 `docs/en/AUTHENTICATION.md`、`docs/joint-case-operations.md`、`docs/known-issues-fixes.md`、`docs/RELEASE_ASSETS.md`、`docs/dm-oracle-dialect.md`、`docs/AUDIT_REPORTING.md`，也未触碰 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id`。
- `pending` 用于审批单、决策和审计状态的功能事实保留。数据库兼容性边界、未验项目、支持承诺与版本历史没有调整。
- v0.4.0 及更早发布物保留原许可、v0.5.0 起采用 Apache-2.0 的许可事实及根 README 的许可更换声明保留。
- `docs/release-notes-v0.5.md` 内「待发布日生成／重建」、旧资产状态、发布闸门和批次结论保留为当时的流程记录；`CHANGELOG.md` 引言以外的发布准备过程正文也保留。
- 英文管理和覆盖文档对 audit CLI 仍显示 “planned for v0.5” 的 wording debt 记录保留。

## 拿不准的项与处置

- `docs/en/SECURITY.md` 的支持矩阵段落仍含 “after release” 和 “currently released v0.4.x”。该段涉及安全修复支持政策，任务要求不改支持承诺，故保留原文，待单独核定措辞。
- `docs/release-notes-v0.5.md` 仍有 2026-10-16 发布闸门、官网旧状态等发布前快照。任务明确只改状态头，故未以当前结果覆盖历史过程。

## 检查与遗留问题

- `git diff` 仅涉及逐项列出的 13 个文档；新增文件仅为本报告。`git diff --check` 通过。
- `README.md` 与 `CHANGELOG.md` 中指定的计划发布、旧公开 Release 和待发布标题表述均已消除；中文状态头为已发布。
- 全仓精确检索发现 `README.en.md:5` 仍含标记 v0.5.0 为 pending 的旧版 Next release 徽标。该文件不在唯一允许修改的路径清单内，已按 fail-closed 原则保留；因此「全仓不再出现该徽标」的验收项未通过，需另批授权更新。
- 开始前已有两个未跟踪路径：`cmd/agentsql/b5-wal/` 和 `cmd/agentsql/﹎memory﹎.instance-id`。本批未触碰。
