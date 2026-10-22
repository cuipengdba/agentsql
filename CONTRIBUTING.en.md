# Contribute to AgentSQL

**Language:** [English](CONTRIBUTING.en.md) | [Simplified Chinese](CONTRIBUTING.md)

Thank you for participating in AgentSQL. Submitting a contribution indicates that you are willing to abide by the [ Code of Conduct ](CODE_OF_CONDUCT.en.md) and follow these guidelines to keep changes reviewable and reproducible.

Do not submit security vulnerabilities to public issues; please use private channels to report according to [ security policy ](SECURITY.en.md).

## Project scope

AgentSQL is a database security gateway and MCP Server for AI Agents. It focuses on allowing PostgreSQL/MySQL requests to undergo authentication, parsing, authorization, risk rules, approval, controlled execution, result masking and auditing before reaching the database.

The project adopts a "narrow and deep" scope: Prioritizing the boundaries, correctness, auditability and operational quality of the database security gateway. Pull Requests should not generalize the project to BI, ORM, a general Text2SQL platform, or an Agent framework unrelated to the gateway security mainline. For larger capability proposals, please first explain the usage scenarios, safety boundaries and maintenance costs in Discussions.

## development environment

The backend build requires:

- Go 1.25; `go.mod` currently declares `go 1.25.0`;
- Enable cgo and install an available C compiler;
- glibc compatible build environment. `pg_query_go` does not support this project to be built using `CGO_ENABLED=0` or Alpine/musl.

If your native Go version is older, you can use Go's toolchain automatic selection capabilities, such as setting `GOTOOLCHAIN=auto`. When dependent downloads are affected by the network environment, `GOPROXY` can be set according to organizational policies; the default value for the repository Docker build is `https://proxy.golang.org,direct`, and it also supports overwriting through build parameters. Do not submit personal proxy addresses or credentials to the repository.

Commonly used Makefile goals are as follows:

```bash
make build VERSION=v0.5.0
make test
make race
make vet
make fmt
```

Additionally, the Makefile provides `lint`, `webui`, `release`, `docker-build`, and `docker-linux-amd64`. Among them, `lint` requires that the local machine already has `golangci-lint`; the general backend build uses the submitted Web console product in the repository and does not require Node.js.

### Web console

Node.js and npm are only required when modifying the `web/` source code:

```bash
cd web
npm ci
npm run typecheck
npm run build
```

`make webui` will execute `npm ci` and `npm run build`. The current front-end quality gate is TypeScript type checking with Vite builds. Do not add new front-end dependencies, and do not introduce front-end unit testing frameworks such as Vitest and Jest. When it is necessary to adjust dependencies, you should first discuss with the maintainer and explain the supply chain and maintenance costs.

### Local Live Demo

First copy the environment sample and replace all public sample credentials in it:

```bash
cp examples/docker/demo.env.example demo/demo.env
bash ./demo/reset.sh
```

Windows PowerShell：

```powershell
Copy-Item examples/docker/demo.env.example demo/demo.env
.\demo\reset.ps1
```

The reset script is for the fixed `agentsql-demo` Compose project. It will execute `down -v --remove-orphans` and then rebuild and start it, and verify the data count. It will delete the volume of the Demo project. Please do not connect the real database, real credentials or data that needs to be retained to the Demo.

## Testing and Quality Gate

Run at least the tests associated with the change before committing; run a full quality gate when affecting core links:

```bash
make fmt
make vet
go test -race -count=1 ./...
```

Confirm that `gofmt -l` does not output the modified Go file, and check that the formatting does not bring about irrelevant changes.

The SQL corpus is divided into two layers:

- `tests/corpus/postgres.json` and `tests/corpus/mysql.json` are parser layer AST assertions, consumed by `TestCorpus`;
- `tests/corpus/decision_cases.json` is a risk decision-making corpus, which currently contains 252 items, expanded to 353 judgments according to applicable dialects, and is consumed by `TestDecisionCorpus`.

Can be run separately:

```bash
go test ./internal/parser -run TestCorpus -count=1
go test ./internal/pipeline -run TestDecisionCorpus -count=1 -v
```

When adding or modifying SQL risk rules, you must add corpus and unit tests that cover normal, dangerous, dialect differences, and bypass attempts. Implementation defects must not be covered up by rewriting expected values; all 353 decision regressions must be passed, with zero missed dangerous statements and 0 false positives for normal statements.

The P99 gate of the read-only path is an exclusive, non-race, non-`-short` gateway's own overhead test, which does not include the real database network and execution time. Please run alone:

```bash
go test ./internal/pipeline -run TestT25LatencyPercentile -v -count=1
```

The threshold for this test is P99 less than 5 ms. Don't mix race instrumentation results or end-to-end database latency with this caliber.

## Branches, Commits and Pull Requests

1. Create a short-lived topic branch from the latest default branch, such as `feat/rule-name`, `fix/parser-case`, or `docs/security-policy`.
2. A Pull Request only solves one thing; avoid refactoring, batch renaming, or formatting irrelevant files.
3. Submit information using Conventional Commits, such as `feat: ...`, `fix: ...`, `docs: ...`, `test: ...`, `chore: ...`.
4. Each submission must contain the line DCO `Signed-off-by`, indicating that you have permission to submit that contribution under the project license. `git commit -s` can be added automatically, such as `Signed-off-by: Your Name <you@example.com>`.
5. Push the personal branch and create a Pull Request, associate the corresponding Issue, and explain the problem, plan, risk and verification results. Large designs or features with unclear scope should be discussed first before being implemented.
6. Add clear submissions based on review comments; ensure the quality gate is passed, submissions are signed completely, and documents are updated simultaneously before merging.

Please do not put real keys, passwords, DSNs, personal information, or production data in commits, issues, test fixtures, screenshots, or logs.

## Documentation and Copywriting

- All commands, paths, versions, support matrices, and feature claims must be verifiable against current repositories or real-world testing.
- Simplified Chinese is the primary documentation language; the first occurrence of abbreviations or technical terms should be given necessary explanations.
- Do not exaggerate security capabilities, and do not use unprovable expressions such as "absolute security", "zero risk", "complete DLP", and "non-tamperable auditing".
- When describing the security capabilities, it should be clear: only the running account passing through the gateway is protected; it does not prevent the owner, superuser or DBA from bypassing the gateway; the result masking is not a complete DLP; the application layer audit is not a regulatory-level WORM.
- When behavior, configuration, compatibility or user flow changes, the corresponding documentation in README or `docs/` will be updated synchronously.

## Code of Conduct Summary

Please be kind, respect different viewpoints, and provide actionable and constructive feedback; do not harass, do not personally attack, and do not disclose other people's privacy; when conflicts are discovered, focus on technical facts and the common interests of the community. For complete rules, scope of application and reporting methods, see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.en.md).
