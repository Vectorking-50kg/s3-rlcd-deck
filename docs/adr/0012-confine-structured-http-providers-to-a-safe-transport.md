# Confine Structured HTTP Providers to a safe transport

Structured HTTP Providers are data definitions interpreted by one Companion-owned module, not
scripts or general HTTP clients. The module alone resolves secret references, pins every DNS result
through an address-policy dialer, rejects loopback/link-local targets and mixed-safe DNS answers,
permits cleartext HTTP only to private addresses with a warning, disables proxies and compression,
and follows only bounded same-origin redirects. It limits requests and responses before strictly
decoding JSON, then exports only the shared Provider DTO and fixed diagnostics; URLs, headers,
credentials, raw bodies, and upstream account fields cannot cross the module seam.

We reject executing imported curl, shell, JavaScript, arbitrary JSONPath expressions, or plugins.
The curl importer is a pure parser for a small GET/POST/header/JSON-body allowlist and separates
every imported header value from the persistable definition; the definition stores only a platform
secret reference. Sensitive URL query fields are rejected rather than guessed or persisted. This is
implemented as rejecting every query string until a structured query-parameter secret-reference
model exists. This is less flexible than embedding a general request engine, but it makes SSRF, credential ownership,
resource limits, logging, and failure isolation uniform for built-in and user-defined Providers.
