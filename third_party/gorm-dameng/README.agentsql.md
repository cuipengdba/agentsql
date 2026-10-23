# AgentSQL pinned DM8 driver fork

This is the `dm8` package from `github.com/godoes/gorm-dameng` v0.7.2 under the upstream MIT license in `LICENSE`. The upstream module and checksum remain in the root `go.mod` / `go.sum`; the root `replace` makes builds use this reviewed local fork.

AgentSQL changes are limited to verified TLS configuration in `dm8/n.go`, `dm8/a.go`, and `dm8/security/zzi.go`:

- Accept explicit CA path and server name properties.
- Require an encrypted DM data channel when verification is configured.
- Require TLS 1.2 or newer and verify the certificate chain and server name, with no insecure fallback.
- Allow an optional client certificate pair for installations that require mutual authentication.

The fork is intentionally pinned. Update it only alongside an upstream diff review, the verified-TLS negative test matrix, and a licensed live DM8 regression.
