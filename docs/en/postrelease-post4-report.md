# post4 wording update report after document release

**Language:** [English](postrelease-post4-report.md) | [Simplified Chinese](../postrelease-post4-report.md)

Verification date: 2026-10-12. According to the given facts of the task, v0.5.0 has been officially released on 2026-10-10 (Release is not draft, GHCR is all public, and the official website has been cut to v0.5.0). This batch only adjusts the current release status and tense in the document; no external revalidation of released assets, images, or official websites is performed.

## File-by-file changes

The text of the task is called "12 documents", and the itemized list actually lists the following 13 documents; this batch is executed according to the itemized path, and this report is added.

| File | Status and tense changes |
| --- | --- |
| `README.md` | Changed the pre-release wording of the logo, version overview, amd64 asset example, and exact image tag to released; the compatibility matrix's "code to be released" was changed to "v0.5.0 code". |
| `CHANGELOG.md` | Change the v0.5.0 title date to 2026-10-10, and change the introduction to official release status; the rest of the text remains unchanged. |
| `docs/release-notes-v0.5.md` | Just change the status header to "Released". |
| `docs/SPEC.md` | Change the version header and "Current code to be released" to released wording. |
| `docs/USER_GUIDE.md` | Change the release status header to Published, indicating that the installation assets and v0.5.0 image are available, and the source code repository address is retained. |
| `docs/test-coverage-notes.md` | Change "To be published" in the title to "Published". |
| `docs/en/ADMIN.md` | Change the release status of the current code and installation assets to Published; leave the CLI wording debt as is. |
| `docs/en/COVERAGE.md` | Change the release status of the v0.5.0 source code and distribution assets to released; keep the historical license and CLI wording debt as they are. |
| `docs/en/GETTING_STARTED.md` | Change status headers and image pull sentences to published tense. |
| `docs/en/INTEGRATIONS.md` | Change the status header to Published tense. |
| `docs/en/QUICKSTART.md` | Change the status header to Published, Asset Installed, and Image Available. |
| `docs/en/README.md` | Change the logo text and URL to `Release v0.5.0`. |
| `docs/en/SECURITY.md` | Only changed the compatibility evidence object from pending v0.5.0 code to released v0.5.0 code. |

## Checking reserved items

- `docs/en/AUTHENTICATION.md`, `docs/joint-case-operations.md`, `docs/known-issues-fixes.md`, `docs/RELEASE_ASSETS.md`, `docs/dm-oracle-dialect.md`, `docs/AUDIT_REPORTING.md` within the prohibited range have not been modified, and `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` have not been touched.
- `pending` Functional fact retention for approval orders, decisions, and audit status. Database compatibility boundaries, untested projects, support commitments, and version history are not adjusted.
- v0.4.0 and earlier releases retain the original license, the fact that Apache-2.0 is adopted from v0.5.0 onwards, and the license change statement in the root README are retained.
- The "Generation/Reconstruction on Release Date", old asset status, release gate and batch conclusion in `docs/release-notes-v0.5.md` are retained as process records at that time; the text of the release preparation process other than the introduction of `CHANGELOG.md` is also retained.
- English management and coverage documentation for audit CLI still shows wording debt record retention as "planned for v0.5".

## Uncertain items and disposals

- The support matrix section for `docs/en/SECURITY.md` still contains "after release" and "currently released v0.4.x". This paragraph involves the security repair support policy, and the task requirements do not change the support commitment, so the original text is retained and the wording is to be approved separately.
- `docs/release-notes-v0.5.md` still has pre-release snapshots of the 2026-10-16 release gate, old official website status, etc. The task clearly only changes the status header, so the historical process is not overwritten with the current results.

## Inspections and remaining issues

- `git diff` covers only the 13 items itemized; the new files are for this report only. `git diff --check` passed.
- The planned release, old public Release and to-be-released title expressions specified in `README.md` and `CHANGELOG.md` have been eliminated; the Chinese status header is Released.
- An accurate search of the entire warehouse found that `README.en.md:5` still contains the old Next release logo that marks v0.5.0 as pending. This file is not in the list of the only paths that are allowed to be modified, and has been retained according to the fail-closed principle; therefore, the acceptance item "This logo will no longer appear in the entire warehouse" has not been passed, and another authorization update is required.
- There are already two untracked paths before starting: `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id`. This batch is untouched.
