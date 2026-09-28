# AgentSQL 官网部署说明

目标系统为 Alibaba Cloud Linux 3，流程兼容 RHEL 8 系的 `dnf`、systemd、firewalld 与 SELinux enforcing。站点分两个阶段上线：阶段 A 仅回环预览，阶段 B 在备案与 DNS 就绪后启用正式域名和自动 HTTPS。

## 1. 前置与网络边界

- ECS 安全组仅开放 22、80、443；22 建议限制为管理员来源 IP。
- 备案完成前不要对公网开放 Web，不要提前反复触发 ACME 签发。
- 阶段 A 不需要在安全组或 firewalld 开放 8080，因为 Caddy 只监听 `127.0.0.1:8080`。
- 上传整个 `website/` 目录或一个只含 `public/` 的发布目录；部署脚本只会复制公开白名单内容。
- 示例服务器地址统一使用文档地址 `203.0.113.10`，实际执行时替换为自己的 ECS 地址。

### 安装 Caddy：EPEL 优先、COPR 兜底

Alibaba Cloud Linux 3 启用 EPEL 后可直接安装 EPEL 提供的 Caddy 2.6.4，这是首选路径：

```bash
dnf install -y caddy
```

部署脚本具有幂等安装判断：已存在 `caddy` 命令时跳过安装；未安装时先尝试 EPEL，只有该路径失败或没有可用包时，才自动回退到 `dnf-command(copr)`、`@caddy/caddy` 和 COPR 安装流程。这样在国内网络无法访问 COPR 时，不会无故依赖 COPR。

离线环境应在可信联网环境取得并核验固定版本 RPM，再传到服务器，以本地文件安装；禁止使用 `curl | sh`：

```bash
dnf install -y ./caddy-*.rpm
```

## 2. 镜像市场模板机清理（仅当 80/443 被占用时的可选前置）

普通干净镜像不需要本节。部分镜像市场模板机会预装 Harbor、Nginx Proxy Manager、Portainer 等 Docker Compose 环境，并在开机后占用 80/443。清理前先记录容器、镜像、卷和端口现状，确认这些业务确实无用：

```bash
ss -tlnp
docker ps -a
docker images
docker volume ls
```

结合监听进程和容器端口映射确认占用者；常见模板目录包括 `/clouddream/harbor` 和 `/clouddream/nginx-proxy-manage`。只对已确认无用的对应项目执行清理：

```bash
cd /clouddream/harbor
docker compose down -v

cd /clouddream/nginx-proxy-manage
docker compose down -v

docker stop portainer
docker rm portainer
```

若独立 Portainer 容器不叫 `portainer`，用 `docker ps -a` 中记录的实际容器名或 ID 替换上述参数。

再次确认没有需要保留的容器、镜像和卷后，才执行全局回收：

```bash
docker system prune -af --volumes
```

若模板附带 enabled 的 Harbor systemd unit，还需停用并删除它，防止重启后重新拉起 Compose 项目：

```bash
systemctl disable --now harbor.service
rm -f /etc/systemd/system/harbor.service
systemctl daemon-reload
```

> **警告：阿里云 `remote-manage.service`（监听 5000）、aegis、cloudmonitor 等实例管控/安全组件必须保留，绝不能停用或删除。** 它们不是上述模板业务环境的一部分；不要仅凭端口或进程不熟悉就清理。

完成后再次运行 `ss -tlnp` 和 `docker ps`，确认 80/443 已释放，再继续部署。

## 3. 阶段 A：回环预览

假设已经把 `website/` 上传到 `/root/website-release/`：

```bash
cd /root/website-release
bash ./deploy/deploy-alinux3.sh --stage a --src .
curl -I http://127.0.0.1:8080/
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

`/demo/` 是无需登录即可读取的静态安全落地页。当前控制台不支持 base-path：`internal/webui/dist/index.html` 使用绝对 `/assets/...`，`web/vite.config.ts` 的 `base` 为 `/`，路由使用无 `basename` 的 `createBrowserRouter`，API 客户端固定为 `/api/v1`。因此不要对 HTML 正文做字符串替换，也不要把控制台直接 `handle_path` 到 `/demo`。

阶段 A 采用独立回环源：主站的 `/demo/console` 跳转到 `http://127.0.0.1:8081/`，8081 再反代 `DEMO_UPSTREAM`。默认值是 `127.0.0.1:17880`，可在启动 Caddy 前用环境变量覆盖。两个 Caddy 监听和 Compose 的 17880、5432、3306 都必须只绑定 `127.0.0.1`：

