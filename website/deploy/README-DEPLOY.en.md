# AgentSQL official website deployment instructions

**Language:** [English](README-DEPLOY.en.md) | [Simplified Chinese](README-DEPLOY.md)

The target system is Alibaba Cloud Linux 3, and the process is compatible with `dnf`, systemd, firewalld and SELinux enforcing of RHEL 8 series. The site goes live in two phases: Phase A is a loopback preview only, and Phase B enables official domain names and automatic HTTPS after registration and DNS are ready.

## 1. Frontend and network boundaries

- ECS security groups are only open to 22, 80, and 443; 22 is recommended to be limited to the administrator source IP.
- Do not open the Web to the public network before the registration is completed, and do not trigger ACME issuance repeatedly in advance.
- Phase A does not require opening 8080 in the security group or firewalld since Caddy only listens on `127.0.0.1:8080`.
- Upload the entire `website/` directory or a release directory containing only `public/`; the deployment script will only copy the public whitelisted content.
- The example server address uniformly uses the document address `203.0.113.10`, and replaces it with your own ECS address during actual execution.

### Installing Caddy: EPEL first, COPR safe

After enabling EPEL on Alibaba Cloud Linux 3, you can directly install Caddy 2.6.4 provided by EPEL. This is the preferred path:

```bash
dnf install -y caddy
```

The deployment script has idempotent installation judgment: if the `caddy` command already exists, the installation will be skipped; if it is not installed, EPEL will be tried first. Only when the path fails or there is no available package, it will automatically fall back to the `dnf-command(copr)`, `@caddy/caddy` and COPR installation processes. In this way, COPR will not be relied on for no reason when the domestic network cannot access COPR.

The offline environment should obtain and verify the fixed version RPM in a trusted network environment, then transfer it to the server and install it as a local file; the use of `curl | sh` is prohibited:

```bash
dnf install -y ./caddy-*.rpm
```

## 2. Image market template machine cleaning (optional pre-installation only when 80/443 is occupied)

Ordinary clean images do not require this section. Some image market templates are pre-installed with Docker Compose environments such as Harbor, Nginx Proxy Manager, and Portainer, and occupy 80/443 after booting. Before cleaning, record the current status of containers, images, volumes and ports to confirm that these services are indeed useless:

```bash
ss -tlnp
docker ps -a
docker images
docker volume ls
```

Confirm the occupier by combining the listening process and container port mapping; common template directories include `/clouddream/harbor` and `/clouddream/nginx-proxy-manage`. Only clean corresponding items that have been confirmed to be useless:

```bash
cd /clouddream/harbor
docker compose down -v

cd /clouddream/nginx-proxy-manage
docker compose down -v

docker stop portainer
docker rm portainer
```

If the independent Portainer container is not called `portainer`, replace the above parameters with the actual container name or ID recorded in `docker ps -a`.

Reconfirm that there are no containers, images, or volumes that need to be retained before performing global recycling:

```bash
docker system prune -af --volumes
```

If the template comes with the enabled Harbor systemd unit, you need to disable and delete it to prevent the Compose project from being restarted after restarting:

```bash
systemctl disable --now harbor.service
rm -f /etc/systemd/system/harbor.service
systemctl daemon-reload
```

> **Warning: Alibaba Cloud `remote-manage.service` (monitoring 5000), aegis, cloudmonitor and other instance management/security components must be retained and must not be deactivated or deleted.** They are not part of the business environment of the template above; do not clean up just because the port or process is unfamiliar.

After completion, run `ss -tlnp` and `docker ps` again to confirm that 80/443 has been released before continuing the deployment.

## 3. Phase A: Loopback Preview

Assume that `website/` has been uploaded to `/root/website-release/`:

```bash
cd /root/website-release
bash ./deploy/deploy-alinux3.sh --stage a --src .
curl -I http://127.0.0.1:8080/
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

`/demo/` is a static secure landing page that can be read without logging in. The current console does not support base-path: `internal/webui/dist/index.html` uses absolute `/assets/...`, `base` for `web/vite.config.ts` is `/`, routes use `createBrowserRouter` without `basename`, and API clients are fixed to `/api/v1`. So don't do string replacement on the HTML body, and don't put the console directly `handle_path` to `/demo`.

Phase A uses an independent loopback source: `/demo/console` of the master station jumps to `http://127.0.0.1:8081/`, and 8081 then back-generates `DEMO_UPSTREAM`. The default value is `127.0.0.1:17880`, which can be overridden with an environment variable before starting Caddy. Both Caddy monitors and Compose's 17880, 5432, 3306 must only be bound to `127.0.0.1`:

