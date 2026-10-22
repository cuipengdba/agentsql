# AgentSQL v1.1.0 Release Notes

**Language:** [English](release-notes-v1.1.0.md) | [Simplified Chinese](../release-notes-v1.1.0.md)

## Capabilities of this edition

- MCP dual identity mode is extended to approval application, approval order reading and `execute_approved`. The immutable seal of the approval document contains the applicant user; the database simultaneously checks the tenant, agent, and applicant user during a single collection. Agent requests from wrong users or old orders cannot consume entrusted approval orders.
- B5 logical session is open in dual identity mode. The server derives cross-request principals from authenticated users, agents, and tenants, and binds persistence sessions, continuation proofs, and transaction plans. The DML grant continues to be taken from the same Agent, and the delegation identity does not increase database permissions.
- The newly issued MCP user delegation token can be revoked individually on the console with the original token, and active MCP requests check the revocation status one by one. v1.0.0's tokenless ID credentials retain their original validity for up to 30 minutes and can be revoked by exiting the parent session.
- Console passwords and LDAP login attempts are limited per minute based on tenants and accounts; the current limit is on a single-instance boundary.

## Compatibility and upgrade

SQLite merge metadata schema is increased from 20 to 21; PostgreSQL merge schema is simultaneously increased to 21. Separate metadata schema increased from 19 to 20; audit schema unchanged. Migrate new approval form application user fields and indexes. The application user of the old approval form is empty, and the application will continue to be executed according to the existing conditions in the original Agent mode; the old approval form cannot be received in dual-identity mode.

This release does not change the production support level for DM8, KingbaseES V9, Vastbase G100, or MySQL. Delegate B5 is only open along authenticated boundaries of existing PostgreSQL B5; still rejected if not proven by database dialect, schema, or audit.

## Phased public release

v1.1.0Independently create GitHub tags, official releases, and precise version assets. According to the release rhythm specified by the product leader, the official website and public website Live Demo arev1.1.0tov1.4.0Keep duringv1.0.0,arrivev1.5.0Update directly after completion; GHCR`latest`also keepv1.0.0, to avoid pointing to a default version different from the official website Demo. Please usev1.1.0Accurate version assets or images for this version deployment.

The official CI, asset signature, real client and upgrade recovery results are subject to the [v1.1.0 acceptance record ](v1.1.0-acceptance.md) and the official GitHub Release attachment; unfinished projects will not be written and passed.