```bash
export DEMO_UPSTREAM=127.0.0.1:17880
ss -tlnp | grep -E ':(8080|8081|7780|17880|5432|3306)\b'
curl -I http://127.0.0.1:8080/demo/
curl -I http://127.0.0.1:8080/demo/console
curl -I http://127.0.0.1:8081/
```

预期只出现回环监听；`/demo/` 返回 200，`/demo/console` 返回到 8081 的 302，8081 经反代返回控制台。演示控制台仍使用自身登录，禁止绕过认证。登录后在“演练场”逐一运行六个预置剧本，确认页面横幅说明数据会重置，并确认数据源只有 `ds-demo-pg` 与 `ds-demo-mysql` 两个合成库。

`--stage` 为必填参数，脚本不会猜测当前阶段。阶段 A 首次流程会创建 `/var/www/agentsql/releases/<UTC时间戳>/`，仅复制 `public/` 白名单内容，把 `current` 更新为 root 拥有的符号链接，安装阶段 A 配置和 systemd drop-in，执行静态校验、SELinux 标记、Caddy 配置校验并 reload-or-restart。

部署后必须核验监听地址：

```bash
ss -tlnp | grep 8080
```

结果必须只显示 `127.0.0.1:8080`，不能是 `*:8080` 或 `0.0.0.0:8080`；公网主机的 8080 不应可连。阶段 A 只能通过下面的 SSH 隧道预览。

从管理电脑建立 SSH 隧道：

```bash
ssh -L 8080:127.0.0.1:8080 root@203.0.113.10
```

保持隧道连接，在本地浏览器打开 `http://127.0.0.1:8080/`。此阶段不改 DNS、不开放 8080，也不申请证书。

## 4. 阶段 B：备案后切换

### `/demo` 与独立控制台源

stage-b 中 `https://agentsql.cn/demo/` 继续由主站静态提供，`/demo/console` 跳转到 `DEMO_PUBLIC_URL`（默认 `https://demo.agentsql.cn/`）；独立的 `demo.agentsql.cn` 站点再反代 `DEMO_UPSTREAM`（默认 `127.0.0.1:17880`）。这样控制台保持根路径语义，官网入口不依赖临时主机名。配置文件只做准备，本 Unit 禁止修改 DNS 或部署 stage-b；正式切换前必须单独确认 `demo.agentsql.cn` 的备案、DNS 与证书条件。

演示源不启用 Caddy access log，避免把 Authorization、Cookie、查询串或其他请求秘密写入官网访问日志；Caddy 也不会记录请求正文。上游应用自身的日志策略仍需在发布审核中单独检查。演示源保留安全响应头，HSTS 初始值仍是 `max-age=300`，控制台所需的内联样式仅在独立演示源 CSP 中放行。

禁止为演示开放 7780、17880、5432、3306 的安全组或 firewalld 规则。唯一公网入口应为 Caddy 的 80/443；`DEMO_UPSTREAM` 必须是回环地址。正式部署环境可显式设置：

```bash
export DEMO_UPSTREAM=127.0.0.1:17880
export DEMO_PUBLIC_URL=https://demo.agentsql.cn/
```

`www.agentsql.cn` 使用 301 跳转到 `https://agentsql.cn{uri}`，保留原路径与查询串。它和两个 HTTPS 站点都使用初始 `Strict-Transport-Security: max-age=300`，此阶段不得拉长或加入 `includeSubDomains` / `preload`。

按顺序完成以下清单：

1. 取得备案号，按实际号码和官方链接更新页脚；上线后在要求时限内完成公安联网备案，目标为 30 日内。
2. 给 `agentsql.cn` 配置指向 ECS 公网 IPv4 的 DNS A 记录。服务器未正确配置 IPv6 前不要擅自添加 AAAA。
3. 如配置 CAA，确认允许 Caddy 所使用的 ACME CA；不确定时先不要添加限制性 CAA。
4. 在 ECS 安全组开放 80/443，并在 firewalld 中开放服务：

   ```bash
   firewall-cmd --permanent --add-service=http
   firewall-cmd --permanent --add-service=https
   firewall-cmd --reload
   ```

5. 使用部署脚本完成阶段 B 首次切换；它会明确安装 `Caddyfile.stage-b`，在 reload 前执行静态与 Caddy 配置校验：

   ```bash
   bash ./deploy/deploy-alinux3.sh --stage b --src .
   ```

6. 观察日志，确认 DNS 解析正确且 ACME 证书签发成功：

   ```bash
   journalctl -u caddy -f
   ```