```bash
export DEMO_UPSTREAM=127.0.0.1:17880
ss -tlnp | grep -E ':(8080|8081|7780|17880|5432|3306)\b'
curl -I http://127.0.0.1:8080/demo/
curl -I http://127.0.0.1:8080/demo/console
curl -I http://127.0.0.1:8081/
```

Only loopback monitoring is expected; `/demo/` returns 200, `/demo/console` returns 302 of 8081, and 8081 returns to the console via inversion. The demo console still uses itself to log in, and bypassing authentication is prohibited. After logging in, run the six preset scripts one by one in the "Practice Field", confirm that the page banner states that the data will be reset, and confirm that the data sources are only two synthesis libraries: `ds-demo-pg` and `ds-demo-mysql`.

`--stage` is a required parameter and the script will not guess the current stage. The Phase A first pass creates `/var/www/agentsql/releases/<UTC时间戳>/`, copies only the `public/` whitelist content, updates `current` to a root-owned symlink, installs the Phase A configuration and systemd drop-in, performs static verification, SELinux tagging, Caddy configuration verification and reload-or-restart.

The listening address must be verified after deployment:

```bash
ss -tlnp | grep 8080
```

The result must only display `127.0.0.1:8080`, not `*:8080` or `0.0.0.0:8080`; the 8080 of the public network host should not be connectable. Phase A can only be previewed via the SSH tunnel below.

Establish an SSH tunnel from the management computer:

```bash
ssh -L 8080:127.0.0.1:8080 root@203.0.113.10
```

Keep the tunnel connected and open `http://127.0.0.1:8080/` in your local browser. At this stage, the DNS is not changed, 8080 is not opened, and no certificate is applied for.

## 4. Phase B: Switching after filing

### `/demo` with standalone console source

In stage-b, `https://agentsql.cn/demo/` continues to be provided statically by the main site, `/demo/console` jumps to `DEMO_PUBLIC_URL` (default `https://demo.agentsql.cn/`); the independent `demo.agentsql.cn` site then inverts to `DEMO_UPSTREAM` (default `127.0.0.1:17880`). In this way, the console maintains root path semantics, and the official website entrance does not rely on the temporary host name. The configuration file is only for preparation. This Unit is prohibited from modifying DNS or deploying stage-b; the registration, DNS and certificate conditions of `demo.agentsql.cn` must be confirmed separately before the official switch.

The demo source does not enable Caddy access log to avoid secretly writing Authorization, Cookie, query string or other requests into the official website access log; Caddy will not record the request body. The upstream application's own logging policy still needs to be checked separately in the release review. The demo source retains the security response header, the HSTS initial value is still `max-age=300`, and the inline styles required by the console are only released in the standalone demo source CSP.

It is not allowed to open security groups or firewalld rules for 7780, 17880, 5432, 3306 for demo. The only public network entrance should be Caddy's 80/443; `DEMO_UPSTREAM` must be the loopback address. The formal deployment environment can be set explicitly:

```bash
export DEMO_UPSTREAM=127.0.0.1:17880
export DEMO_PUBLIC_URL=https://demo.agentsql.cn/
```

`www.agentsql.cn` uses 301 to jump to `https://agentsql.cn{uri}`, retaining the original path and query string. It and both HTTPS sites use the initial `Strict-Transport-Security: max-age=300`, which must not be stretched or joined at this stage to `includeSubDomains` / `preload`.

Complete the following checklist in order:

1. Obtain the registration number, update the footer according to the actual number and official link; complete the public security online registration within the required time limit after going online, the target is within 30 days.
2. Configure `agentsql.cn` with a DNS A record pointing to the ECS public network IPv4. Do not add AAAA without permission before the server is properly configured with IPv6.
3. If configuring CAA, confirm that the ACME CA used by Caddy is allowed; do not add restrictive CAA if you are not sure yet.
4. Open 80/443 in the ECS security group and open the service in firewalld:

   ```bash
   firewall-cmd --permanent --add-service=http
   firewall-cmd --permanent --add-service=https
   firewall-cmd --reload
   ```

5. Use the deployment script to complete the first phase B switch; it will explicitly install `Caddyfile.stage-b` and perform static and Caddy configuration verification before reloading:

   ```bash
   bash ./deploy/deploy-alinux3.sh --stage b --src .
   ```

6. Observe the logs to confirm that the DNS resolution is correct and the ACME certificate is successfully issued:

   ```bash
   journalctl -u caddy -f
   ```

