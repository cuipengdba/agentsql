# post3: Official website homepage v0.5.0 version bit and content-only reissue report

**Language:** [English](postrelease-post3-report.md) | [Simplified Chinese](../postrelease-post3-report.md)

Execution date: 2026-10-12 (Asia/Shanghai).

## Category A: Current version context (10/10 passed)

| No. | location | Check results |
| --- | --- | --- |
| A1 | L67 current version logo | v0.5.0 |
| A2 | L192 One-click installation and multi-architecture image instructions | v0.5.0 |
| A3 | L192 GHCR pull command | agentsql:v0.5.0 |
| A4 | L193 GitHub Release button | v0.5.0 |
| A5 | L200 GA compatible matrix header | v0.5.0 |
| A6 | L200 version and protocol support instructions | v0.5.0 |
| A7 | L203 Compatibility Range Table Title | v0.5.0 |
| A8 | L256 GHCR pull command | agentsql:v0.5.0 |
| A9 | L260 quickstart parameters | --version v0.5.0 |
| A10 | L299 current official version timeline | v0.5.0 GA, retain the "current official version" |

git diff -- website/public/index.html only contains the above 10 text substitutions; git diff --check passes.

## Category B: Historical representation (4/4 retained as is)

| No. | location | Reserved content |
| --- | --- | --- |
| B1 | L192 | The linux/arm64 native glibc package is provided starting from v0.4.0 |
| B2 | L213 | linux/arm64 native glibc package is provided starting from v0.4.0; musl/Alpine or CentOS 7 native package is not provided |
| B3 | L309 | Existing source code, tags and release packages for v0.4.0 and earlier versions |
| B4 | L322 | v0.4.0 and earlier versions continue to be covered by the original license at the time of release |

There are 4 remaining v0.4.0 on the homepage, all of which belong to the above historical expressions.

## Release and public network verification

- Package website/{deploy,public,tools,README.md} as dist/release-post3/site-post3.tar.gz (53 archive entries, SHA-256: badd2b71cdd1ee8152633f2df118192e94e7d9b962b024f54bfb99732a1143a1). After uploading to ECS /root/website-release-post3/, the remote archive hash is consistent with the local machine; after decompression, the home page SHA-256 is consistent with the local machine. Normalize the CRLF of deploy/*.sh and deploy/Caddyfile* to the b83a paradigm.
- Execute bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only on ECS, exit code 0. The static check within the script is 8 PASS / 0 FAIL; the release directory is /var/www/agentsql/releases/20261011T162119Z. Leave traces:/root/site-deploy-post3.log.
- Execute bash ./deploy/verify-static.sh https://agentsql.cn stage-b on the public network, verification time **2026-10-11T16:21:40Z** (Beijing time 2026-10-12 00:21:40), exit code 0, **23 PASS / 0 FAIL**. Traces left: ECS /root/site-verify-post3.log, local dist/release-post3/site-verify.log.
- Fetched https://agentsql.cn/ and https://agentsql.cn/index.html again at **2026-10-11T16:22:15Z** (Beijing time **2026-10-12 00:22:15**); both returned HTTP 200. Both response bodies had SHA-256 578a7fb0fc3a73666d91adc69ac1a3034be71564efcc9c081a5c675aec6bc979, matching the local homepage. Item-by-item checks with rg -n -o -F found category A 10/10 and category B 4/4 on each URL, and four historical v0.4.0 mentions on each. Fetch and match records are in dist/release-post3/homepage-fetch.log and dist/release-post3/homepage-grep.log.

## Documents and legacy issues

- Source code changes for this batch: website/public/index.html; this report: docs/postrelease-post3-report.md.
- Only git-ignored products for this batch of temporary storage: dist/release-post3/ (archive, verification log, two homepage crawls and grep records). Other contents of dist/ are not cleaned.
- There are no remaining issues in this batch. No git write operations were performed; the protected untracked path was not touched.
