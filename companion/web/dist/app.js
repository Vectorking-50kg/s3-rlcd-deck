"use strict";

const state = {
  csrf: "", bootstrap: null, console: null, providers: [], templates: [], providerStates: [], history: [],
  editing: "", backup: null, ota: null, diagnostics: null, otaPreviewEpoch: 0, page: "overview", providerFilter: "all", deckIndex: 0,
  serialPresets: [], serialPresetEditing: "",
  serialPresetOperationEpoch: 0, serialPresetOperationController: new AbortController(),
  pairingV2: {
    epoch: 0, controller: new AbortController(), candidates: [], sessionRef: "", view: null,
    pollTimer: null, countdownTimer: null, originPage: "",
  },
  serial: {
    client: null, terminal: null, fit: null, search: null, resizeObserver: null,
    reconnectTimer: null, heartbeatTimer: null, mode: "text", paused: false,
    byteChunks: [], byteBytes: 0, status: null,
  },
  authenticated: false, refreshTimer: null, authEpoch: 0, authController: new AbortController(),
  sync: {
    console: { lastSuccess: "", error: "" },
    providers: { lastSuccess: "", error: "" },
    history: { lastSuccess: "", error: "" },
    serialPresets: { lastSuccess: "", error: "" },
    diagnostics: { lastSuccess: "", error: "" },
  },
};

const pageConfig = {
  overview: { domain: "home", domainTitle: "首页", title: "概览" },
  providers: { domain: "ai", domainTitle: "AI 采集", title: "AI Provider" },
  "provider-editor": { domain: "ai", domainTitle: "AI 采集", title: "Provider 编辑器" },
  history: { domain: "ai", domainTitle: "AI 采集", title: "用量历史" },
  sessions: { domain: "ai", domainTitle: "AI 采集", title: "Codex 会话" },
  serial: { domain: "serial", domainTitle: "串口工作台", title: "串口终端" },
  "serial-presets": { domain: "serial", domainTitle: "串口工作台", title: "串口预设" },
  devices: { domain: "devices", domainTitle: "设备", title: "Deck 清单" },
  network: { domain: "devices", domainTitle: "设备", title: "网络与信任" },
  setup: { domain: "devices", domainTitle: "设备", title: "Setup / 恢复" },
  deck: { domain: "devices", domainTitle: "设备", title: "Deck RLCD" },
  system: { domain: "system", domainTitle: "系统", title: "系统设置" },
  updates: { domain: "system", domainTitle: "系统", title: "固件更新" },
  backup: { domain: "system", domainTitle: "系统", title: "备份与恢复" },
  diagnostics: { domain: "system", domainTitle: "系统", title: "诊断" },
  tray: { domain: "system", domainTitle: "系统", title: "托盘 / 菜单" },
};

const domainConfig = {
  home: { title: "首页", icon: "#i-grid", count: "1 个界面" },
  ai: { title: "AI 采集", icon: "#i-spark", count: "4 个界面" },
  serial: { title: "串口工作台", icon: "#i-terminal", count: "2 个界面" },
  devices: { title: "设备", icon: "#i-device", count: "4 个界面" },
  system: { title: "系统", icon: "#i-settings", count: "5 个界面" },
};

const errorMessages = new Map([
  ["forbidden", "请求来源未通过安全检查。"], ["not found", "请求的功能不存在。"],
  ["pairing code unavailable", "暂时无法生成配对码。"], ["Provider history unavailable", "用量历史当前不可用。"],
  ["Device Hub advertised address unavailable", "无法确认当前默认局域网路由；请配置 --device-hub-advertised-address IP:port 后重试。"],
  ["invalid Provider history query", "历史记录筛选条件无效。"], ["invalid Provider history request", "历史记录请求无效。"],
  ["Provider history settings unavailable", "无法保存历史记录设置。"], ["malformed Provider history settings", "历史记录设置格式无效。"],
  ["Provider management unavailable", "Provider 管理当前不可用。"], ["malformed Provider request", "Provider 配置格式无效。"],
  ["invalid Provider request", "Provider 配置未通过校验。"], ["Provider configuration changed", "Provider 配置已在其他位置更新，请刷新后重试。"],
  ["Provider operation unavailable", "Provider 操作暂时不可用。"],
  ["diagnostics unavailable", "脱敏诊断当前不可用。"],
  ["diagnostic bundle unavailable", "诊断包暂时无法生成。"],
  ["Pairing v2 unavailable", "安全配对当前不可用。"],
  ["Pairing v2 scan unavailable", "没有发现可用的 Deck；请确认两台设备在同一局域网，且路由器未隔离客户端或屏蔽 mDNS。"],
  ["Pairing v2 candidate unavailable", "这个 Deck 候选已经过期，请重新扫描。"],
  ["Pairing v2 session unavailable", "配对会话已经失效，请重新扫描。"],
  ["Pairing v2 session state conflict", "配对会话当前不能执行这个操作，请刷新状态或重新开始。"],
  ["malformed Pairing v2 request", "请输入 Deck 屏幕上显示的六位数字配对码。"],
]);

const pairingV2SessionStorageKey = "s3deck.pairing-v2.session-ref";

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => Array.from(document.querySelectorAll(selector));

function element(tag, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== "") node.textContent = text;
  return node;
}

function setText(selector, value) {
  const target = $(selector);
  if (target) target.textContent = value == null || value === "" ? "—" : String(value);
}

function liveFeedback(statusSelector, alertSelector, text = "", isError = false) {
  const status = $(statusSelector);
  const alert = $(alertSelector);
  status.textContent = isError ? "" : text;
  alert.textContent = isError ? text : "";
  status.hidden = !status.textContent;
  alert.hidden = !alert.textContent;
}

function message(text = "", success = false, informational = false) {
  liveFeedback("#global-message", "#global-alert", text, Boolean(text) && !success && !informational);
  $("#global-message").classList.toggle("success", success);
}

function loginFeedback(text = "", isError = false) {
  liveFeedback("#login-message", "#login-alert", text, isError);
}

function resourceFeedback(resource, text = "", isError = false) {
  liveFeedback(`#${resource}-status`, `#${resource}-alert`, text, isError);
}

function toast(title, detail, isError = false) {
  const item = element("div", `toast${isError ? " error" : ""}`);
  const mark = element("span", "summary-avatar", isError ? "!" : "✓");
  const copy = element("div");
  copy.append(element("strong", "", title), element("span", "", detail));
  item.append(mark, copy);
  $(isError ? "#toast-alert-region" : "#toast-region").append(item);
  window.setTimeout(() => item.remove(), 5200);
}

function translatedError(detail, status) {
  const cleaned = detail.trim();
  if (errorMessages.has(cleaned)) return errorMessages.get(cleaned);
  if (status === 429) return "操作过于频繁，请稍后再试。";
  if (status >= 500) return "Companion 暂时无法完成该操作。";
  if (status === 400) return "提交内容未通过校验，请检查后重试。";
  return `请求失败（状态码 ${status}）`;
}

async function request(path, options = {}) {
  const method = options.method || "GET";
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  if (method !== "GET") {
    headers.set("Origin", location.origin);
    if (state.csrf) headers.set("X-CSRF-Token", state.csrf);
  }
  let response;
  try {
    response = await fetch(path, { cache: "no-store", credentials: "same-origin", ...options, method, headers,
      signal: options.signal || state.authController.signal });
  } catch (error) {
    if (error?.name === "AbortError") throw error;
    throw new Error("无法连接 Companion，请检查本机服务后重试。");
  }
  if (!response.ok) {
    const detail = await response.text();
    if (response.status === 401 && state.authenticated) showLogin("本机会话已过期，请重新解锁。", true);
    throw new Error(translatedError(detail, response.status));
  }
  return response;
}

function rotateAuthScope() {
  state.authController.abort();
  state.authController = new AbortController();
  state.authEpoch += 1;
}

function authenticatedOperation() {
  return { epoch: state.authEpoch, signal: state.authController.signal };
}

function operationIsCurrent(operation) {
  return state.authenticated && !operation.signal.aborted && operation.epoch === state.authEpoch;
}