7. 验证 HTTPS、HTTP 到 HTTPS 跳转、安全头和静态资源：

   ```bash
   bash ./deploy/verify-static.sh https://agentsql.cn stage-b
   ```

   另行检查 `https://www.agentsql.cn/path?check=1` 为保留 URI 的 301，并检查 `/demo/`、`/demo/console` 与独立演示源。发布日前，`public/index.html` 与 `public/demo/index.html` 必须保持 `noindex,nofollow`，`public/robots.txt` 必须保持 `Disallow: /`。发布日开关位于这三个文件；只可在最终 Release、备案、DNS、证书和演示隔离全部通过后改为 `index,follow` / `Allow: /`。

8. 验证后确认阶段 A 的 8080 监听已消失；如曾临时添加相关防火墙或安全组规则，应删除。HSTS 初始只使用 `max-age=300`，稳定运行并确认所有资源都可经 HTTPS 获取、没有紧急回退需求后，再评估逐步提高；当前不启用 `includeSubDomains` 或 `preload`。

不要在 DNS 尚未指向正确主机、80/443 不通或备案前反复切换阶段 B，以免无意义触发 ACME 失败与频率限制。

### 阶段 B 日常内容发布

阶段 B 已首次切换并稳定运行后，日常发布必须使用内容模式：

```bash
bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only
```

该模式只把公开白名单静态文件同步到新的 `releases/` 目录、执行静态校验、更新 `current` 软链、校验现有 Caddy 配置并 reload；它不会安装软件、systemd drop-in 或任何 Caddyfile，尤其不会覆盖 `/etc/caddy/Caddyfile`，因此不会把阶段 B 回退到阶段 A。`--content-only` 与 `--stage a` 组合会被拒绝。

## 5. SELinux 与 firewalld 排查

部署脚本为 `/var/www/agentsql(/.*)?` 设置 `httpd_sys_content_t` 并运行 `restorecon`。遇到 403、文件不可读或服务启动失败时，不要执行 `setenforce 0`，而应检查：

```bash
getenforce
ls -lZ /var/www/agentsql/current/public
ausearch -m AVC -ts recent
journalctl -u caddy --since '30 minutes ago'
systemctl status caddy
firewall-cmd --list-all
```

若目录标签异常，重新执行：

```bash
semanage fcontext -m -t httpd_sys_content_t '/var/www/agentsql(/.*)?'
restorecon -Rv /var/www/agentsql
```

systemd 加固将站点设为只读，仅允许 Caddy 写 `/var/lib/caddy` 与 `/var/log/caddy`。如果日志或证书存储报权限错误，检查这两个目录是否为 `caddy:caddy`，不要扩大整棵文件系统的写权限。

## 6. 版本化发布与回滚

每次执行部署脚本（包括阶段 B 的 `--content-only`）都会新建一个 UTC 时间戳目录，旧版本保留。查看版本：

```bash
readlink -f /var/www/agentsql/current
find /var/www/agentsql/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort
```

回滚时选择明确的旧目录，先检查其中有 `public/index.html`，再原子更新链接、验证并 reload：

```bash
test -f /var/www/agentsql/releases/20260917T120000Z/public/index.html
ln -sfn /var/www/agentsql/releases/20260917T120000Z /var/www/agentsql/current
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

默认脚本创建 `<时间戳>/public/index.html`，与 Caddy 的 `current/public` 文档根保持一致。不要删除 `/var/lib/caddy`，其中包含自动 HTTPS 所需状态；回滚站点文件不等于回滚证书存储。

## 7. 日志与轮转

- 访问日志：`/var/log/caddy/agentsql-access.json`，JSON 格式。
- 应用配置轮转：单文件 10 MiB、保留 10 个、最长 720 小时。
- 服务与 ACME 日志：`journalctl -u caddy`。

常用检查：

```bash
journalctl -u caddy --since today
tail -n 100 /var/log/caddy/agentsql-access.json
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

## 8. 常见故障

### 阶段 A 从公网打不开

这是预期行为。阶段 A 只监听回环，必须使用 SSH 隧道；不要为 8080 添加公网规则。

### 阶段 B 证书签发失败

先核对 A 记录是否解析到当前 ECS，80/443 是否同时通过安全组和 firewalld，CAA 是否允许签发，以及系统时间是否准确。修复根因后再 reload，避免反复请求。

### 页面能开但资源 404

确认 `readlink -f /var/www/agentsql/current` 指向完整发布目录，`assets/app.css`、`assets/app.js` 与截图存在，SELinux 标签为 `httpd_sys_content_t`。不要把 `website/` 本身直接当文档根；文档根必须是其 `public/` 内容。

### 修改配置后 reload 失败

先运行 `caddy validate --config /etc/caddy/Caddyfile`，再查看 `journalctl -u caddy`。保留当前运行进程，修好配置后 reload，不要用未经校验的配置覆盖后直接重启。
