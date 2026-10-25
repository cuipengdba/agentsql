#!/usr/bin/env python3
"""Sync the canonical SVG logo to product surfaces; --check refuses drift."""
import argparse
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    mark = (ROOT/'brand/agentsql-mark.svg').read_text(encoding='utf-8')
    body = mark[mark.index('  <defs>'):mark.rindex('</svg>')]
    artifacts = {
        'website/public/favicon.svg': mark,
        'website/public/assets/brand/agentsql-mark.svg': mark,
        'web/public/favicon.svg': mark,
        'web/src/assets/agentsql-mark.svg': mark,
        'internal/chatbi/ui/mark.svg': mark,
    }
    for variant, color in [('light', '#142033'), ('dark', '#f0f6ff')]:
        wordmark = ('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 88" role="img" aria-label="AgentSQL">\n'
            '<g transform="translate(4 4) scale(1.25)">\n'+body+'</g>\n'
            f'<text x="104" y="60" fill="{color}" font-family="Segoe UI,Arial,sans-serif" font-size="49" font-weight="700" letter-spacing="-1.5">AgentSQL</text>\n</svg>\n')
        artifacts[f'brand/agentsql-wordmark-{variant}.svg'] = wordmark
        artifacts[f'website/public/assets/brand/agentsql-wordmark-{variant}.svg'] = wordmark
    failures=[]
    for name, content in artifacts.items():
        path=ROOT/name
        if args.check:
            if not path.exists() or path.read_text(encoding='utf-8') != content: failures.append(name)
        else:
            path.parent.mkdir(parents=True,exist_ok=True)
            path.write_text(content,encoding='utf-8',newline='\n')
    if failures: raise SystemExit('Brand assets differ: '+', '.join(failures))
    print(f'BRAND_ASSETS=PASS synchronized={len(artifacts)}')


if __name__ == '__main__': main()
