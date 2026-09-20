# 为 AgentSQL 做贡献

感谢你参与 AgentSQL。提交贡献即表示你愿意遵守 [行为准则](CODE_OF_CONDUCT.md)，并按本指南保持变更可审查、可复现。

安全漏洞不要提交到公开 Issue；请按 [安全政策](SECURITY.md) 使用私密渠道报告。

## 项目范围

AgentSQL 是面向 AI Agent 的数据库安全网关和 MCP Server，专注于让 PostgreSQL/MySQL 请求在到达数据库前经过鉴权、解析、授权、风险规则、审批、受控执行、结果脱敏与审计。

项目采用“窄而深”的范围：优先把数据库安全网关的边界、正确性、可审计性和运行质量做扎实。Pull Request 不应把项目泛化为 BI、ORM、通用 Text2SQL 平台或与网关安全主线无关的 Agent 框架。较大的能力提案请先在 Discussions 说明使用场景、安全边界和维护成本。

## 开发环境

后端构建需要：

- Go 1.25；`go.mod` 当前声明 `go 1.25.0`；
- 启用 cgo，并安装可用的 C 编译器；
- glibc 兼容构建环境。`pg_query_go` 不支持本项目使用 `CGO_ENABLED=0` 或 Alpine/musl 方式构建。

如果本机 Go 版本较旧，可使用 Go 的工具链自动选择能力，例如设置 `GOTOOLCHAIN=auto`。依赖下载受网络环境影响时，可按组织策略设置 `GOPROXY`；仓库 Docker 构建的默认值是 `https://proxy.golang.org,direct`，也支持通过构建参数覆盖。不要把个人代理地址或凭据提交到仓库。

常用 Makefile 目标如下：

```bash
make build VERSION=v0.3.0
make test
make race
make vet
make fmt
```

此外，Makefile 还提供 `lint`、`webui`、`release`、`docker-build` 和 `docker-linux-amd64`。其中 `lint` 需要本机已有 `golangci-lint`；普通后端构建使用仓库中已提交的 Web 控制台产物，不要求 Node.js。

### Web 控制台

仅在修改 `web/` 源码时需要 Node.js 与 npm：

```bash
cd web
npm ci
npm run typecheck
npm run build
```

`make webui` 会执行 `npm ci` 和 `npm run build`。当前前端质量门是 TypeScript 类型检查与 Vite 构建。不要新增前端依赖，也不要引入 Vitest、Jest 等前端单测框架；确有必要调整依赖时，应先与维护者讨论并说明供应链与维护成本。

### 本地 Live Demo

先复制环境样例并替换其中全部公开示例凭据：

```bash
cp examples/docker/demo.env.example demo/demo.env
bash ./demo/reset.sh
```

Windows PowerShell：

```powershell
Copy-Item examples/docker/demo.env.example demo/demo.env
.\demo\reset.ps1
```

reset 脚本面向固定的 `agentsql-demo` Compose 项目，会执行 `down -v --remove-orphans` 后重新构建和启动，并验证数据计数。它会删除该 Demo 项目的卷，请勿把真实数据库、真实凭据或需要保留的数据接入 Demo。

## 测试与质量门

提交前至少运行与改动相关的测试；影响核心链路时应运行完整质量门：

```bash
make fmt
make vet
go test -race -count=1 ./...
```

确认 `gofmt -l` 对改动的 Go 文件没有输出，并检查格式化没有带来无关变更。

SQL 语料分为两层：

- `tests/corpus/postgres.json` 与 `tests/corpus/mysql.json` 是 parser 层 AST 断言，由 `TestCorpus` 消费；
- `tests/corpus/decision_cases.json` 是风险决策语料，当前包含 252 个条目，按适用方言展开为 353 次判定，由 `TestDecisionCorpus` 消费。

可分别运行：

```bash
go test ./internal/parser -run TestCorpus -count=1
go test ./internal/pipeline -run TestDecisionCorpus -count=1 -v
```

新增或修改 SQL 风险规则时，必须补充能覆盖正常、危险、方言差异和绕过尝试的语料及单元测试。不得通过改写期望值掩盖实现缺陷；353 次决策回归必须全部通过，危险语句零漏拦，正常语句误报为 0。

只读路径的 P99 门是独占、非 race、非 `-short` 的网关自身开销测试，不包含真实数据库网络与执行耗时。请单独运行：

```bash
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
```

该测试的门限为 P99 小于 5 ms。不要把 race 插桩结果或端到端数据库延迟与此口径混用。

## 分支、提交与 Pull Request

1. 从最新的默认分支创建短期主题分支，例如 `feat/rule-name`、`fix/parser-case` 或 `docs/security-policy`。
2. 一个 Pull Request 只解决一件事；避免顺手重构、批量改名或格式化无关文件。
3. 提交信息使用 Conventional Commits，例如 `feat: ...`、`fix: ...`、`docs: ...`、`test: ...`、`chore: ...`。
4. 每个提交都必须包含 DCO `Signed-off-by` 行，表示你有权按项目许可证提交该贡献。可用 `git commit -s` 自动添加，例如 `Signed-off-by: Your Name <you@example.com>`。
5. 推送个人分支并创建 Pull Request，关联对应 Issue，说明问题、方案、风险和验证结果。大型设计或范围不明确的功能应先讨论再实现。
6. 根据审查意见追加清晰的提交；合并前确保质量门通过、提交签署完整且文档同步更新。

请不要在提交、Issue、测试夹具、截图或日志中放入真实密钥、密码、DSN、个人信息或生产数据。

## 文档与文案

- 所有命令、路径、版本、支持矩阵和功能声明都必须能在当前仓库或实际测试中验证。
- 简体中文是主要文档语言；首次出现的缩写或专业术语应给出必要解释。
- 不夸大安全能力，不使用“绝对安全”“零风险”“完整 DLP”“不可篡改审计”等无法证明的表述。
- 描述安全能力时，应明确：只保护经过网关的运行账号；不防绕过网关的 owner、superuser 或 DBA；结果脱敏不是完整 DLP；应用层审计不是法规级 WORM。
- 行为、配置、兼容性或用户流程发生变化时，同步更新 README 或 `docs/` 中对应文档。

## 行为准则摘要

请友善、尊重不同观点，提供可执行的建设性反馈；不骚扰、不人身攻击、不公开他人隐私；发现冲突时聚焦技术事实和社区共同利益。完整规则、适用范围和举报方式见 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。
