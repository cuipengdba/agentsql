# KingbaseES + HighGo joint case community operation caliber

**Language:** [English](joint-case-operations.md) | [Simplified Chinese](../joint-case-operations.md)

> Applicable channels: Official account, technical group, official website/GitHub update instructions.
>
> Prerequisites for release: The factual status must be consistent with the [ joint case verification record ](joint-case-kingbase-highgo.md); without written confirmation from the manufacturer, no wording implying endorsement, certification or formal cooperation is allowed.

## 1. Facts that must be consistent

- The first batch of joint case verification targets are two companies: **China Electronics Technology KingbaseES KingbaseES + HighGo HighGo**.
- IvorySQL is the HighGo community product line and can be used as a reference for the same-origin community version project, but it cannot replace the final verification of the HighGo commercial version and will not be included in the first batch of third manufacturers.
- Current HighGo conclusions only cover use cases for specified third-party SEE images, synthetic data, and listed PostgreSQL protocol paths; they are not vendor certified.
- Currently, the old third-party image of KingbaseES cannot start the database due to expired license. The V9R1C10 target image needs to be provided by the manufacturer; connection, discovery, query and security closed loop cannot be written as passed.
- There are 11 items in the ecological matrix: KingbaseES, HighGo, IvorySQL, openGauss, TiDB, OceanBase, TDSQL, OpenTenBase, YashanDB, Dameng DM, PolarDB. The matrix indicates actual testing, adaptation, or pending verification status, and does not mean that all 11 companies have supported it.
- Since v0.5, the current source uses Apache License 2.0; its rights and obligations are in `LICENSE` and `NOTICE`. This does not constitute database-vendor authorization or endorsement.

## 2. Standard wording

| Can be used | Use only if conditions are met | should not be used |
| --- | --- | --- |
| Actual measurement of protocol path, adaptation verification in progress, final verification of manufacturer environment, first batch of verification objects, engineering reference | "Joint release" and "joint practice": only after both parties have completed acceptance of the target version and confirmed it in writing | Official support, manufacturer certification, joint certification, full compatibility, cooperation completion, production-level compatibility |

When "tested" appears externally, the version/image source, use case scope and "non-manufacturer certification" must be clearly stated in the same paragraph or in an adjacent comment. Don’t just keep the words “tested”.

## 3. Public account proposal draft

### Title

Domestic database adaptation verification progress: the first batch focuses on KingbaseES KingbaseES and HighGo HighGo

### Summary

AgentSQL v0.5 is promoting joint case verification of domestic databases. The first batch focuses on CETC's KingbaseES KingbaseES and HighGo HighGo, and uses IvorySQL, which belongs to the HighGo community product line, as an independent engineering reference. We synchronize public verification matrices, reproducible SQL, and known boundaries so that every conclusion can be traced back to specific circumstances and evidence.

### Short text draft

AgentSQL is a database security gateway for AI Agents. Domestic database adaptation should not only focus on "whether a connection can be established", but also need to verify column discovery, SQL parsing, execution plan, rule interception, result masking, error handling and audit closed loop item by item.

The boundaries of this batch of records are very clear: the designated third-party HighGo SEE image has completed the minimum closed-loop experiment of the PostgreSQL protocol path; the HighGo commercial version still needs to be finalized for the manufacturer's target environment. The existing third-party old image of KingbaseES is blocked by the expired license, and the V9R1C10 target image is not yet in place, so the connection, discovery and query of KingbaseES remain "to be verified by the manufacturer's environment" and have not been written as passed.

The domestic database ecological matrix on the official website and GitHub now contains 11 items, which are respectively marked as actually tested, under adaptation, or to be verified. The matrix is ​​a public route and project progress, which does not mean manufacturer certification or full support of the current version.

Since v0.5, the current source has been released under Apache License 2.0, with conditions in `LICENSE` and `NOTICE`. Source licensing is independent of database-vendor certification.

Verification records and three sections of demonstration SQL: `docs/joint-case-kingbase-highgo.md`, `examples/joint-case-highgo.sql`.

## 4. Technical group SMS

AgentSQL v0.5 The first batch of domestic database joint case verification focuses on CETC KingbaseES KingbaseES and HighGo HighGo (IvorySQL serves as an independent reference for the HighGo community product line). The PostgreSQL protocol path currently designated for the third-party HighGo SEE image has completed the minimum closed loop of connection, column discovery, read-only query, R006 interception, masking and auditing; the HighGo commercial version is still to be finalized by the manufacturer. The old KingbaseES image has not been started because the license has expired. The V9R1C10 target image is yet to be provided by the manufacturer, and the relevant capabilities have not been approved. There are 11 items in the ecological matrix, and the status does not equal manufacturer certification or full support. Verification records and demo SQL are exposed in the repository.

## 5. English short version

**AgentSQL v0.5 domestic-database validation update**

The first case-validation batch focuses on KingbaseES and HighGo. IvorySQL is tracked separately as a community product from the same vendor line as HighGo; its results do not replace commercial-edition validation.

For the currently available third-party HighGo SEE image, AgentSQL completed a scoped PostgreSQL-protocol-path exercise covering connectivity, column discovery, a read-only query, R006 rule enforcement, masking, and audit records. This is not vendor certification and does not establish support for the HighGo commercial edition. Commercial-edition validation remains pending a vendor-provided target environment.

The available third-party KingbaseES image is blocked by an expired license, and the target V9R1C10 image has not yet been provided. No KingbaseES connectivity, discovery, query, or security-flow result is claimed as passed.

The public ecosystem matrix currently tracks 11 database entries at different stages. A listing means tested, in progress, or pending as explicitly labeled; it does not mean universal support or vendor endorsement. The current source license is Apache License 2.0. Its terms are independent of database-vendor validation.

## 6. Review before publishing

- Review the image, version, date, audit ID, and "measured/pending" status against the master verification record.
- Confirm that there are no passwords, API keys, full DSNs, real customer data, or non-masked screenshots.
- Before KingbaseES completes the target environment verification, all capability items will be kept as "Not Executed/Pending Vendor".
- HighGo commercial version will not extrapolate the SEE / IvorySQL results before completing the final verification.
- If you use the manufacturer's name, logo, quotation or joint signature, obtain the corresponding written permission first; if there is no permission, only the factual product name will be quoted.
- Release links point to verification records with version boundaries, not just ecological matrix status labels.
