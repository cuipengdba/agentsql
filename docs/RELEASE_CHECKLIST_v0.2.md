# AgentSQL v0.2.0 GA 发布检查清单与收尾工单

> 负责人：崔鹏（产品/版权主体待确认）　｜　主控：豆包（规格、验收、发布工程）　｜　实现：Codex
> 准备日期：2026-09-17　｜　目标版本：**v0.2.0**　｜　仓库（当前私有）：`https://github.com/cuipengdba/agentsql-gateway.git`
>
> 说明：本清单区分「主控可自动完成」与「必须人工/必须负责人亲口下令」两类。**打 GA tag、发 GitHub Release、私有转公开、对外宣传四件事，在负责人明确下令前不执行。**

---

## 0. 当前基线（发布准备日核对）

| 项 | 值 |
|---|---|
| main HEAD | `4a96e8c`（官网部署回写），与 `origin/main` 一致，工作区干净 |
| 最新存档 tag | `t26-2c`（annotated，已推送） |
| v0.2 范围 | T27 大屏 SSE、T28 控制面/审计 PostgreSQL18、T26 在线 Live Demo，全部闭环 |
| 许可证 | `LICENSE` = GNU AGPLv3；`COMMERCIAL-LICENSE.md` = AGPLv3 + 商业双授权 + 商标保留 |
| 正式 tag | `v0.1.0`、`v0.2.0` **尚未创建**（现有 `v0.1-rc1` 与大量 `tNN`/`g1-*` 为过程存档 tag） |

---

## 1. 已完成并验证（主控已做，附证据）

### 1.1 版本号一致性（七处规范来源 + 运行时，全部 v0.2.0）

| # | 位置 | 期望值 | 实测 |
|---|---|---|---|
| 1 | `internal/version/version.go` `Version` | `v0.2.0` | ✅ |
| 2 | `Makefile` `VERSION ?=` | `v0.2.0` | ✅ |
| 3 | `Dockerfile` `ARG VERSION=` | `v0.2.0` | ✅ |
| 4 | `docker-compose.yml` build arg 与 image tag（两处） | `v0.2.0` | ✅ |
| 4b | `docker-compose.demo.yml` build arg 与 image tag（两处） | `v0.2.0` | ✅ |
| 5 | `examples/docker/.env.example` `VERSION=` | `v0.2.0` | ✅（SECRET/口令留空） |
| 5b | `examples/docker/demo.env.example` `VERSION=` | `v0.2.0` | ✅（本地占位，标注必换） |
| 6 | `web/package.json` `version` | `0.2.0` | ✅ |
| 7 | `web/package-lock.json` 顶层与 `packages[""]` | `0.2.0` | ✅ |
| 运行时 | `agentsql --version` / `agentsqlctl version` | `v0.2.0` | ✅ |
| 运行时 | `GET /healthz` `version` | `v0.2.0` | ✅（demo 容器实测） |
| 文案 | README 徽章与构建示例、`docs/DEPLOY.md` 示例 | `v0.2.0` | ✅ |

> 发布日复跑：`bin\agentsql.exe --version`、`bin\agentsqlctl.exe version`、容器 `curl 127.0.0.1:7780/healthz`，三者必须都是 `v0.2.0`。

### 1.2 质量门（最近一次全量，全绿）

- 全量验收 `gofmt / vet / go test -race ./... / 353 语料 / -short 全量 / P99 延迟 / build`：**ALL_GREEN**。
- 353 语料基线零变化：total=353、passed=353、误报 FP=0（danger deny=109、normal allow=165 等口径见 SPEC）。
- Live Demo 真实容器黑盒 E2E：**20/20 PASS**（六卡、零触库写拦截、脱敏 10 单元、限流、白名单、夹带字段拒绝、无密钥泄漏）。
- 写攻击零触库证据：攻击前后 MySQL 快照 diff 为空（orders=2400、状态分布不变、明文 PII 不变）。
- 历史 fuzz：约 4814.7 万次变异 PASS（`v0.1-rc1` 阶段）。
- 浏览器黑盒：Live 横幅/徽标、静态/Live 切换、卡④金额定点（22.74 等）、卡⑤触库前拦截无底层报错、卡⑥ `/audit?focus=<id>` 证据链与总览 SSE「实时」均通过。

### 1.3 安全/密钥扫描（发布准备日）

- `.gitignore` 覆盖 `.env` 与 `demo/demo.env`；真实演示环境文件未入库。
- 入库的 env 类文件仅 `examples/docker/.env.example`、`examples/docker/demo.env.example`（占位且标注「本地回环、必须替换」）。
- 跟踪文件中无真实 `asql_` Agent Key、无真实 DSN/口令；`demo_credentials.go` 从环境读取、不硬编码。
- **转公开前需再跑一次全历史扫描**（见 §4.4），因为公开后历史提交也可见。

