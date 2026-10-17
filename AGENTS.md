# AgentSQL release work

When the user asks to develop or release a version, own the complete delivery: product and security design, implementation, automated and real integration tests, upgrade and recovery rehearsal, GitHub CI and Release, signed packages, GHCR images, the official website, the Live Demo, and public post-release smoke tests. Follow [docs/RELEASE_ROUTINE.md](docs/RELEASE_ROUTINE.md) and record the actual evidence for that version. Do not describe a step as complete until it passes.

Keep database support claims tied to verified versions, protocol modes, and test evidence. A new version number does not promote every registered database to full production support.

Use `D:\CodexLS` for new Codex workspaces, temporary files, caches, and outputs. Respect explicitly selected existing repository and document paths.
