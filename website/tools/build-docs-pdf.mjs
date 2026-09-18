import { spawn } from 'node:child_process';
import {
  access,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rename,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises';
import { readFileSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, extname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const VERSION = 'v0.2.0';
const REPOSITORY_BLOB = 'https://github.com/cuipengdba/agentsql-gateway/blob/main';
const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const REPOSITORY_ROOT = resolve(SCRIPT_DIR, '..', '..');
const DOCS_ROOT = resolve(REPOSITORY_ROOT, 'docs');
const OUTPUT_DIR = resolve(REPOSITORY_ROOT, 'website', 'public', 'assets', 'docs');
const MINIMUM_PDF_BYTES = 20_000;

const DOCUMENTS = [
  {
    source: resolve(DOCS_ROOT, 'GETTING_STARTED.md'),
    output: 'agentsql-getting-started-v0.2.0.pdf',
    title: '快速上手',
    slug: 'getting-started',
  },
  {
    source: resolve(DOCS_ROOT, 'USER_GUIDE.md'),
    output: 'agentsql-user-guide-v0.2.0.pdf',
    title: '使用手册',
    slug: 'user-guide',
  },
  {
    source: resolve(DOCS_ROOT, 'INTEGRATIONS.md'),
    output: 'agentsql-mcp-integrations-v0.2.0.pdf',
    title: 'MCP 接入指南',
    slug: 'mcp-integrations',
  },
];

const MIME_TYPES = new Map([
  ['.png', 'image/png'],
  ['.jpg', 'image/jpeg'],
  ['.jpeg', 'image/jpeg'],
  ['.gif', 'image/gif'],
  ['.webp', 'image/webp'],
  ['.svg', 'image/svg+xml'],
]);

function escapeHtml(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function isWithin(parent, child) {
  const rel = relative(parent, child);
  return rel === '' || (!rel.startsWith(`..${sep}`) && rel !== '..' && !isAbsolute(rel));
}

function splitTargetSpec(spec) {
  const trimmed = spec.trim();
  if (trimmed.startsWith('<')) {
    const end = trimmed.indexOf('>');
    if (end !== -1) return trimmed.slice(1, end);
  }

  let depth = 0;
  let escaped = false;
  for (let index = 0; index < trimmed.length; index += 1) {
    const character = trimmed[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === '\\') {
      escaped = true;
      continue;
    }
    if (character === '(') depth += 1;
    if (character === ')' && depth > 0) depth -= 1;
    if (/\s/.test(character) && depth === 0) return trimmed.slice(0, index);
  }
  return trimmed;
}

function rewriteLinkTarget(rawTarget, sourcePath) {
  const target = splitTargetSpec(rawTarget);
  if (/^(?:https?:|mailto:)/i.test(target) || target.startsWith('#')) return target;
  if (/^[a-z][a-z\d+.-]*:/i.test(target) || target.startsWith('//')) return null;

  const hashIndex = target.indexOf('#');
  const pathPart = hashIndex === -1 ? target : target.slice(0, hashIndex);
  const fragment = hashIndex === -1 ? '' : target.slice(hashIndex);
  const absoluteTarget = resolve(dirname(sourcePath), decodeURIComponent(pathPart));
  if (!isWithin(REPOSITORY_ROOT, absoluteTarget)) return null;
  const repositoryPath = relative(REPOSITORY_ROOT, absoluteTarget).split(sep).join('/');
  return `${REPOSITORY_BLOB}/${repositoryPath}${fragment}`;
}

function imageDataUri(rawTarget, sourcePath) {
  const target = splitTargetSpec(rawTarget);
  if (/^[a-z][a-z\d+.-]*:/i.test(target) || target.startsWith('//')) {
    throw new Error(`Only local images under docs/ are allowed: ${target}`);
  }
  const hashIndex = target.indexOf('#');
  const pathPart = hashIndex === -1 ? target : target.slice(0, hashIndex);
  const candidate = resolve(dirname(sourcePath), decodeURIComponent(pathPart));
  let realCandidate;
  try {
    realCandidate = realpathSync(candidate);
  } catch {
    throw new Error(`Markdown image does not exist: ${target}`);
  }
  const realDocsRoot = realpathSync(DOCS_ROOT);
  if (!isWithin(realDocsRoot, realCandidate)) {
    throw new Error(`Markdown image escapes docs/: ${target}`);
  }
  const mimeType = MIME_TYPES.get(extname(realCandidate).toLowerCase());
  if (!mimeType) throw new Error(`Unsupported Markdown image type: ${target}`);
  return `data:${mimeType};base64,${readFileSync(realCandidate).toString('base64')}`;
}

function findClosingBracket(text, openingIndex) {
  let depth = 0;
  let escaped = false;
  for (let index = openingIndex; index < text.length; index += 1) {
    const character = text[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === '\\') {
      escaped = true;
      continue;
    }
    if (character === '[') depth += 1;
    if (character === ']') {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function findBalancedParenthesis(text, openingIndex) {
  let depth = 0;
  let escaped = false;
  for (let index = openingIndex; index < text.length; index += 1) {
    const character = text[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (character === '\\') {
      escaped = true;
      continue;
    }
    if (character === '(') depth += 1;
    if (character === ')') {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function plainText(value) {
  return value
    .replaceAll('`', '')
    .replaceAll('**', '')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .trim();
}

function renderOrdinaryInline(text, context) {
  let html = '';
  let index = 0;
  while (index < text.length) {
    if (text[index] === '\uE000') {
      const tokenEnd = text.indexOf('\uE001', index + 1);
      if (tokenEnd !== -1) {
        const tokenIndex = Number.parseInt(text.slice(index + 1, tokenEnd), 10);
        const token = context.inlineCodeTokens?.[tokenIndex];
        if (token !== undefined) {
          html += token;
          index = tokenEnd + 1;
          continue;
        }
      }
    }

    const isImage = text[index] === '!' && text[index + 1] === '[';
    const isLink = text[index] === '[';
    if (isImage || isLink) {
      const bracketStart = isImage ? index + 1 : index;
      const bracketEnd = findClosingBracket(text, bracketStart);
      if (bracketEnd !== -1 && text[bracketEnd + 1] === '(') {
        const targetEnd = findBalancedParenthesis(text, bracketEnd + 1);
        if (targetEnd !== -1) {
          const label = text.slice(bracketStart + 1, bracketEnd);
          const rawTarget = text.slice(bracketEnd + 2, targetEnd);
          if (isImage) {
            const source = imageDataUri(rawTarget, context.sourcePath);
            html += `<img src="${source}" alt="${escapeHtml(plainText(label))}">`;
          } else {
            const href = rewriteLinkTarget(rawTarget, context.sourcePath);
            const renderedLabel = renderInline(label, context);
            html += href
              ? `<a href="${escapeHtml(href)}">${renderedLabel}</a>`
              : renderedLabel;
          }
          index = targetEnd + 1;
          continue;
        }
      }
    }

    if (text.startsWith('**', index)) {
      const end = text.indexOf('**', index + 2);
      if (end !== -1) {
        html += `<strong>${renderOrdinaryInline(text.slice(index + 2, end), context)}</strong>`;
        index = end + 2;
        continue;
      }
    }

    if (text[index] === '<') {
      const end = text.indexOf('>', index + 1);
      if (end !== -1) {
        const possibleUrl = text.slice(index + 1, end);
        if (/^https?:\/\/[^\s]+$/i.test(possibleUrl)) {
          html += `<a href="${escapeHtml(possibleUrl)}">${escapeHtml(possibleUrl)}</a>`;
          index = end + 1;
          continue;
        }
      }
    }

    if (text[index] === '\\' && index + 1 < text.length) {
      html += escapeHtml(text[index + 1]);
      index += 2;
      continue;
    }

    html += escapeHtml(text[index]);
    index += 1;
  }
  return html;
}

function renderInline(text, context) {
  const inlineCodeTokens = [];
  let tokenized = '';
  let ordinaryStart = 0;
  let index = 0;
  while (index < text.length) {
    if (text[index] !== '`') {
      index += 1;
      continue;
    }
    let runLength = 1;
    while (text[index + runLength] === '`') runLength += 1;
    const delimiter = '`'.repeat(runLength);
    const end = text.indexOf(delimiter, index + runLength);
    if (end === -1) {
      index += runLength;
      continue;
    }
    tokenized += text.slice(ordinaryStart, index);
    let code = text.slice(index + runLength, end).replaceAll('\n', ' ');
    if (/^\s.*\s$/.test(code) && !/^\s+$/.test(code)) code = code.slice(1, -1);
    const tokenIndex = inlineCodeTokens.push(`<code>${escapeHtml(code)}</code>`) - 1;
    tokenized += `\uE000${tokenIndex}\uE001`;
    index = end + runLength;
    ordinaryStart = index;
  }
  tokenized += text.slice(ordinaryStart);
  return renderOrdinaryInline(tokenized, { ...context, inlineCodeTokens });
}

function splitTableRow(line) {
  const cells = [];
  let current = '';
  let activeTicks = 0;
  let index = 0;
  while (index < line.length) {
    const character = line[index];
    if (character === '\\' && index + 1 < line.length) {
      current += line[index + 1];
      index += 2;
      continue;
    }
    if (character === '`') {
      let runLength = 1;
      while (line[index + runLength] === '`') runLength += 1;
      if (activeTicks === 0) activeTicks = runLength;
      else if (activeTicks === runLength) activeTicks = 0;
      current += '`'.repeat(runLength);
      index += runLength;
      continue;
    }
    if (character === '|' && activeTicks === 0) {
      cells.push(current.trim());
      current = '';
      index += 1;
      continue;
    }
    current += character;
    index += 1;
  }
  cells.push(current.trim());
  if (line.trimStart().startsWith('|')) cells.shift();
  if (line.trimEnd().endsWith('|')) cells.pop();
  return cells;
}

function tableAlignments(line) {
  const cells = splitTableRow(line);
  if (cells.length === 0 || !cells.every((cell) => /^:?-{3,}:?$/.test(cell.trim()))) return null;
  return cells.map((cell) => {
    const value = cell.trim();
    if (value.startsWith(':') && value.endsWith(':')) return 'center';
    if (value.endsWith(':')) return 'right';
    return 'left';
  });
}

function isFenceStart(line) {
  return line.match(/^\s{0,3}(`{3,}|~{3,})([^`]*)$/);
}

function isHorizontalRule(line) {
  const trimmed = line.trim();
  return /^(?:\*\s*){3,}$/.test(trimmed)
    || /^(?:-\s*){3,}$/.test(trimmed)
    || /^(?:_\s*){3,}$/.test(trimmed);
}

function listMatch(line) {
  const match = line.match(/^(\s*)([-+*]|\d+[.)])\s+(.+)$/);
  if (!match) return null;
  const indent = [...match[1]].reduce((width, character) => width + (character === '\t' ? 4 : 1), 0);
  return { indent, ordered: /^\d/.test(match[2]), text: match[3] };
}

function renderListTree(items, context) {
  function renderChildren(children) {
    let result = '';
    let currentTag = null;
    for (const item of children) {
      const tag = item.ordered ? 'ol' : 'ul';
      if (tag !== currentTag) {
        if (currentTag) result += `</${currentTag}>`;
        result += `<${tag}>`;
        currentTag = tag;
      }
      result += `<li>${renderInline(item.text, context)}${renderChildren(item.children)}</li>`;
    }
    if (currentTag) result += `</${currentTag}>`;
    return result;
  }
  return renderChildren(items);
}

function parseList(lines, startIndex, context) {
  const roots = [];
  const stack = [];
  let index = startIndex;
  while (index < lines.length) {
    const match = listMatch(lines[index]);
    if (!match) break;
    while (stack.length && match.indent <= stack.at(-1).indent) stack.pop();
    const item = { ...match, children: [] };
    const siblings = stack.length ? stack.at(-1).item.children : roots;
    siblings.push(item);
    stack.push({ indent: match.indent, item });
    index += 1;
  }
  return { html: renderListTree(roots, context), nextIndex: index };
}

function headingMatch(line) {
  const match = line.match(/^(#{1,4})\s+(.+?)\s*#*\s*$/);
  return match ? { level: match[1].length, text: match[2] } : null;
}

function startsBlock(lines, index) {
  const line = lines[index];
  if (line === undefined || line.trim() === '') return true;
  if (isFenceStart(line) || headingMatch(line) || isHorizontalRule(line) || listMatch(line)) return true;
  if (/^\s{0,3}>/.test(line)) return true;
  return index + 1 < lines.length && tableAlignments(lines[index + 1]) !== null;
}

function markdownToHtml(markdown, sourcePath) {
  const lines = markdown.replaceAll('\r\n', '\n').replaceAll('\r', '\n').split('\n');
  const context = { sourcePath };
  const toc = [];
  const blocks = [];
  let sectionNumber = 0;
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    if (line.trim() === '') {
      index += 1;
      continue;
    }

    const fence = isFenceStart(line);
    if (fence) {
      const markerCharacter = fence[1][0];
      const minimumLength = fence[1].length;
      const language = fence[2].trim().split(/\s+/, 1)[0].replace(/[^a-zA-Z0-9_-]/g, '');
      const codeLines = [];
      index += 1;
      let closed = false;
      while (index < lines.length) {
        const closing = lines[index].match(/^\s{0,3}(`+|~+)\s*$/);
        if (closing && closing[1][0] === markerCharacter && closing[1].length >= minimumLength) {
          closed = true;
          index += 1;
          break;
        }
        codeLines.push(lines[index]);
        index += 1;
      }
      if (!closed) throw new Error(`Unclosed code fence in ${basename(sourcePath)}`);
      const languageClass = language ? ` class="language-${escapeHtml(language)}"` : '';
      blocks.push(`<pre><code${languageClass}>${escapeHtml(codeLines.join('\n'))}</code></pre>`);
      continue;
    }

    const heading = headingMatch(line);
    if (heading) {
      let id = '';
      if (heading.level === 2 || heading.level === 3) {
        sectionNumber += 1;
        id = `section-${sectionNumber}`;
        toc.push({ level: heading.level, text: plainText(heading.text), id });
      }
      const idAttribute = id ? ` id="${id}"` : '';
      blocks.push(`<h${heading.level}${idAttribute}>${renderInline(heading.text, context)}</h${heading.level}>`);
      index += 1;
      continue;
    }

    const alignments = index + 1 < lines.length ? tableAlignments(lines[index + 1]) : null;
    if (alignments) {
      const headers = splitTableRow(line);
      const columnCount = headers.length;
      if (alignments.length !== columnCount) {
        throw new Error(`Malformed table delimiter in ${basename(sourcePath)} at line ${index + 2}`);
      }
      index += 2;
      const rows = [];
      while (index < lines.length && lines[index].trim() !== '' && lines[index].includes('|')) {
        const cells = splitTableRow(lines[index]);
        rows.push(Array.from({ length: columnCount }, (_, cellIndex) => cells[cellIndex] ?? ''));
        index += 1;
      }
      const headerHtml = headers.map((cell, cellIndex) => `<th class="align-${alignments[cellIndex]}">${renderInline(cell, context)}</th>`).join('');
      const bodyHtml = rows.map((row) => `<tr>${row.map((cell, cellIndex) => `<td class="align-${alignments[cellIndex]}">${renderInline(cell, context)}</td>`).join('')}</tr>`).join('');
      blocks.push(`<div class="table-container"><table><thead><tr>${headerHtml}</tr></thead><tbody>${bodyHtml}</tbody></table></div>`);
      continue;
    }

    if (/^\s{0,3}>/.test(line)) {
      const quoteLines = [];
      while (index < lines.length && /^\s{0,3}>/.test(lines[index])) {
        quoteLines.push(lines[index].replace(/^\s{0,3}>\s?/, ''));
        index += 1;
      }
      blocks.push(`<blockquote><p>${quoteLines.map((quoteLine) => renderInline(quoteLine, context)).join('<br>')}</p></blockquote>`);
      continue;
    }

    if (listMatch(line)) {
      const list = parseList(lines, index, context);
      blocks.push(list.html);
      index = list.nextIndex;
      continue;
    }

    if (isHorizontalRule(line)) {
      blocks.push('<hr>');
      index += 1;
      continue;
    }

    const paragraph = [line.trim()];
    index += 1;
    while (index < lines.length && !startsBlock(lines, index)) {
      paragraph.push(lines[index].trim());
      index += 1;
    }
    blocks.push(`<p>${renderInline(paragraph.join(' '), context)}</p>`);
  }

  return { body: blocks.join('\n'), toc };
}

function renderToc(toc) {
  if (toc.length === 0) return '<p>本手册没有二级或三级标题。</p>';
  return `<ol>${toc.map((entry) => `<li class="toc-level-${entry.level}"><a href="#${entry.id}">${escapeHtml(entry.text)}</a></li>`).join('')}</ol>`;
}

function htmlDocument(document, rendered) {
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <title>AgentSQL ${escapeHtml(document.title)} ${VERSION}</title>
  <style>
    @page {
      size: A4;
      margin: 17mm 16mm 20mm;
      @bottom-center { content: "AgentSQL · agentsql.cn · v0.2.0"; font-family: "Microsoft YaHei", "PingFang SC", "Noto Sans CJK SC", sans-serif; font-size: 8pt; color: #64748b; }
    }
    * { box-sizing: border-box; }
    html { color: #172033; background: #fff; font-family: "Microsoft YaHei", "PingFang SC", "Noto Sans CJK SC", sans-serif; font-size: 10.5pt; }
    body { margin: 0; line-height: 1.7; }
    .cover { min-height: 240mm; display: flex; flex-direction: column; justify-content: center; text-align: center; break-after: page; page-break-after: always; }
    .brand { margin: 0 0 8mm; color: #075985; font-size: 31pt; font-weight: 800; letter-spacing: .02em; }
    .brand-cn { margin: 0 0 18mm; color: #475569; font-size: 16pt; letter-spacing: .28em; }
    .cover h1 { margin: 0 0 10mm; color: #0f172a; font-size: 27pt; line-height: 1.3; }
    .cover-meta { margin: 2.5mm 0; color: #475569; font-size: 11pt; }
    .release-candidate { display: inline-block; align-self: center; margin-top: 10mm; padding: 2mm 5mm; border: .4mm solid #0284c7; border-radius: 999px; color: #0369a1; font-size: 9.5pt; font-weight: 700; }
    .toc { break-after: page; page-break-after: always; }
    .toc h1 { margin-top: 0; font-size: 24pt; }
    .toc ol { padding-left: 0; list-style: none; }
    .toc li { margin: 2.3mm 0; }
    .toc-level-3 { padding-left: 7mm; font-size: 9.5pt; }
    .toc a { color: #075985; text-decoration: none; }
    main h1 { margin: 0 0 10mm; color: #0f172a; font-size: 24pt; line-height: 1.3; }
    h2 { margin: 10mm 0 3.5mm; padding-bottom: 2mm; border-bottom: .35mm solid #cbd5e1; color: #0f3c5c; font-size: 17pt; line-height: 1.35; break-after: avoid-page; page-break-after: avoid; }
    h3 { margin: 7mm 0 2.5mm; color: #164e63; font-size: 13pt; line-height: 1.4; break-after: avoid-page; page-break-after: avoid; }
    h4 { margin: 5mm 0 2mm; color: #334155; font-size: 11pt; }
    p { margin: 0 0 3.5mm; orphans: 3; widows: 3; }
    ul, ol { margin: 2mm 0 4mm; padding-left: 7mm; }
    li { margin: 1.3mm 0; orphans: 3; widows: 3; }
    a { color: #0369a1; text-decoration: underline; text-underline-offset: 1px; }
    strong { color: #0f172a; }
    code { padding: .2mm .9mm; border-radius: 1mm; background: #eef2f7; color: #7c2d12; font-family: Consolas, "Courier New", monospace; font-size: .9em; overflow-wrap: anywhere; word-break: break-word; white-space: normal; }
    pre { margin: 3mm 0 5mm; padding: 4mm; border: .3mm solid #cbd5e1; border-radius: 2mm; background: #f1f5f9; color: #172033; font-family: Consolas, "Courier New", monospace; font-size: 8.4pt; line-height: 1.55; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; break-inside: avoid; page-break-inside: avoid; }
    pre code { padding: 0; background: transparent; color: inherit; font-size: inherit; white-space: pre-wrap; }
    blockquote { margin: 4mm 0; padding: 3mm 4mm; border-left: 1.2mm solid #38bdf8; background: #f0f9ff; color: #334155; break-inside: avoid; page-break-inside: avoid; }
    blockquote p { margin: 0; }
    hr { margin: 7mm 0; border: 0; border-top: .35mm solid #cbd5e1; }
    img { display: block; max-width: 100%; max-height: 220mm; height: auto; margin: 4mm auto 6mm; object-fit: contain; break-inside: avoid; page-break-inside: avoid; }
    .table-container { margin: 3mm 0 5mm; overflow: visible; }
    table { width: 100%; border-collapse: collapse; table-layout: fixed; font-size: 7.8pt; line-height: 1.42; }
    thead { display: table-header-group; }
    tr { break-inside: avoid; page-break-inside: avoid; }
    th, td { padding: 1.8mm 2mm; border: .25mm solid #cbd5e1; vertical-align: top; overflow-wrap: anywhere; word-break: break-word; white-space: normal; }
    th { background: #e2e8f0; color: #0f172a; font-weight: 700; }
    td code, th code { padding: 0; background: transparent; white-space: normal; overflow-wrap: anywhere; word-break: break-word; }
    .align-center { text-align: center; }
    .align-right { text-align: right; }
  </style>
</head>
<body>
  <section class="cover">
    <p class="brand">AgentSQL</p>
    <p class="brand-cn">智盾</p>
    <h1>${escapeHtml(document.title)}</h1>
    <p class="cover-meta">${VERSION}</p>
    <p class="cover-meta">https://agentsql.cn</p>
    <span class="release-candidate">发布候选</span>
  </section>
  <nav class="toc" aria-label="目录">
    <h1>目录</h1>
    ${renderToc(rendered.toc)}
  </nav>
  <main>${rendered.body}</main>
</body>
</html>`;
}

function validateHtml(html, document, rendered, temporaryDirectory) {
  const ids = new Set([...html.matchAll(/\sid="(section-\d+)"/g)].map((match) => match[1]));
  const links = [...html.matchAll(/href="#(section-\d+)"/g)].map((match) => match[1]);
  if (ids.size !== rendered.toc.length || links.length !== rendered.toc.length || links.some((id) => !ids.has(id))) {
    throw new Error(`TOC anchors do not match headings for ${document.output}`);
  }
  if (/href="file:\/\//i.test(html) || html.includes(temporaryDirectory)) {
    throw new Error(`Temporary file navigation leaked into ${document.output}`);
  }
  const navigationTargets = [...html.matchAll(/href="([^"]+)"/g)].map((match) => match[1]);
  if (navigationTargets.some((target) => !/^(?:https?:|mailto:|#)/i.test(target) && /\.md(?:#|$)/i.test(target))) {
    throw new Error(`Relative Markdown link remains in ${document.output}`);
  }
  if (navigationTargets.some((target) => /^https:\/\/github\.com\/cuipengdba\//i.test(target)
    && !/^https:\/\/github\.com\/cuipengdba\/agentsql-gateway(?:\/|$)/i.test(target))) {
    throw new Error(`Unexpected GitHub repository name in ${document.output}`);
  }
  if (document.slug === 'getting-started' && !/src="data:image\/png;base64,/.test(html)) {
    throw new Error('Getting Started image was not embedded as a PNG data URI');
  }
  if (document.slug === 'mcp-integrations' && !html.includes('&lt;Agent API Key&gt;')) {
    throw new Error('Angle-bracket API key placeholder was not escaped');
  }
  if (document.slug === 'user-guide' && !html.includes('page=1&amp;page_size=20')) {
    throw new Error('Ampersand text was not escaped');
  }
}

async function findEdge() {
  const candidates = [
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
  ];
  for (const candidate of candidates) {
    try {
      await access(candidate);
      return candidate;
    } catch {
      // Probe the next documented location.
    }
  }
  throw new Error(`Microsoft Edge was not found. Checked:\n${candidates.join('\n')}`);
}

function runEdge(edgePath, argumentsList, timeoutMs = 60_000) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(edgePath, argumentsList, { windowsHide: true, stdio: ['ignore', 'ignore', 'pipe'] });
    let stderr = '';
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill('SIGKILL');
    }, timeoutMs);
    child.stderr.setEncoding('utf8');
    child.stderr.on('data', (chunk) => {
      if (stderr.length < 1_000_000) stderr += chunk;
    });
    child.on('error', (error) => {
      clearTimeout(timer);
      rejectPromise(error);
    });
    child.on('close', (code, signal) => {
      clearTimeout(timer);
      if (timedOut) {
        rejectPromise(new Error(`Edge PDF print timed out after ${timeoutMs / 1000}s.\n${stderr}`));
      } else if (code !== 0) {
        rejectPromise(new Error(`Edge PDF print failed with exit code ${code}${signal ? ` (${signal})` : ''}.\n${stderr}`));
      } else {
        resolvePromise({ stderr });
      }
    });
  });
}

async function validatePdf(pdfPath) {
  const details = await stat(pdfPath);
  if (!details.isFile() || details.size < MINIMUM_PDF_BYTES) {
    throw new Error(`PDF is missing or implausibly small: ${pdfPath} (${details.size} bytes)`);
  }
  const bytes = await readFile(pdfPath);
  if (bytes.subarray(0, 5).toString('ascii') !== '%PDF-') {
    throw new Error(`PDF signature is missing: ${pdfPath}`);
  }
  const tail = bytes.subarray(Math.max(0, bytes.length - 4096)).toString('latin1');
  if (!tail.includes('%%EOF')) throw new Error(`PDF EOF marker is missing: ${pdfPath}`);
  return details.size;
}

async function waitForPdf(pdfPath) {
  let lastError;
  for (let attempt = 0; attempt < 20; attempt += 1) {
    try {
      return await validatePdf(pdfPath);
    } catch (error) {
      lastError = error;
      await new Promise((resolvePromise) => setTimeout(resolvePromise, 100));
    }
  }
  throw lastError;
}

async function main() {
  const temporaryDirectory = await mkdtemp(join(tmpdir(), 'agentsql-docs-'));
  const stagedFiles = [];
  try {
    const edgePath = await findEdge();
    const generated = [];
    for (const document of DOCUMENTS) {
      const markdown = await readFile(document.source, 'utf8');
      const rendered = markdownToHtml(markdown, document.source);
      const html = htmlDocument(document, rendered);
      validateHtml(html, document, rendered, temporaryDirectory);

      const htmlPath = join(temporaryDirectory, `${document.slug}.html`);
      const temporaryPdf = join(temporaryDirectory, document.output);
      const profileDirectory = join(temporaryDirectory, `edge-profile-${document.slug}`);
      await mkdir(profileDirectory, { recursive: true });
      await writeFile(htmlPath, html, 'utf8');

      const edgeArguments = [
        '--headless=new',
        '--disable-gpu',
        '--no-first-run',
        '--no-default-browser-check',
        '--disable-extensions',
        '--disable-background-networking',
        `--user-data-dir=${profileDirectory}`,
        `--print-to-pdf=${temporaryPdf}`,
        '--no-pdf-header-footer',
        '--virtual-time-budget=5000',
        pathToFileURL(htmlPath).href,
      ];
      await runEdge(edgePath, edgeArguments);
      const size = await waitForPdf(temporaryPdf);
      generated.push({ document, temporaryPdf, size });
    }

    const userGuide = generated.find((item) => item.document.slug === 'user-guide');
    const otherSizes = generated.filter((item) => item !== userGuide).map((item) => item.size);
    if (!userGuide || userGuide.size < Math.max(...otherSizes)) {
      throw new Error('The User Guide PDF is unexpectedly smaller than another manual');
    }

    await mkdir(OUTPUT_DIR, { recursive: true });
    for (const item of generated) {
      const finalPath = join(OUTPUT_DIR, item.document.output);
      const stagedPath = `${finalPath}.${process.pid}-${Date.now()}.tmp`;
      stagedFiles.push(stagedPath);
      await copyFile(item.temporaryPdf, stagedPath);
      await validatePdf(stagedPath);
    }
    for (let index = 0; index < generated.length; index += 1) {
      const finalPath = join(OUTPUT_DIR, generated[index].document.output);
      await rename(stagedFiles[index], finalPath);
      stagedFiles[index] = null;
    }

    const actualFiles = (await readdir(OUTPUT_DIR)).sort();
    const expectedFiles = DOCUMENTS.map((document) => document.output).sort();
    if (JSON.stringify(actualFiles) !== JSON.stringify(expectedFiles)) {
      throw new Error(`Unexpected files in ${OUTPUT_DIR}: ${actualFiles.join(', ')}`);
    }

    for (const item of generated) {
      const finalPath = join(OUTPUT_DIR, item.document.output);
      const finalSize = await validatePdf(finalPath);
      process.stdout.write(`${item.document.output}: ${finalSize} bytes\n`);
    }
  } finally {
    for (const stagedPath of stagedFiles.filter(Boolean)) {
      await rm(stagedPath, { force: true }).catch(() => {});
    }
    await rm(temporaryDirectory, { recursive: true, force: true });
  }
}

main().catch((error) => {
  process.stderr.write(`${error.stack ?? error.message}\n`);
  process.exitCode = 1;
});
