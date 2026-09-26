# B5 v1 独立评审（fresh Codex，2026-09-25）

总评：GO-WITH-REQUIRED-CHANGES，仅允许进入新增 S0 真库/真协议实证阶段。P0 未关闭前不得冻结 S1 合同、开启事务 feature flag 或宣称 B5 已实现。

## P0
1. 审批摘要循环依赖（设计 5.1/5.5/9.4/10.3）：plan_digest 含 approval_id，approval 又先绑 plan_digest。拆为稳定 plan_digest（不含 approval id）+ begin_authorization_digest。
2. session handle 劫持与多实例路由未闭合（0.9-10/4.2-3/8/12）：同 API key 他客户端拿到 session id 即可操作；随机 id 无法判 owner。handle 当敏感 capability 或加 continuation secret/HMAC proof；定义可实施粘性路由/共享目录；未认证/owner 不匹配不得触发受害事务回滚。
3. 终结接口不能表达确定失败/outcome unknown（5.4/7/9.3/S3）：Commit/Rollback 返回 error 无法区分 committed/not_committed/unknown 与连接 release/discard。返回 typed terminal outcome，禁按错误文本推断。
4. MySQL 有界 commit/rollback 是纸面机制：mysqlWriteTx 调无 context 的 sql.Tx.Commit/Rollback，终结抽象未保留可强制销毁的物理连接。真库证明 watchdog/网络 deadline/物理 discard 前 b5_tx_mysql 必须 off。
5. MCP 2025-06-18 取消与版本拒绝假设不成立（2.1/4.3-4/7.3/S6）：go-sdk v1.7.0 PropagateRequestCancellation 仅 >=2026-07-28 生效；2025-06-18 HTTP 断连不能靠它回滚；SDK 对未知 legacy 默认协商到 2025-11-25 而非拒绝。需独立 watchdog + 产品级协议 allowlist。
6. 审计耐久承诺自相矛盾（0.5/0.8/3.6-7/5.3-4/10）：audit outage 触发 rollback 时无法保证 rollback event 同 store 耐久；business commit 成功/outcome audit 失败无耐久终态。明确 best-effort 边界或独立 emergency WAL；区分 committed/audit_pending 与真 DB outcome unknown；补 reconciliation event、锁图、等待预算。
7. 资源限额可被多实例与 tombstone churn 绕过且缺 S0（9/11/14/15）：进程内 quota 可多实例扇出绕过；关 session 可造无界 15min tombstone；1MiB plan/幂等结果无 retained-byte 总预算。补集群级 admission/一致性路由、DB 总连接预算、tombstone/plan byte budget。PG binder、MySQL MDL/终结、MCP 取消、B6 锁序须 S1 冻结前先 S0 实证。

## 已核验事实
HTTP stateless；现有 SessionID 仅新建连接不可 resume；PG binder SELECT-only；B2 列授权仅 SELECT；MySQL multiStatements=false。
发布结论：MySQL DML transaction 在 capability 未真库证明前严格 feature-off，不得降级为 AST-only/表级授权。