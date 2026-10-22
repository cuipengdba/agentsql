# post4b README.en.md Post-release wording report

**Language:** [English](postrelease-post4b-report.md) | [Simplified Chinese](../postrelease-post4b-report.md)

Verification date: 2026-10-12. According to the given facts of this batch of tasks, v0.5.0 was released on 2026-10-10. The existing files modified in this batch are only the root directory `README.en.md`; this report is a newly delivered file.

## Summary of changes

- Changed v0.5.0 logo from Pending Release to `Release v0.5.0`.
- Change the planned release date of the opening chapter, the old version of the public Release and the code to be released to the published caliber; retain the fact that the original license of the v0.4.0 release is retained.
- Change `pending v0.5.0 code` in the compatibility matrix introduction to `v0.5.0 code` without changing the number of databases or compatibility boundaries.
- Change the GHCR subtitle and image description to the post-release tense, retaining the image tag and dual-architecture fact.

## Checking reserved items

- `pending validation` is the verification status waiting for the manufacturer's environment; `session/planned-transaction` is the function name, both are reserved.
- `unpublished local dry-run artifact` describes YashanDB test evidence, and `not yet translated` describes document coverage, both are reserved.
- The source code, tags, and publications of v0.4.0 and earlier retain the original license; the license statement of Apache-2.0 adopted from v0.5.0 has not been changed.
- The approval order status, database validation boundaries, technical facts, and task listings of `cmd/agentsql/b5-wal/` and `cmd/agentsql/﹎memory﹎.instance-id` have not been modified.

## Inspections and remaining issues

- A precise search confirmed that `README.md`, `docs/en/README.md`, and `README.en.md` do not have the old `Next release: v0.5.0 pending` logo; `README.en.md` does not have other pre-release status wording identified in this batch.
- After expanding the search scope, the old logos were found at `dist/release-b81/validation-source/README.md:3` and `dist/release-b81/validation-source/README.en.md:5`. They are historical snapshots that are prohibited from being modified in this batch, so the acceptance item "Full warehouse will no longer appear" was not passed. The extended scan stopped after discovering these two items, and did not claim that they were the only ones.
- According to the "git ban" requirement in the mission statement, `git diff` or `git diff --check` was not run. Only `README.en.md` was checked without trailing whitespace; `git diff --check` cannot be claimed to have passed on this basis.
