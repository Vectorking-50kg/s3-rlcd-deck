import assert from "node:assert/strict";
import { createRequire } from "node:module";
import test from "node:test";

const require = createRequire(import.meta.url);
const pairing = require("./dist/pairing-v2-ui.js");

test("candidate projection expires entries and disambiguates duplicate labels", () => {
  const now = Date.parse("2026-08-18T10:00:00Z");
  assert.deepEqual(pairing.candidates([
    { candidate_ref: "opaque-a", label: "S3 RLCD Deck", expires_at: "2026-08-18T10:00:05Z" },
    { candidate_ref: "opaque-b", label: "S3 RLCD Deck", expires_at: "2026-08-18T10:00:06Z" },
    { candidate_ref: "expired", label: "Old Deck", expires_at: "2026-08-18T09:59:59Z" },
  ], now), [
    { reference: "opaque-a", label: "S3 RLCD Deck · 同名设备 1", expiresAt: "2026-08-18T10:00:05Z" },
    { reference: "opaque-b", label: "S3 RLCD Deck · 同名设备 2", expiresAt: "2026-08-18T10:00:06Z" },
  ]);
});

test("six-digit code and session presentation fail closed", () => {
  assert.equal(pairing.validCode("123456"), true);
  for (const invalid of ["", "12345", "1234567", "12a456", "１２３４５６"]) {
    assert.equal(pairing.validCode(invalid), false);
  }
  assert.deepEqual(pairing.presentation({ state: "paired", error_code: "none" }), {
    state: "paired", title: "配对完成",
    detail: "Deck 已通过证书固定、Token 认证和心跳验证并正式在线。",
    kind: "success", terminal: true, success: true,
  });
  const unknown = pairing.presentation({ state: "future_state", error_code: "future_error" });
  assert.equal(unknown.terminal, false);
  assert.equal(unknown.success, false);
  assert.match(unknown.detail, /未知错误/);
});

test("countdown is monotonic and clamps at zero", () => {
  const deadline = "2026-08-18T10:00:03.100Z";
  assert.equal(pairing.secondsRemaining(deadline, Date.parse("2026-08-18T10:00:00Z")), 4);
  assert.equal(pairing.secondsRemaining(deadline, Date.parse("2026-08-18T10:00:04Z")), 0);
  assert.equal(pairing.secondsRemaining("invalid"), 0);
});

test("every stable backend failure has a local fail-closed explanation", () => {
  const expected = [
    "busy", "expired", "rate_limited", "incompatible_protocol", "malformed",
	"authentication_failed", "storage_failure", "capacity_reached", "hub_unavailable",
	"link_failed", "cancelled",
  ];
  for (const errorCode of expected) {
    const view = pairing.presentation({ state: "failed", error_code: errorCode });
    assert.equal(view.terminal, true, errorCode);
    assert.equal(view.success, false, errorCode);
    assert.equal(view.kind, "danger", errorCode);
    assert.ok(view.detail.length > 0, errorCode);
    assert.doesNotMatch(view.detail, /未知错误/, errorCode);
  }
});
