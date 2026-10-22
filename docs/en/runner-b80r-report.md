# batch80r runner transformation report

**Language:** [English](runner-b80r-report.md) | [Simplified Chinese](../runner-b80r-report.md)

Date: 2026-10-09. Only `scripts/codex-batch-runner.ps1` was modified; no release tasks were run, networked, or git writes were performed.

## Usage

Single batch mode maintains the original call and product format:

```powershell
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -BatchNumber 80 -TaskFile D:\ruanjiansheji\codex-task-batch80b.md -ExpectedMinutes 120
```

The products are still `D:\ruanjiansheji\current-batch.json`, `batch-done.flag`, `codex-batch<N>-run.log`, `codex-batch<N>-err.log`, and `codex-batch<N>.pid`. The main code of the single batch has not been changed, and the parameter set parsing check has passed; the real Codex verification single batch release behavior has not been started in this batch.

Chain mode:

```powershell
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -ChainFile D:\ruanjiansheji\chain.json
powershell.exe -NoProfile -File scripts\codex-batch-runner.ps1 -ChainFile D:\ruanjiansheji\chain.json -StartBatch 81
```

`-StartBatch`from`number`The match begins, containing the batch.`-CodexExecutable`and`-LogDirectory`It is an overlay parameter used for local self-test; it is not passed during official operation. Default Codex path`C:\Users\Administrator\AppData\Local\OpenAI\Codex\bin\f544b3844e0f14e9\codex.exe`Confirmed to exist; if missing in the future, runner will be in the same`bin`Find the only one`codex.exe`, do not rewrite the fixed path.

## Chain file

The field names are as follows, and all relative paths are resolved by `repository`. The status, handoff, and watchdog paths of the official chain are specified by the chain file; the log and pid are written in the parent directory of `repository` by default and are named after `logLabel`.

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

Supported checks:

| type | Required fields | Judgment |
| --- | --- | --- |
| `fileExists` | `path` | The relative path of the repository is an ordinary file |
| `fileCount` | `dir`, `count` | The number of non-recursive ordinary files in the directory is exactly equal |
| `exactFiles` | `dir`, `names` | Ordinary file basename collection is identical verbatim |
| `logContains` | `file`, `marker`; optional `minCount` | The number of rows containing marker reaches the threshold, default 1 |
| `commandSucceeds` | `command`; optional `timeoutSeconds` | The read-only whitelist command completes with exit code 0 in the repository directory, with a default timeout of 600 seconds. |
| `commandOutputs` | `command`, `marker`; optional `timeoutSeconds`, `minCount` | Outside of the previous condition, the number of stdout hit lines reaches the threshold |
| `jsonEquals` | `file`, `jsonPath`, `value` | The dotted attribute and `[i]` array path value string are literally equal to value |

Each result records type, target, passed, actual, UTC time. Up to 2000 characters at the end of each command stdout/stderr are recorded in the result; the entire line containing common key fields or private key headers is replaced by `[REDACTED]`. Unknown types, missing files, illegal paths, timeouts, and commands not in the command whitelist all fail and stop the chain.

Save the read-only `git status --porcelain=v1 --untracked-files=all` snapshot for each batch before starting Codex. Stop when the exit code is non-zero or there is no `tokens used` at the end of stderr; when there is no update in the log for 30 minutes, `taskkill /PID ... /T /F` will only be executed for the Codex PID started by this runner. VERIFIED will be recorded only after all checks have passed and the next batch will be started immediately. The status is written using UTF-8 BOM-free temporary files and atomic replacement; handoff appends titles, task books, logs, checks, changedFiles and remaining issues to each batch.

## Windows PowerShell 5.1 self-test

The self-test script, fake Codex executable, chain file, status, handoff and full timestamped logs are all in `dist/runner-b80r-selftest/` (ignored by `.gitignore`). Run command:

```powershell
powershell.exe -NoProfile -NonInteractive -File dist\runner-b80r-selftest\run-selftest.ps1
```

| path | UTC result time | result |
| --- | --- | --- |
| Normal: 2 batches of automatic relay, all seven types of checks passed | 2026-10-09 09:08:47 | PASS, chain `complete`, two batches `verified` |
| Breakpoint: `-StartBatch 10` | 2026-10-09 09:08:49 | PASS, only start matching batches |
| Acceptance failed: `fileExists` is missing | 2026-10-09 09:08:50 | PASS, chain `verify_failed`, subsequent batches are not started |
| Abnormal exit: stderr None `tokens used` | 2026-10-09 09:08:52 | PASS, chain `failed`, subsequent batches are not started |
| Negative commands: `git commit`, `docker push` | 2026-10-09 09:08:56 | PASS, both items are rejected before execution and become `verify_failed` |

`Get-Command` displays the `Single` and `Chain` parameter sets; `BatchNumber` and `TaskFile` are still required in single batch sets. `beforeFiles` of the chain status is a flat string array, and `changedFiles` is an array; the first byte of the status file is `7B 0D 0A`, and there is no UTF-8 BOM.

## Security and restrictions

The runner itself only calls`git status`Same as the original single batch mode`git rev-parse`, does not call the git write command. Command checks first pass the single command character limit, change action blacklist and read-only subcommand whitelist. Allow git`status/diff/log/show/rev-parse/ls-remote`, Docker's`inspect/ps/pull`with partial read-only`buildx`View command,`gh release view`, explicit GET`gh api`, package view command, and warehouse`scripts`The following names include`verify/check/test/audit`of`.ps1`. Uncertain command rejection; for example, complex PowerShell pipes, quotes, and command combinations are not supported. Chain output paths must not point to protected WAL and instance-id data.

Known limitations: `changedFiles` Press the porcelain status line difference twice to list additions/modifications and exclude deletions; if the file maintains the same status mark before and after but the content changes, this status snapshot cannot be identified. Sensitive output masking is based on common format identification, and the mission statement and re-inspection script must still avoid outputting private key, license, and token content. The self-test did not wait 30 minutes to actually trigger stall, nor did it run the real release chain. Before official use, you should fill in the checks that can be objectively re-verified batch by batch, check the status/handoff path and task book path, and after each batch of VERIFIED, MainAgent will accurately handle git operations according to the handoff list.
