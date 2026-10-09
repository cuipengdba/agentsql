# batch80r runner 改造报告

日期：2026-10-09。只改造了 `scripts/codex-batch-runner.ps1`；没有运行发布任务、联网或执行 git 写操作。

## 用法

单批模式保持原调用和产物格式：

```powershell
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -BatchNumber 80 -TaskFile D:\ruanjiansheji\codex-task-batch80b.md -ExpectedMinutes 120
```

产物仍为 `D:\ruanjiansheji\current-batch.json`、`batch-done.flag`、`codex-batch<N>-run.log`、`codex-batch<N>-err.log`、`codex-batch<N>.pid`。单批主体代码未改，参数集解析检查通过；本批没有启动真实 Codex 验证单批发布行为。

链式模式：

```powershell
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -ChainFile D:\ruanjiansheji\chain.json
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -ChainFile D:\ruanjiansheji\chain.json -StartBatch 81
```

`-StartBatch` 从 `number` 匹配项开始，包含该批。`-CodexExecutable` 和 `-LogDirectory` 是本地自检用覆盖参数；正式运行不传。默认 Codex 路径 `C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\f544b3844e0f14e9\codex.exe` 已确认存在；若未来缺失，runner 会在同一 `bin` 下寻找唯一 `codex.exe`，不改写固定路径。

## Chain 文件

字段名如下，所有相对路径均按 `repository` 解析。正式链的状态、handoff、watchdog 路径由 chain 文件指定；日志和 pid 默认写在 `repository` 的父目录，以 `logLabel` 命名。

```json
{
  "chainName": "v0.5-release",
  "repository": "D:\\ruanjiansheji\\agentsql-v04",
  "model": "gpt-6-sol",
  "chainStatePath": "D:\\ruanjiansheji\\chain-state.json",
  "handoffRecordPath": "D:\\ruanjiansheji\\transfer\\private\\chain-handoff.md",
  "watchdogLogPath": "D:\\ruanjiansheji\\transfer\\private\\codex-watchdog.log",
  "batches": [
    {
      "number": 80,
      "logLabel": "80b",
      "taskFile": "D:\\ruanjiansheji\\codex-task-batch80b.md",
      "expectedMinutes": 120,
      "checks": [{ "type": "fileExists", "path": "dist/release-prepare/v0.5.0-final/assets/provenance.json" }]
    }
  ]
}
```

支持的 checks：

| type | 必需字段 | 判定 |
| --- | --- | --- |
| `fileExists` | `path` | 仓库相对路径是普通文件 |
| `fileCount` | `dir`, `count` | 目录下非递归普通文件数恰好相等 |
| `exactFiles` | `dir`, `names` | 普通文件 basename 集合逐字完全一致 |
| `logContains` | `file`, `marker`；可选 `minCount` | 包含 marker 的行数达到阈值，缺省 1 |
| `commandSucceeds` | `command`；可选 `timeoutSeconds` | 只读白名单命令在仓库目录以退出码 0 完成，缺省超时 600 秒 |
| `commandOutputs` | `command`, `marker`；可选 `timeoutSeconds`, `minCount` | 在上一条件之外，stdout 命中行数达到阈值 |
| `jsonEquals` | `file`, `jsonPath`, `value` | 点分属性及 `[i]` 数组路径取值字符串与 value 逐字相等 |

每项结果记录 type、target、passed、actual、UTC 时间。命令 stdout/stderr 尾部各最多 2000 字符记入结果；含常见密钥字段或私钥头的整行以 `[REDACTED]` 代替。未知类型、缺失文件、非法路径、超时和不在命令白名单内的命令均失败并停止链。

每批先保存只读 `git status --porcelain=v1 --untracked-files=all` 快照，再启动 Codex。退出码非零或 stderr 尾部无 `tokens used` 时停止；30 分钟日志无更新时只对本 runner 启动的 Codex PID 执行 `taskkill /PID ... /T /F`。checks 全过才记 VERIFIED 并立即启动下一批。状态用 UTF-8 无 BOM临时文件加原子替换写入；handoff 每批追加标题、任务书、日志、checks、changedFiles 和遗留问题。

## Windows PowerShell 5.1 自检

自检脚本、假 Codex 可执行、chain 文件、状态、handoff 和完整带时间戳日志均在 `dist/runner-b80r-selftest/`（被 `.gitignore` 忽略）。运行命令：

```powershell
powershell.exe -NoProfile -NonInteractive -File dist\runner-b80r-selftest\run-selftest.ps1
```

| 路径 | UTC 结果时间 | 结果 |
| --- | --- | --- |
| 正常：2 批自动接力，七种 checks 全通过 | 2026-10-09 09:08:47 | PASS，chain `complete`，两批 `verified` |
| 断点：`-StartBatch 10` | 2026-10-09 09:08:49 | PASS，只启动匹配批次 |
| 验收失败：`fileExists` 缺失 | 2026-10-09 09:08:50 | PASS，chain `verify_failed`，后续批次未启动 |
| 异常退出：stderr 无 `tokens used` | 2026-10-09 09:08:52 | PASS，chain `failed`，后续批次未启动 |
| 负向命令：`git commit`、`docker push` | 2026-10-09 09:08:56 | PASS，两项均在执行前拒绝并成为 `verify_failed` |

`Get-Command` 显示 `Single` 和 `Chain` 参数集；`BatchNumber`、`TaskFile` 在单批集仍是必填。链状态的 `beforeFiles` 是平坦字符串数组，`changedFiles` 是数组；状态文件首字节为 `7B 0D 0A`，没有 UTF-8 BOM。

## 安全与限制

runner 自身只调用 `git status` 和原单批模式已有的 `git rev-parse`，不调用 git 写命令。命令 checks 先通过单命令字符限制、变更动作黑名单及只读子命令白名单。允许 git 的 `status/diff/log/show/rev-parse/ls-remote`、Docker 的 `inspect/ps/pull` 与部分只读 `buildx` 查看命令、`gh release view`、显式 GET 的 `gh api`、包查看命令，以及仓库 `scripts` 下命名含 `verify/check/test/audit` 的 `.ps1`。不确定的命令拒绝；例如复杂 PowerShell 管道、引号与命令组合不支持。链输出路径禁止指向受保护的 WAL 和 instance-id 数据。

已知限制：`changedFiles` 按两次 porcelain 状态行差异列出新增/修改，排除删除；如果文件前后保持相同状态标记但内容又变化，这种状态快照无法识别。敏感输出遮蔽是按常见格式识别，任务书和复验脚本仍须避免输出私钥、license、token 内容。自检未等待 30 分钟实际触发 stall，也未运行真实发布链。正式使用前应逐批填写可客观复验的 checks，检查状态/handoff 路径和任务书路径，并在每批 VERIFIED 后依据 handoff 清单由 MainAgent 精确处理 git 操作。
