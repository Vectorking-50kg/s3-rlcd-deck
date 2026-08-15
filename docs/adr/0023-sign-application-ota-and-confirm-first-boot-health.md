# Sign application OTA and confirm first-boot health

Firmware Bundles use a versioned project ECDSA P-256 public-key catalog and bind the version, Deck
board, image size, SHA-256 digest, signing-key ID, and minimum Device Link protocol. The Companion
validates the complete bundle before issuing a short-lived in-memory preview receipt; only a second,
explicitly confirmed operation may start the exact OTA Transaction. The Deck independently verifies
the same manifest before opening the inactive OTA slot and accepts only a strictly newer semantic
version. Key rotation adds a catalog version/key ID; the application never writes eFuse or enables
irreversible Secure Boot.

The Deck streams one sequential bounded chunk at a time within a ten-minute total deadline, hashes while writing, validates the ESP
image and embedded version, then selects the inactive slot. An interrupted transfer, wrong target,
bad signature/digest, stale offset, size violation, or timeout leaves the running slot selected. The
first candidate boot remains pending until display, peripherals, Wi-Fi, and an authenticated Active
Companion connection are healthy; failure to confirm within 60 seconds invokes ESP-IDF rollback.
BOOT and the release USB Serial/JTAG bridge remain independent recovery paths throughout.
