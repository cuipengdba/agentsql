# Easy-deploy S6 differential / coverage report

状态：feature-off 质量证据；不接线生产 handler，不激活 flag，不作为 S7 发布签收。  
日期：2026-09-26（Asia/Shanghai）  
基线：`feature/v0.4`, `HEAD=577d26a`

## 1. 口径与语料

语料源为 `internal/authorizedexecute/internal/businessdb/testdata/easy_deploy_s6_customer_corpus.json`。每条同时记录：

- `syntax_expectation`：无连接 raw parser 的分类；
- `expectation`：叠加 catalog shape、implicit-object 和 B2/B5 产品边界后的最终分类；
- `native_supported`：当前 `NATIVE_C_V1` 实现实际可覆盖的声明；
- `eligible_direct_crud`：第 5.1 节 `<5%` 误拒目标的分母；
- `gate`：拒绝发生在 grammar、catalog、native shape 还是既有产品边界。

这一区分避免把 view、trigger/default/RLS、RETURNING 等“raw syntax 可解析但最终必须拒绝”的样本误报为 closed coverage，也避免把安全边界包装成 parser 误拒。

| 层级 | 条数 | closed 实测覆盖 | native 实测覆盖 | 说明 |
|---|---:|---:|---:|---|
| direct CRUD | 24 | 23/24 (95.83%) | 23/24 (95.83%) | 单行 INSERT VALUES、简单 UPDATE/DELETE、点查/闭合 JOIN/子查询；multi-row VALUES 当前双方拒绝 |
| reporting | 12 | 4/12 (33.33%) | 12/12 (100.00%) | closed 仅签收 equijoin/EXISTS/IN slice；aggregate/group/sort/distinct/derived/cast/CASE 走 native |
| view / CTE / LATERAL | 10 | 0/10 (0.00%) | 5/10 (50.00%) | native 覆盖普通/嵌套 view 与 LATERAL；3 条 CTE、recursive、matview 保持拒绝 |
| DML product boundary | 14 | 5/14 (35.71%) | 5/14 (35.71%) | simple DML 可用；INSERT SELECT/upsert/RETURNING/UPDATE FROM/DELETE USING/DML CTE/MERGE 不计覆盖 |
| negative escape | 14 | 0/14 | 0/14 | stacked/DDL/PREPARE/EXECUTE/transaction/role/implicit closure/operator/parameter/whole-row 均应拒绝 |
| **总计** | **74** | **32/74 (43.24%)** | **45/74 (60.81%)** | 分母包含 14 条必须拒绝的负向安全语料 |

去掉 14 条 negative escape 后，客户工作负载形态分母为 60：closed 为 32/60（53.33%），native 为 45/60（75.00%）。这个数字仍包含 complex DML、recursive、matview 等明确产品边界，不能宣传为一般 SQL 成功率。

eligible direct CRUD 误拒为 **1/24（4.17%）**，低于设计目标 `<5%`。唯一误拒是 multi-row `INSERT ... VALUES (...), (...)`；当前 C 扩展返回 SQLSTATE `0A000`，因此 closed parser 同步收窄为 `must_reject`，没有为了降低误拒放宽语义。

## 2. 真容器矩阵

门控：`AGENTSQL_EASYDEPLOY_S6_MATRIX=1`。测试文件：`easy_deploy_s6_shadow_e2e_test.go`。

| PostgreSQL | 传输 | C 扩展 | 实测 |
|---|---|---|---|
| 14 | 明文 loopback | 安装并 `CREATE EXTENSION agentsql_binder` | 全 74 条 corpus 按声明回放：closed 32/32、native 45/45；共同支持集 4 SELECT + 3 DML 双跑逐字段一致 |
| 18 | TLS (`sslmode=require`) | 不安装，仅 `CATALOG_CLOSED_V1` | `pg_stat_ssl.ssl=true`；probe 报 native absent 而非错误；native-required 新请求确定选择 closed；14 条 escape 全拒绝 |

PG14 shadow runner 的非故障样本为 7 条，共同支持集 divergence 为 **0/7**。另外注入 1 次 `ColumnUses.Name` 差异，得到预期 `AUTH_BINDER_DIVERGENCE`，native capability 变为 unhealthy；相同 attestation digest 的 probe 不自愈，随后 2 个新 request digest 均确定回落 `CATALOG_CLOSED_V1`。因此报告中的真实 divergence 数为 **0**；故障注入 divergence 为 **1（预期测试事件，不计产品差异）**。

两格容器各运行 14 条 negative escape，共 28 次真实 closed bind/selector gate 检查，无绕过。

## 3. 差异处置

### 已安全修正

1. **multi-row INSERT VALUES 的共同支持集不对称**：原 closed parser 接受多行 VALUES，但 PG14 真 C binder 返回 `0A000`。为满足“closed allow 不多于 native allow”，将多行 VALUES 分类为 raw `unsupported` / final `must_reject`；单行 VALUES 保持原能力。同步增加 parser fail-closed 回归。
2. **DML shadow 测试适配**：DML 使用自身 B5 attestation envelope，不再错误套用 SELECT 专用 `NativeBoundProgram` attestation；shadow 仅比较统一的 `SemanticFacts`，mode-private evidence 不进入差异。

本轮没有发现可安全扩大 allow-set 的 parser bug；没有修改 selector/shadow 核心逻辑。

### 保持 fail-closed

- current native C raw-shape gate 对 3 条非递归 CTE 返回 `0A000`。虽然设计将 CTE列为“需 native”，当前实现不能计入覆盖；保留 `native_required` raw 分类和最终拒绝，待独立 native capability 修复。
- closed 对 ordinary view 为 0/10；native 仅覆盖 ordinary/nested view 与 LATERAL（5/10）。不把 view lineage 猜测、plan/deparse 或表级退化计作支持。
- recursive query、materialized view（S3m）、whole-row/system/composite、unbound parameter、explicit `OPERATOR(...)`、trigger/default/RLS 等 implicit closure 保持拒绝。
- B5 RETURNING、upsert、MERGE、INSERT SELECT、UPDATE FROM、DELETE USING、DML CTE 保持既有产品边界；native 能分析不等于产品可授权。

## 4. 结论与发布边界

- S6 corpus 目标满足：eligible direct CRUD 误拒 4.17%，共同支持集真实 divergence 0，closed 未多于 native 放行。
- reporting 的 closed 覆盖只有 33.33%；view/CTE/LATERAL 层 closed 为 0%，native 也只有 50%。这些是当前实现数据，不得外推为开放 SQL 支持。
- 本证据不激活任何 flag，也未接线 MCP/HTTP handler。S7 故障/发布门、S3m matview、MySQL inspector、B2/B5 GA owner 签收仍是发布阻塞项。
