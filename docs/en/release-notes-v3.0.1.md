# AgentSQL v3.0.1 Release Notes

**Language:** [English](release-notes-v3.0.1.md) | [Simplified Chinese](../release-notes-v3.0.1.md)

Status: Published (2026-10-22). [GitHub Release](https://github.com/cuipengdba/agentsql/releases/tag/v3.0.1), [ official website ](https://agentsql.cn/) and [Live Demo](https://demo.agentsql.cn/) have been updated. For acceptance details, see [ release record ](v3.0.1-release-evidence.md).

## Changes in this version

- The official website has added Chinese and English pages and language switching, and the Live Demo security entrance has a corresponding English version.
- The GitHub repository retains the complete Chinese README, the English README is updated to the current capabilities, and the two copies are linked to each other. The interface language of the GitHub website itself is set by GitHub users.
- The Web console can switch between Chinese and English on the login page and after login; the browser saves the selection, and the Demo deep link on the English official website can directly open the English console. The existing page static copy, Ant Design control and date number format follow the language; SQL and result data remain the original text.
- The left navigation has an independent scroll area, which solves the problem of inaccessible bottom options under short effective viewports.

## upgrades and boundaries

This release does not change the backend API, MCP protocol, database adaptation, permissions, execution, masking, auditing, or schema. The compatibility range of v3.0.0 and the original evidence of each database are still applicable. Before installation, follow the [ deployment guide ](DEPLOY.md) to back up the control library and key and verify the recovery. The English detailed manual and PDF are not yet available, but the Chinese version can continue to be used. The existing page is translated from the inspected static copy catalog. If a small amount of dynamic business data is exactly the same as the catalog key, copy matching may occur; the query result cells have been excluded. For the acceptance status, see [ acceptance record ](v3.0.1-acceptance.md).
