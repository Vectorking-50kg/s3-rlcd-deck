# Use HTTP inside the ephemeral Setup Mode network

Serve the M0 recovery page over HTTP at a displayed local address inside an ephemeral, randomly protected WPA2 Setup Mode network. Do not add self-signed HTTPS, DNS interception, or a captive portal: those mechanisms add certificate warnings and platform-specific behavior without improving trust beyond the isolated network for this local recovery flow. Setup Mode remains time-limited, locally initiated, and unable to expose Provider credentials.
