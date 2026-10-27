# AgentSQL brand identity

**Language:** [English](BRAND.md) | [Simplified Chinese](../BRAND.md)

This version uses a unified vector logo: the blue shield represents the access boundary, the white database represents protected data, and the cyan connection point in the upper right represents Agent requesting access through the gateway. Graphics are available for light and dark interfaces; text logos are available in dark text and light text versions.

- Master: [ graphic logo ](../../brand/agentsql-mark.svg).
- Light background: [ horizontal text logo ](../../brand/agentsql-wordmark-light.svg).
- Dark background: [ horizontal text logo ](../../brand/agentsql-wordmark-dark.svg).
- The minimum recommended graphic size is 24 pixels, with at least one-eighth of the width of the graphic on all sides. Do not stretch, rewrite product names, or add unsubstantiated claims such as certifications or manufacturer endorsements to the logo.

The official website navigation, browser icon, GitHub README, Web login page and folding/expanding sidebar share the master template. Run `python scripts/sync-brand.py` to synchronize the replica, and CI uses `--check` to check consistency. The source license is in the repository's LICENSE and NOTICE; the graphics do not establish any third-party trademark search or registration result.
