# Batch 83a（batch830）发布核验报告

日期：2026-10-10

## Live Demo v0.5.0

- ECS：`root@47.105.71.43`，目录 `/opt/agentsql-demo`。
- `docker-compose.yml` 仅将 PostgreSQL binder 改为 `ghcr.io/cuipengdba/agentsql-postgres-binder:16-0.5`，seed/gateway 改为 `ghcr.io/cuipengdba/agentsql:v0.5.0-demo`；MySQL 保持 `mysql:8`。
- 已同步 `examples/docker/config.demo.yaml`、`examples/docker/demo-seed.yaml` 与有变化的 `demo/postgres/01_schema.sql`。ECS SHA256 分别为 `a0cb3e1bf1eb0b6878839107b524cbf147344ecefdb7b6e868279d71bef84813`、`715ae699e24fd96fcf8f1ddf87034a9e75e59b84ae2409310f4dce49e2ae101e`、`199a6351fbe94bedfb72d0bdf8e191a6ba6f47561cbb2c45cf5b5d5693005ee3`，与本机一致；其他初始化脚本哈希一致。
- 完整重置命令：`bash /root/ecs-sync-demo.sh`，退出码 0；ECS 留痕：`/root/demo-reset-b83a.log`，包含一次 `DEMO_SEED_OK`。
- `docker ps` 显示 PostgreSQL binder `16-0.5`、MySQL `mysql:8`、gateway `v0.5.0-demo` 三容器均为 healthy：`DEMO_CONTAINERS_HEALTHY_PASS`。
- `GET https://demo.agentsql.cn/healthz`：`status=ok`、`version=v0.5.0`；`GET /readyz`：`status=ready`。
- 控制台 `admin` 登录成功：`DEMO_LOGIN_PASS`。登录密码及返回的会话凭据未写入日志。以上证据见 `dist/release-b83a/demo-verify.log`。

## 手册 PDF

- 修改 `website/tools/build-docs-pdf.mjs` 的版本、三份输出文件名及固定日期 `D:20261010000000+00'00'`；页脚改为引用版本常量。源文档现无图片，移除了过时的《快速上手》强制 PNG 断言。
- 修改 `website/public/index.html` 的三个 PDF 下载链接和下载文件名，修改 `website/deploy/verify-static.sh` 的三份预期文件名。
- 执行 `node website/tools/build-docs-pdf.mjs`，删除旧版三份 PDF。新文件均含 `%PDF-` 头和 `%%EOF` 尾，创建/修改时间元数据均为固定日期；检查了封面及代表性内页的渲染和 v0.5.0 页脚。

| PDF | 字节数 |
| --- | ---: |
| `agentsql-getting-started-v0.5.0.pdf` | 453,336 |
| `agentsql-user-guide-v0.5.0.pdf` | 1,274,820 |
| `agentsql-mcp-integrations-v0.5.0.pdf` | 567,123 |

## 官网 content-only 发布

- 本机打包 `website/{deploy,public,tools,README.md}` 为 `dist/release-b83a/site-v05.tar.gz`，上传并解压到 ECS `/root/website-release-b83a`；对 `deploy/*.sh` 与 `deploy/Caddyfile*` 去 CRLF。
- 使用 `bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only` 发布，退出码 0；ECS 留痕：`/root/site-deploy-b83a-final.log`。脚本内静态闸门通过，未安装 Caddy 或改 Caddyfile。
- 公网 `bash ./deploy/verify-static.sh https://agentsql.cn stage-b`：23 PASS、0 FAIL，详见 `dist/release-b83a/site-verify.log`。
- 三份公网 PDF 均为 HTTP 200、`application/pdf`，下载字节数分别为 453,336、1,274,820、567,123，与本机文件一致。

## PVR 与搜索放行

- GitHub 只读端点 `/repos/cuipengdba/agentsql/private-vulnerability-reporting` 返回 `enabled=true`：`PVR_ENABLED_PASS`。
- 线上 `robots.txt` 含 `Allow: /`、`Disallow: /demo/`、`Sitemap: https://agentsql.cn/sitemap.xml`；首页为 `index,follow`，`/demo/` 为 `noindex,nofollow`：`SEARCH_GATE_PASS`。

## 结论

Live Demo v0.5.0 完整重置、健康端点和登录通过；官网 content-only 发布、三份 PDF、公网 stage B、PVR 与搜索闸门全部通过。未执行 git 写操作，未触碰两处受保护未跟踪路径。
