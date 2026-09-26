# Easy-deploy binder S0

This directory contains an evidence-only harness for the zero-server-install
PostgreSQL binder investigation. It does not import or modify production
packages and is excluded from normal builds with `//go:build ignore`.

Run from the repository root:

```powershell
$env:DOCKER_HOST='npipe:////./pipe/docker_engine'
$env:TESTCONTAINERS_RYUK_DISABLED='true'
go run ./.design/easy-deploy-s0/probe.go -out ./.design/easy-deploy-s0/raw
```

The runner starts one disposable container for each PostgreSQL major 14--18,
creates both a superuser and an ordinary login, runs the same probes through
both accounts, records JSONL/CSV evidence, and terminates only containers with
the `agentsql.easy-deploy.s0=true` label and its own exact handle.

Generated evidence:

- `raw/environment.json`: runner, image, and server versions.
- `raw/pgNN-{super,regular}.jsonl`: one record per representative statement.
- `raw/pgNN-{super,regular}-metadata.json`: catalog/deparse visibility.
- `raw/concurrency.jsonl`: DDL lock, prepared-statement ABA, plan jitter, and
  search-path probes.
- `raw/matrix.csv`: compact per-version/account/statement comparison.
- `raw/timing.csv`: nearest-rank warm-connection latency samples.
- `raw/run.log`: lifecycle and cleanup log.

The evidence is intentionally descriptive. In particular, plan JSON and
deparsed expressions are captured to demonstrate their limitations; they are
not proposed as authorization inputs.

