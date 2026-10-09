# Batch 82：GHCR v0.5.0 精确 tag 推送与发布闸门报告

**结论：PARTIAL / FAIL-CLOSED。** 记录时间为 2026-10-09 Asia/Shanghai。三个精确 tag 已推送、双架构 OCI 索引与匿名 pull 均通过，主镜像及 quickstart 镜像的单容器验证通过。`deploy/quickstart/docker-compose.yml` 所需数据库镜像未满足匿名拉取条件，seed → gateway 链路未完成；因此未执行 `PromoteLatest`，`latest` 未移动。Release 仍为 draft；未部署官网、未宣传。

## 前置条件与构建

- batch81 报告为 `VERIFIED`；远端 annotated tag `v0.5.0` 存在，Release 为 draft 且有 15 个资产。`gh auth status` 为 `cuipengdba`，含 `write:packages`。Docker daemon 与 buildx 可用，构建器列出 `linux/amd64` 和 `linux/arm64`。
- 推送前 `gh api --paginate /users/cuipengdba/packages/container/agentsql/versions` 中不存在 `v0.5.0`、`v0.5.0-demo` 或 `v0.5.0-quickstart`；`latest` 为 `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e`。
- 首次构建在导出或推送前中止：仓库 `.dockerignore` 未排除受保护的未跟踪文件。随后从已跟踪的 HEAD `0a1930472ec32cd0510fb41a8844ee00115ef1a9` 导出临时源码副本执行同一构建脚本；副本不含两个受保护路径。该 HEAD 与发布 tag 的差异仅为 `docs/release-b81-report.md`。临时副本已删除。
- `scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags -Push` 推送了三个 tag，并写出 `dist/release-prepare/v0.5.0-final/image-digests-pushed.json`，其中顶层 `pushed=true`、记录数为 3。脚本最后执行 `PATCH /user/packages/container/agentsql` 请求设置 public 时返回 HTTP 404，故脚本退出码为 1；独立 GET 随后确认该包实际为 public。

| tag | 顶层 digest | 远端 OCI manifest 的平台描述符 |
| --- | --- | --- |
| `v0.5.0` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` | `linux/amd64`, `linux/arm64` |
| `v0.5.0-demo` | `sha256:645d0ef0a60c16873bc8eea0ea563f5a662968963bd02fad7be96024efe7e204` | `linux/amd64`, `linux/arm64` |
| `v0.5.0-quickstart` | `sha256:bb432a0cff7fcc0fe8c8106c36fe4b852cbd7d18a6f823e859e247d7eba6508e` | `linux/amd64`, `linux/arm64` |

三个 tag 均用 `docker buildx imagetools inspect --raw` 解析 registry 返回的 OCI index，并与 pushed JSON 的 digest 逐项一致。详情见 `dist/release-b82/build-and-push.log`。

## 可见性与匿名拉取

- `gh api /user/packages/container/agentsql` 返回 `owner=cuipengdba`、`name=agentsql`、`visibility=public`。证据见 `dist/release-b82/visibility.log`。
- 已执行 `docker logout ghcr.io`，随后使用新建的空 `DOCKER_CONFIG` 目录执行下列 pull；该目录事后删除。完整带时间戳输出见 `dist/release-b82/anonymous-pull.log`。

| 平台 | tag | 完成时间（Asia/Shanghai） | 结果 |
| --- | --- | --- | --- |
| `linux/amd64` | `v0.5.0` | 21:36:00 | PASS |
| `linux/arm64` | `v0.5.0` | 21:36:56 | PASS；只拉取未运行 |
| `linux/amd64` | `v0.5.0-demo` | 21:37:11 | PASS |
| `linux/amd64` | `v0.5.0-quickstart` | 21:37:30 | PASS |

## 运行验证与 compose 阻断

- amd64 demo 镜像运行 `agentsql --version` 返回 `v0.5.0`；quickstart 镜像内 `config.demo.yaml`、`demo-seed.yaml` 存在且 `agentsql --version` 为 `v0.5.0`。
- amd64 主镜像用仅在测试进程内生成的一次性 `AGENTSQL_SECRET` 与 `AGENTSQL_ADMIN_PASSWORD` 启动：`/healthz` 为 `status=ok, version=v0.5.0`，`/readyz` 为 `status=ready`。此前缺少必需凭据的两次启动诊断与清理也记录在 `dist/release-b82/run-verification.log`；凭据值未输出或写入日志。
- quickstart compose 使用相同的空凭据 Docker 配置。`docker-compose -p agentsql-b82 -f deploy/quickstart/docker-compose.yml up -d` 在拉取 `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5-quickstart` 时返回 `not found`，因而 seed 与 gateway 未启动，健康链路未通过。GHCR API 复核表明 binder 包为 public，但只有 `16-0.4-quickstart` 和 `16-0.4` tag；所需 `16-0.5-quickstart` 不存在。另一个依赖 `agentsql-mysql-demo:8` 的 tag 虽存在，包可见性仍为 private，也不满足匿名 quickstart 条件。
- `docker compose` 子命令在空 Docker 配置下未被 CLI 加载，改用已安装的独立 `docker-compose`；首次调用未创建资源。上述 compose 失败及只读诊断见 `run-verification.log`。

## latest 闸门与清理

- PromoteLatest 前的 `latest` digest 为 `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e`；compose 闸门失败后复核仍为同一 digest。`v0.5.0` 为 `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278`。未执行 `-PromoteLatest`，也未做 latest 匿名 pull/运行验收。见 `dist/release-b82/promote-latest.log`。
- 临时主镜像容器、quickstart compose 项目的容器、网络、卷均为 0；临时源码副本与空匿名 Docker 配置目录已删除。原有其他 Docker 资源未动。
- 除本报告外没有 tracked 改动；未执行任何 git 写操作。受保护的 `cmd/agentsql/b5-wal/` 与 `cmd/agentsql/﹎memory﹎.instance-id` 未修改。日志未记录 token、私钥或一次性凭据值。

## 遗留问题

1. 需另行提供并公开 quickstart compose 所需的 `agentsql-postgres-binder:16-0.5-quickstart`，并解决 `agentsql-mysql-demo:8` 的 private 可见性，之后重新验证 seed → gateway 健康链路。此批未修改这些依赖包。
2. 构建脚本的可见性 `PATCH` 返回 404；应在后续维护中改用受支持的可见性设置流程或仅在需要时设置，并保留独立 GET 验证。
3. 上述闸门通过前，`latest` 保持 v0.4.0 digest；不得将本批视为完成 PromoteLatest。
