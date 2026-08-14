"use strict";

const state = { csrf: "", bootstrap: null, providers: [], templates: [], providerStates: [], editing: "", backup: null };
const $ = (selector) => document.querySelector(selector);

function message(text, success = false) {
  const target = $("#global-message");
  target.textContent = text;
  target.classList.toggle("success", success);
}

async function request(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  if (options.method && options.method !== "GET") {
    headers.set("Origin", location.origin);
    headers.set("X-CSRF-Token", state.csrf);
  }
  const response = await fetch(path, { cache: "no-store", credentials: "same-origin", ...options, headers });
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `请求失败 (${response.status})`);
  }
  return response;
}

function bytesBase64(value) {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function navigate(page) {
  document.querySelectorAll(".nav-item").forEach((button) => button.classList.toggle("active", button.dataset.page === page));
  document.querySelectorAll(".page").forEach((section) => section.classList.toggle("active", section.id === `page-${page}`));
  $("#page-title").textContent = { overview: "概览", providers: "AI Providers", history: "历史记录", backups: "备份与恢复" }[page];
  if (page === "providers") loadProviders();
  if (page === "history") loadHistory();
}

async function loadStatus() {
  const runtime = await (await request("/api/v1/status")).json();
  $("#runtime-version").textContent = runtime.version;
  $("#runtime-state").textContent = runtime.state.toUpperCase();
  $("#deck-count").textContent = String(runtime.connected_decks);
  $("#history-state").textContent = runtime.history_enabled ? "ON" : "OFF";
  $("#exposure-badge").textContent = runtime.lan_management_enabled ? "LAN EXPOSED" : "LOOPBACK";
}

async function loadProviders() {
  try {
    const document = await (await request("/api/v1/providers")).json();
    state.providers = document.providers || [];
    state.templates = document.templates || [];
    state.providerStates = document.states || [];
    $("#provider-count").textContent = String(state.providers.length);
    renderProviders();
    renderTemplates();
  } catch (error) {
    message(error.message);
  }
}

function renderProviders() {
  const list = $("#provider-list");
  list.replaceChildren();
  if (state.providers.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty";
    empty.textContent = "尚未配置额外 Provider。Deck 会显示配置提示页。";
    list.append(empty);
    return;
  }
  state.providers.forEach((provider, index) => {
    const runtime = state.providerStates.find((candidate) => candidate.id === provider.id);
    const card = document.createElement("article");
    card.className = "provider-card";
    const identity = document.createElement("div");
    const name = document.createElement("h3");
    name.textContent = provider.display_name;
    const meta = document.createElement("div");
    meta.className = "provider-meta";
    meta.textContent = `${provider.id} · ${provider.refresh_minutes} min${provider.experimental ? " · EXPERIMENTAL" : ""}`;
    identity.append(name, meta);
    const status = document.createElement("span");
    status.className = `provider-state ${(runtime?.status || "unavailable").toLowerCase()}`;
    status.textContent = (runtime?.status || "UNAVAILABLE").toUpperCase();
    const actions = document.createElement("div");
    actions.className = "provider-actions";
    const commands = [
      ["↑", () => moveProvider(index, -1), index === 0],
      ["↓", () => moveProvider(index, 1), index === state.providers.length - 1],
      ["测试", () => testProvider(provider.id), false],
      ["编辑", () => openEditor(provider), false],
      ["删除", () => deleteProvider(provider.id), false],
    ];
    for (const [label, action, disabled] of commands) {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = label;
      button.disabled = disabled;
      button.addEventListener("click", action);
      actions.append(button);
    }
    card.append(identity, status, actions);
    list.append(card);
  });
}

function renderTemplates() {
  const target = $("#template-list");
  target.replaceChildren();
  state.templates.forEach((template) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "template-chip";
    button.textContent = `${template.display_name} 模板`;
    button.addEventListener("click", () => openEditor(template));
    target.append(button);
  });
}