---

## 2. GA 前必补物料（BLOCKING，主控可代做但需你给信息或确认）

- [x] **2.1 商业授权联系信息（法务/商务门面）**：已定稿——版权主体 **崔鹏**，联系邮箱 **87326549@qq.com**，官网 **https://agentsql.cn**；已统一替换 COMMERCIAL-LICENSE、README、Release Notes 中的联系点。
- [x] **2.2 品牌决策**：**保留中文名「智盾」**（控制台仍为「AgentSQL 智盾控制台」，前端零改动、无需重建/重截图）；README、Release Notes、SPEC 已统一标注中文品牌名。
- [x] **2.3 真实截图（修复 README 死链）**：已完成（commit `c9b5508`），README 引用的 `docs/images/demo-scenario-1..6.png` 六张均已存在且为 Live Demo 真实截图、无死链。原计划要点（留档）：
  1. 正常放行（5 行 + audit_id）；2. 无 WHERE 写拦截（红色、无底层报错）；3. phone/email 脱敏；4. 大结果告警（金额 22.74 + 截断 20）；5. 越权表拒绝；6. 审计证据链 / 总览实时大屏。
  - 主控可在你确认后用浏览器一次性截齐并落到 `docs/images/`，同时把 `docs/screenshots/` 补 1–2 张控制台总览/审计图。**不允许用合成图冒充真实截图。**
- [ ] **2.4 CHANGELOG 日期定稿**：当前 v0.2.0 标注 2026-09-17（准备日）。若 GA 延后，发布日统一更新 `CHANGELOG.md` 本行与 Release 标题日期。
- [ ] **2.5 Release Notes 定稿**：用 `docs/release-notes-v0.2.0.md` 草稿，补截图、联系信息后作为 GitHub Release 正文。

---

## 3. 人工 / 环境门（主控无法代做，建议录屏作为宣传素材）

- [ ] **3.1 Cursor 桌面端真实接入四场景录屏**：①只读成功；②越权对象被拒；③无 WHERE 更新被拒；④审计可查。
- [ ] **3.2 Claude 桌面端（或其它 MCP 宿主）同样四场景录屏**。
- [ ] **3.3 Linux 真机 systemd + 非 root UID 演练**：按 `deploy/systemd/agentsql.service` 与 `docs/DEPLOY.md` 走一遍二进制部署，确认非 root、数据目录权限、探针、开机自启。
- [ ] **3.4 干净环境 5 分钟从零复跑**：全新目录 `cp .env.example .env` → 填随机值 → `docker compose up -d --build` → 登录，全程录屏，验证 README 无跳步。
- [ ] **3.5（建议）Live Demo 一条命令复跑录屏**：`demo/reset.ps1` 或 `bash demo/reset.sh` 到打开六卡，作为开源 README 的演示 GIF/视频素材。

> 录屏里不要出现真实 SECRET、Agent Key、内网地址与真实业务数据；演示一律用合成库。

---

## 4. 发布执行（**需负责人亲口下令**；以下为可直接执行的命令，PowerShell 为主）

### 4.1 发布日定稿提交（仅当 §2、§3 全绿）

```powershell
cd D:\ruanjiansheji\agentsql
git status                       # 必须干净
# 定稿 CHANGELOG 日期、补齐截图与商业授权信息后：
git add CHANGELOG.md COMMERCIAL-LICENSE.md README.md docs/images docs/screenshots docs/RELEASE_CHECKLIST_v0.2.md docs/release-notes-v0.2.0.md
git commit -m "chore(release): finalize v0.2.0 notes, screenshots, contact info"
git push origin main
```

### 4.2 打正式 tag（annotated）

```powershell
$env:GIT_TERMINAL_PROMPT='0'
git tag -a v0.2.0 -m "AgentSQL v0.2.0: SSE live dashboard, PostgreSQL18 control/audit plane, self-hosted Live Demo"
git push origin v0.2.0
```

### 4.3 生成源码包与校验和（GitHub Release 附件）

```powershell
# 以正式 tag 导出干净源码（不含工作区杂物）
git archive --format=tar.gz --prefix=agentsql-v0.2.0/ -o ..\agentsql-v0.2.0.tar.gz v0.2.0
git archive --format=zip    --prefix=agentsql-v0.2.0/ -o ..\agentsql-v0.2.0.zip    v0.2.0
Get-FileHash ..\agentsql-v0.2.0.tar.gz, ..\agentsql-v0.2.0.zip -Algorithm SHA256 |
  Format-Table Path, Hash -AutoSize
```

> 容器镜像推送到镜像仓库（GHCR/Docker Hub）属可选项，决定后再补登录与 push 步骤。

