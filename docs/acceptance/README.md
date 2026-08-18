# Acceptance evidence

Pairing v2 same-LAN acceptance is defined in `pairing-v2.md` and
`pairing-v2-manifest.json`. Its controller is `tools/pairing_v2_acceptance.py`.
It keeps the Mac and Deck on their normal LAN and requires the user to read the
six-digit code from the Deck screen; neither the code nor any resulting credential is retained.

M1 Pairing and Device Link acceptance is defined separately in `m1.md` and
`m1-manifest.json`. Its real-Deck transaction is run by `tools/m1_acceptance.py`; it does not
replace Pairing v2 or the M0 duration gates below. M1 remains the Pairing v1 compatibility
evidence until the Pairing v2 migration gate passes.

`m0.md` is the version-controlled single source of truth for M0 release acceptance. It is
generated from `m0-manifest.json` and remains `BLOCKED` until all three evidence records are
present and independently pass validation:

- the two-hour smoke `summary.json` from `tools/hil_smoke.py`;
- the 24-hour soak `summary.json` from the same harness;
- the completed second-device Setup Mode checklist based on `setup-result.example.json`.

Raw serial logs stay under the ignored `.hil-results/` directory. Copy only the redacted
machine summaries into `docs/acceptance/evidence/`, update the three paths in the manifest,
and regenerate the report:

```bash
python tools/m0_acceptance_report.py \
  --manifest docs/acceptance/m0-manifest.json \
  --output docs/acceptance/m0.md
```

The command returns zero only when every release gate passes. Missing, failed, dirty-build,
wrong-commit, wrong-toolchain, malformed, reset, watchdog, display, I²C, Setup, or
unrecovered Wi-Fi evidence produces a `BLOCKED` report and a non-zero exit status. Never
copy raw logs, Wi-Fi passwords, credentials, or private device data into this directory.

Deck UI logical-frame evidence is stored under `ui-frames/<firmware-commit>/`. Each set is
captured from the display service's last successfully transferred 15,000-byte RLCD frame,
contains deterministic sample data only, and is bound to the exact firmware and capture-tool
commits by `manifest.json`. `test_deck_ui_frame_evidence.py` verifies hashes, PNG structure,
400×300 dimensions, and strict black/white pixels. This evidence proves the rendered and
transferred frame; it does not replace straight-on physical photos needed to verify panel
contrast, difficult CJK strokes, reflections, bezel clipping, or refresh artifacts. The
manifest therefore keeps `optical_photo_status` as `pending` until that independent gate is
complete.