7. Verify HTTPS, HTTP to HTTPS jumps, security headers, and static resources:

   ```bash
   bash ./deploy/verify-static.sh https://agentsql.cn stage-b
   ```

   Also check `https://www.agentsql.cn/path?check=1` for a 301 reserved URI, and `/demo/`, `/demo/console` with standalone demo sources. Before the release date, `public/index.html` and `public/demo/index.html` must remain `noindex,nofollow`, and `public/robots.txt` must remain `Disallow: /`. The release date switch is located in these three files; it can only be changed to `index,follow` / `Allow: /` after the final Release, filing, DNS, certificate and demonstration isolation are all passed.

8. After verification, confirm that the 8080 listening in stage A has disappeared; if relevant firewall or security group rules have been temporarily added, they should be deleted. HSTS initially only uses `max-age=300`. After stable operation and confirmation that all resources can be obtained via HTTPS and there is no emergency rollback requirement, HSTS will be evaluated and gradually improved; `includeSubDomains` or `preload` is not currently enabled.

Do not repeatedly switch to phase B before DNS points to the correct host, 80/443 is unavailable, or before registration, to avoid meaningless triggering of ACME failures and frequency limits.

### Phase B Daily content release

After Phase B has been switched over for the first time and is running stably, content mode must be used for daily publishing:

```bash
bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only
```

This mode only synchronizes publicly whitelisted static files to the new `releases/` directory, performs static verification, restores new directory labels when SELinux is enabled, verifies existing Caddy configuration, atomically updates the `current` soft link and reloads. When switching, re-verification or reload fails, the script will try to restore the old version and reload again; if the automatic rollback fails, follow the manual rollback steps below. It does not install the software, the systemd drop-in, or any Caddyfile, and in particular it does not overwrite `/etc/caddy/Caddyfile` and therefore does not roll back stage B to stage A. The combination `--content-only` and `--stage a` will be rejected.

## 5. Troubleshooting SELinux and firewalld

The deployment script sets up `httpd_sys_content_t` for `/var/www/agentsql(/.*)?` and runs `restorecon`. When encountering 403, file unreadable or service startup failure, do not execute `setenforce 0`, but check:

```bash
getenforce
ls -lZ /var/www/agentsql/current/public
ausearch -m AVC -ts recent
journalctl -u caddy --since '30 minutes ago'
systemctl status caddy
firewall-cmd --list-all
```

If the directory label is abnormal, re-execute:

```bash
semanage fcontext -m -t httpd_sys_content_t '/var/www/agentsql(/.*)?'
restorecon -Rv /var/www/agentsql
```

The systemd hardening makes the site read-only, allowing Caddy to write only `/var/lib/caddy` and `/var/log/caddy`. If the log or certificate storage reports a permission error, check whether these two directories are `caddy:caddy`, and do not expand the write permission of the entire file system.

## 6. Versioned release and rollback

Each execution of the deployment script (including `--content-only` in stage B) will create a new UTC timestamp directory, and the old version will be retained. View version:

```bash
readlink -f /var/www/agentsql/current
find /var/www/agentsql/releases -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort
```

When rolling back, select a clear old directory, first check that `public/index.html` is in it, and then atomically update the link, verify and reload:

```bash
test -f /var/www/agentsql/releases/20260917T120000Z/public/index.html
ln -sfn /var/www/agentsql/releases/20260917T120000Z /var/www/agentsql/current
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

The default script creates `<时间戳>/public/index.html`, consistent with Caddy's `current/public` document root. Do not delete `/var/lib/caddy`, which contains the automatic HTTPS required state; rolling back the site files does not equal rolling back the certificate store.

## 7. Logging and rotation

- Access log: `/var/log/caddy/agentsql-access.json`, JSON format.
- Application configuration rotation: 10 MiB per file, 10 retained, up to 720 hours.
- Service and ACME log: `journalctl -u caddy`.

Commonly used checks:

```bash
journalctl -u caddy --since today
tail -n 100 /var/log/caddy/agentsql-access.json
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

## 8. Common faults

### Stage A cannot be opened from the public network

This is expected behavior. Phase A only listens for loopback and must use SSH tunnel; do not add public network rules for 8080.

### Phase B certificate issuance failed

First check whether the A record is resolved to the current ECS, whether 80/443 passes both the security group and firewalld, whether CAA is allowed to be issued, and whether the system time is accurate. Fix the root cause before reloading to avoid repeated requests.

### The page can be opened but resource 404

Confirm that `readlink -f /var/www/agentsql/current` points to the full release directory, `assets/app.css`, `assets/app.js` and screenshots exist, and the SELinux tag is `httpd_sys_content_t`. Do not use `website/` itself as the document root; the document root must be its `public/` content.

### Reload fails after modifying configuration

Run `caddy validate --config /etc/caddy/Caddyfile` first, then view `journalctl -u caddy`. Keep the current running process and reload after fixing the configuration. Do not overwrite it with unverified configuration and then restart directly.
