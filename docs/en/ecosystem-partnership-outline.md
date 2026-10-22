# Domestic database joint case outline

**Language:** [English](ecosystem-partnership-outline.md) | [Simplified Chinese](../ecosystem-partnership-outline.md)

> Document status: A framework for external collaboration discussions, not a compatibility certification or statement of support. AgentSQL currently supports PostgreSQL and MySQL natively; the first batch of joint vendors are planned to be Dianke KingbaseES and HighGo. Both must complete end-to-end verification in the target version environment provided by the vendor and be confirmed by both parties before a joint release conclusion can be formed.

## 1. Joint case value proposition

As a database-neutral security gateway, AgentSQL is located between Text2SQL applications, AI Agents and domestic databases. It provides a unified security gate for model access to the database: API Key authentication, SQL AST rule evaluation, object and column authorization, result masking and full-link auditing. Database vendors do not need to bind upper-layer AI applications to a single implementation. They can work with AgentSQL to demonstrate that "AI access to databases must also go through controllable, explainable, and traceable security boundaries" and publish cases verified by both parties as AI security best practices.

The external caliber must be based on actual measurement evidence: the protocol connectability does not mean that the SQL dialect, system directory, driver, type, `EXPLAIN`, cancellation semantics and fault shutdown are all compatible. When the target version verification has not been completed, only "under joint evaluation" or "under adaptation verification" will be used, and "supported", "native compatible" or "joint certification" will not be used.

## 2. Minimum verifiable scenario

### 2.1 Unified architecture

```text
Text2SQL Application/AI Agent/MCP Client
                  ↓
AgentSQL Security Gateway (Authentication · AST Rules · Authorization · masking · Auditing)
                  ↓
Vendor Database Docker/Authorized Test Instance (synthetic demo data only)
```

Each manufacturer reuses the same set of skeletons and only replaces the database target version, legal image or test instance, official recommended driver and necessary dialect configuration. Verification must cover normal paths and fail-closed paths; only "driver connection successful" cannot be accepted as a joint case.

### 2.2 Demo script (5 steps)

| steps | Demonstration action | Expected picture/acceptance point |
| --- | --- | --- |
| 1. Connect to data source | Deploy AgentSQL and manufacturer database test instances in an isolated environment and record the versions of both parties; use a minimum-privileged test account to add a data source and test the connection. | The AgentSQL console shows that the data source connection is successful, and the data source type, target version and deployment form are archived; the page and log do not display the password. |
| 2. Discovery and Rules | Run sensitivity discovery on clearly selected demo tables such as `customers`, manually confirm mobile phone number/email candidate, generate and enable masking rules; authorize only the required table columns for the demo Agent | The discovery page displays candidate categories and confidence but does not return the original samples; masking rules and Agent authorization boundaries are visible in the console, and unauthorized objects remain rejected by default |
| 3. Dangerous SQL interception | Through AgentSQL, submit a `UPDATE` test case against `customers` without `WHERE`, and fill in `reason`. Adjust identifiers for the target database dialect. | Return to `deny`, the screen displays the rule hit and the reason; review the test table to confirm that the data has not changed, and the decision can be traced during the audit. |
| 4. masking query | Read authorized columns such as `phone` and `email` of `customers` through controlled query | The query is `allow` or has an explainable alarm; the mobile phone number and email address in the results are masked according to the enabled rules, and the masked cell information is displayed on the page without displaying the original values. |
| 5. Audit export | Filter by Agent, data source and time range on the audit page, view allow/deny records and export them | Details can be found in SQL, hit objects, rules, decisions and time consumption; CSV or JSONL is successfully exported, and the export behavior generates `audit.export` management traces |

The discovery capabilities of step 2, the dialect parsing of step 3, and the column origin/masking of step 4 must all be verified on a real version of the target database. If any step can only be safely rejected, it should be truthfully recorded as a known boundary, and no closing rules or relaxing the fail-closed method should be used in exchange for "run-through".

### 2.3 Minimum acceptance evidence

- AgentSQL version, database product full name, version, compatibility mode, deployment form and driver version list.
- Reproducible, non-sensitive configuration template and demo data generation; all keys, passwords, and internal addresses cleared.
- Screenshots or screen recordings of the five-step script, as well as evidence of the results exported by allowing, denying, desensitizing, and auditing.
- Dialect, type, system directory, `EXPLAIN`, timeout cancellation, and known restriction logging; safe rejection must be demonstrated when parsing is indeterminate.
- The conclusions and external wording confirmed by the technical leaders of both parties clarify the applicable version and the scope of inapplicability.

## 3. Demonstration data caliber

Uniformly use public, reproducibly generated synthetic sample data without touching any real customer or production data. The table structure follows the business caliber of AgentSQL demo:

