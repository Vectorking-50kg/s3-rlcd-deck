# Bootstrap Companion trust on the random Setup AP

Use the Deck's locally initiated, time-limited, random WPA2 Setup AP as the authorization
channel for the first Companion certificate exchange. The browser submitting Pairing must
be the Device Hub computer currently connected to that AP; firmware derives its peer IP
from the HTTP socket and permits only the submitted Hub port. The Deck performs one HTTPS
redeem without an existing CA, then independently hashes the returned DER certificate and
commits it only when it matches the returned SHA-256 fingerprint.

This exception exists only because certificate pinning cannot precede first trust. After
Pairing, the Deck closes Setup and every Device Link uses the exact committed certificate
and its per-Deck Token. We reject trusting a certificate supplied over the ordinary LAN,
asking users to bypass browser certificate warnings, globally discovering Companions, and
silently accepting certificate replacement. Replacing the Companion certificate requires
an explicit new Pairing session.
