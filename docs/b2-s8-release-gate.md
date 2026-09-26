# B2 S8 release reliability gate

This workflow produces engineering evidence only. It does not enable a
production flag and it does not replace the release owner's decision.

## Lock/SLO matrix

The qualifying run is ten minutes per PostgreSQL major (14 and 18). Closed and
native workers run together through the `4,12,24` concurrency staircase while
separate workers hold `ACCESS EXCLUSIVE` locks on the target and run real DDL
against a churn table.

```powershell
$env:AGENTSQL_B2_S8_MATRIX='1'
$env:AGENTSQL_B2_S8_OUTPUT_DIR='artifacts/b2-s8'
go test ./internal/authorizedexecute/internal/businessdb -run '^TestB2S8PostgresSLOMatrix$' -count=1 -timeout 30m
```

`AGENTSQL_B2_S8_DURATION` may shorten a developer smoke only when
`AGENTSQL_B2_S8_ALLOW_SHORT=1`; such a report has
`qualifying_evidence=false` and the GA gate rejects it. Optional thresholds are
`AGENTSQL_B2_S8_MAX_P99_MS` (default 2000) and
`AGENTSQL_B2_S8_MAX_ERROR_RATE` (default 0.005).
`AGENTSQL_B2_S8_MAX_DDL_SILENCE_MS` defaults to 5000 and rejects a run whose
DDL worker stopped making progress. The report also records DDL failures,
periodic table rebuilds, and the age of the last successful DDL. Periodic
rebuilds avoid PostgreSQL's 1600 historical-column limit after repeated
`ADD COLUMN`/`DROP COLUMN` churn.

Run the S7 attack matrix with `AGENTSQL_B2_S7_MATRIX=1`; it writes a PG14/18
report when `AGENTSQL_B2_S7_OUTPUT_DIR` is set.

## Signed fallback drill

Collect all seven observations in JSON. Each case must carry the audit or
attestation identifier observed during injection, the observed route, zero
silent allow/unauthorized-column assertions, and successful recovery followed
by explicit reactivation. The required cases are runtime missing, extension
missing, version/hash mismatch, heartbeat/lease loss, activation failure,
native unhealthy, and shadow divergence.

```powershell
go run ./cmd/agentsql-b2-fallback -observations artifacts/b2-s8/fallback-observations.json -signing-key .secrets/b2-s8-ed25519.key -output artifacts/b2-s8/fallback-signed.json
```

The signing key file is base64 Ed25519 seed/private-key material. Do not commit
it. Put the emitted public key and evidence ID into the GA evidence manifest.
The verifier rejects tampering, missing cells, unsafe routes, missing recovery,
or a digest mismatch.

Before activation, runtime/activation failure may preserve the explicitly
requested legacy table-level path. After protocol 3 has been active, heartbeat
or lease loss stays on the B2 route and fails closed. Native absence/mismatch
may select the independently attested closed subset; shadow divergence fails
closed. No path silently weakens an active column policy to table-level.

## Dry-run, activation, and GA checks

Runtime dry-run is configured without enabling column authorization:

```yaml
column_authorization:
  enabled: false
  dry_run: true
  instance_id: b2-dry-run-instance
```

It runs the same metadata snapshot, capability probes, reservation check, and
artifact calculation as activation, audits `b2_dry_run`, but does not mutate
the fence or register a runtime. `enabled` and `dry_run` are mutually
exclusive.

Activation is explicit (`enabled: true`) and two phase: phase one returns a
quoted strong ETag from a serializable metadata/fence snapshot; phase two takes
the exclusive fence, recomputes readiness, requires the exact ETag, checks the
same artifact against every live runtime, and only then changes protocol and
creates the lease. A mismatch leaves protocol 2 unchanged.

The release checker never activates anything:

```powershell
go run ./cmd/agentsql-b2-gate -mode dry-run -evidence artifacts/b2-s8/evidence.json -fallback-public-key release-trust/b2-fallback.pub
go run ./cmd/agentsql-b2-gate -mode activation -evidence artifacts/b2-s8/evidence.json -fallback-public-key release-trust/b2-fallback.pub -output artifacts/b2-s8/activation-gate.json
go run ./cmd/agentsql-b2-gate -mode ga -evidence artifacts/b2-s8/evidence.json -fallback-public-key release-trust/b2-fallback.pub -output artifacts/b2-s8/ga-gate.json
```

Dry-run always emits `OBSERVE_ONLY`. Activation and GA emit `NO-GO` and exit 2
when anything is missing. `GO` means only that supplied, digest-verified
engineering evidence is complete; M2/M3 and the release owner still decide.
The trusted fallback public key is supplied separately from the evidence
manifest so replacing a report and its self-declared key cannot satisfy the
gate.
