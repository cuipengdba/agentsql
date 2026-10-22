#!/usr/bin/env python3
"""Verify complete source/English documentation structure and source freshness."""
import hashlib
import json
import re
from html.parser import HTMLParser
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def executable_block(block):
    lines=block.splitlines()
    language=re.sub(r'^\s*[`~]+','',lines[0]).strip().lower()
    if language=='text':
        return ('text',len(lines))
    if language=='mermaid':
        return '\n'.join(re.sub(r'\[[^\]\n]*\]|"[^"\n]*"','<label>',line) for line in lines)
    normalized=[]
    for line in lines:
        if re.match(r'^\s*(?:#|//|--)(?:\s|[^\x00-\x7f])',line):
            normalized.append(re.match(r'^(\s*(?:#|//|--))',line)[1])
        elif language in {'yaml','yml','toml','bash','sh','shell','powershell','ps1','python','py'} and ' #' in line:
            before,sep,comment=line.partition(' #')
            normalized.append(before+sep if before.count('"')%2==0 and before.count("'")%2==0 else line)
        else: normalized.append(line)
    return '\n'.join(normalized)


def structure(text):
    headings, tables, code, lists = [], 0, [], []
    fence = None
    block = []
    for line in text.splitlines():
        match = re.match(r'^\s*(`{3,}|~{3,})', line)
        if match:
            if fence is None:
                fence = match.group(1)[0]
                block = [line]
            elif match.group(1)[0] == fence:
                block.append(line)
                code.append(executable_block('\n'.join(block)))
                fence = None
            continue
        if fence:
            block.append(line)
            continue
        match = re.match(r'^(#{1,6})\s+', line)
        if match:
            headings.append(len(match.group(1)))
        if line.strip().startswith('|'):
            tables += 1
        match = re.match(r'^(\s*)([-*+]|\d+[.)])\s+', line)
        if match:
            lists.append((len(match.group(1)), 'ordered' if match.group(2)[0].isdigit() else 'bullet'))
    return headings, tables, code, lists


class Website(HTMLParser):
    def __init__(self, text):
        super().__init__()
        self.sections = []
        self.tables = 0
        self.groups = 0
        self.feed(text)
    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == 'section': self.sections.append(values.get('id'))
        if tag == 'tr': self.tables += 1
        if tag == 'img' and 'wechat-group-' in values.get('src', ''): self.groups += 1


def main():
    document = json.loads((ROOT/'docs/en/translation-manifest.json').read_text(encoding='utf-8'))
    failures = []
    covered = set()
    for item in document['documents']:
        source, english = ROOT/item['source'], ROOT/item['english']
        covered.add(source.resolve())
        if hashlib.sha256(source.read_bytes()).hexdigest() != item['source_sha256']:
            failures.append(f'Stale English source hash: {item["source"]}')
        if not english.is_file():
            failures.append(f'Missing English document: {item["english"]}')
            continue
        if structure(source.read_text(encoding='utf-8-sig')) != structure(english.read_text(encoding='utf-8')):
            failures.append(f'Heading/table/code parity mismatch: {item["source"]}')
        fence = False
        for number,line in enumerate(english.read_text(encoding='utf-8').splitlines(),1):
            if re.match(r'^\s*(`{3,}|~{3,})',line):
                fence = not fence
                continue
            if fence: continue
            prose = re.sub(r'`[^`]*`|\]\([^)]*\)|<[^>]*>', '', line)
            if re.search(r'[\u3400-\u9fff]',prose):
                failures.append(f'Untranslated English prose: {item["english"]}:{number}')
    expected = {p.resolve() for p in (ROOT/'docs').rglob('*.md') if 'en' not in p.relative_to(ROOT/'docs').parts}
    for source in sorted(expected-covered): failures.append(f'Untranslated document: {source.relative_to(ROOT)}')
    for suffix in ['', '/demo']:
        zh = Website((ROOT/f'website/public{suffix}/index.html').read_text(encoding='utf-8'))
        en = Website((ROOT/f'website/public/en{suffix}/index.html').read_text(encoding='utf-8'))
        if zh.sections != en.sections or zh.tables != en.tables:
            failures.append(f'Website section/table parity mismatch: {suffix or "/"}')
        if zh.groups or en.groups: failures.append('Group QR codes remain on website')
    for name in ['README.md', 'README.en.md']:
        if 'wechat-group-' in (ROOT/name).read_text(encoding='utf-8'):
            failures.append('Group QR codes remain in '+name)
    if failures:
        raise SystemExit('\n'.join(failures))
    print(f'DOCS_I18N=PASS documents={len(document["documents"])} sites=2 full_structure_parity=true')


if __name__ == '__main__': main()