function bytesBase64(value) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function formatNumber(value) { return value == null ? "—" : new Intl.NumberFormat("zh-CN").format(value); }
function formatTokens(value) {
  if (value == null) return "—";
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function formatMoney(balance) {
  if (!balance || balance.amount_micros == null) return "—";
  const value = balance.amount_micros / 1000000;
  try {
    return new Intl.NumberFormat("zh-CN", { style: "currency", currency: balance.currency, maximumFractionDigits: 2 }).format(value);
  } catch (_) { return `${value.toFixed(2)} ${balance.currency || ""}`.trim(); }
}

function formatTime(value, includeDate = true) {
  if (!value) return "未知";
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return "未知";
  return new Intl.DateTimeFormat("zh-CN", {
    month: includeDate ? "2-digit" : undefined, day: includeDate ? "2-digit" : undefined,
    hour: "2-digit", minute: "2-digit", hour12: false,
  }).format(date);
}

function formatRelative(value) {
  if (!value) return "尚未更新";
  const seconds = Math.round((new Date(value).valueOf() - Date.now()) / 1000);
  if (!Number.isFinite(seconds)) return "更新时间未知";
  const absolute = Math.abs(seconds);
  const formatter = new Intl.RelativeTimeFormat("zh-CN", { numeric: "auto" });
  if (absolute < 60) return formatter.format(seconds, "second");
  if (absolute < 3600) return formatter.format(Math.round(seconds / 60), "minute");
  if (absolute < 86400) return formatter.format(Math.round(seconds / 3600), "hour");
  return formatter.format(Math.round(seconds / 86400), "day");
}

function formatDuration(seconds) {
  if (seconds == null) return "—";
  if (seconds < 60) return `${seconds} 秒`;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`;
}

function initials(value) {
  const compact = String(value || "AI").replace(/[^\p{L}\p{N}]/gu, "");
  return compact.slice(0, 2).toUpperCase() || "AI";
}

function runtimeLabel(value) { return { ready: "正常", new: "正在启动", stopped: "已停止" }[value] || "未知"; }
function providerStatusLabel(value) { return { ok: "正常", degraded: "降级", unavailable: "不可用" }[value] || "未知"; }
function confidenceLabel(value) { return { verified: "已验证", inferred: "推断？", unavailable: "不可用" }[value] || "未知"; }
function sessionStateLabel(value) {
  return {
    running: "运行中", waiting_approval: "等待批准", waiting_input: "等待输入", completed: "已完成",
    failed: "失败", recent: "最近活动", ended: "已结束", unknown: "未知？", unavailable: "不可用",
  }[value] || "未知？";
}
function sourceLabel(value) {
  return {
    codex_app_server: "Codex App Server", cursor_local: "Cursor 本机", structured_http: "结构化 HTTP",
    codex_app_server_owned: "Codex App Server", process_jsonl_observer: "本机进程观察", none: "无",
  }[value] || "未知";
}
function statusClass(value) {
  return { ready: "success", ok: "success", degraded: "warning", unavailable: "danger", stopped: "danger" }[value] || "neutral";
}
function setPill(target, text, kind = "neutral") {
  if (!target) return;
  target.textContent = text;
  target.className = `status-pill ${kind}`;
}
function emptyState(title, detail) {
  const empty = element("div", "empty-state");
  empty.append(element("strong", "", title), element("span", "", detail));
  return empty;
}

function closeMobileNavigation() {
  $("#context-panel").classList.remove("open");
  $("#mobile-menu").setAttribute("aria-expanded", "false");
  $("#mobile-backdrop").hidden = true;
}
function openMobileNavigation() {
  const isOpen = $("#context-panel").classList.toggle("open");
  $("#mobile-menu").setAttribute("aria-expanded", String(isOpen));
  $("#mobile-backdrop").hidden = !isOpen;
}

function navigate(page, updateHash = true) {
  if ($("#pairing-v2-dialog")?.open && state.pairingV2.originPage &&
      page !== state.pairingV2.originPage) {
    void cancelPairingV2();
  }
  if (page === "provider-editor" && !providerDataWritable()) {
    page = "providers";
    if (state.sync.providers.error) providerConfigurationIsFresh(true);
  }
  const config = pageConfig[page] || pageConfig.overview;
  page = pageConfig[page] ? page : "overview";
  if (state.page === "serial-presets" && page !== "serial-presets") clearSerialPresetEditor();
  state.page = page;
  $$('[data-page-view]').forEach((view) => { view.hidden = view.dataset.pageView !== page; });
  $$('[data-page]').forEach((button) => button.classList.toggle("active", button.dataset.page === page));
  $$('[data-domain]').forEach((button) => button.classList.toggle("active", button.dataset.domain === config.domain));
  $$('[data-domain-nav]').forEach((group) => { group.hidden = group.dataset.domainNav !== config.domain; });
  const domain = domainConfig[config.domain];
  setText("#breadcrumb-domain", config.domainTitle);
  setText("#breadcrumb-page", config.title);
  setText("#module-title", domain.title);
  setText("#module-count", domain.count);
  $("#module-icon use")?.setAttribute("href", domain.icon);
  document.title = `${config.title} · S3 RLCD Deck`;
  closeMobileNavigation();
  if (page === "providers") loadProviders();
  if (page === "provider-editor" && !state.editing && !$("#provider-id").value) openEditor(null, false);
  if (page === "history") loadHistory();
  if (page === "deck") renderDeckPreview();
  if (config.domain === "serial") startSerial();
  else stopSerial();
  if (page === "serial-presets") loadSerialPresets();
  if (page === "diagnostics") loadDiagnostics();
  if (updateHash && location.hash !== `#${page}`) history.pushState(null, "", `#${page}`);
  $("#main-content").focus({ preventScroll: true });
  window.scrollTo({ top: 0, behavior: "auto" });
}

function scrubSensitiveState() {
  resetPairingV2({ forget: true, close: true });
  state.console = null;
  state.providers = [];
  state.templates = [];
  state.providerStates = [];
  state.history = [];
  state.editing = "";
  state.backup = null;
  state.diagnostics = null;
  resetOTAPreview();
  $("#ota-file").value = "";
  $("#ota-device-id").value = "";
  stopSerial();
  state.serialPresets = [];
  rotateSerialPresetOperations();
  scrubSerialPresetEditor();
  state.deckIndex = 0;
  Object.values(state.sync).forEach((sync) => { sync.lastSuccess = ""; sync.error = ""; });
  $("#management-token").value = "";
  $("#provider-form").reset();
  $("#provider-headers").replaceChildren();
  $("#test-preview").textContent = "";
  $("#test-preview-card").hidden = true;
  $("#export-passphrase").value = "";
  $("#import-passphrase").value = "";
  $("#import-file").value = "";
  $("#backup-conflicts").replaceChildren();
  $("#backup-preview").textContent = "等待预览";
  $("#apply-backup").disabled = true;
  $("#serial-preset-list").replaceChildren();
  const dialog = $("#app-dialog");
  if (dialog.open) dialog.close();
  $("#dialog-body").replaceChildren();
  message();
  ["console", "providers", "history", "diagnostics"].forEach((resource) => resourceFeedback(resource));
  $("#toast-region").replaceChildren();
  $("#toast-alert-region").replaceChildren();
}

function showLogin(detail = "", isError = false) {
  rotateAuthScope();
  state.authenticated = false;
  state.csrf = "";
  if (state.refreshTimer) window.clearInterval(state.refreshTimer);
  state.refreshTimer = null;
  scrubSensitiveState();
  $("#application").hidden = true;
  $("#login-view").hidden = false;
  $("#session-resume").hidden = true;
  loginFeedback(detail, isError);
  $("#management-token").focus();
}

async function authenticate(csrf) {
  rotateAuthScope();
  state.csrf = csrf;
  state.authenticated = true;
  $("#login-view").hidden = true;
  $("#application").hidden = false;
  loginFeedback();
  await Promise.all([loadConsole(), loadProviders()]);
  if (!state.authenticated) return;
  navigate(location.hash.slice(1) || "overview", false);
  await resumePairingV2Session();
  if (!state.authenticated) return;
  if (state.refreshTimer) window.clearInterval(state.refreshTimer);
  state.refreshTimer = window.setInterval(() => {
    if (!document.hidden && state.authenticated) loadConsole(true);
  }, 15000);
}

async function loadConsole(quiet = false) {
  if (!state.sync.console.lastSuccess) resourceFeedback("console", "正在加载运行状态……");
  try {
    state.console = await (await request("/api/v1/console")).json();
    state.sync.console.lastSuccess = new Date().toISOString();
    state.sync.console.error = "";
    resourceFeedback("console");
    renderConsole();
    if (!quiet) message("状态已刷新。", true);
  } catch (error) {
    state.sync.console.error = error.message;
    const lastSuccess = state.sync.console.lastSuccess;
    resourceFeedback("console", lastSuccess ?
      `无法刷新运行状态；保留 ${formatTime(lastSuccess, false)} 的最后有效数据。` :
      "运行状态当前不可用，请检查 Companion 后重试。", true);
    if (state.console) renderConsole();
    else renderConsoleUnavailable();
    if (!quiet) message(error.message);
  }
}

function renderConsoleUnavailable() {
  setPill($("#companion-badge"), "Companion 状态不可用", "danger");
  setPill($("#deck-badge"), "Deck —", "neutral");
  setText("#metric-decks", "—");
  setText("#metric-providers", "—");
  setText("#metric-provider-detail", "等待可用状态");
  setText("#metric-sessions", "—");
  setText("#metric-history", "不可用");
  setText("#context-health", "运行状态不可用");
  setText("#context-summary", "没有可验证的当前数据");
  setText("#context-updated", "同步失败");
  $("#context-dot").classList.add("danger");
  renderOverviewProviders([]);
  renderOverviewSessions([]);
  renderSessions([]);
  renderDeckPreview();
}

function renderConsole() {
  if (!state.console) return;
  const runtime = state.console.runtime || {};
  const providers = state.console.providers || [];
  const sessions = state.console.sessions || [];
  const healthy = providers.filter((provider) => provider.status === "ok").length;
  const runtimeReady = runtime.state === "ready";
  const stale = Boolean(state.sync.console.error);
  setText("#runtime-version", runtime.version || state.bootstrap?.version || "Companion");
  setPill($("#companion-badge"), stale ? "Companion 数据已过期" : `Companion ${runtimeLabel(runtime.state)}`,
    stale ? "warning" : statusClass(runtime.state));
  setPill($("#deck-badge"), stale ? "Deck 状态已过期" : `Deck ${runtime.connected_decks ?? 0}`,
    stale ? "warning" : runtime.connected_decks > 0 ? "success" : "neutral");
  setText("#metric-decks", stale ? "—" : runtime.connected_decks ?? 0);
  setText("#metric-providers", stale ? "—" : providers.length ? `${healthy}/${providers.length}` : "0");
  setText("#metric-provider-detail", stale ? "Provider 状态已过期" :
    providers.length ? `${providers.length - healthy} 个需要关注` : "尚无规范化快照");
  setText("#metric-sessions", stale ? "—" : sessions.length);
  setText("#metric-history", stale ? "已过期" : runtime.history_available ? (runtime.history_enabled ? "已开启" : "已关闭") : "不可用");
  $("#lan-warning").hidden = !runtime.lan_management_enabled;
  setText("#lan-warning-text", runtime.security_warning ? "当前监听入口不只限于本机，请确认网络可信。" : "请确认只在可信网络中使用。");
  setText("#context-health", stale ? "运行状态已过期" : runtimeReady ? "系统运行正常" : `系统${runtimeLabel(runtime.state)}`);
  setText("#context-summary", stale ? "保留最后有效数据" : `${providers.length} 个 Provider · ${runtime.connected_decks ?? 0} 个 Deck`);
  setText("#context-updated", stale ? `同步失败 · 最后有效 ${formatTime(state.sync.console.lastSuccess, false)}` :
    `刚刚同步 · ${formatTime(state.sync.console.lastSuccess, false)}`);
  $("#context-dot").classList.toggle("danger", stale || !runtimeReady);
  renderOverviewProviders(providers);
  renderOverviewSessions(sessions);
  renderSessions(sessions);
  renderRuntimeSurfaces(runtime, providers);
  renderDeckPreview();
  updateCapabilityControls();
}

function renderOverviewProviders(providers) {
  const target = $("#overview-providers");
  target.replaceChildren();
  if (!providers.length) {
    target.append(emptyState("尚无 Provider 快照", "配置 Provider 或等待内置采集器首次同步。"));
    return;
  }
  providers.slice(0, 5).forEach((provider) => {
    const row = element("div", "summary-row");
    const copy = element("div");
    const detail = provider.balance ? formatMoney(provider.balance) : quotaSummary(provider);
    copy.append(element("strong", "", provider.display_name), element("small", "", `${sourceLabel(provider.source)} · ${detail}`));
    const pill = element("span");
    setPill(pill, state.sync.console.error ? "已过期" : providerStatusLabel(provider.status),
      state.sync.console.error ? "warning" : statusClass(provider.status));
    row.append(element("span", "summary-avatar", initials(provider.display_name)), copy, pill);
    target.append(row);
  });
}

function renderOverviewSessions(sessions) {
  const target = $("#overview-sessions");
  target.replaceChildren();
  if (!sessions.length) {
    target.append(emptyState("没有可显示的会话", "Codex App Server 提供脱敏会话后会出现在这里。"));
    return;
  }
  [...sessions].sort((left, right) => String(right.last_activity_at || "").localeCompare(String(left.last_activity_at || ""))).slice(0, 5)
    .forEach((session) => {
      const row = element("div", "summary-row");
      const copy = element("div");
      copy.append(element("strong", "", session.display_name || "未命名会话"),
        element("small", "", `${formatRelative(session.last_activity_at)} · ${formatTokens(session.turn_tokens)} Token`));
      const pill = element("span");
      const stale = Boolean(state.sync.console.error);
      setPill(pill, stale ? "已过期" : sessionStateLabel(session.state),
        stale ? "warning" : session.state === "failed" ? "danger" : session.state === "running" ? "success" : "neutral");
      row.append(element("span", "summary-avatar", initials(session.display_name || "会话")), copy, pill);
      target.append(row);
    });
}

function renderSessions(sessions) {
  const target = $("#sessions-table");
  target.replaceChildren();
  if (!sessions.length) {
    const row = element("tr");
    const cell = element("td", "", "当前没有脱敏会话数据。");
    cell.colSpan = 7;
    row.append(cell);
    target.append(row);
    return;
  }
  sessions.forEach((session) => {
    const row = element("tr");
    const context = session.context_used_basis_points == null ? "—" : `${(session.context_used_basis_points / 100).toFixed(0)}%`;
    [session.display_name || "未命名会话", state.sync.console.error ? "已过期" : sessionStateLabel(session.state), confidenceLabel(session.confidence),
      formatDuration(session.duration_seconds), formatTokens(session.turn_tokens), context, sourceLabel(session.source)]
      .forEach((value) => row.append(element("td", "", value)));
    target.append(row);
  });
}

function renderRuntimeSurfaces(runtime, providers) {
  const stale = Boolean(state.sync.console.error);
  const label = runtimeLabel(runtime.state);
  const exposure = runtime.lan_management_enabled ? "局域网" : "仅本机";
  const healthy = providers.filter((provider) => provider.status === "ok").length;
  setText("#devices-count", stale ? "—" : runtime.connected_decks ?? 0);
  setPill($("#devices-count-pill"), stale ? "状态已过期" : `${runtime.connected_decks ?? 0} 个在线`,
    stale ? "warning" : runtime.connected_decks > 0 ? "success" : "neutral");
  setText("#management-address", runtime.management_address);
  setText("#device-hub-address", runtime.device_hub_address);
  setText("#network-runtime-state", stale ? "已过期" : label);
  setText("#network-decks", stale ? "—" : runtime.connected_decks ?? 0);
  setPill($("#network-exposure"), exposure, runtime.lan_management_enabled ? "warning" : "success");
  setText("#system-runtime", stale ? "已过期" : label);
  setText("#system-version", runtime.version);
  setText("#system-exposure", exposure);
  setText("#system-history", runtime.history_available ? (runtime.history_enabled ? "已开启" : "已关闭") : "不可用");
  const healthyText = state.sync.console.error ? "已过期" :
    runtime.state === "ready" && healthy === providers.length ? "正常" : "需关注";
  setPill($("#diagnostics-health"), healthyText, healthyText === "正常" ? "success" : "warning");
  setText("#diagnostics-runtime", stale ? "已过期" : label);
  setText("#diagnostics-providers", stale ? `${providers.length} 条最后有效数据` :
    providers.length ? `${healthy}/${providers.length} 正常` : "尚无快照");
  setText("#diagnostics-device-link", stale ? "连接状态已过期" : `${runtime.connected_decks ?? 0} 个 Deck 已连接`);
  setText("#diagnostics-error", stale ? "运行诊断已过期" :
    runtime.last_error ? "运行时报告了错误（详细信息仅保留在本机日志）" : "无");
  setText("#tray-runtime", stale ? "Companion 状态已过期" : `Companion ${label}`);
  setText("#tray-decks", stale ? "Deck —" : `Deck ${runtime.connected_decks ?? 0}`);
  setText("#tray-clock", formatTime(new Date().toISOString(), false));
}

function formatDiagnosticBytes(value) {
  if (!Number.isSafeInteger(value) || value < 0) return "—";
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}

function renderDiagnosticsStorage() {
  const diagnostic = state.diagnostics;
  const stale = Boolean(state.sync.diagnostics.error);
  const available = Boolean(diagnostic?.available) && !stale;
  setPill($("#diagnostics-storage-state"), stale ? "已过期" : available ? "可导出" : "不可用",
    available ? "success" : "warning");
  setText("#diagnostics-retention", diagnostic ?
    `${diagnostic.retention_days} 天 / ${formatDiagnosticBytes(diagnostic.maximum_bytes)}` : "—");
  setText("#diagnostics-segments", diagnostic ?
    `${diagnostic.segments} 段 · ${formatDiagnosticBytes(diagnostic.stored_bytes)}` : "—");
  setText("#diagnostics-rings", diagnostic ? `${diagnostic.deck_rings} 个 Deck` : "—");
  setText("#diagnostics-files", diagnostic?.bundle_files?.length ?
    `${diagnostic.bundle_files.length} 个固定路径` : "—");
  $("#export-diagnostics").disabled = !available;
}

async function loadDiagnostics(quiet = false) {
  const operation = authenticatedOperation();
  if (!state.sync.diagnostics.lastSuccess) resourceFeedback("diagnostics", "正在读取脱敏诊断状态……");
  try {
    const response = await request("/api/v1/diagnostics", { signal: operation.signal });
    const diagnostic = await response.json();
    if (!operationIsCurrent(operation)) return;
    state.diagnostics = diagnostic;
    state.sync.diagnostics.lastSuccess = new Date().toISOString();
    state.sync.diagnostics.error = "";
    resourceFeedback("diagnostics");
    renderDiagnosticsStorage();
    if (!quiet) message("诊断状态已刷新。", true);
  } catch (error) {
    if (!operationIsCurrent(operation)) return;
    state.sync.diagnostics.error = error.message;
    resourceFeedback("diagnostics", state.sync.diagnostics.lastSuccess ?
      `无法刷新诊断状态；保留 ${formatTime(state.sync.diagnostics.lastSuccess, false)} 的最后有效统计。` :
      "脱敏诊断当前不可用。", true);
    renderDiagnosticsStorage();
    if (!quiet) message(error.message);
  }
}

async function exportDiagnostics() {
  if ($("#export-diagnostics").disabled) return;
  const operation = authenticatedOperation();
  $("#export-diagnostics").disabled = true;
  resourceFeedback("diagnostics", "正在本机生成脱敏诊断包……");
  try {
    const response = await request("/api/v1/diagnostics/export", {
      method: "POST", signal: operation.signal,
    });
    const archive = await response.blob();
    if (!operationIsCurrent(operation)) return;
    const url = URL.createObjectURL(archive);
    const link = element("a");
    link.href = url;
    link.download = "s3-rlcd-deck-diagnostics.zip";
    link.click();
    URL.revokeObjectURL(url);
    resourceFeedback("diagnostics", "诊断包已交给浏览器下载；没有上传到外部服务。", false);
    toast("诊断包已生成", "ZIP 包含 manifest、逐文件 SHA-256、脱敏事件和 Deck 内存环。", false);
    await loadDiagnostics(true);
  } catch (error) {
    if (operationIsCurrent(operation)) resourceFeedback("diagnostics", error.message, true);
  } finally {
    if (operationIsCurrent(operation)) renderDiagnosticsStorage();
  }
}

function updateCapabilityControls() {
  const capabilities = state.console?.capabilities || {};
  $$('[data-action="pair-deck"], #pair-deck').forEach((button) => { button.disabled = !capabilities.pairing_v2; });
  $("#new-provider").disabled = !providerDataWritable();
  $("#history-enabled").disabled = !capabilities.history;
  $("#save-history").disabled = !capabilities.history;
  $("#export-backup").disabled = !capabilities.backup;
  $("#preview-backup").disabled = !capabilities.backup;
  $("#ota-preview").disabled = !capabilities.updates;
  updateOTAApplyAvailability();
  updateProviderEditorControls();
}

function providerDataWritable() {
  return Boolean(state.console?.capabilities?.provider_management) &&
    Boolean(state.sync.providers.lastSuccess) && !state.sync.providers.error;
}

function providerConfigurationIsFresh(report = false) {
  if (providerDataWritable()) return true;
  if (report) {
    const detail = state.sync.providers.error ? "Provider 配置已过期；请先刷新并确认最新配置。" :
      "Provider 配置尚未就绪；请等待加载完成后重试。";
    message(detail);
    toast("操作已阻止", detail, true);
  }
  return false;
}

function updateProviderEditorControls() {
  const available = providerDataWritable();
  $$('[data-page="provider-editor"]').forEach((button) => { button.disabled = !available; });
  const save = document.querySelector('[type="submit"][form="provider-form"]');
  if (save) save.disabled = !available;
  $("#test-provider").disabled = !available || !state.editing;
  for (const control of $("#provider-form").elements) {
    control.disabled = !available || (control.id === "provider-id" && Boolean(state.editing));
  }
}

async function loadProviders() {
  if (!state.sync.providers.lastSuccess) {
    resourceFeedback("providers", "正在加载 Provider 配置……");
    const target = $("#provider-list");
    target.replaceChildren();
    for (let index = 0; index < 3; index += 1) {
      const skeleton = element("article", "provider-card skeleton-card");
      skeleton.setAttribute("aria-hidden", "true");
      skeleton.append(element("span", "skeleton-line wide"), element("span", "skeleton-line"),
        element("span", "skeleton-line short"));
      target.append(skeleton);
    }
  }
  try {
    const view = await (await request("/api/v1/providers")).json();
    state.providers = view.providers || [];
    state.templates = view.templates || [];
    state.providerStates = view.states || [];
    state.sync.providers.lastSuccess = new Date().toISOString();
    state.sync.providers.error = "";
    resourceFeedback("providers");
    renderProviders();
    renderTemplates();
    updateCapabilityControls();
  } catch (error) {
    state.sync.providers.error = error.message;
    const lastSuccess = state.sync.providers.lastSuccess;
    resourceFeedback("providers", lastSuccess ?
      `无法刷新 Provider；保留 ${formatTime(lastSuccess, false)} 的最后有效配置。` :
      "Provider 配置当前不可用，请重试。", true);
    if (lastSuccess) {
      renderProviders();
      renderTemplates();
    } else {
      $("#provider-list").replaceChildren(emptyState("Provider 当前不可用", "请检查 Companion 后重试。"));
    }
    updateCapabilityControls();
  }
}
function providerRuntime(id) { return state.providerStates.find((provider) => provider.provider_id === id); }
function quotaSummary(provider) {
  const windowView = provider?.windows?.[0];
  if (windowView?.remaining_basis_points != null) return `剩余 ${(windowView.remaining_basis_points / 100).toFixed(0)}%`;
  if (windowView?.used_basis_points != null) return `已用 ${(windowView.used_basis_points / 100).toFixed(0)}%`;
  if (provider?.tokens?.total != null) return `${formatTokens(provider.tokens.total)} Token`;
  return "等待可用指标";
}

function renderProviders() {
  const stale = Boolean(state.sync.providers.error);
  const target = $("#provider-list");
  target.replaceChildren();
  const visible = state.providers.filter((provider) => state.providerFilter === "all" ||
    (providerRuntime(provider.id)?.status || "unavailable") === state.providerFilter);
  if (!visible.length) {
    target.append(emptyState(state.providers.length ? "此筛选下没有 Provider" : "尚未配置结构化 Provider",
      "可以从模板开始，也可以创建自定义 HTTPS 数据源。"));
    return;
  }
  visible.forEach((provider) => {
    const index = state.providers.findIndex((candidate) => candidate.id === provider.id);
    const runtime = providerRuntime(provider.id);
    const status = runtime?.status || "unavailable";
    const card = element("article", "provider-card");
    const header = element("div", "provider-card-header");
    const identity = element("div");
    identity.append(element("h3", "", provider.display_name),
      element("p", "", `${provider.id} · 每 ${provider.refresh_minutes} 分钟${provider.experimental ? " · 实验性" : ""}`));
    const pill = element("span");
    setPill(pill, stale ? "已过期" : providerStatusLabel(status), stale ? "warning" : statusClass(status));
    header.append(identity, pill);
    const metric = element("div", "provider-card-metric");
    metric.append(element("strong", "", runtime?.balance ? formatMoney(runtime.balance) : quotaSummary(runtime)),
      element("span", "", stale ? `最后有效 ${formatTime(state.sync.providers.lastSuccess, false)}` :
        runtime ? `${confidenceLabel(runtime.confidence)} · ${formatRelative(runtime.updated_at)}` : "等待首次采集"));
    const actions = element("div", "provider-card-actions");
    [["上移", () => moveProvider(index, -1), index === 0], ["下移", () => moveProvider(index, 1), index === state.providers.length - 1],
      ["测试", () => testProvider(provider.id), false], ["编辑", () => openEditor(provider), false],
      ["删除", () => deleteProvider(provider), false, "danger"]].forEach(([label, action, disabled, kind]) => {
      const button = element("button", `button small${kind ? ` ${kind}` : ""}`, label);
      button.type = "button";
      button.disabled = disabled || stale;
      button.addEventListener("click", action);
      actions.append(button);
    });
    card.append(header, metric, actions);
    target.append(card);
  });
}

function renderTemplates() {
  const target = $("#template-list");
  target.replaceChildren();
  if (!state.templates.length) return;
  target.append(element("span", "muted", "从模板开始："));
  state.templates.forEach((template) => {
    const button = element("button", "template-chip", template.display_name);
    button.type = "button";
    button.disabled = Boolean(state.sync.providers.error);
    button.addEventListener("click", () => openEditor(template));
    target.append(button);
  });
}

function addHeaderRow(header = {}, originalIndex = -1) {
  const row = element("div", "header-row");
  row.dataset.originalIndex = String(originalIndex);
  row.dataset.secretConfigured = header.secret_configured ? "true" : "false";
  const nameLabel = element("label", "field");
  const name = element("input", "provider-header-name");
  name.required = true;
  name.value = header.name || "Authorization";
  nameLabel.append(element("span", "", "Header 名称"), name);
  const prefixLabel = element("label", "field");
  const prefix = element("input", "provider-header-prefix");
  prefix.value = header.prefix ?? "Bearer ";
  prefixLabel.append(element("span", "", "值前缀"), prefix);
  const secretLabel = element("label", "field");
  const secret = element("input", "provider-header-secret");
  secret.type = "password";
  secret.autocomplete = "new-password";
  secretLabel.append(element("span", "", "API Key / Token"), secret,
    element("small", "", header.secret_configured ? "留空可保留现有凭据" : "请输入新的凭据"));
  const remove = element("button", "button small danger", "移除");
  remove.type = "button";
  remove.addEventListener("click", () => row.remove());
  row.append(nameLabel, prefixLabel, secretLabel, remove);
  $("#provider-headers").append(row);
}

function renderHeaderRows(headers, suggestDefault) {
  $("#provider-headers").replaceChildren();
  const values = headers?.length ? headers : suggestDefault ? [{ name: "Authorization", prefix: "Bearer " }] : [];
  values.forEach((header, index) => addHeaderRow(header, headers?.length ? index : -1));
}

function openEditor(provider = null, shouldNavigate = true) {
  if (!providerConfigurationIsFresh(true)) return;
  const saved = provider && state.providers.some((item) => item.id === provider.id);
  state.editing = saved ? provider.id : "";
  const value = provider || { request: { method: "GET", headers: [] }, mapping: {}, refresh_minutes: 5 };
  setText("#editor-title", saved ? `编辑 ${value.display_name}` : "添加 Provider");
  $("#provider-id").value = value.id || "";
  $("#provider-id").disabled = saved;
  $("#provider-name").value = value.display_name || "";
  $("#provider-method").value = value.request?.method || "GET";
  $("#provider-refresh").value = String(value.refresh_minutes || 5);
  $("#provider-url").value = value.request?.url || "";
  renderHeaderRows(value.request?.headers || [], !saved);
  $("#map-balance").value = value.mapping?.balance_path || "";
  $("#map-currency").value = value.mapping?.currency_path || "";
  $("#map-used").value = value.mapping?.used_path || "";
  $("#map-total").value = value.mapping?.total_path || "";
  $("#map-reset").value = value.mapping?.reset_path || "";
  $("#map-reset-format").value = value.mapping?.reset_format || "";
  $("#map-window").value = value.mapping?.window_name || "";
  $("#map-fixed-currency").value = value.mapping?.fixed_currency || "";
  $("#map-divisor").value = value.mapping?.balance_divisor || 1;
  $("#provider-body").value = value.request?.body ? JSON.stringify(value.request.body, null, 2) : "";
  $("#provider-experimental").checked = Boolean(value.experimental);
  $("#test-preview-card").hidden = true;
  updateProviderEditorControls();
  if (shouldNavigate) navigate("provider-editor");
}

function definitionFromForm() {
  const bodyText = $("#provider-body").value.trim();
  const resetPath = $("#map-reset").value.trim();
  const resetFormat = $("#map-reset-format").value;
  if (Boolean(resetPath) !== Boolean(resetFormat)) throw new Error("重置时间 JSONPath 与格式必须同时配置。");
  const requestDefinition = { method: $("#provider-method").value, url: $("#provider-url").value.trim(), headers: [] };
  $$("#provider-headers .header-row").forEach((row) => requestDefinition.headers.push({
    name: row.querySelector(".provider-header-name").value.trim(), prefix: row.querySelector(".provider-header-prefix").value,
  }));
  if (bodyText) {
    try { requestDefinition.body = JSON.parse(bodyText); }
    catch (_) { throw new Error("POST JSON 正文格式无效。"); }
  }
  return {
    id: $("#provider-id").value.trim(), display_name: $("#provider-name").value.trim(),
    experimental: $("#provider-experimental").checked, request: requestDefinition,
    mapping: {
      balance_path: $("#map-balance").value.trim(), currency_path: $("#map-currency").value.trim(),
      used_path: $("#map-used").value.trim(), total_path: $("#map-total").value.trim(),
      reset_path: resetPath, reset_format: resetFormat, window_name: $("#map-window").value.trim(),
      fixed_currency: $("#map-fixed-currency").value.trim().toUpperCase(), balance_divisor: Number($("#map-divisor").value || 1),
    },
    refresh_minutes: Number($("#provider-refresh").value), request_timeout_seconds: 10, maximum_response_bytes: 262144,
  };
}

async function saveProvider(event) {
  event.preventDefault();
  if (!providerConfigurationIsFresh(true)) return;
  try {
    const definition = definitionFromForm();
    const current = state.providers.find((provider) => provider.id === state.editing);
    const secrets = [];
    const keep = [];
    $$("#provider-headers .header-row").forEach((row, index) => {
      const secret = row.querySelector(".provider-header-secret").value;
      if (secret) { secrets.push({ header_index: index, value: bytesBase64(secret) }); return; }
      if (row.dataset.secretConfigured !== "true") return;
      const originalIndex = Number(row.dataset.originalIndex);
      const original = current?.request?.headers?.[originalIndex];
      const next = definition.request.headers[index];
      if (originalIndex !== index || !original || original.name !== next.name || (original.prefix || "") !== (next.prefix || "")) {
        throw new Error("调整已配置凭据的 Header 后，需要重新输入对应凭据。");
      }
      keep.push(index);
    });
    const path = state.editing ? `/api/v1/providers/${encodeURIComponent(state.editing)}` : "/api/v1/providers";
    await request(path, { method: state.editing ? "PUT" : "POST", body: JSON.stringify({ definition, secrets, keep_existing: keep }) });
    $$(".provider-header-secret").forEach((input) => { input.value = ""; });
    toast("Provider 已保存", "动态采集已按新配置刷新。");
    await Promise.all([loadProviders(), loadConsole(true)]);
    navigate("providers");
  } catch (error) { message(error.message); toast("无法保存 Provider", error.message, true); }
}

async function moveProvider(index, delta) {
  if (!providerConfigurationIsFresh(true)) return;
  const ordered = state.providers.map((provider) => provider.id);
  [ordered[index], ordered[index + delta]] = [ordered[index + delta], ordered[index]];
  try {
    await request("/api/v1/providers/order", { method: "PUT", body: JSON.stringify({ provider_ids: ordered }) });
    toast("顺序已更新", "Deck 将按新的 Provider 顺序显示。");
    await loadProviders();
  } catch (error) { toast("无法更新顺序", error.message, true); }
}

async function deleteProvider(provider) {
  if (!providerConfigurationIsFresh(true)) return;
  const confirmed = await showDialog({ eyebrow: "不可撤销操作", title: `删除 ${provider.display_name}？`,
    paragraphs: ["将删除 Provider 定义及其凭据库引用。历史记录不会自动删除。"], confirmText: "确认删除", danger: true });
  if (!confirmed || !providerConfigurationIsFresh(true)) return;
  try {
    await request(`/api/v1/providers/${encodeURIComponent(provider.id)}`, { method: "DELETE" });
    toast("Provider 已删除", `${provider.display_name} 已从采集顺序中移除。`);
    await Promise.all([loadProviders(), loadConsole(true)]);
  } catch (error) { toast("无法删除 Provider", error.message, true); }
}

async function testProvider(id) {
  if (!providerConfigurationIsFresh(true)) return;
  message("正在执行受限测试请求……", false, true);
  try {
    const result = await (await request(`/api/v1/providers/${encodeURIComponent(id)}/test`, { method: "POST", body: "{}" })).json();
    const diagnostic = result.preview?.diagnostic || {};
    $("#test-preview-card").hidden = false;
    $("#test-preview").textContent = JSON.stringify(result.preview || {}, null, 2);
    const detail = result.ok ? `状态码 ${diagnostic.http_status || "—"} · ${diagnostic.latency_millis ?? "—"} 毫秒`
      : `错误码 ${diagnostic.error_code || "不可用"}`;
    message(result.ok ? `测试通过 · ${detail}` : `测试未通过 · ${detail}`, result.ok);
    toast(result.ok ? "测试通过" : "测试未通过", detail, !result.ok);
  } catch (error) { message(error.message); toast("测试请求失败", error.message, true); }
}

async function loadHistory() {
  if (!state.console?.capabilities?.history) return;
  if (!state.sync.history.lastSuccess) {
    resourceFeedback("history", "正在加载历史记录……");
    const target = $("#history-table");
    target.replaceChildren();
    const row = element("tr", "skeleton-row");
    const cell = element("td", "", "正在加载规范化小时记录……");
    cell.colSpan = 6;
    row.append(cell);
    target.append(row);
  }
  try {
    const selected = $("#history-provider").value;
    const query = new URLSearchParams({ limit: "200" });
    if (selected) query.set("provider_id", selected);
    const [settings, historyView] = await Promise.all([
      request("/api/v1/history/settings").then((response) => response.json()),
      request(`/api/v1/history?${query.toString()}`).then((response) => response.json()),
    ]);
    state.history = historyView.records || [];
    state.sync.history.lastSuccess = new Date().toISOString();
    state.sync.history.error = "";
    resourceFeedback("history");
    $("#history-enabled").checked = Boolean(settings.enabled);
    setText("#history-retention", `${settings.retention_days || 90} 天`);
    renderHistoryProviderOptions(selected);
    renderHistory();
  } catch (error) {
    state.sync.history.error = error.message;
    const lastSuccess = state.sync.history.lastSuccess;
    resourceFeedback("history", lastSuccess ?
      `无法刷新历史；保留 ${formatTime(lastSuccess, false)} 的最后有效记录。` :
      "历史记录当前不可用，请重试。", true);
    if (!lastSuccess) {
      const target = $("#history-table");
      target.replaceChildren();
      const row = element("tr");
      const cell = element("td", "", "历史记录当前不可用。");
      cell.colSpan = 6;
      row.append(cell);
      target.append(row);
    }
  }
}

function renderHistoryProviderOptions(selected) {
  const target = $("#history-provider");
  const ids = new Set([...state.history.map((record) => record.provider_id),
    ...(state.console?.providers || []).map((provider) => provider.provider_id)]);
  target.replaceChildren();
  const all = element("option", "", "全部 Provider");
  all.value = "";
  target.append(all);
  [...ids].filter(Boolean).sort().forEach((id) => {
    const option = element("option", "", id);
    option.value = id;
    target.append(option);
  });
  target.value = ids.has(selected) ? selected : "";
}

function renderHistory() {
  const target = $("#history-table");
  target.replaceChildren();
  const providerIDs = new Set(state.history.map((record) => record.provider_id));
  const knownTokens = state.history.map((record) => record.tokens?.total).filter((value) => value != null);
  setText("#history-count", state.history.length);
  setText("#history-provider-count", providerIDs.size);
  setText("#history-token-total", knownTokens.length ? formatTokens(knownTokens.reduce((sum, value) => sum + value, 0)) : "—");
  if (!state.history.length) {
    const row = element("tr");
    const cell = element("td", "", "当前筛选范围没有历史记录。");
    cell.colSpan = 6;
    row.append(cell);
    target.append(row);
    return;
  }
  state.history.forEach((record) => {
    const row = element("tr");
    [formatTime(record.observed_at_utc), record.provider_id, providerStatusLabel(record.status), formatMoney(record.balance),
      formatTokens(record.tokens?.total), record.error_code || "—"].forEach((value) => row.append(element("td", "", value)));
    target.append(row);
  });
}

async function saveHistory() {
  try {
    await request("/api/v1/history/settings", { method: "PUT", body: JSON.stringify({ enabled: $("#history-enabled").checked }) });
    toast("设置已保存", $("#history-enabled").checked ? "后续快照将写入本机历史。" : "后续快照将不再写入历史。");
    await loadConsole(true);
  } catch (error) { toast("无法保存设置", error.message, true); }
}

async function clearHistory() {
  const confirmed = await showDialog({ eyebrow: "本机数据", title: "清空全部用量历史？",
    paragraphs: ["此操作会永久删除本机 SQLite 中的 Provider 小时级记录，不影响当前 Provider 配置。"],
    confirmText: "确认清空", danger: true });
  if (!confirmed) return;
  try {
    await request("/api/v1/history", { method: "DELETE" });
    toast("历史已清空", "所有本机 Provider 历史记录均已删除。");
    await loadHistory();
  } catch (error) { toast("无法清空历史", error.message, true); }
}

function renderDeckPreview() {
  const target = $("#deck-screen");
  if (!target) return;
  const providers = state.console?.providers || [];
  target.replaceChildren();
  if (!providers.length) {
    const empty = element("div", "rlcd-empty");
    const copy = element("div");
    copy.append(element("h2", "", "尚无 AI Snapshot"), element("p", "", "请在 Companion 中配置 Provider"),
      element("p", "", "TX 未启用"));
    empty.append(copy);
    target.append(empty);
    $("#deck-empty-note").hidden = false;
    setText("#deck-provider-name", "—"); setText("#deck-provider-confidence", "—"); setText("#deck-provider-updated", "—");
    return;
  }
  state.deckIndex %= providers.length;
  const provider = providers[state.deckIndex];
  const status = element("div", "rlcd-status");
  status.append(element("span", "", "S3 RLCD"), element("span", "", state.sync.console.error ? "Deck —" : `Deck ${state.console.runtime?.connected_decks ?? 0}`),
    element("span", "", state.sync.console.error ? "已过期" : providerStatusLabel(provider.status)),
    element("span", "", formatTime(provider.updated_at, false)));
  const body = element("div", "rlcd-body");
  const title = element("div", "rlcd-title-row");
  title.append(element("h2", "", provider.display_name), element("span", "rlcd-badge", confidenceLabel(provider.confidence)));
  body.append(title);
  const windowView = provider.windows?.[0];
  if (windowView) {
    const used = windowView.used_basis_points != null ? windowView.used_basis_points :
      windowView.remaining_basis_points != null ? 10000 - windowView.remaining_basis_points : null;
    if (used != null) {
      const metric = element("div", "rlcd-metric");
      metric.append(element("span", "", windowView.name || "配额"));
      const bar = element("span", "rlcd-bar");
      const fill = element("i");
      const progress = Math.round(Math.min(100, used / 100) / 10) * 10;
      fill.className = `progress-${progress}`;
      bar.append(fill);
      metric.append(bar, element("strong", "", `${(used / 100).toFixed(0)}%`));
      body.append(metric);
    }
  }
  const details = element("div", "rlcd-details");
  if (provider.balance) details.append(element("span", "", formatMoney(provider.balance)));
  if (provider.tokens?.total != null) details.append(element("span", "", `${formatTokens(provider.tokens.total)} Token`));
  details.append(element("span", "", provider.experimental ? "实验性" : sourceLabel(provider.source)));
  const footer = element("div", "rlcd-footer");
  footer.append(element("span", "", "TX 未启用"), element("span", "", `${state.deckIndex + 1}/${providers.length}`),
    element("span", "", "KEY 下一页"));
  body.append(details, footer);
  target.append(status, body);
  $("#deck-empty-note").hidden = true;
  setText("#deck-provider-name", provider.display_name);
  setText("#deck-provider-confidence", confidenceLabel(provider.confidence));
  setText("#deck-provider-updated", `${formatTime(provider.updated_at)}（${formatRelative(provider.updated_at)}）`);
}

function clearPairingV2Timers() {
  if (state.pairingV2.pollTimer) window.clearTimeout(state.pairingV2.pollTimer);
  if (state.pairingV2.countdownTimer) window.clearTimeout(state.pairingV2.countdownTimer);
  state.pairingV2.pollTimer = null;
  state.pairingV2.countdownTimer = null;
}

function rotatePairingV2Scope() {
  clearPairingV2Timers();
  state.pairingV2.controller.abort();
  state.pairingV2.controller = new AbortController();
  state.pairingV2.epoch += 1;
}

function pairingV2Operation() {
  return {
    authEpoch: state.authEpoch, epoch: state.pairingV2.epoch,
    signal: state.pairingV2.controller.signal,
  };
}

function pairingV2OperationIsCurrent(operation) {
  return state.authenticated && operation.authEpoch === state.authEpoch &&
    operation.epoch === state.pairingV2.epoch && !operation.signal.aborted;
}

function storePairingV2Session(reference = "") {
  try {
    if (reference) sessionStorage.setItem(pairingV2SessionStorageKey, reference);
    else sessionStorage.removeItem(pairingV2SessionStorageKey);
  } catch (_) { /* private browsing may disable session storage; the live flow still works. */ }
}

function resetPairingV2({ forget = true, close = false } = {}) {
  rotatePairingV2Scope();
  state.pairingV2.candidates = [];
  state.pairingV2.view = null;
  if (forget) {
    state.pairingV2.sessionRef = "";
    storePairingV2Session();
  }
  const code = $("#pairing-v2-code");
  if (code) code.value = "";
  const candidates = $("#pairing-v2-candidates");
  if (candidates) candidates.replaceChildren();
  const form = $("#pairing-v2-code-form");
  if (form) form.hidden = true;
  const dialog = $("#pairing-v2-dialog");
  if (close) {
    state.pairingV2.originPage = "";
    if (dialog?.open) dialog.close();
  }
}

function setPairingV2Feedback(title, detail, kind = "neutral") {
  setText("#pairing-v2-state", title);
  setText("#pairing-v2-detail", detail);
  const callout = $("#pairing-v2-feedback");
  callout.className = `callout compact ${kind}`;
}

function pairingV2SecondsText(expiresAt) {
  const remaining = window.S3DeckPairingV2UI.secondsRemaining(expiresAt);
  return remaining > 0 ? `配对窗口剩余 ${remaining} 秒。验证码只会交给本机 Companion。` : "配对窗口正在过期。";
}

function renderPairingV2Candidates() {
  if (!state.authenticated || state.pairingV2.sessionRef) return;
  const candidates = window.S3DeckPairingV2UI.candidates(state.pairingV2.candidates);
  const target = $("#pairing-v2-candidates");
  target.replaceChildren();
  if (!candidates.length) {
    target.append(emptyState("没有可用的 Deck", "请确认 Deck 已打开配对窗口，并点击“重新扫描”。"));
    setPairingV2Feedback("未发现设备", "同网不等于一定可互访；访客网络、客户端隔离或 mDNS 过滤会阻止发现。", "warning");
    return;
  }
  setPairingV2Feedback("请选择 Deck", "候选是短期匿名记录；页面不会获得 IP、MAC、Device ID 或信任材料。", "info");
  for (const candidate of candidates) {
    const row = element("div", "pairing-candidate");
    const copy = element("div");
    copy.append(element("strong", "", candidate.label),
      element("small", "", pairingV2SecondsText(candidate.expiresAt)));
    const button = element("button", "button primary", "选择并继续");
    button.type = "button";
    button.addEventListener("click", () => beginPairingV2Session(candidate.reference));
    row.append(copy, button);
    target.append(row);
  }
  state.pairingV2.countdownTimer = window.setTimeout(renderPairingV2Candidates, 1000);
}

function schedulePairingV2Poll(delay = 900) {
  if (state.pairingV2.pollTimer) window.clearTimeout(state.pairingV2.pollTimer);
  state.pairingV2.pollTimer = window.setTimeout(pollPairingV2Session, delay);
}

function renderPairingV2Session(view) {
  state.pairingV2.view = view;
  state.pairingV2.sessionRef = view.session_ref;
  $("#pairing-v2-candidates").replaceChildren();
  const presentation = window.S3DeckPairingV2UI.presentation(view);
  setPairingV2Feedback(presentation.title, presentation.detail, presentation.kind);
  const awaitingCode = presentation.state === "awaiting_code";
  $("#pairing-v2-code-form").hidden = !awaitingCode;
  $("#pairing-v2-rescan").hidden = !presentation.terminal;
  $("#pairing-v2-cancel").textContent = presentation.terminal ? "关闭" : "取消配对";
  $("#pairing-v2-countdown").textContent = pairingV2SecondsText(view.expires_at);
  if (awaitingCode) $("#pairing-v2-code").focus();
  if (presentation.terminal) {
    clearPairingV2Timers();
    storePairingV2Session();
    if (presentation.success) toast("Deck 配对完成", "新的 Device Link 已通过认证心跳并正式在线。");
    return;
  }
  storePairingV2Session(view.session_ref);
  state.pairingV2.countdownTimer = window.setTimeout(() => {
    if (state.pairingV2.view?.session_ref === view.session_ref) {
      $("#pairing-v2-countdown").textContent = pairingV2SecondsText(view.expires_at);
    }
  }, 1000);
  schedulePairingV2Poll();
}

async function scanPairingV2Candidates() {
  if (!state.console?.capabilities?.pairing_v2) {
    toast("安全配对不可用", "当前 Companion 未启用 Pairing v2。", true);
    return;
  }
  resetPairingV2({ forget: true });
  const operation = pairingV2Operation();
  $("#pairing-v2-rescan").hidden = true;
  $("#pairing-v2-cancel").textContent = "关闭";
  setPairingV2Feedback("正在扫描", "Companion 正在默认局域网接口上查找已打开配对窗口的 Deck。", "info");
  $("#pairing-v2-candidates").replaceChildren(element("span", "skeleton-line"), element("span", "skeleton-line"));
  try {
    const response = await request("/api/v1/pairing-v2/scan", {
      method: "POST", body: "{}", signal: operation.signal,
    });
    const result = await response.json();
    if (!pairingV2OperationIsCurrent(operation)) return;
    state.pairingV2.candidates = result.candidates || [];
    $("#pairing-v2-rescan").hidden = false;
    renderPairingV2Candidates();
  } catch (error) {
    if (error?.name === "AbortError" || !pairingV2OperationIsCurrent(operation)) return;
    state.pairingV2.candidates = [];
    $("#pairing-v2-rescan").hidden = false;
    $("#pairing-v2-candidates").replaceChildren(emptyState("扫描不可用", error.message));
    setPairingV2Feedback("无法扫描", error.message, "danger");
  }
}

async function beginPairingV2Session(candidateReference) {
  const operation = pairingV2Operation();
  clearPairingV2Timers();
  setPairingV2Feedback("正在建立会话", "请保持 Deck 配对窗口开启。", "info");
  $("#pairing-v2-candidates").replaceChildren(element("span", "skeleton-line"));
  try {
    const response = await request("/api/v1/pairing-v2/sessions", {
      method: "POST", body: JSON.stringify({ candidate_ref: candidateReference }), signal: operation.signal,
    });
    const view = await response.json();
    if (!pairingV2OperationIsCurrent(operation)) return;
    renderPairingV2Session(view);
  } catch (error) {
    if (error?.name === "AbortError" || !pairingV2OperationIsCurrent(operation)) return;
    setPairingV2Feedback("无法建立会话", error.message, "danger");
    $("#pairing-v2-rescan").hidden = false;
    $("#pairing-v2-candidates").replaceChildren(emptyState("候选已失效", "请重新扫描并再次选择 Deck。"));
  }
}

async function confirmPairingV2(event) {
  event.preventDefault();
  if (!state.pairingV2.sessionRef || state.pairingV2.view?.state !== "awaiting_code") return;
  const input = $("#pairing-v2-code");
  let code = input.value;
  if (!window.S3DeckPairingV2UI.validCode(code)) {
    input.setCustomValidity("请输入六位数字配对码。");
    input.reportValidity();
    return;
  }
  input.setCustomValidity("");
  let body = JSON.stringify({ code });
  input.value = "";
  const operation = pairingV2Operation();
  state.pairingV2.view = { ...state.pairingV2.view, state: "authenticating" };
  $("#pairing-v2-code-form").hidden = true;
  setPairingV2Feedback("正在验证", "正在建立 PAKE 安全通道；请保持 Deck 在线。", "info");
  try {
    const reference = encodeURIComponent(state.pairingV2.sessionRef);
    const response = await request(`/api/v1/pairing-v2/sessions/${reference}/confirm`, {
      method: "POST", body, signal: operation.signal,
    });
    const view = await response.json();
    if (!pairingV2OperationIsCurrent(operation)) return;
    renderPairingV2Session(view);
  } catch (error) {
    if (error?.name !== "AbortError" && pairingV2OperationIsCurrent(operation)) {
      setPairingV2Feedback("无法提交验证码", error.message, "danger");
      $("#pairing-v2-code-form").hidden = false;
    }
  } finally {
    code = "";
    body = "";
    input.value = "";
  }
}

async function pollPairingV2Session() {
  const reference = state.pairingV2.sessionRef;
  if (!reference) return;
  const operation = pairingV2Operation();
  try {
    const response = await request(`/api/v1/pairing-v2/sessions/${encodeURIComponent(reference)}`, {
      signal: operation.signal,
    });
    const view = await response.json();
    if (!pairingV2OperationIsCurrent(operation) || state.pairingV2.sessionRef !== reference) return;
    renderPairingV2Session(view);
  } catch (error) {
    if (error?.name === "AbortError" || !pairingV2OperationIsCurrent(operation)) return;
    if (state.pairingV2.view &&
        window.S3DeckPairingV2UI.secondsRemaining(state.pairingV2.view.expires_at) === 0) {
      renderPairingV2Session({
        ...state.pairingV2.view, state: "expired", error_code: "expired",
      });
      return;
    }
    setPairingV2Feedback("连接状态暂不可用", `${error.message} Companion 会继续检查，且不会假定配对成功。`, "warning");
    schedulePairingV2Poll(1500);
  }
}

async function cancelPairingV2() {
  const reference = state.pairingV2.sessionRef;
  const terminal = window.S3DeckPairingV2UI.presentation(state.pairingV2.view).terminal;
  $("#pairing-v2-code").value = "";
  const dialog = $("#pairing-v2-dialog");
  state.pairingV2.originPage = "";
  if (dialog.open) dialog.close();
  if (reference && !terminal) {
    rotatePairingV2Scope();
    const operation = pairingV2Operation();
    try {
      await request(`/api/v1/pairing-v2/sessions/${encodeURIComponent(reference)}`, {
        method: "DELETE", signal: operation.signal,
      });
    } catch (error) {
      if (error?.name !== "AbortError" && pairingV2OperationIsCurrent(operation)) {
        toast("配对取消待过期", "Companion 暂时无法确认远端取消；临时会话仍会按截止时间自动清理。", true);
      }
    }
  }
  resetPairingV2({ forget: true, close: true });
}

async function openPairingV2Dialog() {
  if (!state.console?.capabilities?.pairing_v2) {
    toast("安全配对不可用", "当前 Companion 未启用 Pairing v2。", true);
    return;
  }
  const dialog = $("#pairing-v2-dialog");
  state.pairingV2.originPage = state.page;
  if (!dialog.open) dialog.showModal();
  if (state.pairingV2.sessionRef && state.pairingV2.view) {
    renderPairingV2Session(state.pairingV2.view);
    return;
  }
  await scanPairingV2Candidates();
}

async function resumePairingV2Session() {
  let reference = "";
  try { reference = sessionStorage.getItem(pairingV2SessionStorageKey) || ""; } catch (_) { return; }
  if (!/^[A-Za-z0-9_-]{16,64}$/.test(reference)) {
    storePairingV2Session();
    return;
  }
  resetPairingV2({ forget: false });
  state.pairingV2.sessionRef = reference;
  const operation = pairingV2Operation();
  try {
    const response = await request(`/api/v1/pairing-v2/sessions/${encodeURIComponent(reference)}`, {
      signal: operation.signal,
    });
    const view = await response.json();
    if (!pairingV2OperationIsCurrent(operation)) return;
    state.pairingV2.originPage = state.page;
    $("#pairing-v2-dialog").showModal();
    renderPairingV2Session(view);
  } catch (error) {
    if (error?.name === "AbortError" || !pairingV2OperationIsCurrent(operation)) return;
    state.pairingV2.sessionRef = "";
    storePairingV2Session();
    toast("无法恢复配对会话", "会话已失效；请重新扫描 Deck。", true);
  }
}

async function exportBackup() {
  const operation = authenticatedOperation();
  const passphrase = $("#export-passphrase").value;
  if (!passphrase) { toast("需要备份口令", "请先输入用于加密归档的强口令。", true); return; }
  try {
    const response = await request("/api/v1/backups/export", { method: "POST", body: JSON.stringify({ passphrase }), signal: operation.signal });
    const archive = await response.blob();
    if (!operationIsCurrent(operation)) return;
    const url = URL.createObjectURL(archive);
    const link = element("a");
    link.href = url;
    link.download = "s3-rlcd-deck-backup.age";
    link.click();
    URL.revokeObjectURL(url);
    $("#export-passphrase").value = "";
    toast("备份已生成", "加密归档已交给浏览器下载。请将口令分开保存。");
  } catch (error) { if (operationIsCurrent(operation)) toast("无法生成备份", error.message, true); }
}

async function backupPayload() {
  const file = $("#import-file").files[0];
  if (!file) throw new Error("请选择加密归档。");
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return { archive: btoa(binary), passphrase: $("#import-passphrase").value, mode: $("#backup-mode").value };
}

function resetBackupPreview(text = "输入已更改，请重新预览。") {
  state.backup = null;
  $("#backup-conflicts").replaceChildren();
  $("#backup-preview").textContent = text;
  $("#apply-backup").disabled = true;
}

function renderBackupConflicts(preview) {
  const target = $("#backup-conflicts");
  target.replaceChildren();
  const required = (preview.conflicts || []).filter((conflict) => conflict.decision_required);
  required.forEach((conflict) => {
    const label = element("label", "field");
    label.append(element("span", "", `${conflict.kind}：${conflict.current_label} ↔ ${conflict.backup_label}`));
    const select = element("select");
    [["", "请选择处理方式"], ["keep_current", "保留当前"], ["use_backup", "使用备份"]].forEach(([value, text]) => {
      const option = element("option", "", text);
      option.value = value;
      select.append(option);
    });
    select.addEventListener("change", () => {
      if (select.value) state.backup.decisions[conflict.key] = select.value;
      else delete state.backup.decisions[conflict.key];
      $("#apply-backup").disabled = Object.keys(state.backup.decisions).length !== required.length;
    });
    label.append(select);
    target.append(label);
  });
  $("#apply-backup").disabled = required.length !== 0;
}

async function previewBackup() {
  resetBackupPreview("正在验证加密归档……");
  const operation = authenticatedOperation();
  try {
    const payload = await backupPayload();
    if (!operationIsCurrent(operation)) return;
    const response = await request("/api/v1/backups/preview", { method: "POST", body: JSON.stringify(payload), signal: operation.signal });
    const preview = await response.json();
    if (!operationIsCurrent(operation)) return;
    state.backup = { ...payload, preview_id: preview.preview_id, decisions: {} };
    renderBackupConflicts(preview);
    $("#backup-preview").textContent = JSON.stringify(preview, null, 2);
  } catch (error) {
    if (!operationIsCurrent(operation)) return;
    resetBackupPreview("预览失败。请检查归档和口令。");
    toast("无法预览备份", error.message, true);
  }
}

async function applyBackup() {
  if (!state.backup) return;
  const operation = authenticatedOperation();
  const replace = state.backup.mode === "replace";
  const confirmed = await showDialog({ eyebrow: replace ? "完整替换 · 破坏性操作" : "事务导入",
    title: replace ? "替换当前可迁移配置？" : "应用已预览的备份？",
    paragraphs: replace ? [
      "这会以归档中的 Provider、显示顺序和可迁移设置替换当前配置；Pairing 信任、历史和 Web 会话不受影响。",
      "请先导出当前配置作为恢复点。导入失败会保持当前配置；导入成功后可重新导入该恢复点撤销。",
    ] : ["Companion 将按当前模式和逐项决定写入可恢复配置。Pairing 信任、历史和 Web 会话不会导入。"],
    confirmText: replace ? "确认替换当前配置" : "确认导入", danger: replace });
  if (!confirmed || !operationIsCurrent(operation) || !state.backup) return;
  try {
    const response = await request("/api/v1/backups/import", {
      method: "POST", body: JSON.stringify(state.backup), signal: operation.signal,
    });
    const result = await response.json();
    if (!operationIsCurrent(operation)) return;
    state.backup = null;
    $("#import-passphrase").value = "";
    $("#apply-backup").disabled = true;
    $("#backup-conflicts").replaceChildren();
    toast("导入完成", result.restart_required ? "需要重启 Companion 才能应用全部设置。" : "配置已完成事务写入。");
    await Promise.all([loadProviders(), loadConsole(true)]);
  } catch (error) { if (operationIsCurrent(operation)) toast("无法导入备份", error.message, true); }
}

function resetOTAPreview(text = "请选择签名固件包并先执行校验。") {
  state.otaPreviewEpoch += 1;
  state.ota = null;
  const file = $("#ota-file");
  if (!file) return;
  $("#ota-confirm").checked = false;
  setText("#ota-version", "—");
  setText("#ota-board", "—");
  setText("#ota-size", "—");
  setText("#ota-key", "—");
  setText("#ota-digest", "—");
  setText("#ota-state", "等待预览");
  setText("#ota-progress", "—");
  setText("#ota-result", "—");
  resourceFeedback("ota", text);
  updateOTAApplyAvailability();
}

function updateOTAApplyAvailability() {
  const apply = $("#ota-apply");
  if (!apply) return;
  apply.disabled = !state.console?.capabilities?.updates || !state.ota?.receipt ||
    !$("#ota-confirm").checked || !/^[a-z0-9][a-z0-9_-]{7,63}$/.test($("#ota-device-id").value);
}

async function previewOTA() {
  resetOTAPreview("正在验证签名、镜像摘要与目标板卡……");
  const previewEpoch = state.otaPreviewEpoch;
  const file = $("#ota-file").files[0];
  if (!file) {
    resourceFeedback("ota", "请选择 .s3ota 签名固件包。", true);
    return;
  }
  const operation = authenticatedOperation();
  let document;
  try {
    document = new Uint8Array(await file.arrayBuffer());
    if (!operationIsCurrent(operation) || previewEpoch !== state.otaPreviewEpoch) return;
    const response = await request("/api/v1/ota/preview", {
      method: "POST", body: document,
      headers: { "Content-Type": "application/vnd.s3deck.ota+json" }, signal: operation.signal,
    });
    const preview = await response.json();
    if (!operationIsCurrent(operation) || previewEpoch !== state.otaPreviewEpoch) return;
    state.ota = preview;
    setText("#ota-version", preview.version);
    setText("#ota-board", preview.board);
    setText("#ota-size", `${formatNumber(preview.image_length)} B`);
    setText("#ota-key", `v${preview.signing_key_id}`);
    setText("#ota-digest", preview.image_sha256);
    setText("#ota-state", "签名校验通过");
    resourceFeedback("ota", "更新包有效；尚未向 Deck 发送任何固件字节。", false);
    updateOTAApplyAvailability();
  } catch (error) {
    if (!operationIsCurrent(operation) || previewEpoch !== state.otaPreviewEpoch) return;
    resetOTAPreview("更新包校验失败；没有向 Deck 写入任何内容。");
    resourceFeedback("ota", error.message, true);
  } finally {
    document?.fill(0);
  }
}

function renderOTAStatus(status) {
  const labels = { offering: "等待 Deck 接受", receiving: "正在写入非活动槽", ready_to_reboot: "已写入并准备重启", failed: "更新失败" };
  setText("#ota-state", labels[status.state] || "状态未知");
  const percent = status.image_length > 0 ? Math.floor(status.received_bytes * 100 / status.image_length) : 0;
  setText("#ota-progress", status.image_length > 0 ?
    `${formatNumber(status.received_bytes)} / ${formatNumber(status.image_length)} B（${percent}%）` : "—");
  setText("#ota-result", status.code);
}

async function pollOTAStatus(deviceID, operation) {
  const deadline = Date.now() + 6 * 60 * 1000;
  while (operationIsCurrent(operation) && Date.now() < deadline) {
    const response = await request(`/api/v1/ota/status?device_id=${encodeURIComponent(deviceID)}`, { signal: operation.signal });
    const status = await response.json();
    if (!operationIsCurrent(operation)) return;
    renderOTAStatus(status);
    if (status.state === "failed") {
      resourceFeedback("ota", "Deck 拒绝或中止了更新；当前活动固件保持不变。", true);
      return;
    }
    if (status.state === "ready_to_reboot") {
      resourceFeedback("ota", "镜像已写入。Deck 将重启并自行执行 60 秒健康确认；失败时自动回滚。", false);
      return;
    }
    await new Promise((resolve) => window.setTimeout(resolve, 500));
  }
  if (operationIsCurrent(operation)) resourceFeedback("ota", "更新状态等待超时；请保留 BOOT/UART 恢复路径并检查 Deck。", true);
}

async function applyOTA() {
  updateOTAApplyAvailability();
  if ($("#ota-apply").disabled || !state.ota) return;
  const operation = authenticatedOperation();
  const deviceID = $("#ota-device-id").value;
  const preview = state.ota;
  const confirmed = await showDialog({ eyebrow: "固件写入 · 设备将重启", title: `安装 ${preview.version}？`,
    paragraphs: [
      `目标：${deviceID} · ${preview.board} · ${formatNumber(preview.image_length)} B`,
      "只写入非活动 OTA 槽。签名、摘要、版本或首启健康检查失败都会拒绝或回滚；BOOT/UART 保持可用。",
    ], confirmText: "确认安装并重启", danger: true });
  if (!confirmed || !operationIsCurrent(operation) || state.ota !== preview) return;
  try {
    const response = await request("/api/v1/ota/apply", { method: "POST", signal: operation.signal,
      body: JSON.stringify({ receipt: preview.receipt, device_id: deviceID, confirm: true }) });
    const status = await response.json();
    if (!operationIsCurrent(operation)) return;
    state.ota = null;
    $("#ota-confirm").checked = false;
    updateOTAApplyAvailability();
    renderOTAStatus(status);
    resourceFeedback("ota", "更新事务已开始；不要断开 Deck 电源。", false);
    await pollOTAStatus(deviceID, operation);
  } catch (error) {
    if (operationIsCurrent(operation)) resourceFeedback("ota", error.message, true);
  }
}

const SERIAL_BROWSER_LOG_BYTES = 1 << 20;

function initializeSerialTerminal() {
  if (state.serial.terminal) return true;
  if (!window.Terminal || !window.FitAddon?.FitAddon || !window.SearchAddon?.SearchAddon ||
      !window.Unicode11Addon?.Unicode11Addon || !window.S3DeckSerialTerminal) {
    resourceFeedback("serial", "终端组件不可用，请重新加载 Companion。", true);
    return false;
  }
  const terminal = new window.Terminal({
    allowProposedApi: true,
    convertEol: false,
    cursorBlink: false,
    disableStdin: true,
    fontFamily: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
    fontSize: 13,
    linkHandler: null,
    screenReaderMode: true,
    scrollback: 5000,
    theme: { background: "#101914", foreground: "#d9e7e0", cursor: "#d9e7e0", selectionBackground: "#49675a" },
    windowOptions: {},
  });
  const fit = new window.FitAddon.FitAddon();
  const search = new window.SearchAddon.SearchAddon();
  const unicode = new window.Unicode11Addon.Unicode11Addon();
  terminal.loadAddon(fit);
  terminal.loadAddon(search);
  terminal.loadAddon(unicode);
  terminal.unicode.activeVersion = "11";
  terminal.open($("#serial-xterm"));
  state.serial.terminal = terminal;
  state.serial.fit = fit;
  state.serial.search = search;
  state.serial.resizeObserver = new ResizeObserver(() => {
    if (!$("#serial-xterm").hidden) fit.fit();
  });
  state.serial.resizeObserver.observe($("#serial-xterm"));
  window.requestAnimationFrame(() => fit.fit());
  return true;
}

function clearSerialOutput() {
  state.serial.byteChunks.forEach((frame) => frame.payload.fill(0));
  state.serial.byteChunks = [];
  state.serial.byteBytes = 0;
  $("#serial-byte-view").textContent = "";
  state.serial.terminal?.reset();
}

function rememberSerialFrame(frame) {
  const retained = { ...frame, payload: frame.payload.slice() };
  state.serial.byteChunks.push(retained);
  state.serial.byteBytes += retained.payload.length;
  while (state.serial.byteBytes > SERIAL_BROWSER_LOG_BYTES && state.serial.byteChunks.length > 1) {
    const removed = state.serial.byteChunks.shift();
    state.serial.byteBytes -= removed.payload.length;
    removed.payload.fill(0);
  }
}

function writeTerminalPayload(payload) {
  const owned = payload.slice();
  state.serial.terminal.write(owned, () => owned.fill(0));
}

function renderSerialOutput() {
  const textMode = state.serial.mode === "text";
  $("#serial-xterm").hidden = !textMode;
  $("#serial-byte-view").hidden = textMode;
  if (state.serial.paused) return;
  if (textMode) {
    state.serial.terminal.reset();
    state.serial.byteChunks.forEach((frame) => writeTerminalPayload(frame.payload));
    window.requestAnimationFrame(() => state.serial.fit?.fit());
    return;
  }
  const terminal = window.S3DeckSerialTerminal;
  const lines = state.serial.byteChunks.map((frame) => {
    const prefix = `#${frame.sequence.toString()} @${frame.monotonicMS.toString()}ms`;
    if (state.serial.mode === "hex") return `${prefix}  ${terminal.formatHex(frame.payload)}`;
    const mixed = terminal.formatMixed(frame.payload);
    return `${prefix}  ${mixed.hex}\n${" ".repeat(Math.min(prefix.length + 2, 32))}${mixed.text}`;
  });
  $("#serial-byte-view").textContent = lines.join("\n");
  $("#serial-byte-view").scrollTop = $("#serial-byte-view").scrollHeight;
}

function handleSerialFrame(frame) {
  if (frame.channel !== "target_rx") return;
  rememberSerialFrame(frame);
  if (state.serial.paused) return;
  if (state.serial.mode === "text") writeTerminalPayload(frame.payload);
  else renderSerialOutput();
}

function serialOwnerLabel(owner) {
  return { usb: "USB", web: "Web", transitioning: "切换中", unavailable: "不可用", none: "无" }[owner] || "未知";
}

function renderSerialState(next) {
  const previous = state.serial.status;
  if (previous?.sessionID && previous.sessionID !== "0" && next.sessionID !== previous.sessionID) clearSerialOutput();
  state.serial.status = next;
  const active = next.connected && next.sessionID !== "0" && next.serialState !== "disarmed";
  setText("#serial-session", active ? next.sessionID : "—");
  setText("#serial-state", active ? next.serialState : "不可用");
  setText("#serial-buffered", active ? `${formatNumber(next.bufferedBytes)} B / ${formatNumber(next.bufferedFrames)} 帧` : "—");
  setText("#serial-overwritten", active ? `${formatNumber(next.overwrittenBytes)} B` : "—");
  setText("#serial-observers", active ? formatNumber(next.observers) : "—");
  setText("#serial-owner", serialOwnerLabel(next.leaseOwner));
  setText("#serial-remaining", next.leaseOwner === "web" && next.leaseRemainingMS > 0 ?
    `${Math.ceil(next.leaseRemainingMS / 1000)} 秒` : "—");
  $("#serial-download").href = active ?
    `/api/v1/serial/download?session_id=${encodeURIComponent(next.sessionID)}` : "#";
  $("#serial-download").setAttribute("aria-disabled", String(!active));

  const leaseButton = $("#serial-lease");
  leaseButton.className = `button ${next.canTransmit ? "danger" : "primary"}`;
  if (!active) {
    setPill($("#serial-owner-pill"), "Serial 不可用", "danger");
    setText("#serial-owner-title", "当前没有可观察的 Serial Session");
    setText("#serial-owner-detail", "Deck 离线、串口未启用或浏览器正在重新连接。");
    leaseButton.textContent = "申请 Web TX";
    leaseButton.disabled = true;
    resourceFeedback("serial", "Serial Session 当前不可用；将自动重连。", true);
  } else if (next.canTransmit) {
    setPill($("#serial-owner-pill"), "本浏览器 · WEB TX", "warning");
    setText("#serial-owner-title", "本浏览器持有 Web TX Lease");
    setText("#serial-owner-detail", "USB 输入在释放、页面关闭或十分钟 Lease 到期前会被拒绝。");
    leaseButton.textContent = "释放 Web TX";
    leaseButton.disabled = false;
    resourceFeedback("serial", "Serial Session 已连接；发送仍受 256-byte 上限约束。");
  } else if (next.leaseOwner === "usb") {
    setPill($("#serial-owner-pill"), "USB TX Owner", "info");
    setText("#serial-owner-title", "串口输入由 USB 持有");
    setText("#serial-owner-detail", "多个浏览器可以观察；申请成功且 Deck 明确确认后才允许发送。");
    leaseButton.textContent = "申请 Web TX";
    leaseButton.disabled = false;
    resourceFeedback("serial");
  } else {
    setPill($("#serial-owner-pill"), serialOwnerLabel(next.leaseOwner), "warning");
    setText("#serial-owner-title", next.leaseOwner === "web" ? "另一个浏览器持有 Web TX" : "TX Owner 正在切换");
    setText("#serial-owner-detail", "本浏览器保持只读，直到收到 Deck 的精确 Owner 状态。");
    leaseButton.textContent = "申请 Web TX";
    leaseButton.disabled = true;
    resourceFeedback("serial");
  }
  updateSerialSendAvailability();
  renderSerialPresets();
  if (!next.connected) scheduleSerialReconnect();
}

function updateSerialSendAvailability() {
  const canSend = Boolean(state.serial.status?.canTransmit) && !state.serial.paused;
  $("#serial-input").disabled = !canSend;
  $("#serial-send").disabled = !canSend;
  $("#serial-input").placeholder = canSend ?
    (state.serial.mode === "hex" ? "48 65 6C 6C 6F" : "发送到 Target…") :
    (state.serial.paused ? "终端暂停时不能发送" : "申请 Web TX Lease 后可发送");
  setPill($("#serial-live-pill"), state.serial.paused ? "已暂停" :
    (state.serial.status?.connected ? "实时" : "离线"), state.serial.paused ? "warning" :
    (state.serial.status?.connected ? "success" : "neutral"));
}

function handleSerialResult(document) {
  if (document.type === "serial.tx.result" && document.accepted !== true) {
    toast("串口发送未完成", "Deck 未确认当前发送条件，请检查 TX Owner。", true);
  } else if (document.type === "serial.lease.result" && document.accepted !== true) {
    toast("无法取得 Web TX", "Lease 已被占用、Session 已变化或 Deck 当前不可用。", true);
  } else if (document.type === "serial.lease.heartbeat.result" && document.accepted !== true) {
    toast("Web TX Lease 已失效", "发送已立即禁用，等待 Deck 发布当前 Owner。", true);
  }
}

function scheduleSerialReconnect() {
  if (state.serial.reconnectTimer || !state.authenticated || pageConfig[state.page]?.domain !== "serial") return;
  state.serial.reconnectTimer = window.setTimeout(() => {
    state.serial.reconnectTimer = null;
    if (state.authenticated && pageConfig[state.page]?.domain === "serial") connectSerialObserver();
  }, 1500);
}

function connectSerialObserver() {
  if (state.serial.client || !state.authenticated) return;
  let client;
  client = window.S3DeckSerialTerminal.createClient({
    origin: location.origin,
    onFrame: handleSerialFrame,
    onState: (next) => {
      if (!next.connected && state.serial.client === client) state.serial.client = null;
      renderSerialState(next);
    },
    onResult: handleSerialResult,
    onError: () => resourceFeedback("serial", "Serial 数据未通过协议校验；正在重新连接。", true),
  });
  state.serial.client = client;
  try { client.connect(); }
  catch (_) {
    state.serial.client = null;
    renderSerialState({ connected: false, serialState: "unavailable", sessionID: "0", leaseID: "0",
      leaseOwner: "unavailable", canTransmit: false, deviceID: "", bufferedBytes: 0,
      bufferedFrames: 0, overwrittenBytes: 0, observers: 0, leaseRemainingMS: 0 });
  }
}

function startSerial() {
  if (!state.authenticated || !initializeSerialTerminal()) return;
  connectSerialObserver();
  if (!state.serial.heartbeatTimer) {
    state.serial.heartbeatTimer = window.setInterval(() => {
      if (!state.serial.status?.canTransmit) return;
      try { state.serial.client?.heartbeat(); }
      catch (_) { /* close/status callback removes transmit authority. */ }
    }, 30000);
  }
  window.requestAnimationFrame(() => state.serial.fit?.fit());
}

function stopSerial() {
  if (state.serial.reconnectTimer) window.clearTimeout(state.serial.reconnectTimer);
  if (state.serial.heartbeatTimer) window.clearInterval(state.serial.heartbeatTimer);
  state.serial.reconnectTimer = null;
  state.serial.heartbeatTimer = null;
  const client = state.serial.client;
  state.serial.client = null;
  client?.disconnect();
  state.serial.resizeObserver?.disconnect();
  state.serial.resizeObserver = null;
  state.serial.terminal?.dispose();
  state.serial.terminal = null;
  state.serial.fit = null;
  state.serial.search = null;
  state.serial.status = null;
  clearSerialOutput();
}

function setSerialMode(mode) {
  if (!["text", "hex", "mixed"].includes(mode)) return;
  state.serial.mode = mode;
  $$('[data-serial-mode]').forEach((button) => {
    const selected = button.dataset.serialMode === mode;
    button.classList.toggle("active", selected);
    button.setAttribute("aria-selected", String(selected));
  });
  renderSerialOutput();
  updateSerialSendAvailability();
}

function sendSerialPayload(payload) {
  if (state.serial.paused || !state.serial.status?.canTransmit) throw new Error("当前浏览器没有可用的 Web TX Lease");
  state.serial.client.send(payload);
}

function submitSerial(event) {
  event.preventDefault();
  try {
    const terminal = window.S3DeckSerialTerminal;
    const payload = state.serial.mode === "hex" ? terminal.parseHex($("#serial-input").value) :
      terminal.encodeText($("#serial-input").value, $("#serial-line-ending").value);
    sendSerialPayload(payload);
    payload.fill(0);
    $("#serial-input").value = "";
  } catch (error) { toast("无法发送串口数据", error.message, true); }
}

function changeSerialLease() {
  const client = state.serial.client;
  const status = state.serial.status;
  if (!client || !status?.connected || status.sessionID === "0") return;
  const releasing = status.canTransmit;
  $("#serial-lease").disabled = true;
  resourceFeedback("serial", releasing ? "正在请求 Deck 释放 Web TX……" : "正在等待 Deck 精确确认 Web TX……");
  try {
    if (releasing) client.release();
    else client.acquire();
  } catch (error) {
    renderSerialState(client.status());
    toast(releasing ? "无法释放 Web TX" : "无法申请 Web TX", error.message, true);
  }
}

function toggleSerialPause() {
  state.serial.paused = !state.serial.paused;
  const button = $("#serial-pause");
  button.textContent = state.serial.paused ? "▶" : "Ⅱ";
  button.setAttribute("aria-label", state.serial.paused ? "继续终端" : "暂停终端");
  button.setAttribute("aria-pressed", String(state.serial.paused));
  if (!state.serial.paused) renderSerialOutput();
  updateSerialSendAvailability();
  renderSerialPresets();
}

function searchSerialOutput() {
  const query = $("#serial-search").value;
  if (!query || state.serial.mode !== "text") return;
  if (!state.serial.search?.findNext(query)) {
    resourceFeedback("serial", "当前 Text / ANSI 缓冲中没有匹配内容。", true);
  }
}

function downloadSerialCapture(event) {
  const status = state.serial.status;
  if (!status?.connected || status.sessionID === "0") {
    event.preventDefault();
    toast("无法下载串口会话", "当前没有可下载的 Serial Session。", true);
  }
}

function serialPresetID() {
  const bytes = new Uint8Array(15);
  window.crypto.getRandomValues(bytes);
  return `p${Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("")}`;
}

async function loadSerialPresets() {
  if (!state.authenticated) return;
  resourceFeedback("serial-presets", "正在读取受保护的串口预设……");
  try {
    const document = await (await request("/api/v1/serial/presets")).json();
    if (!Array.isArray(document.presets)) throw new Error("串口预设响应格式无效。");
    state.serialPresets = document.presets;
    state.sync.serialPresets.lastSuccess = new Date().toISOString();
    state.sync.serialPresets.error = "";
    resourceFeedback("serial-presets");
    renderSerialPresets();
  } catch (error) {
    state.sync.serialPresets.error = error.message;
    resourceFeedback("serial-presets", "串口预设当前不可用；已禁用保存和发送。", true);
    clearSerialPresetEditor();
  }
}

function serialPresetsWritable() {
  return state.authenticated && Boolean(state.sync.serialPresets.lastSuccess) && !state.sync.serialPresets.error;
}

function renderSerialPresets() {
  const target = $("#serial-preset-list");
  if (!target) return;
  target.replaceChildren();
  if (!state.sync.serialPresets.lastSuccess) {
    target.append(emptyState("等待预设", "打开此页后从受保护配置读取。"));
  } else if (!state.serialPresets.length) {
    target.append(emptyState("没有串口预设", "新建 Text 或严格 HEX 命令。"));
  } else {
    state.serialPresets.forEach((preset) => {
      const row = element("div", `serial-preset-row${state.serialPresetEditing === preset.id ? " active" : ""}`);
      const open = element("button", "serial-preset-open");
      open.type = "button";
      open.append(element("strong", "", preset.name), element("span", "",
        window.S3DeckSerialTerminal.describePreset(preset)));
      open.addEventListener("click", () => editSerialPreset(preset.id));
      const send = element("button", "button small", "发送");
      send.type = "button";
      send.disabled = !state.serial.status?.canTransmit || state.serial.paused || !serialPresetsWritable();
      send.addEventListener("click", () => sendSerialPreset(preset.id));
      row.append(open, send);
      target.append(row);
    });
  }
  const writable = serialPresetsWritable();
  $("#new-serial-preset").disabled = !writable;
  $$("#serial-preset-form input, #serial-preset-form textarea, #serial-preset-form select, #serial-preset-form button").forEach((control) => {
    control.disabled = !writable;
  });
  updateSerialPresetMode();
  $("#delete-serial-preset").disabled = !writable || !state.serialPresetEditing;
}

function clearSerialPresetEditor() {
  rotateSerialPresetOperations();
  scrubSerialPresetEditor();
  renderSerialPresets();
}

function rotateSerialPresetOperations() {
  state.serialPresetOperationController.abort();
  state.serialPresetOperationController = new AbortController();
  state.serialPresetOperationEpoch += 1;
}

function beginSerialPresetOperation() {
  rotateSerialPresetOperations();
  return {
    epoch: state.serialPresetOperationEpoch,
    signal: state.serialPresetOperationController.signal,
  };
}

function serialPresetOperationIsCurrent(operation) {
  return state.authenticated && state.page === "serial-presets" && !operation.signal.aborted &&
    operation.epoch === state.serialPresetOperationEpoch;
}

function scrubSerialPresetEditor() {
  state.serialPresetEditing = "";
  $("#serial-preset-form").reset();
  $("#serial-preset-id").value = "";
  $("#serial-preset-payload").value = "";
  $("#serial-preset-mode").value = "text";
  $("#serial-preset-ending").value = "current";
  setText("#serial-preset-editor-note", "新建一个有界命令");
  updateSerialPresetMode();
}

async function fetchSerialPreset(id, signal) {
  const document = await (await request(
    `/api/v1/serial/presets/${encodeURIComponent(id)}`, { signal },
  )).json();
  if (!document || document.id !== id || typeof document.payload !== "string") {
    throw new Error("串口预设详情响应格式无效。");
  }
  return document;
}

async function editSerialPreset(id) {
  if (!serialPresetsWritable()) return;
  if (!state.serialPresets.some((candidate) => candidate.id === id)) return;
  const operation = beginSerialPresetOperation();
  scrubSerialPresetEditor();
  try {
    const preset = await fetchSerialPreset(id, operation.signal);
    if (!serialPresetOperationIsCurrent(operation) || !serialPresetsWritable() ||
        !state.serialPresets.some((candidate) => candidate.id === id)) {
      preset.payload = "";
      return;
    }
    state.serialPresetEditing = id;
    $("#serial-preset-id").value = id;
    $("#serial-preset-name").value = preset.name;
    $("#serial-preset-mode").value = preset.mode;
    $("#serial-preset-payload").value = preset.payload;
    $("#serial-preset-ending").value = preset.line_ending;
    preset.payload = "";
    setText("#serial-preset-editor-note", "已显式打开受保护的预设正文");
    updateSerialPresetMode();
    renderSerialPresets();
  } catch (error) {
    if (error?.name !== "AbortError" && serialPresetOperationIsCurrent(operation)) {
      toast("无法打开串口预设", error.message, true);
    }
  }
}

function updateSerialPresetMode() {
  const hexadecimal = $("#serial-preset-mode").value === "hex";
  const ending = $("#serial-preset-ending");
  if (hexadecimal) ending.value = "none";
  ending.disabled = hexadecimal || !serialPresetsWritable();
}

async function persistSerialPreset(preset) {
  if (!serialPresetsWritable()) throw new Error("串口预设不是最新状态，请先刷新。");
  try {
    await request(`/api/v1/serial/presets/${encodeURIComponent(preset.id)}`, {
      method: "PUT", body: JSON.stringify(preset),
    });
  } finally {
    preset.payload = "";
  }
  await loadSerialPresets();
}

async function saveSerialPreset(event) {
  event.preventDefault();
  try {
    if (!serialPresetsWritable()) throw new Error("串口预设不是最新状态，请先刷新。");
    const mode = $("#serial-preset-mode").value;
    let payload = $("#serial-preset-payload").value;
    let ending = $("#serial-preset-ending").value;
    if (mode === "hex") {
      const bytes = window.S3DeckSerialTerminal.parseHex(payload);
      payload = window.S3DeckSerialTerminal.formatHex(bytes);
      bytes.fill(0);
      ending = "none";
    } else {
      const checkEnding = ending === "current" ? "crlf" : ending;
      const bytes = window.S3DeckSerialTerminal.encodeText(payload, checkEnding);
      bytes.fill(0);
    }
    const id = state.serialPresetEditing || serialPresetID();
    const preset = { id, name: $("#serial-preset-name").value.trim(), mode, payload, line_ending: ending };
    if (!preset.name) throw new Error("请输入预设名称。");
    await persistSerialPreset(preset);
    clearSerialPresetEditor();
    toast("串口预设已保存", "预设不会绕过 Web TX Lease 或暂停状态。");
  } catch (error) { toast("无法保存串口预设", error.message, true); }
}

async function deleteSerialPreset() {
  if (!serialPresetsWritable() || !state.serialPresetEditing) return;
  const preset = state.serialPresets.find((candidate) => candidate.id === state.serialPresetEditing);
  if (!preset || !await showDialog({ eyebrow: "串口预设", title: `删除“${preset.name}”？`,
    paragraphs: ["此操作只删除预设，不会发送任何串口字节。"], confirmText: "删除预设", danger: true })) return;
  try {
    if (!serialPresetsWritable()) throw new Error("串口预设已变化，请刷新后重试。");
    await request(`/api/v1/serial/presets/${encodeURIComponent(preset.id)}`, { method: "DELETE" });
    await loadSerialPresets();
    clearSerialPresetEditor();
    toast("串口预设已删除", "没有串口字节被发送。");
  } catch (error) { toast("无法删除串口预设", error.message, true); }
}

async function sendSerialPreset(id) {
  if (!state.serialPresets.some((candidate) => candidate.id === id) || !serialPresetsWritable()) return;
  const operation = beginSerialPresetOperation();
  try {
    const preset = await fetchSerialPreset(id, operation.signal);
    try {
      if (!serialPresetOperationIsCurrent(operation) || !serialPresetsWritable() ||
          !state.serial.status?.canTransmit) {
        throw new Error("Web TX Lease 已失效。");
      }
      const ending = preset.line_ending === "current" ? $("#serial-line-ending").value : preset.line_ending;
      const payload = preset.mode === "hex" ? window.S3DeckSerialTerminal.parseHex(preset.payload) :
        window.S3DeckSerialTerminal.encodeText(preset.payload, ending);
      try {
        sendSerialPayload(payload);
      } finally {
        payload.fill(0);
      }
    } finally {
      preset.payload = "";
    }
  } catch (error) {
    if (error?.name !== "AbortError" && serialPresetOperationIsCurrent(operation)) {
      toast("无法发送串口预设", error.message, true);
    }
  }
}

function showDialog({ eyebrow, title, paragraphs = [], body = null, confirmText = "确认", danger = false, informational = false }) {
  const dialog = $("#app-dialog");
  setText("#dialog-eyebrow", eyebrow);
  setText("#dialog-title", title);
  const target = $("#dialog-body");
  target.replaceChildren();
  if (body) target.append(body);
  else paragraphs.forEach((paragraph) => target.append(element("p", "", paragraph)));
  $("#dialog-confirm").textContent = confirmText;
  $("#dialog-confirm").className = `button ${danger ? "danger" : "primary"}`;
  $("#dialog-cancel").hidden = informational;
  dialog.returnValue = "";
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener("close", () => {
      $("#dialog-cancel").hidden = false;
      resolve(dialog.returnValue === "confirm");
    }, { once: true });
  });
}

