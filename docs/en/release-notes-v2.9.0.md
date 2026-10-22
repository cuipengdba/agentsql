# AgentSQL v2.9.0 Release Notes

**Language:** [English](release-notes-v2.9.0.md) | [Simplified Chinese](../release-notes-v2.9.0.md)

Status: Published. [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v2.9.0) and [ officially release evidence ](v2.9.0-release-evidence.md) records assets, mirrors, official website and Live Demo acceptance.

## Scope of this edition

- Added tenant policy package export, difference plan, conservative table-level playback, proposal, alien approval, atomic application and approval baseline drift prompts. A new "Policy Package Management" entry has been added to the console.
- Added Ed25519 audit ZIP external witness and verification command to bind archive bytes and declared audit chain headers with independently held keys. `verify-archive` can still independently verify the internal integrity of the archive.
- Modifications are limited to table-level allow/deny policies without B2 bindings and column/row constraints; a maximum of 32 changes at a time, and a maximum of 128 policies for a single tenant package. Offline playback only generates `candidate`, `deny`, and `unknown`. The real business SQL still needs to be verified by the gateway execution chain.

## Upgrades and boundaries

The metadata schema merge deployment increased from 22 to 23, and the sub-database deployment increased from 21 to 22; the independent audit database remains at 6. According to the [ version upgrade and recovery steps ](v2.9.0-upgrade.md), first do pre-check and paired backup, and then explicitly migrate. The previous database adaptation capabilities and support levels maintain the original measured boundaries; this version does not add new database production support statements. For complete acceptance, see [ candidate record ](v2.9.0-acceptance.md) and [ official release evidence ](v2.9.0-release-evidence.md).
