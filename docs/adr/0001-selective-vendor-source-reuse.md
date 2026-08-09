# Selectively reuse vendor sources

Reuse and refactor only the Waveshare Apache-2.0 RLCD register sequence and bit-mapping logic, preserving its provenance. Do not copy the vendor-generated UI or images carrying NXP proprietary notices, or the button library marked “All rights reserved”; implement those capabilities within this project. MIT-licensed RTC code may be reused only with its notice retained. This narrower boundary costs more initial implementation work but prevents ambiguous licensing and avoids importing unsafe concurrency and lifecycle behavior from the examples.
