import { CopyOutlined } from "@ant-design/icons";
import { Button, Tooltip, message } from "antd";
import { useMemo } from "react";

const sqlTokenPattern = /(--[^\n]*|\/\*[\s\S]*?\*\/|'(?:''|[^'])*'|\b\d+(?:\.\d+)?\b|\b(?:SELECT|INSERT|UPDATE|DELETE|MERGE|FROM|WHERE|JOIN|LEFT|RIGHT|INNER|OUTER|ON|GROUP|ORDER|BY|HAVING|LIMIT|OFFSET|AS|AND|OR|NOT|NULL|IS|IN|EXISTS|CREATE|ALTER|DROP|TRUNCATE|TABLE|INDEX|VALUES|SET|INTO|RETURNING|BEGIN|COMMIT|ROLLBACK|EXPLAIN)\b)/gi;
const keywordPattern = /^(?:SELECT|INSERT|UPDATE|DELETE|MERGE|FROM|WHERE|JOIN|LEFT|RIGHT|INNER|OUTER|ON|GROUP|ORDER|BY|HAVING|LIMIT|OFFSET|AS|AND|OR|NOT|NULL|IS|IN|EXISTS|CREATE|ALTER|DROP|TRUNCATE|TABLE|INDEX|VALUES|SET|INTO|RETURNING|BEGIN|COMMIT|ROLLBACK|EXPLAIN)$/i;
const numberPattern = /^\d+(?:\.\d+)?$/;

function tokenClass(token: string): string {
  if (token.startsWith("--") || token.startsWith("/*")) return "audit-sql-comment";
  if (token.startsWith("'")) return "audit-sql-string";
  if (numberPattern.test(token)) return "audit-sql-number";
  if (keywordPattern.test(token)) return "audit-sql-keyword";
  return "";
}

function highlightedSQL(sql: string) {
  const nodes: Array<{ text: string; className: string }> = [];
  let cursor = 0;
  for (const match of sql.matchAll(sqlTokenPattern)) {
    const index = match.index ?? cursor;
    if (index > cursor) nodes.push({ text: sql.slice(cursor, index), className: "" });
    nodes.push({ text: match[0], className: tokenClass(match[0]) });
    cursor = index + match[0].length;
  }
  if (cursor < sql.length) nodes.push({ text: sql.slice(cursor), className: "" });
  return nodes;
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand("copy");
  document.body.removeChild(textarea);
  if (!copied) throw new Error("copy command failed");
}

interface SQLHighlightProps {
  title: string;
  value: string | null | undefined;
}

export function SQLHighlight({ title, value }: SQLHighlightProps) {
  const sql = value || "";
  const nodes = useMemo(() => highlightedSQL(sql), [sql]);

  const handleCopy = async () => {
    if (!sql) return;
    try {
      await copyText(sql);
      void message.success(`${title}已复制`);
    } catch {
      void message.error("复制失败，请手动选择文本");
    }
  };

  return (
    <section className="audit-sql-block">
      <header className="audit-sql-heading">
        <span>{title}</span>
        <Tooltip title={`复制${title}`}>
          <Button type="text" size="small" icon={<CopyOutlined />} disabled={!sql} onClick={() => void handleCopy()} aria-label={`复制${title}`} />
        </Tooltip>
      </header>
      <pre>{sql ? nodes.map((node, index) => <span className={node.className || undefined} key={`${index}-${node.text.length}`}>{node.text}</span>) : "—"}</pre>
    </section>
  );
}
