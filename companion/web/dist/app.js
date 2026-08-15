"use strict";

const state = {
  csrf: "", bootstrap: null, console: null, providers: [], templates: [], providerStates: [], history: [],
  editing: "", backup: null, page: "overview", providerFilter: "all", deckIndex: 0,
  authenticated: false, refreshTimer: null,
  sync: {
    console: { lastSuccess: "", error: "" },
    providers: { lastSuccess: "", error: "" },
    history: { lastSuccess: "", error: "" },
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
  ["invalid Provider history query", "历史记录筛选条件无效。"], ["invalid Provider history request", "历史记录请求无效。"],
  ["Provider history settings unavailable", "无法保存历史记录设置。"], ["malformed Provider history settings", "历史记录设置格式无效。"],
  ["Provider management unavailable", "Provider 管理当前不可用。"], ["malformed Provider request", "Provider 配置格式无效。"],
  ["invalid Provider request", "Provider 配置未通过校验。"], ["Provider configuration changed", "Provider 配置已在其他位置更新，请刷新后重试。"],
  ["Provider operation unavailable", "Provider 操作暂时不可用。"],
]);

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
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  if (method !== "GET") {
    headers.set("Origin", location.origin);
    if (state.csrf) headers.set("X-CSRF-Token", state.csrf);
  }
  let response;
  try {
    response = await fetch(path, { cache: "no-store", credentials: "same-origin", ...options, method, headers });
  } catch (_) {
    throw new Error("无法连接 Companion，请检查本机服务后重试。");
  }
  if (!response.ok) {
    const detail = await response.text();
    if (response.status === 401 && state.authenticated) showLogin("本机会话已过期，请重新解锁。", true);
    throw new Error(translatedError(detail, response.status));
  }
  return response;
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
  const config = pageConfig[page] || pageConfig.overview;
  page = pageConfig[page] ? page : "overview";
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
  if (updateHash && location.hash !== `#${page}`) history.pushState(null, "", `#${page}`);
  $("#main-content").focus({ preventScroll: true });
  window.scrollTo({ top: 0, behavior: "auto" });
}

function scrubSensitiveState() {
  state.console = null;
  state.providers = [];
  state.templates = [];
  state.providerStates = [];
  state.history = [];
  state.editing = "";
  state.backup = null;
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
  const dialog = $("#app-dialog");
  if (dialog.open) dialog.close();
  $("#dialog-body").replaceChildren();
  message();
  ["console", "providers", "history"].forEach((resource) => resourceFeedback(resource));
  $("#toast-region").replaceChildren();
  $("#toast-alert-region").replaceChildren();
}

function showLogin(detail = "", isError = false) {
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
  state.csrf = csrf;
  state.authenticated = true;
  $("#login-view").hidden = true;
  $("#application").hidden = false;
  loginFeedback();
  await Promise.all([loadConsole(), loadProviders()]);
  if (!state.authenticated) return;
  navigate(location.hash.slice(1) || "overview", false);
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
  setPill($("#deck-badge"), `Deck ${runtime.connected_decks ?? 0}`, runtime.connected_decks > 0 ? "success" : "neutral");
  setText("#metric-decks", runtime.connected_decks ?? 0);
  setText("#metric-providers", providers.length ? `${healthy}/${providers.length}` : "0");
  setText("#metric-provider-detail", providers.length ? `${providers.length - healthy} 个需要关注` : "尚无规范化快照");
  setText("#metric-sessions", sessions.length);
  setText("#metric-history", runtime.history_available ? (runtime.history_enabled ? "已开启" : "已关闭") : "不可用");
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
      setPill(pill, sessionStateLabel(session.state), session.state === "failed" ? "danger" : session.state === "running" ? "success" : "neutral");
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
    [session.display_name || "未命名会话", sessionStateLabel(session.state), confidenceLabel(session.confidence),
      formatDuration(session.duration_seconds), formatTokens(session.turn_tokens), context, sourceLabel(session.source)]
      .forEach((value) => row.append(element("td", "", value)));
    target.append(row);
  });
}

