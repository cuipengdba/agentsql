# AgentSQL v1.8.0 Release Notes

**Language:** [English](release-notes-v1.8.0.md) | [Simplified Chinese](../release-notes-v1.8.0.md)

v1.8.0Deepened PG compatible, restricted read-only paths for Vastbase G100 V3.0 Build 9. Console and management APIs can now be standalone`vastbase`Data source save`verify-full`, CA file and certificate host name, and verify the server identity in the specified isolation instance. This capability only applies to tested Builds, protocol modes, accounts, and single-instance topologies in the acceptance record; it does not represent full production support.

## New additions and improvements

- The management API configures the verification mode, CA absolute path and hostname for Vastbase explicit TLS, and rejects unverified server certificates. The new Vastbase data source in the console defaults to `verify-full`, which clearly displays bounded SQL and deployment constraints.
- Maintain single, explicit schema, single table, direct column projection SELECT with valid `WHERE` and `LIMIT`; database connection still checks G100 Build 9, `sql_compatibility=PG` and read-only transaction status. Table-level authorization, masking and persistent auditing are still performed according to the original secure link.
- CA and hostname verification for the specified isolated instance passes; bad CA, bad hostname, and the negative use case of disabling clear text connections through the HBA pass. Reconnection was successful after certificate rotation; old password was rejected after read-only account credential rotation.
- The console database type name is consistent with the actual acceptance range to avoid marking the accepted version as completely untested.

## Deployment requirements and compatibility boundaries

- Use a read-only database account with minimal privileges, provide the absolute path to the trusted CA file on the gateway side, and match the certificate hostname with the server certificate. The actual isolation measurement uses G100`ssl=on`, certificate and private key`0600`permissions, and prior to ordinary`host`regular`hostnossl ... reject`rule.
- Specify Build 9 to reject the protocol 3.0 startup package of the current pgx driver at `require_ssl=on`, even if the TLS handshake has been completed; the configuration verified in this version is `require_ssl=off` plus account-specific plaintext rejection rules. Other build and driver combinations must be revalidated.
- The empty TLS mode of the old data source can still be read to avoid disrupting existing installations without migration; administrators should explicitly change to `verify-full` before doing certificate and clear text rejection acceptance. Explicit `disable` is only used in controlled isolation environments.
- The actual test license is a BASIC/VECTOR temporary license, valid until 2027-01-12. Formal production licenses, active-standby high availability, failover, manufacturer certification, other Build/modes and production loads have not yet been verified and are not included in the support statement. PostgreSQL B2 column-level binding and B5 cross-request transactions also do not extend to Vastbase.
- There is no metadata/audit schema migration in this version; when upgrading from the v1.7.0 official package, you still need to back up metadata, audit and keys, and review and restore them according to [ release and acceptance ](v1.8.0-acceptance.md).

For design and negative use cases, see [ design description ](v1.8.0-vastbase-design.md). The final seal, real database client, official assets, official website and demo evidence can be found in [ acceptance record ](v1.8.0-acceptance.md).
