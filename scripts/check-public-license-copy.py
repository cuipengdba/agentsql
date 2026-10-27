#!/usr/bin/env python3
"""Keep current AgentSQL public copy aligned with its Apache-2.0 license."""

from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
PUBLIC_ROOTS = (ROOT / "docs", ROOT / "website", ROOT / "deploy", ROOT / ".github",
                ROOT / "internal" / "chatbi" / "ui", ROOT / "web" / "src", ROOT / "brand")
PUBLIC_FILES = (*ROOT.glob("*.md"), ROOT / "NOTICE")
EXTENSIONS = {".md", ".html", ".css", ".js", ".ts", ".tsx", ".json", ".svg", ".txt", ".yml", ".yaml"}
SKIP_PARTS = {"third_party", "node_modules", ".git", "dist"}


def main() -> None:
    # Split the obsolete labels so this guard does not itself become public
    # search-result copy for a license the current product no longer offers.
    forbidden = (
        "COMMERCIAL" + "-LICENSE",
        "商业" + "授权",
        "商业" + "许可说明",
        "商业" + "双授权",
        "commercial " + "license",
        "commercial " + "authorization",
        "dual-" + "license",
    )
    files = list(PUBLIC_FILES)
    for root in PUBLIC_ROOTS:
        files.extend(path for path in root.rglob("*") if path.is_file() and path.suffix in EXTENSIONS)
    failures: list[str] = []
    for path in files:
        if any(part in SKIP_PARTS for part in path.relative_to(ROOT).parts):
            continue
        try:
            body = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for number, line in enumerate(body.splitlines(), 1):
            if any(term.casefold() in line.casefold() for term in forbidden):
                failures.append(f"{path.relative_to(ROOT)}:{number}")
    if (ROOT / ("COMMERCIAL" + "-LICENSE.md")).exists() or (ROOT / ("COMMERCIAL" + "-LICENSE.en.md")).exists():
        failures.append("obsolete root license document remains")
    for path in (ROOT / "LICENSE", ROOT / "NOTICE", ROOT / "README.md", ROOT / "README.en.md",
                 ROOT / "website/public/index.html", ROOT / "website/public/en/index.html"):
        if "Apache" not in path.read_text(encoding="utf-8"):
            failures.append(f"Apache-2.0 reference missing: {path.relative_to(ROOT)}")
    if failures:
        raise SystemExit("Public license copy check failed:\n" + "\n".join(failures))
    print(f"PUBLIC_LICENSE_COPY=PASS files={len(files)}")


if __name__ == "__main__":
    main()
