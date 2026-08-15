import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const distRoot = path.resolve(fileURLToPath(new URL("dist/", import.meta.url)));
const rawDownload = new Uint8Array([0x00, 0xff, 0x41, 0x0d, 0x0a, 0xe6, 0xb8, 0xa9]);

const harness = String.raw`<!doctype html>
<meta charset="utf-8">
<link rel="stylesheet" href="/vendor/xterm/xterm.css">
<style>#terminal { width: 900px; height: 240px; }</style>
<div id="terminal"></div>
<script src="/vendor/xterm/xterm.js"></script>
<script src="/vendor/xterm/addon-search.js"></script>
<script src="/vendor/xterm/addon-unicode11.js"></script>
<script src="/serial-terminal.js"></script>
<script>
window.__serialTerminalBrowserResult = (async () => {
  const checks = [];
  const require = (condition, name) => {
    if (!condition) throw new Error(name);
    checks.push(name);
  };
  let clipboardReads = 0;
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    get() { clipboardReads += 1; throw new Error("clipboard access is forbidden"); },
  });

  const terminal = new Terminal({
    allowProposedApi: true, cols: 80, rows: 4, scrollback: 100,
    convertEol: false, linkHandler: null, windowOptions: {},
  });
  const search = new SearchAddon.SearchAddon();
  const unicode = new Unicode11Addon.Unicode11Addon();
  terminal.loadAddon(search);
  terminal.loadAddon(unicode);
  terminal.unicode.activeVersion = "11";
  terminal.open(document.querySelector("#terminal"));
  const write = (value) => new Promise((resolve) => terminal.write(value, resolve));
  const escape = String.fromCharCode(27);
  const crlf = String.fromCharCode(13, 10);
  await write(escape + '[31mRED' + escape + '[0m <img id="xss-probe" src=x onerror="window.xss=1"> 温度 https://example.invalid' + crlf);
  for (let index = 0; index < 12; index += 1) await write("line-" + index + crlf);
  await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  let visible = "";
  for (let index = 0; index < terminal.buffer.active.length; index += 1) {
    visible += terminal.buffer.active.getLine(index)?.translateToString(true) || "";
  }
  require(visible.includes("RED") && visible.includes("温度"), "ANSI and Unicode reach the xterm buffer");
  require(!visible.includes(escape), "ANSI control bytes are interpreted, not displayed");
  require(document.querySelector("#xss-probe") === null && window.xss !== 1, "xterm text cannot create XSS DOM");
  require(document.querySelector(".xterm a") === null, "untrusted terminal URLs are not activated as links");
  const redCell = terminal.buffer.active.getLine(0)?.getCell(0);
  require(redCell?.isFgPalette() && redCell.getFgColor() === 1, "ANSI colour is rendered by xterm");
  require(terminal.buffer.active.baseY > 0, "xterm scrollback retains overflow rows");
  require(search.findNext("温度"), "xterm search finds Unicode text in scrollback");
  require(clipboardReads === 0, "terminal rendering and search never access the clipboard");

  const anchor = document.createElement("a");
  anchor.href = "/capture.bin?session_id=7";
  anchor.download = "s3deck-serial-session-7.bin";
  document.body.append(anchor);
  const downloaded = new Uint8Array(await (await fetch(anchor.href)).arrayBuffer());
  require(anchor.download.endsWith(".bin") &&
    downloaded.length === 8 && downloaded[0] === 0 && downloaded[1] === 255 && downloaded[5] === 230,
  "raw browser download preserves NUL, 0xff, CR/LF and UTF-8 bytes");
  anchor.remove();

  const protocol = window.S3DeckSerialTerminal;
  const makeSocket = () => ({ readyState: 1, sent: [], send(value) { this.sent.push(value); }, close() {} });
  const holderSocket = makeSocket();
  const observerSocket = makeSocket();
  const holder = protocol.createClient({ origin: location.origin, openSocket: () => holderSocket });
  const observer = protocol.createClient({ origin: location.origin, openSocket: () => observerSocket });
  holder.connect();
  observer.connect();
  const state = (held) => JSON.stringify({
    type: "serial.observer.state", protocol_version: 1, device_id: "deck-browser",
    serial_state: "web_tx", serial_session_id: "7", buffered_bytes: 8, buffered_frames: 1,
    overwritten_bytes: 0, observers: 2, lease_owner: "web",
    lease_held_by_this_observer: held, lease_id: held ? "9" : undefined, lease_remaining_ms: 25000,
  });
  holderSocket.onmessage({ data: state(true) });
  observerSocket.onmessage({ data: state(false) });
  require(holder.status().canTransmit && !observer.status().canTransmit && observer.status().leaseRemainingMS === 25000,
    "two browsers share owner and remaining state but only the holder can transmit");
  let rejected = false;
  try { observer.send(new Uint8Array([0x53])); } catch (_) { rejected = true; }
  require(rejected && observerSocket.sent.length === 0, "read-only browser cannot send raw bytes");
  holder.send(new Uint8Array([0x00, 0xff, 0x41]));
  require(holderSocket.sent.length === 1 && holderSocket.sent[0] instanceof Uint8Array &&
    holderSocket.sent[0][0] === 0 && holderSocket.sent[0][1] === 255,
  "Lease holder sends the exact raw byte array");
  terminal.dispose();
  return { ok: true, checks };
})().catch((error) => ({ ok: false, error: String(error?.stack || error) }));
</script>`;

function contentType(filePath) {
  if (filePath.endsWith(".js")) return "text/javascript; charset=utf-8";
  if (filePath.endsWith(".css")) return "text/css; charset=utf-8";
  return "application/octet-stream";
}

