# Bootstrap Companion trust on the random Setup AP

Use the Deck's locally initiated, time-limited, random WPA2 Setup AP as the authorization
channel for the first Companion certificate exchange. The browser submitting Pairing must
be the Device Hub computer currently connected to that AP; firmware derives its peer IP
from the HTTP socket and permits only the submitted Hub port. The Deck performs one HTTPS
redeem without an existing CA, then independently hashes the returned DER certificate and
commits it only when it matches the returned SHA-256 fingerprint.

This exception exists only because certificate pinning cannot precede first trust. After
the browser reads the Pairing response, it acknowledges that response's opaque transaction
generation over the Setup AP; only the matching acknowledgement closes Setup. Missing or
stale acknowledgement retains the recovery surface. Every Device Link uses the exact committed certificate
and its per-Deck Token. We reject trusting a certificate supplied over the ordinary LAN,
asking users to bypass browser certificate warnings, globally discovering Companions, and
silently accepting certificate replacement. Replacing the Companion certificate requires
an explicit new Pairing session.

The five Profile set is stored transactionally in a dedicated 64 KiB `companion_nvs`
partition. This keeps the existing Wi-Fi/settings partition independent and reserves enough
space for the candidate, both committed 8 KiB records, and NVS replacement/garbage-collection
overhead at maximum certificate size. The two OTA application slots retain their established
offsets.

The ESP-IDF WebSocket transport normally logs the complete upgrade request on a failed write.
Because that request carries the per-Deck bearer Token, the firmware compiles `tcp_transport`
with logging disabled and publishes only Deck-owned redacted connection states.
