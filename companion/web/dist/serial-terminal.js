(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory();
  } else {
    root.S3DeckSerialTerminal = factory();
  }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  const FRAME_HEADER_BYTES = 32;
  const MAX_TRANSMIT_BYTES = 256;
  const SERIAL_SUBPROTOCOL = "s3deck.serial.v1";

  function decimalUint64(value, allowZero) {
    if (typeof value !== "string" || !/^(0|[1-9][0-9]{0,19})$/.test(value)) {
      throw new Error("malformed Serial identifier");
    }
    const parsed = BigInt(value);
    if (parsed > 0xffffffffffffffffn || (!allowZero && parsed === 0n)) {
      throw new Error("malformed Serial identifier");
    }
    return value;
  }

  function createClient(options) {
    const origin = new URL(options.origin);
    const openSocket = options.openSocket || ((url, protocol) => new WebSocket(url, protocol));
    const onFrame = options.onFrame || (() => {});
    const onState = options.onState || (() => {});
    const onResult = options.onResult || (() => {});
    const onError = options.onError || (() => {});
    let socket = null;
    let current = Object.freeze({
      connected: false,
      serialState: "unavailable",
      sessionID: "0",
      leaseID: "0",
      leaseOwner: "none",
      canTransmit: false,
      deviceID: "",
      bufferedBytes: 0,
      bufferedFrames: 0,
      overwrittenBytes: 0,
      observers: 0,
      leaseRemainingMS: 0,
    });

    function publish(next) {
      current = Object.freeze(next);
      onState(current);
    }

    function unavailable(closedSocket) {
      if (closedSocket && socket !== closedSocket) return;
      socket = null;
      publish({
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
    }

    function requireOpen() {
      if (!socket || socket.readyState !== 1) {
        throw new Error("Serial observer is not connected");
      }
    }

    function control(type) {
      requireOpen();
      socket.send(JSON.stringify({
        type,
        protocol_version: 1,
        serial_session_id: current.sessionID,
        lease_id: type === "serial.lease.acquire" ? "0" : current.leaseID,
      }));
    }

    return Object.freeze({
      connect() {
        if (socket) return;
        const scheme = origin.protocol === "https:" ? "wss:" : "ws:";
        socket = openSocket(`${scheme}//${origin.host}/api/v1/serial/observe`, SERIAL_SUBPROTOCOL);
        const openedSocket = socket;
        socket.binaryType = "arraybuffer";
        socket.onclose = () => unavailable(openedSocket);
        socket.onmessage = (event) => {
          try {
            if (typeof event.data !== "string") {
              onFrame(decodeFrame(event.data));
              return;
            }
            const document = JSON.parse(event.data);
            if (document.protocol_version !== 1) throw new Error("unsupported Serial observer version");
            if (document.type !== "serial.observer.state") {
			  const resultTypes = new Set([
			    "serial.tx.result", "serial.lease.result",
			    "serial.lease.heartbeat.result", "serial.observer.heartbeat.result",
			  ]);
			  if (!resultTypes.has(document.type) || typeof document.accepted !== "boolean") {
			    throw new Error("malformed Serial observer result");
			  }
			  decimalUint64(document.reference_id, true);
			  if (document.type === "serial.lease.heartbeat.result" && !document.accepted && current.canTransmit) {
			    publish({ ...current, leaseID: "0", leaseOwner: "unavailable", canTransmit: false, leaseRemainingMS: 0 });
			  }
              onResult(document);
              return;
            }
            const sessionID = decimalUint64(document.serial_session_id, true);
            const leaseHeld = document.lease_held_by_this_observer === true;
            const leaseID = decimalUint64(document.lease_id || "0", true);
			const serialStates = new Set(["disarmed", "usb_tx", "web_tx"]);
			const leaseOwners = new Set(["usb", "web", "transitioning", "unavailable"]);
			if (!serialStates.has(document.serial_state) || !leaseOwners.has(document.lease_owner) ||
			    typeof document.lease_held_by_this_observer !== "boolean" ||
			    typeof document.device_id !== "string" ||
			    (sessionID === "0" && document.serial_state !== "disarmed") ||
			    (!leaseHeld && leaseID !== "0") ||
			    (leaseHeld && (document.serial_state !== "web_tx" || document.lease_owner !== "web" || leaseID === "0"))) {
			  throw new Error("malformed Serial observer state");
			}
            const canTransmit = leaseHeld && document.serial_state === "web_tx" &&
              document.lease_owner === "web" && leaseID !== "0";
			const safeCount = (value) => {
			  if (!Number.isSafeInteger(value) || value < 0) throw new Error("malformed Serial observer count");
			  return value;
			};
            publish({
              connected: true,
              serialState: document.serial_state,
              sessionID,
              leaseID,
              leaseOwner: document.lease_owner,
              canTransmit,
              deviceID: typeof document.device_id === "string" ? document.device_id : "",
              bufferedBytes: safeCount(document.buffered_bytes),
              bufferedFrames: safeCount(document.buffered_frames),
              overwrittenBytes: safeCount(document.overwritten_bytes),
              observers: safeCount(document.observers),
              leaseRemainingMS: safeCount(document.lease_remaining_ms),
            });
          } catch (error) {
            onError(error);
            const failedSocket = socket;
            unavailable(failedSocket);
            if (failedSocket) failedSocket.close();
          }
        };
      },
      disconnect() {
        const closingSocket = socket;
        unavailable(closingSocket);
        if (closingSocket) closingSocket.close();
      },
      status() { return current; },
      acquire() { control("serial.lease.acquire"); },
      release() { control("serial.lease.release"); },
      heartbeat() { control("serial.lease.heartbeat"); },
      send(payload) {
        requireOpen();
        if (!current.canTransmit) throw new Error("Web TX Lease is not held");
        const bytes = payload instanceof Uint8Array ? payload : new Uint8Array(payload);
        if (bytes.length === 0 || bytes.length > MAX_TRANSMIT_BYTES) {
          throw new Error("Serial transmit payload must contain 1 to 256 bytes");
        }
        socket.send(bytes.slice());
      },
    });
  }

  function decodeFrame(document) {
    const bytes = document instanceof Uint8Array
      ? document
      : new Uint8Array(document);
    if (bytes.length < FRAME_HEADER_BYTES) {
      throw new Error("malformed Serial frame");
    }
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    if (view.getUint32(0, false) !== 0x53524431 || view.getUint8(5) !== 0) {
      throw new Error("malformed Serial frame");
    }
    const channel = { 1: "target_rx", 2: "web_tx" }[view.getUint8(4)];
    if (!channel) {
      throw new Error("unsupported Serial channel");
    }
    const payloadLength = view.getUint16(6, false);
    const sessionID = view.getBigUint64(8, false);
    const sequence = view.getBigUint64(16, false);
    if (
      payloadLength === 0 ||
      payloadLength > MAX_TRANSMIT_BYTES ||
      bytes.length !== FRAME_HEADER_BYTES + payloadLength ||
      sessionID === 0n ||
      sequence === 0n
    ) {
      throw new Error("malformed Serial frame");
    }
    return {
      channel,
      sessionID,
      sequence,
      monotonicMS: view.getBigUint64(24, false),
      payload: bytes.slice(FRAME_HEADER_BYTES),
    };
  }

  function parseHex(value) {
    const source = String(value);
    if (!/^[0-9a-fA-F\t\n\r ]+$/.test(source)) {
      throw new Error("HEX input contains an invalid character");
    }
    const compact = source.replace(/[\t\n\r ]/g, "");
    if (compact.length === 0 || compact.length % 2 !== 0) {
      throw new Error("HEX input must contain complete bytes");
    }
    if (compact.length / 2 > MAX_TRANSMIT_BYTES) {
      throw new Error("HEX input exceeds 256 bytes");
    }
    const bytes = new Uint8Array(compact.length / 2);
    for (let index = 0; index < compact.length; index += 2) {
      bytes[index / 2] = Number.parseInt(compact.slice(index, index + 2), 16);
    }
    return bytes;
  }

  function encodeText(value, lineEnding) {
    const endings = { none: "", cr: "\r", lf: "\n", crlf: "\r\n" };
    if (!Object.prototype.hasOwnProperty.call(endings, lineEnding)) {
      throw new Error("unsupported line ending");
    }
    const bytes = new TextEncoder().encode(String(value) + endings[lineEnding]);
    if (bytes.length === 0) {
      throw new Error("text input is empty");
    }
    if (bytes.length > MAX_TRANSMIT_BYTES) {
      throw new Error("text input exceeds 256 bytes");
    }
    return bytes;
  }

  function formatHex(bytes) {
    return Array.from(bytes, (value) => value.toString(16).padStart(2, "0").toUpperCase()).join(" ");
  }

  function formatMixed(bytes) {
    return {
      hex: formatHex(bytes),
      text: Array.from(bytes, (value) => value >= 0x20 && value <= 0x7e ? String.fromCharCode(value) : ".").join(""),
    };
  }

  function describePreset(preset) {
    if (!preset || typeof preset.payload !== "string") {
      throw new Error("malformed Serial Preset");
    }
    if (preset.mode === "hex") {
      return `HEX · ${parseHex(preset.payload).length} bytes · 无行结束符`;
    }
    if (preset.mode !== "text") {
      throw new Error("malformed Serial Preset");
    }
    if (preset.line_ending === "current") {
      return `Text · ${new TextEncoder().encode(preset.payload).length} bytes · 当前设置`;
    }
    const labels = { none: "无行结束符", cr: "CR", lf: "LF", crlf: "CRLF" };
    if (!Object.prototype.hasOwnProperty.call(labels, preset.line_ending)) {
      throw new Error("malformed Serial Preset");
    }
    return `Text · ${encodeText(preset.payload, preset.line_ending).length} bytes · ${labels[preset.line_ending]}`;
  }

  return Object.freeze({ createClient, decodeFrame, describePreset, encodeText, formatHex, formatMixed, parseHex });
});