function addHeaderRow(header = {}, originalIndex = -1) {
  const row = document.createElement("div");
  row.className = "header-row";
  row.dataset.originalIndex = String(originalIndex);
  row.dataset.secretConfigured = header.secret_configured ? "true" : "false";

  const nameLabel = document.createElement("label");
  nameLabel.textContent = "Header";
  const name = document.createElement("input");
  name.className = "provider-header-name";
  name.required = true;
  name.value = header.name || "Authorization";
  nameLabel.append(name);

  const prefixLabel = document.createElement("label");
  prefixLabel.textContent = "值前缀";
  const prefix = document.createElement("input");
  prefix.className = "provider-header-prefix";
  prefix.value = header.prefix ?? "Bearer ";
  prefixLabel.append(prefix);

  const secretLabel = document.createElement("label");
  secretLabel.textContent = "API Key / Token";
  const secret = document.createElement("input");
  secret.className = "provider-header-secret";
  secret.type = "password";
  secret.autocomplete = "new-password";
  const help = document.createElement("small");
  help.textContent = header.secret_configured ? "留空保留现有凭据" : "需要新的凭据";
  secretLabel.append(secret, help);

  const remove = document.createElement("button");
  remove.className = "danger remove-header";
  remove.type = "button";
  remove.textContent = "移除";
  remove.addEventListener("click", () => row.remove());
  row.append(nameLabel, prefixLabel, secretLabel, remove);
  $("#provider-headers").append(row);
}

function renderHeaderRows(headers, suggestDefault) {
  $("#provider-headers").replaceChildren();
  const values = headers?.length ? headers : suggestDefault ? [{ name: "Authorization", prefix: "Bearer " }] : [];
  values.forEach((header, index) => addHeaderRow(header, headers?.length ? index : -1));
}

function openEditor(provider = null) {
  state.editing = provider && state.providers.some((item) => item.id === provider.id) ? provider.id : "";
  const value = provider || { request: { method: "GET", headers: [] }, mapping: {}, refresh_minutes: 5 };
  $("#editor-title").textContent = state.editing ? `编辑 ${value.display_name}` : "新增 Provider";
  $("#provider-id").value = value.id || "";
  $("#provider-id").disabled = Boolean(state.editing);
  $("#provider-name").value = value.display_name || "";
  $("#provider-method").value = value.request?.method || "GET";
  $("#provider-refresh").value = value.refresh_minutes || 5;
  $("#provider-url").value = value.request?.url || "";
  renderHeaderRows(value.request?.headers || [], !state.editing);
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
  $("#provider-editor").hidden = false;
  const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
  $("#provider-editor").scrollIntoView({ behavior: reducedMotion ? "auto" : "smooth", block: "start" });
}

function definitionFromForm() {
  const bodyText = $("#provider-body").value.trim();
  const method = $("#provider-method").value;
  const resetPath = $("#map-reset").value.trim();
  const resetFormat = $("#map-reset-format").value;
  if (Boolean(resetPath) !== Boolean(resetFormat)) {
    throw new Error("Reset JSONPath 与 Reset 格式必须同时配置");
  }
  const requestDefinition = { method, url: $("#provider-url").value.trim(), headers: [] };
  document.querySelectorAll("#provider-headers .header-row").forEach((row) => {
    requestDefinition.headers.push({
      name: row.querySelector(".provider-header-name").value.trim(),
      prefix: row.querySelector(".provider-header-prefix").value,
    });
  });
  if (bodyText) requestDefinition.body = JSON.parse(bodyText);
  return {
    id: $("#provider-id").value.trim(),
    display_name: $("#provider-name").value.trim(),
    experimental: $("#provider-experimental").checked,
    request: requestDefinition,
    mapping: {
      balance_path: $("#map-balance").value.trim(), currency_path: $("#map-currency").value.trim(),
      used_path: $("#map-used").value.trim(), total_path: $("#map-total").value.trim(),
      reset_path: resetPath, reset_format: resetFormat,
      window_name: $("#map-window").value.trim(),
      fixed_currency: $("#map-fixed-currency").value.trim().toUpperCase(),
      balance_divisor: Number($("#map-divisor").value || 1),
    },
    refresh_minutes: Number($("#provider-refresh").value), request_timeout_seconds: 10,
    maximum_response_bytes: 262144,
  };
}

