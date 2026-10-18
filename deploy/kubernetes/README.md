# AgentSQL v2.1.0 双网关 Kubernetes 部署

此清单只针对**两个节点、固定两个副本、无状态 MCP 只读请求**。运行配置把 HTTP 状态会话、事件续传和 B5 事务关闭；这不是已有单节点 B5 能力的迁移方案。双网关拓扑以[v2.0.0 设计与验收](../../docs/v2.0.0-design.md)为基线，v2.1.0 的数据库支持边界见[本版发布说明](../../docs/release-notes-v2.1.0.md)。

## 准备

- 两个可调度 Kubernetes 节点，支持每副本独立 `ReadWriteOnce` PVC 的 StorageClass。
- 已配置 TLS、备份和最小权限运行账号的 PostgreSQL metadata 库及独立 audit 库。不要使用 SQLite。两个 DSN 均须指向同一套经确认的控制面数据，运行账号不得有 DDL 权限。
- 私有环境文件分别供 `agentsql-runtime` 与 `agentsql-migration` Secret 创建。文件不进入 Git；迁移文件使用具备 DDL 权限的双库账号，运行文件使用最小权限账号。两者必须使用同一**恰好 32 字节**的 `AGENTSQL_SECRET`，并提供 `AGENTSQL_ADMIN_PASSWORD`、`AGENTSQL_STORE_METADATA_DSN`、`AGENTSQL_STORE_AUDIT_DSN`。凭据只通过 Secret 注入。
- 迁移前对 metadata 与 audit 做成套备份，并记录回滚点。新部署不要与旧版同时写入同一控制面，除非混合版本验收已通过。

以下命令假设当前目录是仓库根目录，命名空间为 `agentsql`。环境文件路径由运维指定，不在命令行包含凭据内容。

```sh
kubectl create namespace agentsql
kubectl -n agentsql create secret generic agentsql-runtime --from-env-file=/secure/agentsql-runtime.env
kubectl -n agentsql create secret generic agentsql-migration --from-env-file=/secure/agentsql-migration.env
kubectl -n agentsql apply -f deploy/kubernetes/configmap.yaml
```

## 首次安装或从 v2.0.0 升级

先对两个 PostgreSQL 库做成套备份并记录旧镜像摘要。`migrate` 可能执行 DDL；即使本版 schema 未变，也要记录检查结果。Job 不在基础 `kustomization.yaml` 中，避免普通部署隐式迁移。**B2 对活跃网关的二进制摘要要求一致，v2.0.0 与 v2.1.0 不能混合提供服务；此部署使用 `OnDelete`，版本切换必须安排维护窗口。**

从 v2.0.0 升级时，先执行下列维护窗口操作；首次安装跳过这一段。

```sh
kubectl -n agentsql scale statefulset/agentsql --replicas=0
kubectl -n agentsql wait --for=delete pod/agentsql-0 pod/agentsql-1 --timeout=180s
# 等待旧运行时租约到期；默认租约 15 秒，实际以配置值为准，留出时钟偏差。
sleep 30
```

旧网关完全停止后执行迁移和只读 schema 检查。首次安装也从此处开始。

```sh
kubectl -n agentsql apply -f deploy/kubernetes/jobs/migrate.yaml
kubectl -n agentsql wait --for=condition=complete job/agentsql-migrate-v2-1-0 --timeout=300s
kubectl -n agentsql logs job/agentsql-migrate-v2-1-0
kubectl -n agentsql apply -f deploy/kubernetes/jobs/schema-check.yaml
kubectl -n agentsql wait --for=condition=complete job/agentsql-schema-check-v2-1-0 --timeout=300s
kubectl -n agentsql logs job/agentsql-schema-check-v2-1-0
bash deploy/kubernetes/preflight.sh agentsql
```

检查通过后应用并验收双副本。

```sh
kubectl -n agentsql apply -k deploy/kubernetes
kubectl -n agentsql wait --for=create pod/agentsql-0 pod/agentsql-1 --timeout=180s
kubectl -n agentsql wait --for=condition=Ready pod/agentsql-0 pod/agentsql-1 --timeout=600s
```

`migrate --check` 必须对 metadata 和 audit 均显示 `status=current`，退出码为 0。缺失版本表、落后或超前版本时不得启动网关。两个副本的 initContainer 会再次只读检查。就绪探针只把可用副本加入 Service；存活探针只判断进程存活。

新建 PostgreSQL 列级策略时，先创建同对象的表级允许策略，再创建 `object_type: column`、`action: allow`、明确 `columns` 清单的列策略。读取该列策略的 `ETag`，以 `If-Match` 调用 `POST /api/v1/policies/{id}/bind-postgres`，请求体为 `{"sql":"SELECT id,name,phone FROM public.customers"}`。登记 SQL 只用于目录证明，不执行；本版只接受 PostgreSQL `CATALOG_CLOSED_V1` 的单张普通表、明确列投影，且所有引用列必须在策略清单中。返回的新策略修订和正式绑定须经两节点 MCP 允许/拒绝复验。原生扩展模式、视图、跨表或通配投影的管理端登记仍不开放；失败时不会把未绑定列策略当作表授权执行。

部署验收应分别读取 `agentsql-0` 和 `agentsql-1` 的 `/healthz`、`/readyz`，确认不同 B2 实例 ID；通过 Service 各做允许、拒绝、脱敏及持久审计测试。再停止一个 Pod，确认另一 Pod 继续处理、丢失节点上的活跃请求按失败处理且不会静默续接。审计或 metadata 故障时请求须关闭，恢复后再复测。不要把 HTTP 200 作为完整安全验收。

如果 metadata 中断超过 B2 租约，现存进程会保持 `B2_RUNTIME_HEARTBEAT_FAILED` 或 `B2_RUNTIME_LEASE_EXPIRED` 降级；恢复数据库后按维护流程重建两个 Pod，并再次确认两者 `B2_READY`、允许/拒绝/脱敏和持久审计。仅数据库重新连通不能视为能力恢复。

```sh
kubectl -n agentsql get pods -l app.kubernetes.io/name=agentsql -o wide
kubectl -n agentsql get endpointslice -l kubernetes.io/service-name=agentsql
kubectl -n agentsql port-forward pod/agentsql-0 17780:7780
```

两个副本各有独立 1 GiB PVC，用于持久 B2 实例标识。不得把同一 PVC 挂载给两个副本，不得在普通升级时删除 PVC。固定两副本每节点 Agent QPS 上限为 10，集群理论上界为 20；单节点故障时上界为 10，分布不均可能更早限流。改变副本数和预算前必须重新设计并验证。

## 变更、回滚和凭据

升级前运行只读 schema Job、成套备份、记录当前镜像摘要和配置。明确停止两个旧 Pod，等旧租约过期，再应用新镜像并启动两个新 Pod；迁移或版本偏差失败时停止发布，检查日志与数据库快照。代码回滚同样须先停止两个新版 Pod、等待租约过期，再使用旧版正式镜像和经过隔离验证的双库备份；若 schema 已变化，不能仅回退镜像。管理员入口应由外层 TLS/访问控制保护；本清单不创建公网 Ingress。

迁移完成后删除迁移 Job 与 `agentsql-migration` Secret。运行 Secret 轮换需在维护窗口先停止双副本，验证新密钥与现存加密数据和 B2 租约的兼容性，再同时启动新副本；不能直接更换 `AGENTSQL_SECRET` 而不迁移或验证加密数据。所有日志和验收报告仅记录版本、退出码、非敏感实例 ID 和审计 ID，不记录 DSN、密码或 Agent Key。
