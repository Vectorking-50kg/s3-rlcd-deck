# M0 acceptance evidence

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