async function saveProvider(event) {
  event.preventDefault();
  try {
    const definition = definitionFromForm();
    const current = state.providers.find((provider) => provider.id === state.editing);
    const secrets = [];
    const keep = [];
    document.querySelectorAll("#provider-headers .header-row").forEach((row, index) => {
      const secret = row.querySelector(".provider-header-secret").value;
      if (secret) {
        secrets.push({ header_index: index, value: bytesBase64(secret) });
        return;
      }
      if (row.dataset.secretConfigured !== "true") return;
      const originalIndex = Number(row.dataset.originalIndex);
      const original = current?.request?.headers?.[originalIndex];
      const next = definition.request.headers[index];
      if (originalIndex !== index || !original || original.name !== next.name ||
          (original.prefix || "") !== (next.prefix || "")) {
        throw new Error("已配置凭据的 Header 改名、改前缀或移动后必须重新输入凭据");
      }
      keep.push(index);
    });
    const path = state.editing ? `/api/v1/providers/${encodeURIComponent(state.editing)}` : "/api/v1/providers";
    await request(path, { method: state.editing ? "PUT" : "POST", body: JSON.stringify({ definition, secrets, keep_existing: keep }) });
    document.querySelectorAll(".provider-header-secret").forEach((input) => { input.value = ""; });
    $("#provider-editor").hidden = true;
    message("Provider 已保存并开始动态同步。", true);
    await loadProviders();
  } catch (error) {
    message(error.message);
  }
}

async function moveProvider(index, delta) {
  const ordered = state.providers.map((provider) => provider.id);
  [ordered[index], ordered[index + delta]] = [ordered[index + delta], ordered[index]];
  try {
    await request("/api/v1/providers/order", { method: "PUT", body: JSON.stringify({ provider_ids: ordered }) });
    message("Deck 页面顺序已更新。", true);
    await loadProviders();
  } catch (error) { message(error.message); }
}

async function deleteProvider(id) {
  if (!confirm("删除这个 Provider 及其 Vault 凭据？")) return;
  try {
    await request(`/api/v1/providers/${encodeURIComponent(id)}`, { method: "DELETE", body: undefined });
    message("Provider 已删除。", true);
    await loadProviders();
  } catch (error) { message(error.message); }
}

async function testProvider(id) {
  message("正在执行安全 Test Request…");
  try {
    const result = await (await request(`/api/v1/providers/${encodeURIComponent(id)}/test`, { method: "POST", body: "{}" })).json();
    const diagnostic = result.preview?.diagnostic || {};
    message(result.ok ? `测试通过 · HTTP ${diagnostic.http_status} · ${diagnostic.latency_millis} ms` : `测试失败 · ${diagnostic.error_code || "unavailable"}`, result.ok);
  } catch (error) { message(error.message); }
}

async function loadHistory() {
  try {
    const settings = await (await request("/api/v1/history/settings")).json();
    $("#history-enabled").checked = settings.enabled;
    const history = await (await request("/api/v1/history?limit=200")).json();
    const table = $("#history-table");
    table.replaceChildren();
    (history.records || []).forEach((record) => {
      const row = document.createElement("tr");
      const balance = record.balance ? `${(record.balance.amount_micros / 1000000).toFixed(2)} ${record.balance.currency}` : "—";
      const tokens = record.tokens?.total ?? "—";
      [record.observed_at_utc, record.provider_id, record.status, balance, tokens].forEach((value) => {
        const cell = document.createElement("td"); cell.textContent = String(value); row.append(cell);
      });
      table.append(row);
    });
  } catch (error) { message(error.message); }
}

async function saveHistory() {
  try {
    await request("/api/v1/history/settings", { method: "PUT", body: JSON.stringify({ enabled: $("#history-enabled").checked }) });
    message("历史记录设置已保存。", true); await loadStatus();
  } catch (error) { message(error.message); }
}

async function clearHistory() {
  if (!confirm("永久清空本地 Provider 历史？")) return;
  try { await request("/api/v1/history", { method: "DELETE" }); message("历史记录已清空。", true); await loadHistory(); }
  catch (error) { message(error.message); }
}

async function exportBackup() {
  const passphrase = $("#export-passphrase").value;
  try {
    const response = await request("/api/v1/backups/export", { method: "POST", body: JSON.stringify({ passphrase }) });
    const url = URL.createObjectURL(await response.blob());
    const link = document.createElement("a"); link.href = url; link.download = "s3-rlcd-deck-backup.age"; link.click();
    URL.revokeObjectURL(url); $("#export-passphrase").value = ""; message("加密备份已生成。", true);
  } catch (error) { message(error.message); }
}

async function backupPayload() {
  const file = $("#import-file").files[0];
  if (!file) throw new Error("请选择加密归档");
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = ""; for (const byte of bytes) binary += String.fromCharCode(byte);
  return {
    archive: btoa(binary),
    passphrase: $("#import-passphrase").value,
    mode: $("#backup-mode").value,
  };
}

