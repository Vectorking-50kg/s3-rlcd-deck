import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { constants as fsConstants } from "node:fs";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const distRoot = path.resolve(fileURLToPath(new URL("dist/", import.meta.url)));
const browserState = {
  authenticated: false, sessionState: "awaiting_code", errorCode: "none",
  confirmCalls: 0, cancelCalls: 0, submittedCode: "", rejectNextScan: false,
};

function json(response, value, status = 200) {
  response.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  response.end(JSON.stringify(value));
}

function sessionView() {
  return {
    session_ref: "opaque-session-ref-1234", state: browserState.sessionState,
    expires_at: new Date(Date.now() + 90000).toISOString(), error_code: browserState.errorCode,
  };
}

async function body(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
}

function contentType(filePath) {
  if (filePath.endsWith(".js")) return "text/javascript; charset=utf-8";
  if (filePath.endsWith(".css")) return "text/css; charset=utf-8";
  if (filePath.endsWith(".html")) return "text/html; charset=utf-8";
  return "application/octet-stream";
}

async function startServer() {
  const server = http.createServer(async (request, response) => {
    try {
      const requestURL = new URL(request.url, "http://127.0.0.1");
      const route = requestURL.pathname;
      if (route === "/api/v1/bootstrap") return json(response, { version: "test", login_required: true, lan_management_enabled: false });
      if (route === "/api/v1/session/refresh") {
        // Exercise the real startup ordering: the initial session probe must settle
        // before a user can submit the management token.
        if (!browserState.authenticated) await new Promise((resolve) => setTimeout(resolve, 100));
        return browserState.authenticated ? json(response, { csrf_token: "test-csrf" }) : json(response, {}, 401);
      }
      if (route === "/api/v1/login") {
        browserState.authenticated = true;
        return json(response, { csrf_token: "test-csrf" });
      }
      if (route === "/api/v1/console") return json(response, {
        runtime: { state: "ready", version: "test", connected_decks: 0, history_available: false },
        providers: [], sessions: [], capabilities: { pairing: true, pairing_v2: true },
      });
      if (route === "/api/v1/providers") return json(response, { providers: [], templates: [], states: [] });
      if (route === "/api/v1/pairing-v2/scan") {
        if (browserState.rejectNextScan) {
          browserState.rejectNextScan = false;
          browserState.authenticated = false;
          response.writeHead(401, { "Content-Type": "text/plain" });
          return response.end("unauthorized");
        }
        const expires = new Date(Date.now() + 20000).toISOString();
        return json(response, { candidates: [
          { candidate_ref: "candidate-a", label: "S3 RLCD Deck", expires_at: expires },
          { candidate_ref: "candidate-b", label: "S3 RLCD Deck", expires_at: expires },
        ] });
      }
      if (route === "/api/v1/pairing-v2/sessions" && request.method === "POST") {
        const requestBody = await body(request);
        assert.match(requestBody.candidate_ref, /^candidate-[ab]$/);
        browserState.sessionState = "awaiting_code";
        browserState.errorCode = "none";
        return json(response, sessionView(), 201);
      }
      if (route.endsWith("/confirm") && request.method === "POST") {
        const requestBody = await body(request);
        browserState.confirmCalls += 1;
        browserState.submittedCode = requestBody.code;
        browserState.sessionState = "proving_link";
        return json(response, { ...sessionView(), state: "authenticating" }, 202);
      }
      if (route === "/api/v1/pairing-v2/sessions/opaque-session-ref-1234" && request.method === "GET") {
        return json(response, sessionView());
      }
      if (route === "/api/v1/pairing-v2/sessions/opaque-session-ref-1234" && request.method === "DELETE") {
        browserState.cancelCalls += 1;
        browserState.sessionState = "cancelled";
        browserState.errorCode = "cancelled";
        return json(response, sessionView());
      }
      if (route.startsWith("/api/")) return json(response, {}, 404);
      const filePath = path.resolve(distRoot, `.${route === "/" ? "/index.html" : route}`);
      if (!filePath.startsWith(distRoot + path.sep)) throw new Error("asset path escaped dist root");
      const contents = await readFile(filePath);
      response.writeHead(200, { "Content-Type": contentType(filePath), "Cache-Control": "no-store" });
      response.end(contents);
    } catch (error) {
      response.writeHead(500, { "Content-Type": "text/plain" });
      response.end(String(error));
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
    try { await access(candidate, fsConstants.X_OK); return candidate; } catch (_) { /* try next */ }
  }
  throw new Error("a local Chromium-family browser is required for the Pairing v2 DOM contract");
}

async function browserExecutable() {
  if (process.env.S3DECK_BROWSER_BIN) return executable([process.env.S3DECK_BROWSER_BIN]);
  if (process.platform === "darwin") return executable([
    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  ]);
  if (process.platform === "win32") return executable([
    process.env.PROGRAMFILES && path.join(process.env.PROGRAMFILES, "Microsoft", "Edge", "Application", "msedge.exe"),
    process.env["PROGRAMFILES(X86)"] && path.join(process.env["PROGRAMFILES(X86)"], "Microsoft", "Edge", "Application", "msedge.exe"),
  ]);
  throw new Error("Pairing v2 browser acceptance runs on macOS and Windows only");
}

class CDPClient {
  constructor(socket) {
    this.socket = socket;
    this.nextID = 1;
    this.pending = new Map();
    this.eventWaiters = new Map();
    socket.onmessage = (event) => {
      const message = JSON.parse(event.data);
      if (message.method) {
        const waiters = this.eventWaiters.get(message.method) || [];
        this.eventWaiters.delete(message.method);
        for (const waiter of waiters) {
          clearTimeout(waiter.timer);
          waiter.resolve(message.params);
        }
        return;
      }
      const pending = this.pending.get(message.id);
      if (!pending) return;
      this.pending.delete(message.id);
      if (message.error) pending.reject(new Error(message.error.message));
      else pending.resolve(message.result);
    };
  }
  send(method, params = {}) {
    const id = this.nextID++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.socket.send(JSON.stringify({ id, method, params }));
    });
  }
  waitForEvent(method, timeout = 10000) {
    return new Promise((resolve, reject) => {
      const waiters = this.eventWaiters.get(method) || [];
      const waiter = {
        resolve,
        timer: setTimeout(() => {
          const active = this.eventWaiters.get(method) || [];
          this.eventWaiters.set(method, active.filter((entry) => entry !== waiter));
          reject(new Error(`browser event timed out: ${method}`));
        }, timeout),
      };
      waiters.push(waiter);
      this.eventWaiters.set(method, waiters);
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

async function waitForFile(filePath, processHandle) {
  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    if (processHandle.exitCode !== null) throw new Error("browser exited before publishing DevToolsActivePort");
    try { return await readFile(filePath, "utf8"); } catch (_) { /* browser starting */ }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("browser debugging endpoint timed out");
}

async function evaluate(cdp, expression) {
  const result = await cdp.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || "browser evaluation failed");
  return result.result?.value;
}

async function main() {
  const server = await startServer();
  const profile = await mkdtemp(path.join(os.tmpdir(), "s3deck-pairing-v2-browser-"));
  let browser;
  try {
    const binary = await browserExecutable();
    browser = spawn(binary, ["--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
      "--remote-debugging-port=0", `--user-data-dir=${profile}`, "about:blank"],
    { stdio: ["ignore", "ignore", "pipe"], windowsHide: true });
    const activePort = await waitForFile(path.join(profile, "DevToolsActivePort"), browser);
    const port = Number(activePort.split(/\r?\n/, 1)[0]);
    const address = server.address();
    const targetURL = `http://127.0.0.1:${address.port}/`;
    const targetResponse = await fetch(`http://127.0.0.1:${port}/json/new?about%3Ablank`, { method: "PUT" });
    const target = await targetResponse.json();
    const cdp = await connectCDP(target.webSocketDebuggerUrl);
    await cdp.send("Page.enable");
    const initialLoad = cdp.waitForEvent("Page.loadEventFired");
    await cdp.send("Page.navigate", { url: targetURL });
    await initialLoad;
    const phaseOne = await evaluate(cdp, `(async()=>{
      const wait=async(name,fn)=>{const end=Date.now()+10000;while(Date.now()<end){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,30));}throw new Error('DOM timeout: '+name)};
      await wait('initial session probe',()=>document.querySelector('#management-token')&&document.querySelector('#session-resume').hidden&&!document.querySelector('#login-view').hidden);
      document.querySelector('#management-token').value='local-test-token';document.querySelector('#login-form').requestSubmit();
      await wait('authenticated application',()=>!document.querySelector('#application').hidden);
      await wait('Pairing capability',()=>!document.querySelector('#pair-deck').disabled);
      document.querySelector('#pair-deck').click();
      await wait('Pairing candidates',()=>document.querySelectorAll('.pairing-candidate').length===2);
      const labels=[...document.querySelectorAll('.pairing-candidate strong')].map(node=>node.textContent);
      document.querySelector('.pairing-candidate button').click();
      await wait('Pairing code form',()=>!document.querySelector('#pairing-v2-code-form').hidden);
      const input=document.querySelector('#pairing-v2-code');input.value='123456';
      document.querySelector('#pairing-v2-code-form').requestSubmit();document.querySelector('#pairing-v2-code-form').requestSubmit();
      await wait('Pairing proof state',()=>document.querySelector('#pairing-v2-state').textContent==='正在验证连接');
      return {labels,codeCleared:input.value==='',stored:sessionStorage.getItem('s3deck.pairing-v2.session-ref')};
    })()`);
    assert.deepEqual(phaseOne.labels, ["S3 RLCD Deck · 同名设备 1", "S3 RLCD Deck · 同名设备 2"]);
    assert.equal(phaseOne.codeCleared, true);
    assert.equal(phaseOne.stored, "opaque-session-ref-1234");
    assert.equal(browserState.confirmCalls, 1, "duplicate form submission reached PairingCoordinator twice");
    assert.equal(browserState.submittedCode, "123456");

    const reloadFinished = cdp.waitForEvent("Page.loadEventFired");
    try {
      await cdp.send("Page.reload", { ignoreCache: true });
    } catch (error) {
      if (!String(error).includes("Execution context was destroyed")) throw error;
    }
    await reloadFinished;
    const resumed = await evaluate(cdp, `(async()=>{const end=Date.now()+10000;while(Date.now()<end){
      if(document.querySelector('#pairing-v2-dialog')?.open&&document.querySelector('#pairing-v2-state')?.textContent==='正在验证连接')return true;
      await new Promise(r=>setTimeout(r,30));}return false;})()`);
    assert.equal(resumed, true, "page refresh did not resume the opaque Pairing session");
    browserState.sessionState = "paired";
    const paired = await evaluate(cdp, `(async()=>{const end=Date.now()+10000;while(Date.now()<end){
      if(document.querySelector('#pairing-v2-state')?.textContent==='配对完成')return {stored:sessionStorage.getItem('s3deck.pairing-v2.session-ref')};
      await new Promise(r=>setTimeout(r,30));}return null;})()`);
    assert.deepEqual(paired, { stored: null });

    await evaluate(cdp, `(async()=>{const wait=async(name,fn)=>{const end=Date.now()+10000;while(Date.now()<end){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,30));}throw new Error('DOM timeout: '+name)};
      document.querySelector('#pairing-v2-rescan').click();
      const button=await wait('candidate after rescan',()=>document.querySelector('.pairing-candidate button'));button.click();
      await wait('Pairing code form after rescan',()=>!document.querySelector('#pairing-v2-code-form').hidden);
      document.querySelector('#pairing-v2-code').value='654321';document.querySelector('#pairing-v2-close').click();
    })()`);
    const cancelled = await evaluate(cdp, `(async()=>{const end=Date.now()+10000;while(Date.now()<end){
      if(!document.querySelector('#pairing-v2-dialog').open)return {code:document.querySelector('#pairing-v2-code').value,stored:sessionStorage.getItem('s3deck.pairing-v2.session-ref')};
      await new Promise(r=>setTimeout(r,30));}return null;})()`);
    assert.deepEqual(cancelled, { code: "", stored: null });
    assert.equal(browserState.cancelCalls, 1);

    browserState.rejectNextScan = true;
    await evaluate(cdp, `document.querySelector('#pair-deck').click()`);
    const scrubbed = await evaluate(cdp, `(async()=>{const end=Date.now()+10000;while(Date.now()<end){
      if(!document.querySelector('#login-view').hidden)return {dialog:document.querySelector('#pairing-v2-dialog').open,code:document.querySelector('#pairing-v2-code').value};
      await new Promise(r=>setTimeout(r,30));}return null;})()`);
    assert.deepEqual(scrubbed, { dialog: false, code: "" });
    cdp.socket.close();
    console.log(`Pairing v2 DOM browser contract passed in ${path.basename(binary)}: 12/12`);
  } finally {
    await new Promise((resolve) => server.close(resolve));
    if (browser && browser.exitCode === null) {
      browser.kill();
      await new Promise((resolve) => { browser.once("exit", resolve); setTimeout(resolve, 2000).unref(); });
    }
    await rm(profile, { recursive: true, force: true });
  }
}

await main();
