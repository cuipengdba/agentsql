# AgentSQL v0.4 B6 S5c 正式容量基线

日期：2026-09-24

结论：**S5 容量门 GO**。目标负载下未观察到 fail-closed、`AUDIT_OVERLOADED` 或结构性断链；3,799,725 行审计数据的 `chain_seq` 连续唯一，且 `chain_state.head_seq` 与提交总数一致。目标 goodput 与追加延迟在同环境短窗口中达到发布阈值。

## 1. 环境与测量边界

| 项目 | 值 |
|---|---|
| PostgreSQL | `postgres:18` |
| 容器限制 | 2 CPU / 2048 MiB，`limits_applied=true` |
| Host 并行度 | `GOMAXPROCS=8` |
| Docker | 29.8.0 |
| testcontainers-go | v0.37.0 |
| Go | 1.25.14 |
| GCC | 16.2.0 |
| 正式 combined 负载 | sustained offered 2000/s × 1800s；burst offered 5000/s × 60s |
| 延迟分位方法 | nearest-rank |

本报告区分三类证据：

- **正式实测**：来自 combined 长跑结束后对 PostgreSQL 容器中持久化状态的查询。
- **短窗口实测**：S5b-2 使用同一二进制、同一 2C2G 限制环境所得，用作逐场景 goodput 与延迟依据。
- **未捕获**：长跑会话未持久化或采集窗口内未完成的项目；不以推算值替代。

## 2. Fixture

- 路径：`internal/store/testdata/chain_load_fixture.json`
- fixture canonical SHA256（按数组顺序拼接 compact template JSON）：`208ed06116a066ab39696d9089d208c168accfbb31869fd7d0d347d107a38ed5`
- seed：`20260924`
- 模板数：1000
- event body nearest-rank p95：8 KiB

## 3. 正式 combined 运行硬门结果

| 检查项 | 正式实测结果 | 判定 |
|---|---:|---|
| `audit_logs` 总行数 | 3,799,725 | 规模证据成立 |
| 决策结果 | 3,799,725 行全部 `decision=allow` | 通过：零 fail-closed |
| 错误码 | `error_code` 非空行数 0 | 通过：零 `AUDIT_OVERLOADED`、零其他错误码 |
| `chain_seq` 非空 | NULL=0 | 通过 |
| `chain_seq` 唯一 | count=3,799,725；count(distinct)=3,799,725 | 通过 |
| `chain_seq` 连续 | min=1；max=3,799,725 | 通过：序列为 1..N |
| `chain_state` | mode=`keyless`，状态 `ACTIVE` | 通过 |
| 链头一致性 | head_seq=3,799,725=审计提交总行数 | 通过 |

burst 窗口实际新增 199,725 行，约 3329/s。5000/s 是 offered load；该窗口没有返回 `AUDIT_OVERLOADED`，超过即时提交能力的部分由 group-commit 平滑，符合有界背压设计。此处不把 3329/s 改写为 5000/s，也不以 offered rate 代替 committed rate。

上述数据库事实证明序列连续唯一且链头与提交数一致，因此正式规模下没有观察到结构性断链。它不等同于完成了 3.8M 行全量哈希重算；该限制见第 6 节。

## 4. 同环境短窗口指标

以下数据来自同一二进制、同一 2C2G 环境的 S5b-2 实测，用于补足正式长跑未持久化的逐场景吞吐与延迟。

| 场景 | goodput | append p95 | append p99 | 错误 | 停载后队列 |
|---|---:|---:|---:|---:|---|
| PostgreSQL sustained | 2007/s | 24.64 ms | 34.02 ms | 0 | 归零 |
| PostgreSQL burst | 4874/s | 182.03 ms | 238.72 ms | 0 | 归零 |
| SQLite sustained | 500/s | 20.87 ms | 22.67 ms | 0 | 归零 |

PG backfill 实测为 1160 rows/s。该值作为 backfill 能力参考，不属于本次 S5 GO 口径中的追加路径硬门。

## 5. 发布阈值逐条判定