function resetBackupPreview(text = "输入已更改，请重新预览") {
  state.backup = null;
  $("#backup-conflicts").replaceChildren();
  $("#backup-preview").textContent = text;
  $("#apply-backup").disabled = true;
}

function renderBackupConflicts(preview) {
  const target = $("#backup-conflicts");
  target.replaceChildren();
  const required = (preview.conflicts || []).filter((conflict) => conflict.decision_required);
  for (const conflict of required) {
    const label = document.createElement("label");
    label.textContent = `${conflict.kind}: ${conflict.current_label} ↔ ${conflict.backup_label}`;
    const select = document.createElement("select");
    select.dataset.conflictKey = conflict.key;
    for (const [value, text] of [
      ["", "请选择"],
      ["keep_current", "保留当前"],
      ["use_backup", "使用备份"],
    ]) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = text;
      select.append(option);
    }
    select.addEventListener("change", () => {
      if (select.value) state.backup.decisions[conflict.key] = select.value;
      else delete state.backup.decisions[conflict.key];
      $("#apply-backup").disabled = Object.keys(state.backup.decisions).length !== required.length;
    });
    label.append(select);
    target.append(label);
  }
  $("#apply-backup").disabled = required.length !== 0;
}

async function previewBackup() {
  resetBackupPreview("正在验证加密归档…");
  try {
    const payload = await backupPayload();
    const preview = await (await request("/api/v1/backups/preview", { method: "POST", body: JSON.stringify(payload) })).json();
    state.backup = { ...payload, preview_id: preview.preview_id, decisions: {} };
    renderBackupConflicts(preview);
    $("#backup-preview").textContent = JSON.stringify(preview, null, 2);
  } catch (error) { message(error.message); }
}

async function applyBackup() {
  if (!state.backup || !confirm("按预览和逐项决定导入可恢复配置？")) return;
  try {
    const result = await (await request("/api/v1/backups/import", { method: "POST", body: JSON.stringify(state.backup) })).json();
    state.backup = null; $("#import-passphrase").value = ""; $("#apply-backup").disabled = true;
    $("#backup-conflicts").replaceChildren();
    message(result.restart_required ? "导入完成；请重启 Companion。" : "导入完成。", true);
  } catch (error) { message(error.message); }
}

async function login(event) {
  event.preventDefault();
  const token = $("#management-token").value;
  try {
    const response = await fetch("/api/v1/login", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "Origin": location.origin }, body: JSON.stringify({ token }) });
    $("#management-token").value = "";
    if (!response.ok) throw new Error("Token 无效或登录受限");
    state.csrf = (await response.json()).csrf_token;
    $("#login").hidden = true; $("#application").hidden = false;
    await Promise.all([loadStatus(), loadProviders()]);
  } catch (error) { $("#login-message").textContent = error.message; }
}

async function logout() {
  try { await request("/api/v1/logout", { method: "POST", body: "{}" }); } catch (_) { /* local session still discarded */ }
  state.csrf = ""; location.reload();
}

async function start() {
  try {
    state.bootstrap = await (await fetch("/api/v1/bootstrap", { cache: "no-store" })).json();
    $("#exposure-badge").textContent = state.bootstrap.lan_management_enabled ? "LAN EXPOSED" : "LOOPBACK";
  } catch (_) { $("#login-message").textContent = "Companion 不可用"; }
}

$("#login-form").addEventListener("submit", login);
$("#logout").addEventListener("click", logout);
document.querySelectorAll(".nav-item").forEach((button) => button.addEventListener("click", () => navigate(button.dataset.page)));
$("#new-provider").addEventListener("click", () => openEditor());
$("#close-editor").addEventListener("click", () => { $("#provider-editor").hidden = true; });
$("#provider-form").addEventListener("submit", saveProvider);
$("#add-provider-header").addEventListener("click", () => addHeaderRow());
$("#refresh-history").addEventListener("click", loadHistory);
$("#save-history").addEventListener("click", saveHistory);
$("#clear-history").addEventListener("click", clearHistory);
$("#export-backup").addEventListener("click", exportBackup);
$("#preview-backup").addEventListener("click", previewBackup);
$("#apply-backup").addEventListener("click", applyBackup);
$("#import-file").addEventListener("change", () => resetBackupPreview());
$("#import-passphrase").addEventListener("input", () => resetBackupPreview());
$("#backup-mode").addEventListener("change", () => resetBackupPreview("模式已更改，请重新预览"));
start();
