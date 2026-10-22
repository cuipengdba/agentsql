# AgentSQL v1.2.0 Release Notes

**Language:** [English](release-notes-v1.2.0.md) | [Simplified Chinese](../release-notes-v1.2.0.md)

## range

v1.2.0 Add optional MCP OAuth protected resource entry `/mcp/oauth` for stateless HTTP calls to MCP 2026-07-28. The original `/mcp` and `/mcp/2026-07-28` entries are retained. The new entry requires an external authorization server to issue an RS256 JWT Access Token targeting a precise resource URI, and requires a separate Agent Key request header. User mapping, tenant and `query.execute` permissions are re-verified for each request, and Agent permissions are not increased due to OAuth.

This release is limited to the same enabled console OIDC issuer and tenant, and does not claim to support any authorization servers, Opaque Tokens, all signing algorithms, or dynamic client registration. The client is responsible for PKCE, `resource` parameters, and IdP client registration. The specific measured combinations and rejection matrix are based on [ acceptance record ](v1.2.0-acceptance.md).

## Configuration and compatibility

`mcp.oauth.enabled` defaults to `false`. Configure `issuer_url`, `resource_url`, `tenant_id` and a single `required_scope` when enabled; issuer and tenant must match `auth.oidc`. Use HTTPS for production URIs and HTTP for local loopback testing. The OAuth Access Token is placed in the standard `Authorization: Bearer`, and the Agent Key is placed in `X-AgentSQL-Agent-Key`. Console session tokens and legacy human delegation tokens are not replacements for the OAuth Access Token.

The database schema has not been modified in this version. Both SQLite and PostgreSQL metadata/audit libraries use the official v1.1.0 package and the v1.2.0 release package with the same hash as the official Release to complete upgrades, backups, restores, and checks. Database support level does not change with this version.

The indirect dependency `source-map-js` used in front-end construction has been updated to 1.2.2; this version of the lock file has been checked by `npm audit` for 0 vulnerabilities.

## release cadence

This version has independent tags, official GitHub Release and precise version mirrors. According to the scheduled plan, the official website, public network Demo and GHCR `latest` will be synchronized to v1.5.0 after completion; the deployment of this version must use the accurate label and verification assets of v1.2.0. Incomplete acceptance will not be written as passed.
