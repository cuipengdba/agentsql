# AgentSQL 官网部署说明

目标系统为 Alibaba Cloud Linux 3，流程兼容 RHEL 8 系的 `dnf`、systemd、firewalld 与 SELinux enforcing。站点分两个阶段上线：阶段 A 仅回环预览，阶段 B 在备案与 DNS 就绪后启用正式域名和自动 HTTPS。

## 1. 前置与网络边界

- ECS 安全组仅开放 22、80、443；22 建议限制为管理员来源 IP。
- 备案完成前不要对公网开放 Web，不要提前反复触发 ACME 签发。
- 阶段 A 不需要在安全组或 firewalld 开放 8080，因为 Caddy 只监听 `127.0.0.1:8080`。
- 上传整个 `website/` 目录或一个只含 `public/` 的发布目录；部署脚本只会复制公开白名单内容。
- 示例服务器地址统一使用文档地址 `203.0.113.10`，实际执行时替换为自己的 ECS 地址。

官方 COPR 可访问时，部署脚本会依次执行：

```bash
dnf install -y 'dnf-command(copr)'
dnf copr enable -y @caddy/caddy
dnf install -y caddy
```

如果国内网络无法访问 COPR，应在可信联网环境从 Caddy 官方仓库取得并核验一个固定版本 RPM，再传到服务器，以本地文件安装；不要使用 `curl | sh`：

```bash
dnf install -y ./caddy-fixed-version.x86_64.rpm
```

## 2. 阶段 A：回环预览

假设已经把 `website/` 上传到 `/root/website-release/`：

```bash
cd /root/website-release
bash ./deploy/deploy-alinux3.sh .
curl -I http://127.0.0.1:8080/
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

脚本会创建 `/var/www/agentsql/releases/<UTC时间戳>/`，仅复制 `public/` 白名单内容，把 `current` 更新为 root 拥有的符号链接，安装阶段 A 配置和 systemd drop-in，执行 SELinux 标记、Caddy 配置校验并 reload-or-restart。

从管理电脑建立 SSH 隧道：

```bash
ssh -L 8080:127.0.0.1:8080 root@203.0.113.10
```

保持隧道连接，在本地浏览器打开 `http://127.0.0.1:8080/`。此阶段不改 DNS、不开放 8080，也不申请证书。

## 3. 阶段 B：备案后切换

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

5. 安装阶段 B 配置并在 reload 前校验：

   ```bash
   install -o root -g root -m 0644 ./deploy/Caddyfile.common /etc/caddy/Caddyfile.common
   install -o root -g root -m 0644 ./deploy/Caddyfile.stage-b /etc/caddy/Caddyfile
   caddy validate --config /etc/caddy/Caddyfile
   systemctl reload caddy
   ```

6. 观察日志，确认 DNS 解析正确且 ACME 证书签发成功：

   ```bash
   journalctl -u caddy -f
   ```

7. 验证 HTTPS、HTTP 到 HTTPS 跳转、安全头和静态资源：

   ```bash
   bash ./deploy/verify-static.sh https://agentsql.cn stage-b
   ```

8. 验证后确认阶段 A 的 8080 监听已消失；如曾临时添加相关防火墙或安全组规则，应删除。HSTS 初始只使用 `max-age=300`，稳定运行并确认所有资源都可经 HTTPS 获取、没有紧急回退需求后，再评估逐步提高；当前不启用 `includeSubDomains` 或 `preload`。

不要在 DNS 尚未指向正确主机、80/443 不通或备案前反复切换阶段 B，以免无意义触发 ACME 失败与频率限制。

## 4. SELinux 与 firewalld 排查

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

## 5. 版本化发布与回滚

每次执行部署脚本都会新建一个 UTC 时间戳目录，旧版本保留。查看版本：

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

## 6. 日志与轮转

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

## 7. 常见故障

### 阶段 A 从公网打不开

这是预期行为。阶段 A 只监听回环，必须使用 SSH 隧道；不要为 8080 添加公网规则。

### 阶段 B 证书签发失败

先核对 A 记录是否解析到当前 ECS，80/443 是否同时通过安全组和 firewalld，CAA 是否允许签发，以及系统时间是否准确。修复根因后再 reload，避免反复请求。

### 页面能开但资源 404

确认 `readlink -f /var/www/agentsql/current` 指向完整发布目录，`assets/app.css`、`assets/app.js` 与截图存在，SELinux 标签为 `httpd_sys_content_t`。不要把 `website/` 本身直接当文档根；文档根必须是其 `public/` 内容。

### 修改配置后 reload 失败

先运行 `caddy validate --config /etc/caddy/Caddyfile`，再查看 `journalctl -u caddy`。保留当前运行进程，修好配置后 reload，不要用未经校验的配置覆盖后直接重启。
