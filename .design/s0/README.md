# B2 S0 empirical spike

This directory contains the exact, standalone inputs used for the empirical
checks recorded in `../b2-s0-findings.md`.

The primary runner is `spike.go`. It talks directly to the local Docker Engine
through testcontainers-go, so it does not require `docker.exe` to be on PATH.
Every container is registered for cleanup and is terminated with its volumes
at the end of a run.

Run from the repository root on Windows PowerShell:

```powershell
$env:DOCKER_HOST = 'npipe:////./pipe/docker_engine'
go run ./.design/s0/spike.go -experiment versions
```

Individual SQL fixtures are deliberately kept separate so the catalog queries
can also be pasted into `psql`/`mysql` during review.

