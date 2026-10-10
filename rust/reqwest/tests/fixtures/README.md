# Public loopback test fixtures

These certificates and the private key are intentionally public, test-only material.
They authenticate only a controlled local test server; do not use them elsewhere.

`ca.pem` is the explicit test root; `other-ca.pem` is an unrelated test root used
to assert additive trust configuration. `server.der` is a CA:false leaf signed
by the first root, with serverAuth and SAN localhost / 127.0.0.1.
`server-key.der` is the associated PKCS#8 key. RSA-2048 / SHA-256.

The leaf is valid October 10, 2026 through October 10, 2027 and must be renewed
before expiry. Its normal 365-day lifetime is intentional: the macOS platform
verifier rejects the initially authored 100-year leaf. Certificate and hostname
validation are never disabled. The roots have long test-only validity.

`body.json.gz` is gzip of `{"decoded":true}` with mtime zero, testing unchanged
content-coded response bytes.

Renew all TLS fixtures with `sh tests/fixtures/regenerate.sh` from this crate directory
(OpenSSL 3 required), then review the binary changes and update the dates above.
