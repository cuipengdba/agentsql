# Batch 83b (batch831): v0.5.0 official release and post-release smoke test

**Language:** [English](release-b83b-report.md) | [Simplified Chinese](../release-b83b-report.md)

Date: 2026-10-10 (Asia/Shanghai). Final smoke test record: `dist/release-b83b/post-release-smoke.log`; the record of the first script error is retained at `dist/release-b83b/post-release-smoke-attempt1.log`.

## Release to official

- Read-only verification before publishing: `gh release view v0.5.0 --json isDraft,isPrerelease,tagName -q .` returns `isDraft=true`, `isPrerelease=false`, `tagName=v0.5.0`.
- The only remote write operation: `gh release edit v0.5.0 --draft=false --latest`, started at 09:35:46 Beijing time and returned successfully at 09:35:54. GitHub returns to the official entrance: <https://github.com/cuipengdba/agentsql/releases/tag/v0.5.0>.
- `gh release view v0.5.0 --json isDraft,isPrerelease,tagName,publishedAt,url -q .` Review: `isDraft=false`, `isPrerelease=false`, `tagName=v0.5.0`, `publishedAt=2026-10-10T01:35:51Z`. A CLI query containing `isLatest` was rejected (the field is invalid); the anonymous GitHub `/releases/latest` API was used instead, returning `tag_name=v0.5.0`, `draft=false`, `prerelease=false`. **RELEASE_PUBLISHED**.

## Post-launch smoke test

The final complete rerun is executed in the order of requests, and the end of the log is **`SMOKE_SUMMARY PASS=9 FAIL=0`**, reaching ≥8 PASS and 0 FAIL gates.

| Project | result | observation |
| --- | --- | --- |
| RELEASE_PAGE | PASS | Access to the official Release page without authentication is HTTP 200; the public API is v0.5.0, non-draft, and non-prerelease. |
| RELEASE_ASSETS | PASS | Release has exactly 15 items, and the names are consistent with the `dist/release-prepare/v0.5.0-final/assets/` authoritative catalog. |
| GHCR_LATEST | PASS | `docker buildx imagetools inspect` The top-level summary is `sha256:c2091606f6d3cab7d651bf41fadab433045be7afde644250b8d71048b7235278`. `...bf41f1ad...` in the task text has one more hexadecimal character; the comparison uses the valid 64-bit digest accepted by batch82c. |
| ANON_PULL_LATEST | PASS | Use the newly created empty `DOCKER_CONFIG` to execute `docker pull ghcr.io/cuipengdba/agentsql:latest` successfully, and the temporary configuration has been cleared. |
| DEMO_HEALTHZ | PASS | `status=ok`, `version=v0.5.0` of `/healthz`. Demo credentials in response are not output. |
| DEMO_READYZ | PASS | `status=ready` of `/readyz`. |
| SITE_HOME | PASS | HTTPS Home Page GET returns HTTP 200. |
| SITE_PDFS | PASS | Three copies of `agentsql-*-v0.5.0.pdf` all returned HTTP 200, `application/pdf`. |
| DEMO_LOGIN | PASS | Log in using the demo account information provided by Demo `/healthz`, and return success and session token; no password or token is printed or recorded. |
| SIGNATURE (optional) | Citing existing conclusions | batch81 has compared the downloaded 15 items one by one and verified the three signatures of SBOM, provenance, and SHA256SUMS; this round has not been rerun. |

The first attempt stopped at the SITE_HOME item because the smoke test script mistakenly used the PowerShell read-only automatic variable `$HOME` as the response variable, leaving the original log of `PASS=6 FAIL=1`. Then only for the official website diagnosis, HEAD and GET are both HTTP 200; correct the variable name and re-execute from the first item to obtain the 9 PASS items in the above table. The first record was not deleted and was not incorporated into the final rerun count.

## Conclusion and entrance

`v0.5.0` has been officially released and became the latest Release on GitHub; after correcting the smoke test script, a complete rerun resulted in 9 PASS and 0 FAIL, and the gate passed after the release.

- Release：<https://github.com/cuipengdba/agentsql/releases/tag/v0.5.0>
- GHCR latest：`ghcr.io/cuipengdba/agentsql:latest`
- Official website: <https://agentsql.cn>
- Live Demo：<https://demo.agentsql.cn>

No git writes or commits were performed, and the two protected untracked paths were not touched.
