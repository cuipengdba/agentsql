# post3：官网首页 v0.5.0 版本位与 content-only 重发报告

执行日期：2026-10-12（Asia/Shanghai）。

## A 类：当前版本语境（10/10 通过）

| 编号 | 位置 | 核对结果 |
| --- | --- | --- |
| A1 | L67 当前版本徽标 | v0.5.0 |
| A2 | L192 一键安装与多架构镜像说明 | v0.5.0 |
| A3 | L192 GHCR 拉取命令 | agentsql:v0.5.0 |
| A4 | L193 GitHub Release 按钮 | v0.5.0 |
| A5 | L200 GA 兼容矩阵标题 | v0.5.0 |
| A6 | L200 版本与协议支持说明 | v0.5.0 |
| A7 | L203 兼容范围表标题 | v0.5.0 |
| A8 | L256 GHCR 拉取命令 | agentsql:v0.5.0 |
| A9 | L260 quickstart 参数 | --version v0.5.0 |
| A10 | L299 当前正式版本时间线 | v0.5.0 GA，保留“当前正式版本” |

git diff -- website/public/index.html 仅含上述 10 处文本替换；git diff --check 通过。

## B 类：历史表述（4/4 原样保留）

| 编号 | 位置 | 保留内容 |
| --- | --- | --- |
| B1 | L192 | v0.4.0 起提供 linux/arm64 原生 glibc 包 |
| B2 | L213 | v0.4.0 起提供 linux/arm64 原生 glibc 包；不提供 musl / Alpine 或 CentOS 7 原生包 |
| B3 | L309 | v0.4.0 及更早版本的既有源码、tag 与发布包 |
| B4 | L322 | v0.4.0 及更早版本继续适用其发布时的原许可 |

首页中剩余的 v0.4.0 共 4 处，均属于上述历史表述。

## 发布与公网校验

- 将 website/{deploy,public,tools,README.md} 打包为 dist/release-post3/site-post3.tar.gz（53 个归档条目，SHA-256：badd2b71cdd1ee8152633f2df118192e94e7d9b962b024f54bfb99732a1143a1）。上传到 ECS /root/website-release-post3/ 后，远端归档哈希与本机一致；解压后首页 SHA-256 与本机一致。按 b83a 范式规范化 deploy/*.sh 和 deploy/Caddyfile* 的 CRLF。
- 在 ECS 执行 bash ./deploy/deploy-alinux3.sh --stage b --src . --content-only，退出码 0。脚本内静态检查为 8 PASS / 0 FAIL；发布目录为 /var/www/agentsql/releases/20261011T162119Z。留痕：/root/site-deploy-post3.log。
- 公网执行 bash ./deploy/verify-static.sh https://agentsql.cn stage-b，校验时间 **2026-10-11T16:21:40Z**（北京时间 2026-10-12 00:21:40），退出码 0，**23 PASS / 0 FAIL**。留痕：ECS /root/site-verify-post3.log，本机 dist/release-post3/site-verify.log。
- 重抓 https://agentsql.cn/ 和 https://agentsql.cn/index.html：抓取时间均为 **2026-10-11T16:22:15Z**（北京时间 2026-10-12 00:22:15），均返回 HTTP 200；两个正文 SHA-256 均为 578a7fb0fc3a73666d91adc69ac1a3034be71564efcc9c081a5c675aec6bc979，与本机首页一致。逐项 rg -n -o -F 核对：两个 URL 各有 A 类 10/10、B 类 4/4，历史 v0.4.0 总数各为 4。抓取与匹配记录在 dist/release-post3/homepage-fetch.log、dist/release-post3/homepage-grep.log。

## 文件与遗留问题

- 本批源码改动：website/public/index.html；本报告：docs/postrelease-post3-report.md。
- 仅用于本批暂存的 git-ignored 产物：dist/release-post3/（归档、校验日志、两份首页抓取及 grep 记录）。未清理 dist/ 的其它内容。
- 无本批遗留问题。未执行 git 写操作；未触碰受保护的未跟踪路径。
