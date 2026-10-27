# AgentSQL 产品官网

**语言 / Language:** [简体中文](README.md) | [English](README.en.md)

纯静态、无构建步骤的产品官网。站点公开根目录是 `public/`，Caddy 与 Alibaba Cloud Linux 3 部署材料位于 `deploy/`。

## 目录职责

- `public/index.html`：产品单页、SEO 信息、无障碍语义与内联链路图。
- `public/404.html`：自定义 404 页面。
- `public/favicon.svg`：盾牌与数据库图形的 SVG 站点图标。
- `public/assets/app.css`：主题、布局、组件、响应式、打印和减少动态效果规则。
- `public/assets/app.js`：移动导航与年份增强；禁用 JavaScript 不影响正文和原生折叠内容。
- `public/assets/screenshots/`：从 `docs/images/` 原样复制并重命名的 11 张演示截图。
- `public/robots.txt`、`public/sitemap.xml`、`public/.well-known/security.txt`：搜索与安全联系信息。
- `deploy/`：Caddy 两阶段配置、systemd drop-in、部署脚本、验收脚本和运维文档。

## 本地预览

本目录不需要 Node.js、包管理器或前端构建工具。请选择任意已有的静态文件服务器，将文档根目录指向 `website/public/`。不要双击 HTML 作为最终验收方式，因为以 `/` 开头的资源路径需要 HTTP 文档根。

官网为纯静态站，无运行时构建；仅在需要重新生成手册 PDF 时运行 `node website/tools/build-docs-pdf.mjs`（需本机 Node 与 Microsoft Edge）。`website/public/assets`（含 `assets/docs/*.pdf`）由部署脚本递归复制，无需改部署白名单。

部署后在服务器上执行静态验收脚本（示例为阶段 A 的回环地址）：

```bash
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

## 发布约束

- 只发布 `public/` 内容；不得把 `deploy/`、仓库根目录、`.git/` 或环境文件放入文档根。
- 阶段 A 只监听 `127.0.0.1:8080`，通过 SSH 隧道预览；备案、DNS 和防火墙就绪后才切换阶段 B。
- 当前版本页面仅在对应 GitHub Release、双架构镜像和版本化 PDF 通过验收后部署；Release 按钮必须指向同版本正式发布页。公开 Demo 的部署拓扑须在页面明确说明。
- 站点没有 Cookie 或第三方统计。Caddy 访问日志默认按配置保留 720 小时。
- 开源许可链接指向仓库 `LICENSE`，并保留 `NOTICE` 的链接。

## 图标与分享资产

- `public/assets/og-cover.png`：1200×630 社交分享卡，已在 `og:image` / Twitter Card 引用。
- `public/apple-touch-icon.png`（及 `public/assets/` 下同名副本）：180×180 iOS 主屏幕图标。
- `public/favicon.svg`：站点图标；不提供 `favicon.ico`，现代浏览器均使用 SVG 图标。

新增或替换二进制资产后，应同步更新部署白名单并重新运行验收脚本。

## 阶段 B（域名与 HTTPS）运维检查

- 页脚已展示备案号；每次发布复核备案号与官方查询链接，不能回退成占位文本。
- 阶段 B 已上线；日常内容更新按 `deploy/README-DEPLOY.md` 使用 `--content-only`，不重装 Caddy 或覆盖当前 Caddyfile。
- HSTS 先保持 `max-age=300`；稳定运行并确认无回退需求后再评估提高。
- `/demo/` 是稳定的静态安全入口；控制台使用独立源反代回环上游，因为当前 Web 构建固定使用根 `/assets`、`/api/v1` 与 BrowserRouter，不能直接部署到子路径。
- 公网演示开放前，确认仅合成数据、只读或受控剧本、定期重置、不接真实数据库，并确认 7780、17880、5432、3306 均未直接公网暴露。

## 发布日收录闸门

官网已处于阶段 B：主页为 `index,follow`，演示页仍为 `noindex,nofollow`，`robots.txt` 允许主页并排除 `/demo/`。每次更新先确认对应正式 Release、镜像与 PDF 已就绪，再用 `verify-static.sh` 检查这些开关及站内引用，最后通过 `--content-only` 发布。
