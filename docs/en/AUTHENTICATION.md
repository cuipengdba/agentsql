# AgentSQL personnel authentication (v3.0.x)

**Language:** [English](AUTHENTICATION.md) | [Simplified Chinese](../AUTHENTICATION.md)

> Current guidance: Applies to v3.1.0 and compatible 3.1.x maintenance releases. The historical introduction version and acceptance record retain the original facts; for daily entry, see [ document index ](README.md).

v0.5 adds optional TOTP MFA, OIDC and LDAP/AD login on top of the original local password, access token and refresh token rotation. For detailed configuration, threat boundary, OpenLDAP/AD examples and migration rollback instructions, see [. For detailed authentication configuration, refer to ](AUTHENTICATION-REFERENCE.md).

AgentSQL self-managed TOTP only acts as a second factor for local password logins. OIDC's MFA/authentication strength policy is the responsibility of the IdP; v0.5's LDAP login will no longer superimpose AgentSQL TOTP. If you must use AgentSQL to self-manage TOTP, use a local password to log in; if you use OIDC/LDAP, force MFA on the external identity system.

Core Boundary: All external identities must ultimately be mapped to AgentSQL users and roles within a given tenant; logins will be denied if there is no group mapping, the role does not exist, the directory query is not unique, the OIDC claim or signature cannot be verified, or the MFA status cannot be read. LDAP prohibits clear text transmission and skips certificate verification; OIDC only accepts HTTPS endpoints returned by discovery and uses Authorization Code + PKCE, one-time state/nonce and RS256 JWKS verification.

The OIDC client secret and LDAP bind password can only be read through the environment variables specified in the configuration. The MFA key is AES-GCM encrypted using a key derived from `AGENTSQL_SECRET`, and the recovery code only saves the SHA-256 digest. The metadata database and `AGENTSQL_SECRET` must be backed up in pairs before upgrading.

For the current MCP personnel delegation, OAuth/OIDC and session identity boundaries, see [MCP Access Guide ](INTEGRATIONS.md) and [v1.2.0 Acceptance ](v1.2.0-acceptance.md). The console login identity and Agent Key are independent identities and cannot be interchanged.
