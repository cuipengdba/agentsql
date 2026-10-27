# AgentSQL product official website

**Language:** [English](README.en.md) | [Simplified Chinese](README.md)

A purely static product official website with no construction steps. The site's public root directory is `public/`, and the Caddy and Alibaba Cloud Linux 3 deployment materials are located at `deploy/`.

## Directory Responsibilities

- `public/index.html`: Product single page, SEO information, accessibility semantics and inline link map.
- `public/404.html`: Custom 404 page.
- `public/favicon.svg`: SVG site icon for shield and database graphics.
- `public/assets/app.css`: Theme, layout, components, responsiveness, printing and dynamic effect reduction rules.
- `public/assets/app.js`: Mobile navigation and vintage enhancements; disabling JavaScript does not affect body text and native collapsed content.
- `public/assets/screenshots/`: 11 demo screenshots copied and renamed from `docs/images/`.
- `public/robots.txt`, `public/sitemap.xml`, `public/.well-known/security.txt`: Search and security contact information.
- `deploy/`: Caddy two-stage configuration, systemd drop-in, deployment scripts, acceptance scripts and operation and maintenance documents.

## local preview

This directory does not require Node.js, a package manager, or a front-end build tool. Please select any existing static file server and point the document root directory to `website/public/`. Do not double-click the HTML for final acceptance, as resource paths starting with `/` require an HTTP document root.

The official website is a purely static site with no runtime build; it only runs `node website/tools/build-docs-pdf.mjs` when the manual PDF needs to be regenerated (requires native Node and Microsoft Edge). `website/public/assets` (including `assets/docs/*.pdf`) is copied recursively by the deployment script, and there is no need to change the deployment whitelist.

Execute the static acceptance script on the server after deployment (the example is the loopback address of stage A):

```bash
bash ./deploy/verify-static.sh http://127.0.0.1:8080 stage-a
```

## release constraints

- Only publish `public/` content; do not put `deploy/`, repository root directory, `.git/` or environment files into the document root.
- Phase A only listens to `127.0.0.1:8080` and previews through the SSH tunnel; switch to phase B after filing, DNS and firewall are ready.
- The current version page will only be deployed after the corresponding GitHub Release, dual-architecture image, and versioned PDF pass acceptance; the Release button must point to the official release page of the same version. The deployment topology of the public demo must be clearly stated on the page.
- The site has no cookies or third-party statistics. Caddy access logs are configured to be retained for 720 hours by default.
- The open-source license link points to the repository's `LICENSE`, alongside a link to `NOTICE`.

## Icons and shared assets

- `public/assets/og-cover.png`: 1200×630 social sharing card, referenced in `og:image` / Twitter Card.
- `public/apple-touch-icon.png` (and its counterpart under `public/assets/`): 180×180 iOS home screen icon.
- `public/favicon.svg`: site icon; `favicon.ico` is not provided, modern browsers use SVG icons.

After adding or replacing binary assets, the deployment whitelist should be updated simultaneously and the acceptance script should be re-run.

## Phase B (domain name and HTTPS) operation and maintenance inspection

- The filing number is displayed in the footer; each time the review filing number and official query link are released, they cannot be returned to placeholder text.
- Phase B is online; daily content updates use `--content-only` according to `deploy/README-DEPLOY.md`, without reinstalling Caddy or overwriting the current Caddyfile.
- HSTS first maintains `max-age=300`; it will be evaluated for improvement after stable operation and confirmation that there is no need for rollback.
- `/demo/` is a stable static security entrance; the console uses an independent source to reverse the loopback upstream, because the current web build uses the roots `/assets`, `/api/v1` and BrowserRouter and cannot be directly deployed to sub-paths.
- Before the public network demonstration is opened, confirm that only synthetic data, read-only or controlled scripts, regular resets, no real database connections are used, and that 7780, 17880, 5432, and 3306 are not directly exposed to the public network.

## Release day inclusion gate

The official website is already in stage B: the homepage is `index,follow`, the demo page is still `noindex,nofollow`, `robots.txt` allows the homepage and excludes `/demo/`. For each update, first confirm that the corresponding official Release, image and PDF are ready, then use `verify-static.sh` to check these switches and in-site references, and finally release it through `--content-only`.
