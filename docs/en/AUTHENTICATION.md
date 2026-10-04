# Human authentication (v0.5)

AgentSQL v0.5 keeps local password login and rotating refresh tokens, and adds opt-in TOTP MFA, OpenID Connect, and LDAP authentication for the web control plane. These mechanisms authenticate human administrators only; Agent API keys and MCP authentication are unchanged.

## Security invariants

- Authentication is fail-closed. Provider timeouts, malformed claims, missing role mappings, ambiguous LDAP searches, unavailable token state, and invalid MFA state all reject login.
- Every successful login is converted to the existing tenant-bound AgentSQL principal and RBAC permission graph. An external group never grants a permission directly; it maps to explicit AgentSQL role IDs.
- Access tokens remain short-lived signed AgentSQL tokens. Refresh tokens are random, stored only as SHA-256 digests, rotated on every use, and revoked as a family on replay or logout.
- OIDC and LDAP credentials are not accepted as MCP or database credentials.
- AgentSQL TOTP is a second factor for local-password login. OIDC MFA and assurance policy remain the IdP's responsibility, and LDAP login does not add an AgentSQL TOTP step in v0.5; enforce MFA at the directory/IdP or use the local-password flow when AgentSQL-managed TOTP is required.

## TOTP MFA

Enable the feature globally, then let each signed-in user enroll from **Settings → Login security**:

```yaml
auth:
  mfa:
    enabled: true
    issuer: AgentSQL Production
```

Enrollment creates a 160-bit TOTP secret and ten recovery codes. The secret is AES-GCM encrypted with key material derived from `AGENTSQL_SECRET`; recovery codes are stored only as SHA-256 digests. Confirmation requires a valid 6-digit SHA-1 TOTP with a 30-second period. Login accepts the current step plus one adjacent step for clock skew and persists the last accepted counter to reject replay. Recovery codes are single use.

The enrollment response is the only time recovery codes are returned. Store them offline. Restarting a pending enrollment replaces that pending secret and its recovery codes; an enabled enrollment must first be verified and disabled. Disabling MFA requires a current TOTP or unused recovery code. Removing or changing `AGENTSQL_SECRET` makes existing MFA secrets unreadable and login fails closed.

## OpenID Connect

AgentSQL implements discovery and Authorization Code flow with PKCE S256. It validates one-time state, nonce, an RS256 signature selected by `kid`, exact issuer, audience/authorized party, subject, expiry, issued-at time, username, and groups. Discovery, token, JWKS, and logout endpoints must use HTTPS.

```yaml
auth:
  oidc:
    enabled: true
    issuer_url: https://id.example.com/realms/production
    client_id: agentsql
    client_secret_env: AGENTSQL_OIDC_CLIENT_SECRET
    redirect_url: https://agentsql.example.com/api/v1/auth/oidc/callback
    post_logout_url: https://agentsql.example.com/login
    tenant_id: tenant_default
    username_claim: preferred_username
    groups_claim: groups
    scopes: [openid, profile, email, groups]
    auto_provision: true
    group_role_map:
      agentsql-admins: [role_default_admin]
      agentsql-auditors: [role_default_auditor]
```

Mappings contain AgentSQL role IDs, not provider role names. At least one asserted group must map to a valid role in the configured tenant. With `auto_provision: false`, pre-create an external user through the users API using `auth_provider: oidc` and the immutable OIDC `sub` as `external_subject`. Usernames are also checked on every login; a silent rename never relinks an identity.

Provider logout is best-effort and separate from authorization. `POST /api/v1/auth/oidc/logout` always revokes the local access/refresh family first and then returns the discovered RP-initiated logout URL when available.

## LDAP and Active Directory

Plaintext LDAP is forbidden. Use `ldaps://`, or `ldap://` with StartTLS. Certificates are verified against the system roots plus an optional CA file; there is no insecure-skip-verify switch.

OpenLDAP example:

```yaml
auth:
  ldap:
    enabled: true
    url: ldaps://ldap.example.com:636
    ca_file: /etc/agentsql/ldap-ca.pem
    bind_dn: cn=agentsql-reader,ou=svc,dc=example,dc=com
    bind_password_env: AGENTSQL_LDAP_BIND_PASSWORD
    user_base_dn: ou=people,dc=example,dc=com
    user_filter: "(&(objectClass=person)(uid={username}))"
    username_attribute: uid
    group_base_dn: ou=groups,dc=example,dc=com
    group_filter: "(&(objectClass=groupOfNames)(member={user_dn}))"
    group_name_attribute: cn
    tenant_id: tenant_default
    auto_provision: true
    group_role_map:
      agentsql-admins: [role_default_admin]
```

For Active Directory, a typical user filter is `(&(objectClass=user)(sAMAccountName={username}))`, `username_attribute` is `sAMAccountName`, and a direct-membership group filter is `(&(objectClass=group)(member={user_dn}))`. Nested AD group expansion is not implemented in v0.5; configure direct groups or pre-resolve membership in the directory. Filter substitutions are LDAP-escaped, user search must return exactly one entry, and authentication is proved by binding a separate connection as the located user.

Bind passwords and OIDC client secrets are read only from the named environment variables. Do not place them in YAML, logs, images, or source control.

## Migration and rollback

The combined metadata stream adds `0016_human_auth`; the split metadata stream adds `0015_human_auth`. Both SQLite and PostgreSQL have matching down migrations. The migration adds external identities, MFA/recovery state, one-shot MFA challenges, and one-shot OIDC authorization transactions. Back up the metadata store and `AGENTSQL_SECRET` together before upgrading. Roll back only after disabling the three features and terminating active control-plane sessions; down migration deletes all MFA and external-identity linkage data.