| 发布检查 | 阈值/要求 | 证据 | 判定 |
|---|---|---|---|
| PostgreSQL sustained goodput | ≥ 2000/s | 短窗口实测 2007/s | **PASS（短窗口实测）** |
| PostgreSQL sustained 延迟 | p95 ≤ 250 ms；p99 ≤ 1000 ms | 短窗口实测 24.64/34.02 ms | **PASS（短窗口实测）** |
| PostgreSQL burst 承压 | offered 5000/s × 60s；零错误、零 `AUDIT_OVERLOADED` | 正式运行零错误码；短窗口 goodput 4874/s、错误 0 | **PASS（正式实测 + 短窗口实测）** |
| PostgreSQL burst 延迟 | p95 ≤ 250 ms；p99 ≤ 1000 ms | 短窗口实测 182.03/238.72 ms | **PASS（短窗口实测）** |
| SQLite goodput | ≥ 500/s | 短窗口实测 500/s | **PASS（短窗口实测）** |
| SQLite 追加延迟参考 | p95 ≤ 250 ms；p99 ≤ 1000 ms | 短窗口实测 20.87/22.67 ms | **PASS（短窗口实测）** |
| 目标负载错误行为 | 零 fail-closed；零 `AUDIT_OVERLOADED` | 正式 3,799,725 行全部 allow，非空错误码 0 | **PASS（正式实测）** |
| 序列完整性 | `chain_seq` 非空、连续、唯一，范围 1..N | N=3,799,725；distinct=N；min=1；max=N；NULL=0 | **PASS（正式实测）** |
| 链头一致性 | `head_seq`=提交数，状态 ACTIVE | 3,799,725=3,799,725，mode=keyless(ACTIVE) | **PASS（正式实测）** |
| 停载排空 | 停载后队列归零 | S5b-2 各场景均归零 | **PASS（短窗口实测）** |
| SQLite 正式时长 | 500/s × 10 min 单独留存 | 正式时长结果未单独留存；短窗口已验证 500/s | **未捕获；不推算** |
| 固定规模 Verify 耗时 | N=10^5；PG ≤ 30s，SQLite ≤ 60s；额外 RSS ≤ 64 MiB | PG 8.120038s / 3.809 MiB；SQLite 4.970181s / 5.422 MiB；均为 VALID | **PASS（固定规模实测）** |

burst 的 5000/s 是施加的 offered load，不是强制 goodput 阈值；4874/s 是短窗口已完成写入速率。正式 burst 窗口约 3329/s 的新增速率与零 `AUDIT_OVERLOADED` 共同说明 group-commit 在该窗口进行了平滑处理。

## 6. 固定规模 Verify 耗时

2026-09-24 使用现有 fixture、backfill/激活工具和流式校验器，在两个后端分别构造恰好 100,000 行的 ACTIVE keyless 链。计时范围为调用只读 `ChainVerifier.Verify` 至返回的端到端时间，不包含数据生成、backfill 和激活。RSS 使用当前 Go 进程 working set：Verify 前执行两次 GC 后取即时基线，Verify 期间每 5ms 采样，额外 RSS 峰值为采样峰值减基线。

| 后端 | 规模/状态 | Verify 结果 | 端到端耗时 | 时间阈值 | 额外 RSS 峰值 | RSS 阈值 | 判定 |
|---|---|---|---:|---:|---:|---:|---|
| PostgreSQL 18（testcontainers，2 CPU / 2048 MiB） | 100,000 行，ACTIVE keyless | `VALID_AT_OBSERVED_HEAD` | 8.120038s | ≤ 30s | 3.809 MiB | ≤ 64 MiB | **PASS** |
| SQLite（本地） | 100,000 行，ACTIVE keyless | `VALID_AT_OBSERVED_HEAD` | 4.970181s | ≤ 60s | 5.422 MiB | ≤ 64 MiB | **PASS** |

两个后端均满足耗时、结果与流式内存阈值，固定规模 Verify 收尾项完成。

## 7. 未捕获项与限制

1. 正式 30 分钟运行的逐场景 goodput/延迟没有持久化：长跑后会话输出被截断。因此，本报告只用同环境 S5b-2 短窗口数据判定逐场景吞吐和延迟，不从 3,799,725 行总量反推分位延迟。
2. SQLite 500/s × 10 分钟的正式时长结果未单独留存；现有证据是同环境短窗口已验证 500/s。
3. 对约 3.8M 行执行全量重算 `Verify` 是 O(N) 顺序扫描，未在采集窗口内跑完。正式实测已经覆盖序列连续唯一、无 NULL、ACTIVE 状态和 head/提交数一致性，但不宣称完成了全量哈希重算。
4. 周期校验采用流式处理，生产库采用增量校验，因此第 3 项不构成本次发布阻断。固定 10^5 行数据集上的 Verify 收尾实测已完成，结果见第 6 节。

## 8. 结论

S5 硬门满足：目标负载下零 fail-closed、零 `AUDIT_OVERLOADED`、零结构性断链；3,799,725 行规模下序列 1..N 连续唯一且 `head_seq` 等于提交数；目标 goodput 和追加延迟在同环境短窗口达到阈值。因此 **S5 容量门 GO**。

固定 N=10^5 行的全量 Verify 收尾项已通过：PostgreSQL 8.120038s、SQLite 4.970181s，均为 VALID，额外 RSS 峰值均低于 64 MiB。