| presentation table | Purpose | Data requirements |
| --- | --- | --- |
| `customers` | Display customer query, column authorization and masking | Use synthetic customer ID, name, mobile phone number, email, region and time fields; mobile phone number and email are only used for verification rules and must not come from real contacts |
| `orders` | Display common related queries between customers and orders | Use synthetic order ID, customer ID, product ID, quantity, amount, status and time fields, and do not include real transactions |

The schema, identifiers, data types, and table building statements can be adjusted per database dialect, but the business meaning, use case magnitudes, and expected security decisions remain consistent. External materials must be marked as "all synthetic demonstration data"; the distribution of database images, drivers and sample data scripts must also comply with the manufacturer's license.

## 4. Division of labor between the two parties

| work item | AgentSQL side | Database vendor | mutual confirmation |
| --- | --- | --- | --- |
| Environment and Deployment | Provides AgentSQL gateway, deployment instructions, non-sensitive configuration templates and problem location | Provide legal test environment/image, target version, minimum privilege account, network conditions and official recommended driver | Environment topology, version freezing, test windows and data destruction methods |
| Compatibility verification | Responsible for access, rules, authorization, masking, auditing and fail-closed verification, and recording differences | Assist in confirming connection parameters, dialect, system directory, type, `EXPLAIN`, error code and driver behavior | Passed items, restricted items, failed items and whether adaptive development is required |
| Demonstration and Evidence | Provide a unified five-step script, demonstration data caliber, screenshot/screen recording list and audit evidence | Review the behavior on the database side to confirm that the test table has not been modified by dangerous SQL | Reproducibility, conclusion boundaries and public evidence |
| joint copywriting | Draft case pages, technical papers and presentation notes to ensure accurate representation of AgentSQL capabilities | Provide manufacturer brand specifications, product introduction, database-side technical review and release channels | Title, logo usage, version size, release time and contact person |

## 5. List of joint publications

- **official website joint case page**: problem background, joint architecture, five-step demonstration, measured version, key screenshots, known boundaries and acquisition methods.
- **public account technical article**: Suggested title one "Let AI Agent securely access domestic databases: AgentSQL × <Manufacturer/Product> joint practice"; Suggested title two "From Text2SQL to auditable execution: <Product> AI data access security five-step verification". The "joint practice" in the title will only be used after both parties have completed acceptance and confirmed it in writing.
- **technical attachment**: masked deployment topology, version matrix, demonstration data description, verification list and known limitations; API Key, `AGENTSQL_SECRET`, data source password or internal address are not disclosed.
- **Joint live broadcast/screen recording in the group (optional)**: 15–30 minutes to complete the background, structure, five-step practice and Q&A; prepare in advance to disconnect the network screen recording and audit export after masking as a backup.

## 6. Implementation steps

The first batch of joint manufacturers is planned to be **Dianke KingbaseES + HighGo (including IvorySQL Community Edition / HighGo Commercial Edition)**, a total of two. The two companies can advance in parallel; if the test environment, technical contacts or verification windows need to be shifted, Kingbase ES will be used first, followed by HighGo. This sequence is a implementation plan and does not mean that a cooperation agreement has been signed, adaptation has been completed, or compatibility certification has been obtained.

HighGo HighGo and IvorySQL are the commercial version and community version of the same manufacturer and product line: joint cooperation and technical contacts are unified according to the HighGo manufacturer. Demonstration and verification can cover the IvorySQL community version and HighGo commercial version respectively. The two lines must still record the target version, compatibility mode, driver and verification boundary respectively, and the result of one line shall not automatically replace the other line.

| stage | key action | Export conditions |
| --- | --- | --- |
| 1. Evaluation | Select 1–2 companies and specific product versions; confirm contact person, driver, license, test environment, compatibility mode and joint intention | The two parties form a one-page scope statement, unconfirmed items are put into the backlog, and no release date is promised. |
| 2. Trial run | Import unified synthetic data, run through the five-step script and record connection, parsing, rules, masking, auditing and security failure paths | The evidence is complete; problems are classified by "blocking release/known limitations/subsequent enhancements", and dangerous omissions are classified as release blocking items |
| 3. Final draft | Fixed version matrix, reproduction steps, screenshots/screen recordings, known limitations and joint copywriting | The technical and marketing leaders of both parties have confirmed in writing that the sensitive information has been cleared. |
| 4. Release | Simultaneously launch case pages and technical documents, arrange live broadcasts/screen recordings on demand; retain an entry for problem feedback | The release content is consistent with the acceptance version, and the conclusions of a single version are not extrapolated to other product lines or compatibility modes. |

After the first case is completed, the differences are accumulated into a reusable checklist before starting the second case. Any product upgrade, driver switching or compatibility mode change requires re-executing at least five steps before updating the external caliber.
