# Batch 82b 重试（batch824）执行报告

- 执行日期：2026-10-09（Asia/Shanghai）
- 源码 HEAD：`7e587d0bfbe85f7b9b84cd2f3175205ea06a0f36`
- 构建器：`agentsql-multiarch`
- 结论：三个 quickstart 数据库依赖镜像已推送；远端三个 tag 均经 `docker buildx imagetools inspect` 确认包含 `linux/amd64`。脚本可见性逻辑已修正。未执行 compose、匿名拉取或 PromoteLatest。

## 远端镜像核验

| Tag | 远端 digest | 运行平台 | 核验时间（Asia/Shanghai） |
| --- | --- | --- | --- |
| `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5` | `sha256:42108751cfac56554dd720d6220d4679200c68bbca42101c90d54e94fce0e4b3` | `linux/amd64` | `2026-10-09T23:10:04.3864542+08:00` |
| `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5-quickstart` | `sha256:6d8a008eab8e48854c2ba43a6409dcba071d9cb1af3f2ff6abcd94391f24ff24` | `linux/amd64` | `2026-10-09T23:10:12.7276363+08:00` |
| `ghcr.io/cuipengdba/agentsql-mysql-demo:8` | `sha256:c760e1f0c6f6ffdf4164695588d44dc9da271a7f4212893019dc69124db1ea0b` | `linux/amd64` | `2026-10-09T23:10:16.5419828+08:00` |

`imagetools inspect` 还显示 `unknown/unknown` 的 attestation 描述符；上述运行平台均为 `linux/amd64`。详细构建与远端 inspect 结果已追加到 `dist/release-b82b/build-binder-base.log`、`build-binder-quickstart.log`、`build-mysql.log`，汇总见 `dist/release-b82b/verify.log`。

基础 binder 使用首次批次构建的 `dist/binder/deb/pg16/amd64/agentsql-binder-pg16_0.5.0-1_amd64.deb`。首次批次的 `dpkg-deb --field` 证据确认 Package `agentsql-binder-pg16`、Version `0.5.0-1`、Architecture `amd64`，见 `dist/release-b82b/build-packages.log`。本次推送时为让该 deb 进入构建上下文，临时创建了 `dbext/packaging/images/Dockerfile.dockerignore`，构建结束后已删除。

## 可见性与脚本

只读 `gh api --method GET ... --jq .visibility` 核验结果：

| 包 | 可见性 | 核验时间（Asia/Shanghai） |
| --- | --- | --- |
| `agentsql-postgres-binder` | `public` | `2026-10-09T23:10:19.0865377+08:00` |
| `agentsql-mysql-demo` | `private` | `2026-10-09T23:10:20.3642497+08:00` |

`scripts/build-ghcr-multiarch.ps1` 已删除不可用的 `PATCH /user/packages/container/agentsql -f visibility=public` 及推送成功后设置可见性失败即 throw 的逻辑。现在推送后仅以 GET 查询主包可见性：返回 `public` 时打印 `PACKAGE_VISIBILITY_PUBLIC`；返回 `private`、GET 失败或缺少 `gh` 时打印 `VISIBILITY_OWNER_ACTION_REQUIRED=web Package settings Danger Zone Change visibility`，不否定已成功的推送。顶部注释说明可见性须由包所有者在网页设置，不能通过 REST API 修改。PowerShell 解析与 `git diff --check` 已通过；未运行此脚本，因运行它会触及本批禁止的主镜像。

## 首次尝试与遗留

首次 b82b 使用 `agentsql-binder-builder` 推送基础 binder 时，GHCR 匿名 token 请求返回 403，故停止；其本地 digest 不是远端发布证据。重试使用 `agentsql-multiarch`，三个推送与远端核验均成功。首次失败及本次重试均保留在同一组日志中。

1. 包所有者须在网页将 `agentsql-mysql-demo` 设为 public：<https://github.com/users/cuipengdba/packages/container/agentsql-mysql-demo/settings>（Package settings → Danger Zone → Change visibility）。本批未改变该包可见性。
2. 下一批 b82c 在 MySQL 包公开后执行匿名 quickstart 验证与 PromoteLatest。

本批未触碰主镜像 `agentsql`，未创建 Release、部署官网、运行 compose、执行 PromoteLatest 或进行 git 写操作。脚本未提交，报告保持未跟踪。