function renderRuntimeSurfaces(runtime, providers) {
  const label = runtimeLabel(runtime.state);
  const exposure = runtime.lan_management_enabled ? "局域网" : "仅本机";
  const healthy = providers.filter((provider) => provider.status === "ok").length;
  setText("#devices-count", runtime.connected_decks ?? 0);
  setPill($("#devices-count-pill"), `${runtime.connected_decks ?? 0} 个在线`, runtime.connected_decks > 0 ? "success" : "neutral");
  setText("#management-address", runtime.management_address);
  setText("#device-hub-address", runtime.device_hub_address);
  setText("#network-runtime-state", label);
  setText("#network-decks", runtime.connected_decks ?? 0);
  setPill($("#network-exposure"), exposure, runtime.lan_management_enabled ? "warning" : "success");
  setText("#system-runtime", label);
  setText("#system-version", runtime.version);
  setText("#system-exposure", exposure);
  setText("#system-history", runtime.history_available ? (runtime.history_enabled ? "已开启" : "已关闭") : "不可用");
  const healthyText = state.sync.console.error ? "已过期" :
    runtime.state === "ready" && healthy === providers.length ? "正常" : "需关注";
  setPill($("#diagnostics-health"), healthyText, healthyText === "正常" ? "success" : "warning");
  setText("#diagnostics-runtime", label);
  setText("#diagnostics-providers", providers.length ? `${healthy}/${providers.length} 正常` : "尚无快照");
  setText("#diagnostics-device-link", `${runtime.connected_decks ?? 0} 个 Deck 已连接`);
  setText("#diagnostics-error", runtime.last_error ? "运行时报告了错误（详细信息仅保留在本机日志）" : "无");
  setText("#tray-runtime", `Companion ${label}`);
  setText("#tray-decks", `Deck ${runtime.connected_decks ?? 0}`);
  setText("#tray-clock", formatTime(new Date().toISOString(), false));
}

