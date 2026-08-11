# Third-party source boundary

M0 selectively adapts the RLCD controller register sequence and logical-to-physical bit mapping from Waveshare's `ESP32-S3-RLCD-4.2` repository, target example commit `9f8da2c12be0934ba108daa1174c0282cd57a03a`. Copyright 2026 Waveshare. That source is distributed under Apache-2.0; the complete license is retained at `components/board_support/Apache-2.0.txt`.

Do not copy the example's NXP-proprietary generated UI or images, or its button implementation marked “All rights reserved”. If later firmware work reuses MIT-licensed SensorLib RTC code, retain its copyright and license notice alongside the adapted source.

See ADR-0001 for the decision and rationale. Add a source-level SPDX and provenance note to every adapted file when display work begins.

The M0 diagnostic font is a 1bpp subset generated from Source Han Sans SC,
Copyright 2014-2021 Adobe. The source font ships with the locked LVGL 9.4.0
dependency and is licensed under the SIL Open Font License 1.1. The generated
subset uses no Reserved Font Name and includes only ASCII plus the glyphs listed
in `application_ui/assets/m0_glyphs.txt`. See
`application_ui/assets/SourceHanSansSC-OFL.txt`.
