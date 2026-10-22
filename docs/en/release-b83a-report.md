# Batch 83a (batch830) releases verification report

**Language:** [English](release-b83a-report.md) | [Simplified Chinese](../release-b83a-report.md)

Date: 2026-10-10

## Live Demo v0.5.0

- ECS: `root@47.105.71.43`, directory `/opt/agentsql-demo`.
- `docker-compose.yml` only changes the PostgreSQL binder to `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5` and the seed/gateway to `ghcr.io/cuipengdba/agentsql:v0.5.0-demo`; MySQL keeps `mysql:8`.
- `examples/docker/config.demo.yaml`, `examples/docker/demo-seed.yaml` and `demo/postgres/01_schema.sql` with changes have been synchronized. ECS SHA256 are `a0cb3e1bf1eb0b6878839107b524cbf147344ecefdb7b6e868279d71bef84813`, `715ae699e24fd96fcf8f1ddf87034a9e75e59b84ae2409310f4dce49e2ae101e`, and `199a6351fbe94bedfb72d0bdf8e191a6ba6f47561cbb2c45cf5b5d5693005ee3`, which are consistent with the local machine; other initialization script hashes are consistent.
- Complete reset command: `bash /root/ecs-sync-demo.sh`, exit code 0; ECS trace: `/root/demo-reset-b83a.log`, including `DEMO_SEED_OK` once.
- `docker ps` shows that the three containers of PostgreSQL binder `16-0.5`, MySQL `mysql:8`, and gateway `v0.5.0-demo` are all healthy: `DEMO_CONTAINERS_HEALTHY_PASS`.
- `GET https://demo.agentsql.cn/healthz`：`status=ok`、`version=v0.5.0`；`GET /readyz`：`status=ready`。
- Console `admin` Login successful: `DEMO_LOGIN_PASS`. The login password and returned session credentials are not written to the log. See `dist/release-b83a/demo-verify.log` for the above evidence.

## Brochure PDF

- Modify the version of `website/tools/build-docs-pdf.mjs`, the three output file names and the fixed date `D:20261010000000+00'00'`; the footer is changed to reference the version constant. The source document now has no images, and the obsolete Quick Start mandatory PNG assertion has been removed.
- Modify the three PDF download links and download file names of `website/public/index.html`, and modify the three expected file names of `website/deploy/verify-static.sh`.
- Execute `node website/tools/build-docs-pdf.mjs` to delete the three old PDFs. New files all contain a `%PDF-` header and a `%%EOF` footer, and the creation/modification time metadata is a fixed date; the rendering of the cover and representative inner pages and the v0.5.0 footer were checked.

| PDF | Number of bytes |
| --- | ---: |
| `agentsql-getting-started-v0.5.0.pdf` | 453,336 |
| `agentsql-user-guide-v0.5.0.pdf` | 1,274,820 |
| `agentsql-mcp-integrations-v0.5.0.pdf` | 567,123 |

## Official website content-only release

- The machine packages `website/{deploy,public,tools,README.md}` as `dist/release-b83a/site-v05.tar.gz`, uploads and decompresses it to ECS `/root/website-release-b83a`; remove CRLF for `deploy/*.sh` and `deploy/Caddyfile*`.
- Published using `bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only`, exit code 0; ECS trace: `/root/site-deploy-b83a-final.log`. The static gate in the script passes, and Caddy is not installed or the Caddyfile is modified.
- Public network `bash ./deploy/verify-static.sh https://agentsql.cn stage-b`: 23 PASS, 0 FAIL, see `dist/release-b83a/site-verify.log` for details.
- The three public PDFs are all HTTP 200, `application/pdf`, and the download bytes are 453,336, 1,274,820, and 567,123 respectively, which are consistent with the local files.

## PVR and search release

- GitHub read-only endpoint `/repos/cuipengdba/agentsql/private-vulnerability-reporting` returns `enabled=true`: `PVR_ENABLED_PASS`.
- Online `robots.txt` includes `Allow: /`, `Disallow: /demo/`, `Sitemap: https://agentsql.cn/sitemap.xml`; the homepage is `index,follow`, `/demo/` is `noindex,nofollow`: `SEARCH_GATE_PASS`.

## Conclusion

Live Demo v0.5.0 complete reset, health endpoint and login passed; official website content-only publishing, three PDFs, public network stage B, PVR and search gate all passed. No git write operations were performed, and the two protected untracked paths were not touched.
