# Batch 81：v0.5.0 annotated tag、Release 草稿与远端资产复验报告

**结果：VERIFIED；Release 保持 draft，未转正式。** 记录时间为 2026-10-09 Asia/Shanghai。完整带时间戳日志位于 `dist/release-b81/tag-and-push.log`、`dist/release-b81/release-draft.log` 和 `dist/release-b81/redownload-verify.log`。

## 前置条件与 tag

- batch80b 报告已在 tag 前以 `783b008fa9ca381f71509d5079af742933348717` 提交；该提交仅新增 `docs/release-prepare-b80b-report.md`，其直接父提交为实际构建提交 `fe36c7cdecb9a68426727153ff2dc90487c40ca7`。
- 权威目录 `dist/release-prepare/v0.5.0-final/assets/` 恰好 15 个文件。`provenance.json` 的 `source.commit=fe36c7cdecb9a68426727153ff2dc90487c40ca7`、`dirtyAtBuild=false`；`provenance.inputs` 中 `scripts/release-dryrun.ps1` 的 SHA-256 为 `f00dd95883d51c0f6be1184287b8bf0cc91d9f625ae255e3f9a60d90164e5cd0`，与工作区脚本一致。
- 创建前已跟踪工作树干净；未跟踪路径只有受保护的 `cmd/agentsql/b5-wal/`、`cmd/agentsql/﹎memory﹎.instance-id`，`dist/` 受忽略。创建前本地和远端均不存在 `v0.5.0` tag，远端不存在对应 Release。
- `git cat-file -t v0.5.0` 输出 `tag`，annotated tag 对象为 `f2330ec3e65d40acd87fc34211c1ba7d78659cdc`，解引用目标为 `783b008fa9ca381f71509d5079af742933348717`。`git push origin refs/tags/v0.5.0` 退出码 0；随后 `git ls-remote --tags origin v0.5.0` 返回相同 tag 对象。

## Release 草稿和账号

- `gh auth status` 确认账号 `cuipengdba` 已登录，token scopes 包含 `repo`；日志未记录 token。
- `gh release create v0.5.0 --draft --verify-tag` 使用 `docs/release-notes-v0.5.md`，上传权威目录的 15 个文件。`gh release view` 返回 `isDraft=true`、`isPrerelease=false`、`tagName=v0.5.0`、资产数 15，名称及字节数逐项与原件一致。
- 草稿 URL：`https://github.com/cuipengdba/agentsql/releases/tag/untagged-a50cac24fbb6b6a1e819`。下列为 GitHub 返回的草稿资产下载 URL；草稿期间匿名下载不可用。

| 资产名称 | 字节 | 下载 URL（草稿） |
| --- | ---: | --- |
| `agentsql-v0.5.0-linux-amd64.tar.gz` | 103730667 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-amd64.tar.gz |
| `agentsql-v0.5.0-linux-amd64.tar.gz.sha256` | 101 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-amd64.tar.gz.sha256 |
| `agentsql-v0.5.0-linux-arm64.tar.gz` | 94569304 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-arm64.tar.gz |
| `agentsql-v0.5.0-linux-arm64.tar.gz.sha256` | 101 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0-linux-arm64.tar.gz.sha256 |
| `agentsql-v0.5.0.spdx.json` | 2852836 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0.spdx.json |
| `agentsql-v0.5.0.spdx.json.sig.json` | 451 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/agentsql-v0.5.0.spdx.json.sig.json |
| `ed25519-release-public-key.json` | 230 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/ed25519-release-public-key.json |
| `go-version-metadata.txt` | 50348 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/go-version-metadata.txt |
| `install.sh` | 46844 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/install.sh |
| `provenance.json` | 6495 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/provenance.json |
| `provenance.json.sig.json` | 441 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/provenance.json.sig.json |
| `SBOM-GENERATION.txt` | 570 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SBOM-GENERATION.txt |
| `SHA256SUMS` | 1225 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SHA256SUMS |
| `SHA256SUMS.sig.json` | 436 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/SHA256SUMS.sig.json |
| `VERIFYING-SIGNATURES.md` | 1258 | https://github.com/cuipengdba/agentsql/releases/download/untagged-a50cac24fbb6b6a1e819/VERIFYING-SIGNATURES.md |

## 回下载验签

- `gh release download v0.5.0 --dir dist/release-b81/redownload --clobber` 在全新目录完成，得到恰好 15 个普通文件。逐项比较名称、字节数和 SHA-256，15/15 与权威原件相同；逐项哈希见 `redownload-verify.log`。
- 使用回下载的 `ed25519-release-public-key.json` 公钥副本和 batch80b 编译的仓库 `scripts/releasesign/main.go` 验证器，分别对 `agentsql-v0.5.0.spdx.json`、`provenance.json`、`SHA256SUMS` 执行 `-mode verify`：三次均输出 `verified`，key ID 均为 `sha256:771e8b179a031f0e3ac3e09576979532808e39f5f2e6da9d103ddf24c91d6868`。公钥副本的 SHA-256 与权威原件相同，因而验证锚点相同。
- 在隔离的 `fe36c7cdecb9a68426727153ff2dc90487c40ca7` 稀疏源码副本中，对同一回下载目录运行原版 `scripts/release-dryrun.ps1 -Version v0.5.0 -ValidateOnly -AssetsDirectory ...`。退出码 0，输出 `RELEASE_DRYRUN_ASSET_COUNT=15`、`RELEASE_DRYRUN_SHA256SUMS=PASS`、`RELEASE_DRYRUN_ITEM_FAIL_COUNT=0`、`RELEASE_DRYRUN_VALIDATE_ONLY=PASS`。采用构建提交副本的原因是脚本要求 provenance 的 `source.commit` 严格等于运行目录 HEAD；当前发布 tag HEAD 是其 docs-only 子提交。
- 未认证 HTTP 复核：正式 Release API `GET /repos/cuipengdba/agentsql/releases/tags/v0.5.0` 返回 404，草稿资产 `SHA256SUMS` 下载 URL 返回 404。公开 tag 落地页返回 200，但不包含草稿发布说明；这与 tag 可见、Release 草稿未公开的状态一致。

## 边界与遗留

- b80b 报告已在 tag 前提交（`783b008`）；本 b81 报告由 MainAgent 在 tag 和草稿验收后单独提交。tag 指向 `783b008`，provenance 构建提交 `fe36c7c` 为其直接父提交，二者可追溯；报告提交不会移动 tag。
- 首次自动化预检因命令中的预期哈希录入错误，在 tag 创建前停止；修正后通过。首次验签调用因 PowerShell 自动变量 `$input` 导致参数缺失，在 15 项回下载 SHA-256 已通过后停止；改名参数变量后，三次验签及 ValidateOnly 全部通过。原始失败和恢复均保留在带时间戳日志中。
- 本批保持 Release draft。转正式留待 batch83；未推送 GHCR、未部署官网、未宣传。未读取或记录私钥、license 内容；未触碰两个受保护路径。