### 4.4 私有转公开（不可逆，务必先做全历史扫描）

```powershell
# 1) 全历史密钥/内部信息扫描（命中先评估，必要时用 git filter-repo 重写历史）
git grep -n -I -E "asql_[A-Za-z0-9]{20}|AKIA[0-9A-Z]{16}|BEGIN (RSA|OPENSSH|PRIVATE) KEY" $(git rev-list --all)
# 2) 确认 .env / demo/demo.env 从未被任何提交跟踪
git log --all --oneline -- .env demo/demo.env
# 3) 过程 tag 决策：公开仓建议只保留 v0.1.0 / v0.2.0，过程 tag（t01..t26-2c、g1-*、v0.1-rc1）
#    在 GitHub 仓库设置里删除或保留为内部存档（删除远程 tag：git push origin :refs/tags/<name>）
# 4) GitHub 仓库 Settings → General → Change visibility → Public（人工点击，最后一步）
```

### 4.5 发 GitHub Release

- Tag 选 `v0.2.0`，标题 `AgentSQL v0.2.0`；
- 正文粘贴 `docs/release-notes-v0.2.0.md`（定稿版）；
- 附件上传 `agentsql-v0.2.0.tar.gz`、`agentsql-v0.2.0.zip` 与 SHA256；
- 勾选/不勾选 pre-release：v0.2.0 为正式版则**不勾** Set as pre-release。

---

## 5. 发布后立即验证（Post-GA）

- [ ] 公开仓 README 徽章、文档链接、6 张截图、LICENSE / COMMERCIAL-LICENSE 链接全部可打开、无裂图。
- [ ] 全新克隆公开仓，按 README 5 分钟路径与 Live Demo 路径各跑一遍。
- [ ] Release 附件可下载、SHA256 对得上。
- [ ] `/healthz`、MCP stdio 与 HTTP `/mcp` 的 `serverInfo.version` 均为 `v0.2.0`。
- [ ] 用一个全新 MCP 宿主走只读/拦截/脱敏/审计各一次。

---

## 6. 宣传与分发（GA 后启动，物料后置）

- [ ] 产品官网：优先 GitHub Pages（仓库内静态站，免备案）；自有域名再评估 ICP 备案（国内服务器 + 域名解析才强制备案，仅注册域名不需营业执照也可持有，主体信息与 §2.1 一致）。
- [ ] 一页 PDF 功能手册、汇报 PPT、公众号长文、个人技术微信群/朋友圈预热文案（主控可逐份产出）。
- [ ] 演示素材统一用 §3 录屏与 §2.3 截图；对外口径遵守 §7 红线。

---

## 7. 对外口径红线（Release Notes / 官网 / 宣传必须遵守，不得夸大）

- AgentSQL 是**夹在 AI Agent 与数据库之间的 MCP 安全网关**，不是 BI、不是 ORM、不替代数据库账号体系。
- 最小权限只约束**经过网关的运行账号**；不防绕过网关直连的 owner/superuser/DBA。
- v0.2 脱敏是按列规则的结果层打码，**不是完整 DLP**；不宣称「任何别名/写法都不可绕过」。
- 审计在应用层不可变，**不是法规级 WORM**，也不防 DBA 直接改库；WORM/SIEM/等保报告属 T30 企业版。
- 控制面 PG 高可用、跨实例集中审计、K8s Operator 属 T31 企业版，v0.2 不含。
- 国产/商业数据库矩阵（达梦/金仓/瀚高/GaussDB/OceanBase/TiDB/Oracle/SQL Server）属 T29 企业版，v0.2 被防护业务库仅 MySQL 8 与 PostgreSQL 14–18。

---

## 8. 回滚

- 代码回滚：`git checkout v0.1.0` 对应基线（GA 后），或 `git revert <release commit>`；容器用旧镜像 tag。
- 控制面：SQLite 路径无迁移破坏；PostgreSQL 控制面迁移前必须按 `docs/DEPLOY.md` 备份 metadata/audit，回滚用备份恢复，不做自动向下迁移。
- 撤回发布：GitHub Release 可转 Draft/删除；公开仓需紧急转私有时在 Settings 操作（tag 与源码包可能已被他人获取，需另行公告）。

---

## 9. 一页结论（当前能不能发）

- **代码与构建层面：v0.2.0 已具备发布条件**（版本一致、全量与 E2E 全绿、密钥扫描通过）。
- **真正的发布闸口只剩三件事**：①补齐商业授权联系信息/品牌/真实截图（§2）；②人工录屏与 Linux 演练（§3）；③你亲口下令执行 §4 的 tag / Release / 转公开。
- 在①②完成前，建议保持私有仓 + 过程存档 tag；完成后主控可在一次会话内执行 §4.1–§4.3 与 Release 正文装配。
