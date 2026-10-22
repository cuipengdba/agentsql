# AgentSQL Chinese / English UI

**语言 / Language:** [简体中文](i18n-ui.md) | [English](en/i18n-ui.md)

The official site serves Chinese at `/` and English at `/en/`. The corresponding Live Demo safety pages are `/demo/` and `/en/demo/`. Each page links to the other language and declares its canonical and alternate URL. The English safety page opens the separate demo origin with `?lang=en`; the console consumes this query, saves the choice in `agentsql.locale`, and removes the query parameter. A later manual switch persists across reloads. Existing users default to Chinese.

The GitHub repository keeps the full Chinese README and a current English README. GitHub's own interface language is controlled by GitHub; the README language links switch the project content.

The control console translates route labels, sign-in and shell controls directly. Ant Design uses its matching locale. The checked-in English catalog covers static Chinese source literals in the existing pages; `npm run check:i18n` fails when a new literal lacks a catalog entry. A small set of dynamic count and status phrases is handled separately. Only catalogued UI strings are translated in the legacy pages. SQL, code blocks, and editable fields are excluded. Technical identifiers and user-defined values are normally unchanged, though a value identical to a UI catalog entry can collide; new features should use explicit locale keys rather than adding more DOM translation rules.

Numbers and dates use the selected locale. The language button is available before sign-in and in the console header. English manuals beyond the README remain future documentation work; the English website marks detailed manuals and PDFs as Chinese.

The left menu has its own focusable vertical scroll region below the fixed brand area. It supports wheel, touchpad, keyboard, and collapsed navigation. The menu is reachable even when display scaling produces a short CSS viewport.
