import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import terminal from "./dist/serial-terminal.js";

function fixtureBytes(name) {
  const hex = fs.readFileSync(new URL(`../../protocol/fixtures/serial-frame-v1/${name}`, import.meta.url), "utf8").trim();
  return new Uint8Array(Buffer.from(hex, "hex"));
}

function observerState(overrides = {}) {
  return {
    type: "serial.observer.state", protocol_version: 1,
    device_id: "deck-browser-test", serial_state: "usb_tx", serial_session_id: "7",
    buffered_bytes: 0, buffered_frames: 0, overwritten_bytes: 0, observers: 1,
    lease_owner: "usb", lease_held_by_this_observer: false, lease_remaining_ms: 0,
    ...overrides,
  };
}

test("HEX input is converted to exact raw bytes", () => {
  assert.deepEqual(
    terminal.parseHex("00 ff 7A\n10"),
    new Uint8Array([0x00, 0xff, 0x7a, 0x10]),
  );
});

test("invalid or oversized HEX is rejected before transmit", () => {
  for (const invalid of ["", "0", "0g", "00,01", "00\u00a001"]) {
    assert.throws(() => terminal.parseHex(invalid), /HEX/);
  }
  assert.throws(() => terminal.parseHex("aa ".repeat(257)), /256/);
});

test("text transmit preserves Unicode bytes and explicit CR/LF endings", () => {
  assert.deepEqual(
    terminal.encodeText("温度", "crlf"),
    new Uint8Array([0xe6, 0xb8, 0xa9, 0xe5, 0xba, 0xa6, 0x0d, 0x0a]),
  );
  assert.deepEqual(terminal.encodeText("A", "none"), new Uint8Array([0x41]));
  assert.deepEqual(terminal.encodeText("A", "cr"), new Uint8Array([0x41, 0x0d]));
  assert.deepEqual(terminal.encodeText("A", "lf"), new Uint8Array([0x41, 0x0a]));
  assert.throws(() => terminal.encodeText("x".repeat(257), "none"), /256/);
  assert.throws(() => terminal.encodeText("A", "unexpected"), /line ending/);
});

test("HEX and mixed views derive from raw bytes without creating markup", () => {
  const payload = new TextEncoder().encode("<img src=x onerror=alert(1)>\u001b[31m");
  assert.equal(
    terminal.formatHex(payload.subarray(0, 4)),
    "3C 69 6D 67",
  );
  assert.deepEqual(terminal.formatMixed(payload.subarray(0, 4)), {
    hex: "3C 69 6D 67",
    text: "<img",
  });
  assert.equal(typeof terminal.formatMixed(payload).text, "string");
});

test("saved preset list summaries never repeat command or secret content", () => {
  const summary = terminal.describePreset({
    mode: "text", payload: "login --token PRESET_SECRET", line_ending: "crlf",
  });
  assert.equal(summary, "Text · 29 bytes · CRLF");
  assert.equal(summary.includes("PRESET_SECRET"), false);
  assert.equal(terminal.describePreset({
    mode: "hex", payload: "00 ff 41", line_ending: "none",
  }), "HEX · 3 bytes · 无行结束符");
});

test("canonical SRD1 binary frames decode without losing uint64 metadata", () => {
  assert.deepEqual(terminal.decodeFrame(fixtureBytes("valid-target-rx.hex")), {
    channel: "target_rx",
    sessionID: 42n,
    sequence: 7n,
    monotonicMS: 1234n,
    payload: new Uint8Array([0x00, 0xff, 0x41]),
  });
});

test("browser decoder runs the canonical Serial frame manifest unchanged", () => {
  const manifest = JSON.parse(fs.readFileSync(
    new URL("../../protocol/fixtures/serial-frame-v1/manifest.json", import.meta.url), "utf8",
  ));
  for (const fixture of manifest.cases) {
    if (fixture.accepted) assert.doesNotThrow(() => terminal.decodeFrame(fixtureBytes(fixture.file)));
    else assert.throws(() => terminal.decodeFrame(fixtureBytes(fixture.file)), /Serial frame/);
  }
});

test("observer remains read-only until the exact Web TX lease is confirmed", () => {
  const sent = [];
  const socket = {
    readyState: 1,
    send(document) { sent.push(document); },
    close() {},
  };
  const client = terminal.createClient({
    origin: "http://127.0.0.1:17777",
    openSocket(url, protocol) {
      assert.equal(url, "ws://127.0.0.1:17777/api/v1/serial/observe");
      assert.equal(protocol, "s3deck.serial.v1");
      return socket;
    },
  });

  client.connect();
  socket.onmessage({ data: JSON.stringify(observerState({
    serial_state: "usb_tx",
    serial_session_id: "18446744073709551615",
    lease_owner: "usb",
    lease_held_by_this_observer: false,
  })) });
  assert.equal(client.status().canTransmit, false);
  assert.throws(() => client.send(new Uint8Array([0x41])), /Lease/);

  client.acquire();
  assert.deepEqual(JSON.parse(sent.shift()), {
    type: "serial.lease.acquire",
    protocol_version: 1,
    serial_session_id: "18446744073709551615",
    lease_id: "0",
  });

  socket.onmessage({ data: JSON.stringify(observerState({
    serial_state: "web_tx",
    serial_session_id: "18446744073709551615",
    lease_owner: "web",
    lease_held_by_this_observer: true,
    lease_id: "18446744073709551614",
  })) });
  assert.equal(client.status().canTransmit, true);
  client.send(new Uint8Array([0x00, 0xff, 0x41]));
  assert.deepEqual(sent.shift(), new Uint8Array([0x00, 0xff, 0x41]));

  socket.onmessage({ data: JSON.stringify({
    type: "serial.lease.heartbeat.result", protocol_version: 1,
    accepted: false, reference_id: "0",
  }) });
  assert.equal(client.status().canTransmit, false);
  assert.throws(() => client.send(new Uint8Array([0x42])), /Lease/);
});

test("observer disconnect immediately revokes browser transmit capability", () => {
  const socket = { readyState: 1, send() {}, close() {} };
  const client = terminal.createClient({
    origin: "https://companion.local",
    openSocket: () => socket,
  });
  client.connect();
  socket.onmessage({ data: JSON.stringify(observerState({
    serial_state: "web_tx", serial_session_id: "7",
    lease_owner: "web", lease_held_by_this_observer: true, lease_id: "9",
  })) });
  assert.equal(client.status().canTransmit, true);
  socket.onclose();
  assert.deepEqual(client.status(), {
    connected: false,
    serialState: "unavailable",
    sessionID: "0",
    leaseID: "0",
    leaseOwner: "unavailable",
    canTransmit: false,
    deviceID: "",
    bufferedBytes: 0,
    bufferedFrames: 0,
    overwrittenBytes: 0,
    observers: 0,
    leaseRemainingMS: 0,
  });
});

test("malformed observer state fails closed instead of inventing authority", () => {
  let closes = 0;
  let errors = 0;
  const socket = { readyState: 1, send() {}, close() { closes += 1; } };
  const client = terminal.createClient({
    origin: "http://127.0.0.1:17777",
    openSocket: () => socket,
    onError: () => { errors += 1; },
  });
  client.connect();
  socket.onmessage({ data: JSON.stringify(observerState({
    serial_state: "invented", serial_session_id: "7",
    lease_owner: "web", lease_held_by_this_observer: true, lease_id: "9",
  })) });
  assert.equal(errors, 1);
  assert.equal(closes, 1);
  assert.equal(client.status().canTransmit, false);
  assert.equal(client.status().connected, false);
});
