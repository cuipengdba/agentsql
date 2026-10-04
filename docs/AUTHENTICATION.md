# AgentSQL v0.5 人员身份认证

v0.5 在原有本地密码、访问令牌和 refresh token 轮换之上，新增可选的 TOTP MFA、OIDC 与 LDAP/AD 登录。详细配置、威胁边界、OpenLDAP/AD 示例及迁移回滚说明见 [英文认证指南](en/AUTHENTICATION.md)。

AgentSQL 自管 TOTP 仅作为本地密码登录的第二因素。OIDC 的 MFA/认证强度策略由 IdP 负责；v0.5 的 LDAP 登录不会再叠加 AgentSQL TOTP。若必须使用 AgentSQL 自管 TOTP，请使用本地密码登录；若使用 OIDC/LDAP，请在外部身份系统强制 MFA。

核心边界：所有外部身份最终都必须映射到指定租户内的 AgentSQL 用户和角色；没有组映射、角色不存在、目录查询不唯一、OIDC 声明或签名无法验证、MFA 状态无法读取时一律拒绝登录。LDAP 禁止明文传输和跳过证书验证；OIDC 只接受 discovery 返回的 HTTPS 端点并使用 Authorization Code + PKCE、一次性 state/nonce 与 RS256 JWKS 校验。

OIDC client secret 与 LDAP bind password 只能通过配置中指定的环境变量读取。MFA 密钥使用由 `AGENTSQL_SECRET` 派生的密钥进行 AES-GCM 加密，恢复码仅保存 SHA-256 摘要。升级前必须成对备份 metadata 数据库与 `AGENTSQL_SECRET`。
