import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const toolsDirectory = dirname(fileURLToPath(import.meta.url));
const websiteDirectory = resolve(toolsDirectory, "..");
const cdpOrigin = "http://127.0.0.1:9445";

const targets = [
  {
    name: "OG",
    url: "http://127.0.0.1:8767/tools/og-cover.html",
    output: resolve(websiteDirectory, "public/assets/og-cover.png"),
    width: 1200,
    height: 630,
  },
  {
    name: "icon",
    url: "http://127.0.0.1:8767/tools/icon.html",
    output: resolve(websiteDirectory, "public/apple-touch-icon.png"),
    aliases: [resolve(websiteDirectory, "public/assets/apple-touch-icon.png")],
    width: 180,
    height: 180,
  },
];

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));
}

async function waitForCdp() {
  let lastError;
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const response = await fetch(`${cdpOrigin}/json/version`);
      if (response.ok) return;
    } catch (error) {
      lastError = error;
    }
    await delay(250);
  }
  throw new Error(`Edge CDP did not become ready: ${lastError?.message ?? "unknown error"}`);
}

async function createTarget(url) {
  const endpoint = `${cdpOrigin}/json/new?${encodeURIComponent(url)}`;
  let response = await fetch(endpoint, { method: "PUT" });
  if (!response.ok) response = await fetch(endpoint);
  if (!response.ok) throw new Error(`Could not create CDP target (${response.status})`);
  return response.json();
}

function openCdpSocket(webSocketDebuggerUrl) {
  const socket = new WebSocket(webSocketDebuggerUrl);
  const pending = new Map();
  let nextId = 1;

  const ready = new Promise((resolveReady, rejectReady) => {
    socket.addEventListener("open", resolveReady, { once: true });
    socket.addEventListener("error", () => rejectReady(new Error("CDP WebSocket failed to open")), { once: true });
  });

  socket.addEventListener("message", (event) => {
    const message = JSON.parse(event.data);
    if (!message.id || !pending.has(message.id)) return;
    const { resolveCall, rejectCall } = pending.get(message.id);
    pending.delete(message.id);
    if (message.error) rejectCall(new Error(message.error.message));
    else resolveCall(message.result ?? {});
  });

  async function call(method, params = {}) {
    await ready;
    const id = nextId;
    nextId += 1;
    return new Promise((resolveCall, rejectCall) => {
      pending.set(id, { resolveCall, rejectCall });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }

  return { call, close: () => socket.close() };
}

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function ensureSrgb(png) {
  for (let offset = 8; offset + 12 <= png.length;) {
    const length = png.readUInt32BE(offset);
    const type = png.toString("ascii", offset + 4, offset + 8);
    if (type === "sRGB") return png;
    offset += 12 + length;
  }

  const type = Buffer.from("sRGB", "ascii");
  const data = Buffer.from([0]);
  const chunk = Buffer.alloc(12 + data.length);
  chunk.writeUInt32BE(data.length, 0);
  type.copy(chunk, 4);
  data.copy(chunk, 8);
  chunk.writeUInt32BE(crc32(Buffer.concat([type, data])), 8 + data.length);
  return Buffer.concat([png.subarray(0, 33), chunk, png.subarray(33)]);
}

function inspectPng(png, expectedWidth, expectedHeight) {
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (!png.subarray(0, 8).equals(signature)) throw new Error("Output is not a PNG file");
  if (png.toString("ascii", 12, 16) !== "IHDR") throw new Error("PNG is missing IHDR");
  const width = png.readUInt32BE(16);
  const height = png.readUInt32BE(20);
  if (width !== expectedWidth || height !== expectedHeight) {
    throw new Error(`Expected ${expectedWidth}x${expectedHeight}, got ${width}x${height}`);
  }
  if (!png.includes(Buffer.from("sRGB", "ascii"))) throw new Error("PNG is missing sRGB metadata");
  return { width, height, bytes: png.length };
}

async function render(target) {
  const page = await createTarget(target.url);
  const cdp = openCdpSocket(page.webSocketDebuggerUrl);
  try {
    await cdp.call("Page.enable");
    await cdp.call("Emulation.setDeviceMetricsOverride", {
      width: target.width,
      height: target.height,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await cdp.call("Page.navigate", { url: target.url });
    await delay(1500);
    const { result: viewportResult } = await cdp.call("Runtime.evaluate", {
      expression: `({
        innerWidth,
        innerHeight,
        scrollWidth: document.documentElement.scrollWidth,
        scrollHeight: document.documentElement.scrollHeight
      })`,
      returnByValue: true,
    });
    const viewport = viewportResult.value;
    if (
      viewport.innerWidth !== target.width ||
      viewport.innerHeight !== target.height ||
      viewport.scrollWidth !== target.width ||
      viewport.scrollHeight !== target.height
    ) {
      throw new Error(`${target.name} viewport overflow: ${JSON.stringify(viewport)}`);
    }
    const { data } = await cdp.call("Page.captureScreenshot", { format: "png" });
    await mkdir(dirname(target.output), { recursive: true });
    const png = ensureSrgb(Buffer.from(data, "base64"));
    await writeFile(target.output, png);
    for (const alias of target.aliases ?? []) {
      await mkdir(dirname(alias), { recursive: true });
      await writeFile(alias, png);
    }
  } finally {
    cdp.close();
  }

  const png = await readFile(target.output);
  const result = inspectPng(png, target.width, target.height);
  console.log(`${target.name}: ${result.width}x${result.height}, ${result.bytes} bytes, sRGB, exact viewport`);
  for (const alias of target.aliases ?? []) {
    const aliasPng = await readFile(alias);
    inspectPng(aliasPng, target.width, target.height);
    if (!aliasPng.equals(png)) throw new Error(`${target.name} alias differs from source output`);
    console.log(`${target.name} alias: ${alias} (${aliasPng.length} bytes, byte-identical)`);
  }
}

await waitForCdp();
for (const target of targets) await render(target);
