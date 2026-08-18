"use strict";

(function exposePairingV2UI(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.S3DeckPairingV2UI = api;
})(typeof globalThis === "object" ? globalThis : window, () => {
  const terminalStates = new Set(["paired", "failed", "cancelled", "expired"]);

  const statePresentation = new Map([
    ["awaiting_code", ["等待验证码", "请查看 Deck 屏幕，并在下方输入六位配对码。", "info"]],
    ["authenticating", ["正在验证", "正在通过 PAKE 验证六位码；验证码不会以明文发送。", "info"]],
    ["proving_link", ["正在验证连接", "凭据已暂存，正在等待新 Device Link 的认证心跳。", "warning"]],
    ["committing", ["正在提交信任", "连接已经验证，正在原子提交 Deck Profile 与 Companion Trust。", "warning"]],
    ["paired", ["配对完成", "Deck 已通过证书固定、Token 认证和心跳验证并正式在线。", "success"]],
    ["failed", ["配对失败", "本次事务未完成；旧的有效信任和 Profile 保持不变。", "danger"]],
    ["cancelled", ["已取消", "本次配对产生的临时凭据已经清理。", "neutral"]],
    ["expired", ["配对窗口已过期", "请在 Deck 上重新打开配对窗口，然后重新扫描。", "danger"]],
  ]);

  const errorPresentation = new Map([
    ["none", ""],
    ["busy", "Deck 正在处理另一个配对会话，请稍后重试。"],
    ["expired", "Deck 配对窗口或六位码已过期。"],
    ["rate_limited", "尝试次数过多，请等待 Deck 冷却后重试。"],
    ["incompatible_protocol", "Deck 与 Companion 的配对协议版本不兼容。"],
    ["malformed", "Deck 拒绝了格式不正确的配对事务。"],
    ["authentication_failed", "六位码不正确，或安全握手未通过。"],
    ["storage_failure", "Deck 无法安全保存新的 Companion Profile。"],
    ["capacity_reached", "Deck 已达到可保存的 Companion Profile 数量上限。"],
	["hub_unavailable", "Mac 的 Device Hub 尚未在当前局域网准备完成；本次未向 Deck 发送凭据。"],
    ["link_failed", "凭据已暂存，但 Device Link 未能完成认证和心跳验证。"],
    ["cancelled", "本次配对已取消。"],
    ["pairing_failed", "配对事务失败。请确认两台设备仍在同一局域网，并重新打开配对窗口。"],
  ]);

  function validCode(value) {
    return /^\d{6}$/.test(String(value || ""));
  }

  function secondsRemaining(expiresAt, now = Date.now()) {
    const deadline = new Date(expiresAt).valueOf();
    if (!Number.isFinite(deadline)) return 0;
    return Math.max(0, Math.ceil((deadline - now) / 1000));
  }

  function candidates(values, now = Date.now()) {
    const safe = Array.isArray(values) ? values.filter((candidate) =>
      candidate && typeof candidate.candidate_ref === "string" && candidate.candidate_ref &&
      typeof candidate.label === "string" && candidate.label &&
      secondsRemaining(candidate.expires_at, now) > 0) : [];
    const totals = new Map();
    for (const candidate of safe) totals.set(candidate.label, (totals.get(candidate.label) || 0) + 1);
    const indexes = new Map();
    return safe.map((candidate) => {
      const index = (indexes.get(candidate.label) || 0) + 1;
      indexes.set(candidate.label, index);
      return {
        reference: candidate.candidate_ref,
        label: totals.get(candidate.label) > 1 ? `${candidate.label} · 同名设备 ${index}` : candidate.label,
        expiresAt: candidate.expires_at,
      };
    });
  }

  function presentation(view) {
    const state = String(view?.state || "failed");
    const configured = statePresentation.get(state) || statePresentation.get("failed");
    const errorCode = String(view?.error_code || "none");
    const error = errorPresentation.has(errorCode) ? errorPresentation.get(errorCode) :
      "配对事务返回了未知错误；已按失败处理。";
    return {
      state,
      title: configured[0],
      detail: error || configured[1],
      kind: configured[2],
      terminal: terminalStates.has(state),
      success: state === "paired",
    };
  }

  return Object.freeze({ candidates, presentation, secondsRemaining, validCode });
});