function updateCapabilityControls() {
  const capabilities = state.console?.capabilities || {};
  $$('[data-action="pair-deck"], #pair-deck').forEach((button) => { button.disabled = !capabilities.pairing; });
  $("#new-provider").disabled = !capabilities.provider_management;
  $("#history-enabled").disabled = !capabilities.history;
  $("#save-history").disabled = !capabilities.history;
  $("#export-backup").disabled = !capabilities.backup;
  $("#preview-backup").disabled = !capabilities.backup;
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
  } catch (error) {
    state.sync.providers.error = error.message;
    const lastSuccess = state.sync.providers.lastSuccess;
    resourceFeedback("providers", lastSuccess ?
      `无法刷新 Provider；保留 ${formatTime(lastSuccess, false)} 的最后有效配置。` :
      "Provider 配置当前不可用，请重试。", true);
    if (!lastSuccess) $("#provider-list").replaceChildren(emptyState("Provider 当前不可用", "请检查 Companion 后重试。"));
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
    setPill(pill, providerStatusLabel(status), statusClass(status));
    header.append(identity, pill);
    const metric = element("div", "provider-card-metric");
    metric.append(element("strong", "", runtime?.balance ? formatMoney(runtime.balance) : quotaSummary(runtime)),
      element("span", "", runtime ? `${confidenceLabel(runtime.confidence)} · ${formatRelative(runtime.updated_at)}` : "等待首次采集"));
    const actions = element("div", "provider-card-actions");
    [["上移", () => moveProvider(index, -1), index === 0], ["下移", () => moveProvider(index, 1), index === state.providers.length - 1],
      ["测试", () => testProvider(provider.id), false], ["编辑", () => openEditor(provider), false],
      ["删除", () => deleteProvider(provider), false, "danger"]].forEach(([label, action, disabled, kind]) => {
      const button = element("button", `button small${kind ? ` ${kind}` : ""}`, label);
      button.type = "button";
      button.disabled = disabled;
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
  $("#test-provider").disabled = !saved;
  $("#test-preview-card").hidden = true;
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
  const ordered = state.providers.map((provider) => provider.id);
  [ordered[index], ordered[index + delta]] = [ordered[index + delta], ordered[index]];
  try {
    await request("/api/v1/providers/order", { method: "PUT", body: JSON.stringify({ provider_ids: ordered }) });
    toast("顺序已更新", "Deck 将按新的 Provider 顺序显示。");
    await loadProviders();
  } catch (error) { toast("无法更新顺序", error.message, true); }
}

async function deleteProvider(provider) {
  const confirmed = await showDialog({ eyebrow: "不可撤销操作", title: `删除 ${provider.display_name}？`,
    paragraphs: ["将删除 Provider 定义及其凭据库引用。历史记录不会自动删除。"], confirmText: "确认删除", danger: true });
  if (!confirmed) return;
  try {
    await request(`/api/v1/providers/${encodeURIComponent(provider.id)}`, { method: "DELETE" });
    toast("Provider 已删除", `${provider.display_name} 已从采集顺序中移除。`);
    await Promise.all([loadProviders(), loadConsole(true)]);
  } catch (error) { toast("无法删除 Provider", error.message, true); }
}

async function testProvider(id) {
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
  status.append(element("span", "", "S3 RLCD"), element("span", "", `Deck ${state.console.runtime?.connected_decks ?? 0}`),
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

async function issuePairingCode() {
  try {
    const advertisedAddress = state.console?.runtime?.device_hub_advertised_address;
    if (!advertisedAddress) {
      throw new Error("Device Hub 尚无可达公告地址；请先配置 --device-hub-advertised-address IP:port。");
    }
    const issued = await (await request("/api/v1/pairing/codes", { method: "POST", body: "{}" })).json();
    const body = element("div");
    body.append(element("p", "", "在 Deck 的 Setup 页面输入以下一次性配对码："), element("div", "pairing-code", issued.code),
      element("p", "muted", `Device Hub：${advertisedAddress}`),
      element("p", "muted", `有效期至：${formatTime(issued.expires_at)}`));
    await showDialog({ eyebrow: "Deck Pairing", title: "一次性配对码", body, confirmText: "完成", informational: true });
  } catch (error) { toast("无法生成配对码", error.message, true); }
}

async function exportBackup() {
  const passphrase = $("#export-passphrase").value;
  if (!passphrase) { toast("需要备份口令", "请先输入用于加密归档的强口令。", true); return; }
  try {
    const response = await request("/api/v1/backups/export", { method: "POST", body: JSON.stringify({ passphrase }) });
    const url = URL.createObjectURL(await response.blob());
    const link = element("a");
    link.href = url;
    link.download = "s3-rlcd-deck-backup.age";
    link.click();
    URL.revokeObjectURL(url);
    $("#export-passphrase").value = "";
    toast("备份已生成", "加密归档已交给浏览器下载。请将口令分开保存。");
  } catch (error) { toast("无法生成备份", error.message, true); }
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
  try {
    const payload = await backupPayload();
    const preview = await (await request("/api/v1/backups/preview", { method: "POST", body: JSON.stringify(payload) })).json();
    state.backup = { ...payload, preview_id: preview.preview_id, decisions: {} };
    renderBackupConflicts(preview);
    $("#backup-preview").textContent = JSON.stringify(preview, null, 2);
  } catch (error) { resetBackupPreview("预览失败。请检查归档和口令。"); toast("无法预览备份", error.message, true); }
}

async function applyBackup() {
  if (!state.backup) return;
  const replace = state.backup.mode === "replace";
  const confirmed = await showDialog({ eyebrow: replace ? "完整替换 · 破坏性操作" : "事务导入",
    title: replace ? "替换当前可迁移配置？" : "应用已预览的备份？",
    paragraphs: replace ? [
      "这会以归档中的 Provider、显示顺序和可迁移设置替换当前配置；Pairing 信任、历史和 Web 会话不受影响。",
      "请先导出当前配置作为恢复点。导入失败会保持当前配置；导入成功后可重新导入该恢复点撤销。",
    ] : ["Companion 将按当前模式和逐项决定写入可恢复配置。Pairing 信任、历史和 Web 会话不会导入。"],
    confirmText: replace ? "确认替换当前配置" : "确认导入", danger: replace });
  if (!confirmed) return;
  try {
    const result = await (await request("/api/v1/backups/import", { method: "POST", body: JSON.stringify(state.backup) })).json();
    state.backup = null;
    $("#import-passphrase").value = "";
    $("#apply-backup").disabled = true;
    $("#backup-conflicts").replaceChildren();
    toast("导入完成", result.restart_required ? "需要重启 Companion 才能应用全部设置。" : "配置已完成事务写入。");
    await Promise.all([loadProviders(), loadConsole(true)]);
  } catch (error) { toast("无法导入备份", error.message, true); }
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
  try { await request("/api/v1/logout", { method: "POST", body: "{}" }); }
  catch (_) { /* 浏览器侧会话仍会立即丢弃。 */ }
  showLogin("已退出本机控制台。");
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
$("#deck-next-provider").addEventListener("click", () => { state.deckIndex += 1; renderDeckPreview(); });
$("#export-backup").addEventListener("click", exportBackup);
$("#preview-backup").addEventListener("click", previewBackup);
$("#apply-backup").addEventListener("click", applyBackup);
$("#import-file").addEventListener("change", () => resetBackupPreview());
$("#import-passphrase").addEventListener("input", () => resetBackupPreview());
$("#backup-mode").addEventListener("change", () => resetBackupPreview("模式已更改，请重新预览。"));
$("#mobile-menu").addEventListener("click", openMobileNavigation);
$("#mobile-backdrop").addEventListener("click", closeMobileNavigation);
$$('[data-page]').forEach((button) => button.addEventListener("click", () => navigate(button.dataset.page)));
$$('[data-action="refresh"]').forEach((button) => button.addEventListener("click", () => loadConsole()));
$$('[data-action="logout"]').forEach((button) => button.addEventListener("click", logout));
$$('[data-action="pair-deck"], #pair-deck').forEach((button) => button.addEventListener("click", issuePairingCode));
$$('[data-provider-filter]').forEach((button) => button.addEventListener("click", () => {
  state.providerFilter = button.dataset.providerFilter;
  $$('[data-provider-filter]').forEach((candidate) => candidate.classList.toggle("active", candidate === button));
  renderProviders();
}));
window.addEventListener("hashchange", () => navigate(location.hash.slice(1) || "overview", false));
document.addEventListener("visibilitychange", () => { if (!document.hidden && state.authenticated) loadConsole(true); });

start();
