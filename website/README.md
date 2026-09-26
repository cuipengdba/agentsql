# AgentSQL 产品官网

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
- 页面与仓库版本口径统一为 `v0.4.0`；正式发布前再复核一次页面与 Release 状态。
- 站点没有 Cookie 或第三方统计。Caddy 访问日志默认按配置保留 720 小时。
- 开源授权链接指向仓库 `LICENSE`；商业授权链接指向已存在的 `COMMERCIAL-LICENSE.md`。

## 图标与分享资产

- `public/assets/og-cover.png`：1200×630 社交分享卡，已在 `og:image` / Twitter Card 引用。
- `public/apple-touch-icon.png`（及 `public/assets/` 下同名副本）：180×180 iOS 主屏幕图标。
- `public/favicon.svg`：站点图标；不提供 `favicon.ico`，现代浏览器均使用 SVG 图标。

新增或替换二进制资产后，应同步更新部署白名单并重新运行验收脚本。

## 阶段 B（域名与 HTTPS）上线待办

- 备案号与公安联网备案链接取得后再加入页脚，不填占位号码或图标。
- 备案完成、DNS A 记录生效且 80/443 放行后，按 `deploy/README-DEPLOY.md` 切换阶段 B。
- HSTS 先保持 `max-age=300`；稳定运行并确认无回退需求后再评估提高。
- 公网演示开放前，确认只读合成数据、每日重置和不接真实数据库的边界。
