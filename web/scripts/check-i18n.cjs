const fs = require("node:fs");
const path = require("node:path");
const ts = require("typescript");

const sourceRoot = path.resolve(__dirname, "../src");
const english = require("../src/i18n/en.json");
const missing = new Map();

function check(raw, file) {
  const key = raw.replace(/\s+/g, " ").trim();
  if (!/[\u3400-\u9fff]/.test(key) || key.length > 300 || english[key]) return;
  if (!missing.has(key)) missing.set(key, file);
}

function scan(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      if (entry.name !== "i18n") scan(file);
      continue;
    }
    if (!/\.tsx?$/.test(entry.name) || /\.test\./.test(entry.name)) continue;
    const source = ts.createSourceFile(file, fs.readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
    function visit(node) {
      if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isJsxText(node)) {
        check(node.text, path.relative(sourceRoot, file));
      }
      ts.forEachChild(node, visit);
    }
    visit(source);
  }
}

scan(sourceRoot);
if (missing.size) {
  for (const [value, file] of missing) console.error(`${file}: missing English translation for ${JSON.stringify(value)}`);
  process.exitCode = 1;
} else {
  console.log(`English catalog covers all Chinese source literals (${Object.keys(english).length} entries).`);
}
