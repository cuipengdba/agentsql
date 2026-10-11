# post4b README.en.md 发布后措辞报告

核对日期：2026-10-12。按本批任务给定事实，v0.5.0 已于 2026-10-10 发布。本批修改的既有文件仅为根目录 `README.en.md`；本报告为新增交付文件。

## 改动摘要

- 将 v0.5.0 徽标由待发布改为 `Release v0.5.0`。
- 将开篇计划发布日期、旧版公开 Release 和待发布代码表述改为已发布口径；保留 v0.4.0 发布物原许可的事实。
- 将兼容矩阵引言中的 `pending v0.5.0 code` 改为 `v0.5.0 code`，不改数据库数量或兼容性边界。
- 将 GHCR 小标题及镜像说明改为发布后时态，保留镜像 tag 和双架构事实。

## 保留项核对

- `pending validation` 是等待厂商环境的验证状态；`session/planned-transaction` 是功能名称，均保留。
- `unpublished local dry-run artifact` 描述 YashanDB 测试证据，`not yet translated` 描述文档覆盖情况，均保留。
- v0.4.0 及更早源码、标签、发布物保留原许可；v0.5.0 起采用 Apache-2.0 的许可声明未改。
- 审批单状态、数据库验证边界、技术事实，以及任务列出的 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 均未修改。

## 检查与遗留问题

- 精确检索确认 `README.md`、`docs/en/README.md`、`README.en.md` 均无旧版 `Next release: v0.5.0 pending` 徽标；`README.en.md` 无本批识别出的其他发布前状态措辞。
- 扩大检索范围后，在 `dist/release-b81/validation-source/README.md:3` 和 `dist/release-b81/validation-source/README.en.md:5` 发现旧徽标。它们是本批禁止修改的历史快照，因此“全仓不再出现”这一验收项未通过。扩展扫描在发现这两处后停止，不宣称仅有这两处。
- 按任务书“禁 git”要求，未运行 `git diff` 或 `git diff --check`。仅检查了 `README.en.md` 无行尾空白；无法据此宣称 `git diff --check` 通过。
