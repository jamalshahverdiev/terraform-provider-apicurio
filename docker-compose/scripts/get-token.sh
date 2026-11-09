#!/usr/bin/env bash
# Print an access token from the local Keycloak (realm `registry`, port 8092).
#
#   scripts/get-token.sh                    # client credentials of registry-api (sr-admin)
#   scripts/get-token.sh admin              # password grant: user admin / admin
#   scripts/get-token.sh developer          # password grant: user developer / developer
#   scripts/get-token.sh user               # password grant: user user / user
#   scripts/get-token.sh <user> <password>  # any other user of the realm
#
# Env: KEYCLOAK_URL (default http://localhost:8092), CLIENT_ID, CLIENT_SECRET.
# Exit code is curl's (22 on a 4xx, 7 when nothing listens) and stdout is empty
# when no token was obtained.
set -euo pipefail

KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8092}"
CLIENT_ID="${CLIENT_ID:-registry-api}"
CLIENT_SECRET="${CLIENT_SECRET:-registry-api-secret}"
TOKEN_URL="$KEYCLOAK_URL/realms/registry/protocol/openid-connect/token"

if [ $# -eq 0 ]; then
  resp="$(curl -sS -f -X POST "$TOKEN_URL" \
    --data-urlencode grant_type=client_credentials \
    --data-urlencode client_id="$CLIENT_ID" \
    --data-urlencode client_secret="$CLIENT_SECRET")"
else
  user="$1"
  pass="${2:-$1}"   # the demo users have password == username
  resp="$(curl -sS -f -X POST "$TOKEN_URL" \
    --data-urlencode grant_type=password \
    --data-urlencode client_id=apicurio-registry \
    --data-urlencode username="$user" \
    --data-urlencode password="$pass")"
fi

printf '%s' "$resp" | jq -r '.access_token // empty'