async function startServer() {
  const server = http.createServer(async (request, response) => {
    try {
      const requestURL = new URL(request.url, "http://127.0.0.1");
      if (requestURL.pathname === "/test.html") {
        response.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
        response.end(harness);
        return;
      }
      if (requestURL.pathname === "/capture.bin") {
        response.writeHead(200, { "Content-Type": "application/octet-stream", "Cache-Control": "no-store" });
        response.end(rawDownload);
        return;
      }
      const filePath = path.resolve(distRoot, `.${requestURL.pathname}`);
      if (!filePath.startsWith(distRoot + path.sep)) throw new Error("asset path escaped dist root");
      const body = await readFile(filePath);
      response.writeHead(200, { "Content-Type": contentType(filePath), "Cache-Control": "no-store" });
      response.end(body);
    } catch (_) {
      response.writeHead(404);
      response.end("not found");
    }
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return server;
}

async function executable(candidates) {
  for (const candidate of candidates.filter(Boolean)) {
    try {
      await access(candidate, fsConstants.X_OK);
      return candidate;
    } catch (_) { /* try the next installed browser */ }
  }
  throw new Error("a local Chromium-family browser is required for the xterm DOM contract");
}

async function browserExecutable() {
  if (process.env.S3DECK_BROWSER_BIN) return executable([process.env.S3DECK_BROWSER_BIN]);
  if (process.platform === "darwin") {
    return executable([
      "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      "/Applications/Chromium.app/Contents/MacOS/Chromium",
    ]);
  }
  if (process.platform === "win32") {
    return executable([
      process.env.PROGRAMFILES && path.join(process.env.PROGRAMFILES, "Microsoft", "Edge", "Application", "msedge.exe"),
      process.env["PROGRAMFILES(X86)"] && path.join(process.env["PROGRAMFILES(X86)"], "Microsoft", "Edge", "Application", "msedge.exe"),
      process.env.PROGRAMFILES && path.join(process.env.PROGRAMFILES, "Google", "Chrome", "Application", "chrome.exe"),
    ]);
  }
  for (const name of ["microsoft-edge", "google-chrome", "chromium", "chromium-browser"]) {
    const paths = String(process.env.PATH || "").split(path.delimiter).map((directory) => path.join(directory, name));
    try { return await executable(paths); } catch (_) { /* try next name */ }
  }
  throw new Error("a local Chromium-family browser is required for the xterm DOM contract");
}

class CDPClient {
  constructor(socket) {
    this.socket = socket;
    this.nextID = 1;
    this.pending = new Map();
    socket.onmessage = (event) => {
      const message = JSON.parse(event.data);
      if (!message.id) return;
      const request = this.pending.get(message.id);
      if (!request) return;
      this.pending.delete(message.id);
      if (message.error) request.reject(new Error(message.error.message));
      else request.resolve(message.result);
    };
  }

  send(method, params = {}) {
    const id = this.nextID++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }
}

async function connectCDP(url) {
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => {
    socket.onopen = resolve;
    socket.onerror = () => reject(new Error("could not connect to browser debugging endpoint"));
  });
  return new CDPClient(socket);
}

async function waitForFile(filePath, processHandle, timeoutMS = 10000) {
  const deadline = Date.now() + timeoutMS;
  while (Date.now() < deadline) {
    if (processHandle.exitCode !== null) throw new Error("browser exited before publishing DevToolsActivePort");
    try { return await readFile(filePath, "utf8"); } catch (_) { /* browser is starting */ }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("browser debugging endpoint timed out");
}

async function main() {
  const server = await startServer();
  const profile = await mkdtemp(path.join(os.tmpdir(), "s3deck-xterm-browser-"));
  let browser;
  try {
    const binary = await browserExecutable();
    browser = spawn(binary, [
      "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
      "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank",
    ], { stdio: ["ignore", "ignore", "pipe"], windowsHide: true });
    let browserErrors = "";
    browser.stderr.on("data", (chunk) => { browserErrors = (browserErrors + chunk).slice(-8192); });
    const activePort = await waitForFile(path.join(profile, "DevToolsActivePort"), browser);
    const port = Number(activePort.split(/\r?\n/, 1)[0]);
    if (!Number.isInteger(port) || port < 1) throw new Error("browser returned an invalid debugging port");
    const address = server.address();
    const targetURL = `http://127.0.0.1:${address.port}/test.html`;
    const targetResponse = await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(targetURL)}`, { method: "PUT" });
    if (!targetResponse.ok) throw new Error(`browser target creation failed: ${targetResponse.status}`);
    const target = await targetResponse.json();
    const cdp = await connectCDP(target.webSocketDebuggerUrl);
    const deadline = Date.now() + 15000;
    let result;
    while (Date.now() < deadline) {
      const evaluated = await cdp.send("Runtime.evaluate", {
        expression: "window.__serialTerminalBrowserResult || null", returnByValue: true, awaitPromise: true,
      });
      result = evaluated.result?.value;
      if (result) break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!result) throw new Error(`browser contract timed out\n${browserErrors}`);
    assert.equal(result.ok, true, result.error || "browser contract failed");
    assert.equal(result.checks.length, 12, JSON.stringify(result));
    console.log(`xterm DOM browser contract passed in ${path.basename(binary)}: ${result.checks.length}/12`);
    cdp.socket.close();
  } finally {
    await new Promise((resolve) => server.close(resolve));
    if (browser && browser.exitCode === null) {
      browser.kill();
      await new Promise((resolve) => {
        browser.once("exit", resolve);
        setTimeout(resolve, 2000).unref();
      });
    }
    await rm(profile, { recursive: true, force: true });
  }
}

await main();
