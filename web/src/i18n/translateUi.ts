import english from "./en.json";
import type { AppLocale } from "./locale";
import { reviewedEnglish } from "./overrides";

const dictionary: Record<string, string> = { ...(english as Record<string, string>), ...reviewedEnglish };
const originalText = new WeakMap<Text, string>();
const originalAttributes = new WeakMap<Element, Map<string, string>>();
const translatedAttributes = ["title", "placeholder", "aria-label", "alt"] as const;
const excluded = "script, style, pre, code, textarea, [contenteditable], [data-no-translate]";
const dynamicEnglish: Array<[RegExp, (...values: string[]) => string]> = [
  [/^提交变更 \((\d+)\)$/, (count) => `Submit changes (${count})`],
  [/^共 ([\d,]+) 条$/, (count) => `Total: ${count} records`],
  [/^当前 active 版本：(.+)$/, (version) => `Current active version: ${version === "未观察到" ? "not observed" : version}`],
  [/^预估扫描 ([\d,.]+) 行$/, (rows) => `Estimated scan: ${rows} rows`],
  [/^已放行 · 返回 ([\d,]+) 行(.*)$/, (rows, latency) => `Allowed · ${rows} rows returned${latency}`],
  [/^已放行，请注意结果集规模 · 返回 ([\d,]+) 行(.*)$/, (rows, latency) => `Allowed; review result size · ${rows} rows returned${latency}`],
  [/^已转人工审批 · 用户 SQL 未执行(.*)$/, (latency) => `Sent for human approval · SQL not executed${latency}`],
  [/^已拦截 · 用户 SQL 未执行(.*)$/, (latency) => `Blocked · SQL not executed${latency}`],
  [/^数据库执行失败 · 错误已留痕(.*)$/, (latency) => `Database execution failed · Error audited${latency}`],
  [/^结果超过上限，仅显示前 ([\d,]+) 行$/, (rows) => `Result exceeds limit; showing the first ${rows} rows`],
  [/^第 ([\d,]+) 列$/, (column) => `Column ${column}`],
  [/^第 ([\d,]+) 项表名$/, (item) => `Table name for item ${item}`],
  [/^第 ([\d,]+) 项 schema$/, (item) => `Schema for item ${item}`],
  [/^删除第 ([\d,]+) 项$/, (item) => `Delete item ${item}`],
  [/^等级 (.+)$/, (level) => `Level ${level}`],
  [/^有效期至 (.+)。请只放入可信 MCP 客户端的安全凭据配置；退出控制台后会失效。$/, (expiry) => `Valid until ${expiry}. Store it only in a trusted MCP client's secure credential settings; it expires when you sign out.`],
  [/^已成功提交 ([\d,]+) 项策略变更$/, (count) => `Submitted ${count} policy changes`],
  [/^通道 ID“(.+)”重复$/, (id) => `Duplicate channel ID “${id}”`],
  [/^通道“(.+)”的可靠投递仅支持 Webhook$/, (id) => `Channel “${id}” supports reliable delivery only for webhooks`],
  [/^可靠投递工作器异常：(.+)$/, (reason) => `Reliable delivery worker error: ${reason}`],
  [/^环比 ([↑↓→]) ([\d.]+)%$/, (direction, percent) => `Change ${direction} ${percent}%`],
  [/^总计：([\d,]+)$/, (count) => `Total: ${count}`],
  [/^放行：([\d,]+)$/, (count) => `Allowed: ${count}`],
  [/^告警：([\d,]+)$/, (count) => `Warnings: ${count}`],
  [/^待审批：([\d,]+)$/, (count) => `Pending approval: ${count}`],
  [/^拦截：([\d,]+)$/, (count) => `Blocked: ${count}`],
  [/^执行错误：([\d,]+)$/, (count) => `Execution errors: ${count}`],
];

function translated(value: string, locale: AppLocale): string {
  if (locale === "zh-CN") return value;
  const leading = value.match(/^\s*/)?.[0] || "";
  const trailing = value.match(/\s*$/)?.[0] || "";
  const key = value.trim().replace(/\s+/g, " ").replace(/([\u3400-\u9fff])\s+(?=[\u3400-\u9fff])/g, "$1");
  const replacement = dictionary[key];
  if (replacement) return leading + replacement + trailing;
  for (const [pattern, render] of dynamicEnglish) {
    const match = key.match(pattern);
    if (match) return leading + render(...match.slice(1)) + trailing;
  }
  return value;
}

function updateText(node: Text, locale: AppLocale) {
  if (node.parentElement?.closest(excluded)) return;
  const current = node.nodeValue || "";
  const previous = originalText.get(node);
  if (previous === undefined || (current !== previous && current !== translated(previous, "en-US"))) {
    originalText.set(node, current);
  }
  const source = originalText.get(node) || "";
  const next = translated(source, locale);
  if (current !== next) node.nodeValue = next;
}

function updateAttributes(element: Element, locale: AppLocale) {
  if (element.closest(excluded)) return;
  let sources = originalAttributes.get(element);
  if (!sources) {
    sources = new Map();
    originalAttributes.set(element, sources);
  }
  for (const name of translatedAttributes) {
    const current = element.getAttribute(name);
    if (current === null) continue;
    const previous = sources.get(name);
    if (previous === undefined || (current !== previous && current !== translated(previous, "en-US"))) {
      sources.set(name, current);
    }
    const next = translated(sources.get(name) || "", locale);
    if (current !== next) element.setAttribute(name, next);
  }
}

function updateTree(node: Node, locale: AppLocale) {
  if (node.nodeType === Node.TEXT_NODE) {
    updateText(node as Text, locale);
    return;
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return;
  const element = node as Element;
  if (element.matches(excluded)) return;
  updateAttributes(element, locale);
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT);
  while (walker.nextNode()) {
    const child = walker.currentNode;
    if (child.nodeType === Node.TEXT_NODE) updateText(child as Text, locale);
    else updateAttributes(child as Element, locale);
  }
}

/**
 * Translates legacy static console copy while the pages are migrated to explicit
 * locale keys. Only source strings in the checked-in catalog are eligible;
 * SQL/code blocks, editable fields, and arbitrary API content stay untouched.
 */
export function installUiTranslation(locale: AppLocale): () => void {
  // Ant Design places dialogs, tooltips, and messages in portals under body.
  const root = document.body;
  if (!root) return () => undefined;
  updateTree(root, locale);
  const observer = new MutationObserver((records) => {
    for (const record of records) {
      if (record.type === "characterData") updateTree(record.target, locale);
      else if (record.type === "attributes") updateTree(record.target, locale);
      else for (const child of record.addedNodes) updateTree(child, locale);
    }
  });
  observer.observe(root, {
    subtree: true,
    childList: true,
    characterData: true,
    attributes: true,
    attributeFilter: [...translatedAttributes],
  });
  return () => observer.disconnect();
}
