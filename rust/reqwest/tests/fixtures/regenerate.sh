#!/bin/sh
# Intentionally public test material; never use these keys outside loopback tests.
set -eu
fixture_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test_work=$(mktemp -d)
trap 'rm -rf "$test_work"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 36500 -sha256 \
  -subj '/CN=OpenAPI loopback test root' -addext 'basicConstraints=critical,CA:TRUE' \
  -keyout "$test_work/ca-key.pem" -out "$fixture_dir/ca.pem"
openssl req -x509 -newkey rsa:2048 -nodes -days 36500 -sha256 \
  -subj '/CN=OpenAPI unrelated test root' -addext 'basicConstraints=critical,CA:TRUE' \
  -keyout "$test_work/other-key.pem" -out "$fixture_dir/other-ca.pem"
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=localhost' \
  -keyout "$test_work/server-key.pem" -out "$test_work/server.csr"
cat > "$test_work/server.ext" <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
EXT
openssl x509 -req -in "$test_work/server.csr" -CA "$fixture_dir/ca.pem" \
  -CAkey "$test_work/ca-key.pem" -set_serial 1 -days 365 -sha256 \
  -extfile "$test_work/server.ext" -out "$test_work/server.pem"
openssl x509 -in "$test_work/server.pem" -outform DER -out "$fixture_dir/server.der"
openssl pkcs8 -topk8 -nocrypt -in "$test_work/server-key.pem" -outform DER -out "$fixture_dir/server-key.der"
