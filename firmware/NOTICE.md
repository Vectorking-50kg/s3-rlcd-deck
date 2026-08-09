# Third-party source boundary

M0 may selectively adapt the RLCD controller register sequence and logical-to-physical bit mapping from Waveshare's `ESP32-S3-RLCD-4.2` repository, target example commit `9f8da2c12be0934ba108daa1174c0282cd57a03a`. That repository is distributed under Apache-2.0.

Do not copy the example's NXP-proprietary generated UI or images, or its button implementation marked “All rights reserved”. If later firmware work reuses MIT-licensed SensorLib RTC code, retain its copyright and license notice alongside the adapted source.

See ADR-0001 for the decision and rationale. Add a source-level SPDX and provenance note to every adapted file when display work begins.
