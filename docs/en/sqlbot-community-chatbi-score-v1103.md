# SQLBot Community v1.10.3 data-question workflow score contract

> Fixed comparison matrix from v4.9.0 onward. It has not been scored against the final signed candidate. The previous approximate 60% was a rough engineering estimate over 13 feature groups and cannot be converted to a pass rate here.

This matrix measures workflows that actually work in the pinned official SQLBot Community v1.10.3 release. It excludes X-Pack, unavailable Community routes, and unrelated administration; it also does not measure AgentSQL's independent database security gateway depth. There are 25 equally weighted scenarios worth four points each. Only a complete pass earns four points. Coverage is passed scenarios divided by 25, so 80% requires at least 20 passes. The denominator is frozen for this baseline; a newer upstream release needs a separate matrix.

A pass requires evidence for the same exact signed candidate: the unchanged original client, API, or SDK as appropriate; a real model and database; identity and data scale; a successful result; relevant denial or revocation; controlled gateway audit; and raw logs. Record the database and runtime environment. A page load, registered route, mock model, or test of an older commit does not pass. If upstream behavior conflicts with AgentSQL's security boundary, fail closed, score zero, and disclose the difference.

| ID | Group | Fixed scenario (four points each) |
| --- | --- | --- |
| Q1 | Core questioning | Single-table natural-language question and controlled result in the unchanged original `/chat/index` page |
| Q2 | Core questioning | Filtering, grouping, ordering, and limiting within a currently authorized table |
| Q3 | Core questioning | Correct table choice and cold/warm first question among 128 currently authorized tables |
| Q4 | Core questioning | Authorized two-table join and aggregate question |
| Q5 | Core questioning | Questioning under active row/column permissions and masking, with denial after revocation |
| Q6 | Core questioning | Concurrent questions from two differently authorized users with result isolation |
| H1 | Conversation and history | Real contextual follow-up in the same conversation |
| H2 | Conversation and history | Reopened history after service restart, with SQL and result constrained by current permission |
| H3 | Conversation and history | Authorized history CSV/XLSX export, with old export denied after revocation |
| H4 | Conversation and history | New conversation, list, delete, and denial of another user's history |
| V1 | Results and dashboards | Generate, switch, and reopen a table and basic chart |
| V2 | Results and dashboards | Correct field mapping and display in multi-metric bar/line charts |
| V3 | Results and dashboards | Analysis narrative from a real controlled result, with fail-closed handling |
| V4 | Results and dashboards | Add a result chart to a dashboard, save, preview, reopen, and revoke |
| S1 | Semantic configuration | Terminology changes question interpretation; disable/delete removes the effect |
| S2 | Semantic configuration | SQL examples influence generation and remain isolated across datasources and revocation |
| S3 | Semantic configuration | Table/column remarks and relationships affect table selection and generation |
| D1 | Data and models | Trusted administrator binds PostgreSQL and a user questions it |
| D2 | Data and models | Trusted administrator binds MySQL and a user questions it |
| D3 | Data and models | Managed CSV/XLSX datasource used in the original question entry |
| D4 | Data and models | Real inference and fault recovery with a local model and at least one declared cloud model |
| I1 | Integration entry | Non-streaming and streaming questions from the official original Python MCP SDK |
| I2 | Integration entry | MCP Server tool discovery, controlled metadata/query calls, and excessive-access denial |
| I3 | Integration entry | Real questions, origin enforcement, and revocation in basic assistant and Type 4 page embedding |
| I4 | Integration entry | Authorized catalogue selection, real questions, and cross-origin denial in advanced assistant |

Security is a separate release gate and cannot be offset by points. Identity spoofing, excessive access, bypassed row/column rules, missing audit, leaked old results, or unsafe SQL execution blocks the relevant release even at 80% coverage. Performance is measured against a version-specific hardware and workload contract, not the fastest single sample. Save the exact commit and links to all 25 scenario evidence records with each release; leave untested items at zero.
