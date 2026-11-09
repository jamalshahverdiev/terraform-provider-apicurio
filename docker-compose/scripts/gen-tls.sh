#!/usr/bin/env bash
# Generate a private CA and a server certificate for the TLS front
# (registry-33-tls, https://localhost:8096). Idempotent: existing files are kept.
#
#   scripts/gen-tls.sh            # writes tls/certs/{ca.pem,ca-key.pem,server.pem,server-key.pem}
#   scripts/gen-tls.sh --force    # regenerate
set -euo pipefail

cd "$(dirname "$0")/../tls"
mkdir -p certs
if [ "${1:-}" != "--force" ] && [ -s certs/ca.pem ] && [ -s certs/server.pem ] && [ -s certs/server-key.pem ]; then
  echo "tls/certs already present (use --force to regenerate)"
  exit 0
fi

openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -sha256 \
  -subj "/CN=terraform-provider-apicurio test CA" \
  -keyout certs/ca-key.pem -out certs/ca.pem >/dev/null 2>&1

openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" \
  -keyout certs/server-key.pem -out certs/server.csr >/dev/null 2>&1

cat > certs/server.ext <<'EXT'
subjectAltName = DNS:localhost, DNS:registry-33-tls, IP:127.0.0.1
extendedKeyUsage = serverAuth
EXT

openssl x509 -req -in certs/server.csr -CA certs/ca.pem -CAkey certs/ca-key.pem -CAcreateserial \
  -days 3650 -sha256 -extfile certs/server.ext -out certs/server.pem >/dev/null 2>&1
rm -f certs/server.csr certs/server.ext certs/ca.srl
chmod 644 certs/*.pem
echo "wrote tls/certs: ca.pem (trust this), server.pem, server-key.pem"