async function login(event) {
  event.preventDefault();
  const token = $("#management-token").value;
  loginFeedback("正在验证……");
  try {
    const response = await fetch("/api/v1/login", { method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json", "Origin": location.origin }, body: JSON.stringify({ token }) });
    $("#management-token").value = "";
    if (!response.ok) throw new Error(response.status === 429 ? "尝试次数过多，请稍后再试。" : "Token 无效，或登录已受到限制。");
    await authenticate((await response.json()).csrf_token);
  } catch (error) { loginFeedback(error.message, true); }
}

async function logout() {
  const csrf = state.csrf;
  showLogin("已退出本机控制台。");
  try {
    await fetch("/api/v1/logout", { method: "POST", cache: "no-store", credentials: "same-origin",
      headers: { "Content-Type": "application/json", "Origin": location.origin, "X-CSRF-Token": csrf }, body: "{}" });
  }
  catch (_) { /* 浏览器侧会话仍会立即丢弃。 */ }
}

async function resumeSession() {
  const response = await fetch("/api/v1/session/refresh", { method: "POST", cache: "no-store", credentials: "same-origin",
    headers: { "Origin": location.origin } });
  if (!response.ok) return false;
  await authenticate((await response.json()).csrf_token);
  return true;
}

async function start() {
  try {
    const response = await fetch("/api/v1/bootstrap", { cache: "no-store" });
    if (!response.ok) throw new Error("bootstrap");
    state.bootstrap = await response.json();
    setText("#login-address", state.bootstrap.lan_management_enabled ? location.host : "127.0.0.1");
    if (!await resumeSession()) showLogin();
  } catch (_) {
    $("#session-resume").hidden = true;
    loginFeedback("无法连接 Companion，请确认本机服务正在运行。", true);
  }
}

$("#login-form").addEventListener("submit", login);
$("#provider-form").addEventListener("submit", saveProvider);
$("#new-provider").addEventListener("click", () => openEditor());
$("#test-provider").addEventListener("click", () => state.editing && testProvider(state.editing));
$("#add-provider-header").addEventListener("click", () => addHeaderRow());
$("#refresh-console").addEventListener("click", () => loadConsole());
$("#refresh-history").addEventListener("click", loadHistory);
$("#history-provider").addEventListener("change", loadHistory);
$("#save-history").addEventListener("click", saveHistory);
$("#clear-history").addEventListener("click", clearHistory);
$("#serial-compose").addEventListener("submit", submitSerial);
$("#serial-lease").addEventListener("click", changeSerialLease);
$("#serial-pause").addEventListener("click", toggleSerialPause);
$("#serial-search-next").addEventListener("click", searchSerialOutput);
$("#serial-search").addEventListener("keydown", (event) => {
  if (event.key === "Enter") { event.preventDefault(); searchSerialOutput(); }
});
$("#serial-download").addEventListener("click", downloadSerialCapture);
$$('[data-serial-mode]').forEach((button) => button.addEventListener("click", () => setSerialMode(button.dataset.serialMode)));
$("#serial-preset-form").addEventListener("submit", saveSerialPreset);
$("#new-serial-preset").addEventListener("click", clearSerialPresetEditor);
$("#clear-serial-preset").addEventListener("click", clearSerialPresetEditor);
$("#delete-serial-preset").addEventListener("click", deleteSerialPreset);
$("#serial-preset-mode").addEventListener("change", updateSerialPresetMode);
$("#deck-next-provider").addEventListener("click", () => { state.deckIndex += 1; renderDeckPreview(); });
$("#export-backup").addEventListener("click", exportBackup);
$("#preview-backup").addEventListener("click", previewBackup);
$("#apply-backup").addEventListener("click", applyBackup);
$("#import-file").addEventListener("change", () => resetBackupPreview());
$("#import-passphrase").addEventListener("input", () => resetBackupPreview());
$("#backup-mode").addEventListener("change", () => resetBackupPreview("模式已更改，请重新预览。"));
$("#ota-file").addEventListener("change", () => resetOTAPreview("文件已更改，请重新校验。"));
$("#ota-preview").addEventListener("click", previewOTA);
$("#ota-confirm").addEventListener("change", updateOTAApplyAvailability);
$("#ota-device-id").addEventListener("input", updateOTAApplyAvailability);
$("#ota-apply").addEventListener("click", applyOTA);
$("#refresh-diagnostics").addEventListener("click", () => loadDiagnostics());
$("#export-diagnostics").addEventListener("click", exportDiagnostics);
$("#mobile-menu").addEventListener("click", openMobileNavigation);
$("#mobile-backdrop").addEventListener("click", closeMobileNavigation);
$$('[data-page]').forEach((button) => button.addEventListener("click", () => navigate(button.dataset.page)));
$$('[data-action="refresh"]').forEach((button) => button.addEventListener("click", () => loadConsole()));
$$('[data-action="logout"]').forEach((button) => button.addEventListener("click", logout));
$$('[data-action="pair-deck"], #pair-deck').forEach((button) => button.addEventListener("click", openPairingV2Dialog));
$("#pairing-v2-code-form").addEventListener("submit", confirmPairingV2);
$("#pairing-v2-rescan").addEventListener("click", scanPairingV2Candidates);
$("#pairing-v2-cancel").addEventListener("click", cancelPairingV2);
$("#pairing-v2-close").addEventListener("click", cancelPairingV2);
$("#pairing-v2-dialog").addEventListener("cancel", (event) => {
  event.preventDefault();
  cancelPairingV2();
});
$$('[data-provider-filter]').forEach((button) => button.addEventListener("click", () => {
  state.providerFilter = button.dataset.providerFilter;
  $$('[data-provider-filter]').forEach((candidate) => candidate.classList.toggle("active", candidate === button));
  renderProviders();
}));
window.addEventListener("hashchange", () => navigate(location.hash.slice(1) || "overview", false));
document.addEventListener("visibilitychange", () => { if (!document.hidden && state.authenticated) loadConsole(true); });

start();
