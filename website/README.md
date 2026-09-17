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

按工单约束，本次实现未启动本地或真实服务器。正式部署前应在 ECS 上执行：

```bash
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

## 发布约束

- 只发布 `public/` 内容；不得把 `deploy/`、仓库根目录、`.git/` 或环境文件放入文档根。
- 阶段 A 只监听 `127.0.0.1:8080`，通过 SSH 隧道预览；备案、DNS 和防火墙就绪后才切换阶段 B。
- 当前版本口径为 `v0.2.0-rc` 发布候选。正式发布前需统一复核页面与仓库版本状态。
- 站点没有 Cookie 或第三方统计。Caddy 访问日志默认按配置保留 720 小时。
- 开源授权链接指向仓库 `LICENSE`；商业授权链接指向已存在的 `COMMERCIAL-LICENSE.md`。

## 待主控补二进制

以下文件本次不生成，也未写入可加载的 HTML 标签，避免缺失资源请求：

- `public/assets/og-cover.png`：1200×630；补齐后启用 `og:image`。
- `public/apple-touch-icon.png`：建议 180×180；补齐后启用 `apple-touch-icon`。
- `favicon.ico`：仅在确定需要兼容旧客户端时补充；当前使用 `favicon.svg`。

补齐前请保留 `index.html` 头部的注释占位。新增二进制后还应更新部署白名单与验收清单。

## 仍需人工确认

- 备案号与公安联网备案链接取得后再加入页脚，不填占位号码或图标。
- 备案完成、DNS A 记录生效且 80/443 放行后，按 `deploy/README-DEPLOY.md` 切换阶段 B。
- HSTS 先保持 `max-age=300`；稳定运行并确认无回退需求后再人工评估提高。
- 公网演示开放前，确认只读合成数据、每日重置和不接真实数据库的边界。
