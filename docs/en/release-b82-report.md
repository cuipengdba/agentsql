# Batch 82: GHCR v0.5.0 accurate tag push and release gate report

**Language:** [English](release-b82-report.md) | [Simplified Chinese](../release-b82-report.md)

**Conclusion: PARTIAL/FAIL-CLOSED.** was recorded on 2026-10-09 Asia/Shanghai. Three accurate tags have been pushed, the dual-architecture OCI index and anonymous pull have all passed, and the single container verification of the main image and quickstart image has passed. The database image required by `deploy/quickstart/docker-compose.yml` does not meet the anonymous pull conditions and the seed → gateway link is not completed; therefore `PromoteLatest` is not executed and `latest` is not moved. Release is still a draft; no official website has been deployed and no publicity has been made.

## Preconditions and construction

- batch81 is reported as `VERIFIED`; the remote annotated tag `v0.5.0` exists, Release is draft and there are 15 assets. `gh auth status` is `cuipengdba`, including `write:packages`. The Docker daemon is available with buildx and the builder lists `linux/amd64` and `linux/arm64`.
- `v0.5.0`, `v0.5.0-demo` or `v0.5.0-quickstart` does not exist in `gh api --paginate /users/cuipengdba/packages/container/agentsql/versions` before push; `latest` is `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e`.
- First build aborted before export or push: Repository `.dockerignore` did not exclude protected untracked files. The same build script is then executed by exporting a temporary source copy from the tracked HEAD `0a1930472ec32cd0510fb41a8844ee00115ef1a9`; the copy does not contain the two protected paths. The HEAD differs from the release tag by only `docs/release-b81-report.md`. The temporary copy has been deleted.
- `scripts/build-ghcr-multiarch.ps1 -Version v0.5.0 -IncludeAuxiliaryTags -Push` pushed three tags and wrote out `dist/release-prepare/v0.5.0-final/image-digests-pushed.json`, of which the top level is `pushed=true` and the number of records is 3. The script finally executes `PATCH /user/packages/container/agentsql` and returns HTTP 404 when requesting to set public, so the script exit code is 1; an independent GET then confirms that the package is actually public.

| tag | top-level digest | The platform descriptor of the remote OCI manifest |
| --- | --- | --- |
| `v0.5.0` | `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278` | `linux/amd64`, `linux/arm64` |
| `v0.5.0-demo` | `sha256:645d0ef0a60c16873bc8eea0ea563f5a662968963bd02fad7be96024efe7e204` | `linux/amd64`, `linux/arm64` |
| `v0.5.0-quickstart` | `sha256:bb432a0cff7fcc0fe8c8106c36fe4b852cbd7d18a6f823e859e247d7eba6508e` | `linux/amd64`, `linux/arm64` |

All three tags use `docker buildx imagetools inspect --raw` to parse the OCI index returned by the registry, and are consistent with the digest of pushed JSON item by item. See `dist/release-b82/build-and-push.log` for details.

## Visibility and anonymous pulls

- `gh api /user/packages/container/agentsql` returns `owner=cuipengdba`, `name=agentsql`, `visibility=public`. See `dist/release-b82/visibility.log` for evidence.
- `docker logout ghcr.io` was executed, followed by the following pull using the newly created, empty `DOCKER_CONFIG` directory; the directory was subsequently deleted. See `dist/release-b82/anonymous-pull.log` for complete timestamped output.

| platform | tag | Completion time (Asia/Shanghai) | result |
| --- | --- | --- | --- |
| `linux/amd64` | `v0.5.0` | 21:36:00 | PASS |
| `linux/arm64` | `v0.5.0` | 21:36:56 | PASS; only pulled but not run |
| `linux/amd64` | `v0.5.0-demo` | 21:37:11 | PASS |
| `linux/amd64` | `v0.5.0-quickstart` | 21:37:30 | PASS |

## Run verification and compose blocking

- Running `agentsql --version` in the amd64 demo image returns `v0.5.0`; `config.demo.yaml` and `demo-seed.yaml` exist in the quickstart image and `agentsql --version` is `v0.5.0`.
- The amd64 master image is booted with one-time `AGENTSQL_SECRET` and `AGENTSQL_ADMIN_PASSWORD` generated only within the test process: `/healthz` is `status=ok, version=v0.5.0` and `/readyz` is `status=ready`. Two previous boot diagnostics and cleanups that lacked required credentials were also logged at `dist/release-b82/run-verification.log`; the credential values ​​were not output or written to the log.
- quickstart compose uses the same empty credentials as the Docker configuration.`docker-compose -p agentsql-b82 -f deploy/quickstart/docker-compose.yml up -d`Pulling`ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5-quickstart`return when`not found`, so the seed and gateway are not started, and the healthy link is not passed. GHCR API review shows that binder package is public, but only`16-0.4-quickstart`and`16-0.4`tag; required`16-0.5-quickstart`Doesn't exist. another dependency`agentsql-mysql-demo:8`Although the tag exists, the package visibility is still private, and the anonymous quickstart condition is not met.
- The `docker compose` subcommand is not loaded by the CLI under an empty Docker configuration, and the installed standalone `docker-compose` is used instead; no resource is created on the first call. See `run-verification.log` for the above compose failure and read-only diagnosis.

## latest gate and cleanup

- The `latest` digest before PromoteLatest is `sha256:b504daad634dc19c483ad1de47e178f2b04066123632a4c580de990e5fcde23e`; the review after the compose gate fails is still the same digest. `v0.5.0` is `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278`. `-PromoteLatest` has not been executed, and the latest anonymous pull/run acceptance has not been done. See `dist/release-b82/promote-latest.log`.
- The temporary master image container, the container, network, and volume of the quickstart compose project are all 0; the temporary source code copy and the empty anonymous Docker configuration directory have been deleted. Other original Docker resources remain unchanged.
- There are no tracked changes other than this report; no git writes have been performed. Protected `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` are not modified. The logs do not record token, private key, or one-time credential values.

## remaining issues

1. You need to provide and expose the `agentsql-postgres-binder:16-0.5-quickstart` required by the quickstart compose separately, and resolve the private visibility of `agentsql-mysql-demo:8`, and then re-verify the seed → gateway healthy link. These dependent packages have not been modified in this batch.
2. Visibility `PATCH` for build scripts returns 404; subsequent maintenance should be used instead to use the supported visibility setting process or set only when needed and leave independent GET validation.
3. `latest` maintains v0.4.0 digest until the above gate is passed; this batch shall not be considered as completing the PromoteLatest.
