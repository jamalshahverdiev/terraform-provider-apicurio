#!/usr/bin/env bash
# Smoke test of the compose stack: waits for every running registry, checks the
# server version and the deletion flags, and when the `auth` profile is up,
# checks every authentication mode the provider supports against Keycloak.
#
#   docker compose up -d && scripts/smoke.sh
#   docker compose --profile auth up -d && scripts/smoke.sh
#
# Exit code 0 only when at least one service was found and every check passed;
# 2 when no compose service is running (or docker is unreachable).
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

API=/apis/registry/v3
KEYCLOAK_URL="${KEYCLOAK_URL:-http://localhost:8092}"
pass=0; fail=0
tok_admin=; tok_user=; tok_dev=

ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; [ -n "${2:-}" ] && printf '        %s\n' "$2"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

running() { docker compose ps --status running --format '{{.Service}}' 2>/dev/null; }

# The health endpoint moved to the management port (9000, not published) on
# the 3.3 line, so readiness is judged on the API itself: 200 on an open
# registry, 401 on a secured one.
wait_ready() { # base-url
  local c
  for _ in $(seq 1 60); do
    c="$(code "$1$API/groups")"
    if [ "$c" = "200" ] || [ "$c" = "401" ]; then return 0; fi
    sleep 2
  done
  return 1
}

# ---------------------------------------------------------------- no-auth registries
check_registry() { # label base-url expected-version-prefix
  local label="$1" base="$2" want="$3"
  echo "== $label ($base)"
  if ! wait_ready "$base"; then bad "ready"; return; fi
  ok "ready"
  local info; info="$(curl -s "$base$API/system/info")"
  local ver; ver="$(printf '%s' "${info:-null}" | jq -r '.version // empty' 2>/dev/null)"
  case "$ver" in
    "$want"*) ok "version $ver" ;;
    *) bad "version" "got '$ver' from $info" ;;
  esac
  local props; props="$(curl -s "$base$API/admin/config/properties")"
  local p
  for p in apicurio.rest.deletion.group.enabled apicurio.rest.deletion.artifact.enabled apicurio.rest.deletion.artifact-version.enabled; do
    local v; v="$(printf '%s' "${props:-null}" | jq -r --arg n "$p" '.[]? | select(.name==$n) | .value' 2>/dev/null)"
    if [ "$v" = "true" ]; then ok "$p = true"; else bad "$p" "value '$v'"; fi
  done
  # create / read / delete round trip of a group
  local g="smoke-$$"
  local c; c="$(code -X POST "$base$API/groups" -H 'Content-Type: application/json' -d "{\"groupId\":\"$g\"}")"
  [ "$c" = "200" ] && ok "POST /groups -> 200" || bad "POST /groups" "status $c"
  c="$(code "$base$API/groups/$g")"
  [ "$c" = "200" ] && ok "GET /groups/$g -> 200" || bad "GET /groups/$g" "status $c"
  c="$(code -X DELETE "$base$API/groups/$g")"
  [ "$c" = "204" ] && ok "DELETE /groups/$g -> 204" || bad "DELETE /groups/$g" "status $c"
  c="$(code "$base$API/groups/$g")"
  [ "$c" = "404" ] && ok "GET after delete -> 404" || bad "GET after delete" "status $c"
}

svcs="$(running)"
if [ -z "$svcs" ]; then
  echo "no running compose service found (is the stack up? is docker reachable?)" >&2
  exit 2
fi
grep -qx registry-33 <<<"$svcs" && check_registry registry-33 http://localhost:8090 3.3.
grep -qx registry-30 <<<"$svcs" && check_registry registry-30 http://localhost:8091 3.0.
grep -qx registry-31 <<<"$svcs" && check_registry registry-31 http://localhost:8094 3.1.
grep -qx registry-32 <<<"$svcs" && check_registry registry-32 http://localhost:8095 3.2.

# ---------------------------------------------------------------- auth stack
if grep -qx keycloak <<<"$svcs"; then
  echo "== keycloak ($KEYCLOAK_URL)"
  if ! curl -s -o /dev/null "$KEYCLOAK_URL/"; then
    bad "keycloak unreachable at $KEYCLOAK_URL"
  else
    for _ in $(seq 1 90); do
      [ "$(code "$KEYCLOAK_URL/realms/registry/.well-known/openid-configuration")" = "200" ] && break
      sleep 2
    done
    disc="$(curl -s "$KEYCLOAK_URL/realms/registry/.well-known/openid-configuration")"
    iss="$(printf '%s' "${disc:-null}" | jq -r '.issuer // empty' 2>/dev/null)"
    [ "$iss" = "$KEYCLOAK_URL/realms/registry" ] && ok "issuer $iss" || bad "issuer" "got '$iss'"

    tok_admin="$(scripts/get-token.sh 2>/dev/null)"
    [ -n "$tok_admin" ] && ok "client credentials token for registry-api" || bad "client credentials token"
    tok_user="$(scripts/get-token.sh user 2>/dev/null)"
    [ -n "$tok_user" ] && ok "password grant token for user" || bad "password grant token for user"
    tok_dev="$(scripts/get-token.sh developer 2>/dev/null)"
    [ -n "$tok_dev" ] && ok "password grant token for developer" || bad "password grant token for developer"

    if [ -n "$tok_admin" ]; then
      claims="$(printf '%s' "$tok_admin" | cut -d. -f2 | tr '_-' '/+' | base64 -d 2>/dev/null)"
      roles="$(printf '%s' "${claims:-null}" | jq -r '.realm_access.roles | join(",")' 2>/dev/null)"
      case "$roles" in *sr-admin*) ok "service account token carries sr-admin ($roles)" ;; *) bad "service account roles" "got '$roles'" ;; esac
      tiss="$(printf '%s' "${claims:-null}" | jq -r '.iss' 2>/dev/null)"
      [ "$tiss" = "$KEYCLOAK_URL/realms/registry" ] && ok "token iss $tiss" || bad "token iss" "got '$tiss'"
    fi
  fi
fi

if grep -qx registry-33-auth <<<"$svcs"; then
  base=http://localhost:8093
  echo "== registry-33-auth ($base)"
  if wait_ready "$base"; then ok "ready"; else bad "ready"; fi

  c="$(code "$base$API/groups")"
  [ "$c" = "401" ] && ok "GET /groups without a token -> 401" || bad "no token" "status $c (want 401)"
  c="$(code "$base$API/admin/config/properties")"
  [ "$c" = "401" ] && ok "GET /admin/config/properties without a token -> 401" || bad "no token admin" "status $c (want 401)"
  printf '  info  GET /system/info without a token -> %s (public endpoint)\n' "$(code "$base$API/system/info")"

  c="$(code -H "Authorization: Bearer not.a.token" "$base$API/system/info")"
  [ "$c" = "401" ] && ok "garbage bearer -> 401 even on the public endpoint" || bad "garbage bearer" "status $c (want 401)"

  # Authentication is judged on a protected endpoint (/groups); /system/info
  # and /users/me are public and would pass with any or no credential.
  c="$(code -u registry-api:registry-api-secret "$base$API/groups")"
  [ "$c" = "200" ] && ok "basic client credentials: GET /groups -> 200" || bad "basic client credentials" "status $c"
  me="$(curl -s -u registry-api:registry-api-secret "$base$API/users/me")"
  [ "$(printf '%s' "${me:-null}" | jq -r '.admin' 2>/dev/null)" = "true" ] && ok "basic client credentials is admin ($me)" || bad "basic admin" "$me"

  # A failed basic login is not rejected outright: the registry falls back to an
  # anonymous identity, so public endpoints (/system/info, /users/me) still
  # answer 200 and /users/me shows an empty username; protected ones answer 401.
  c="$(code -u registry-api:wrong-secret "$base$API/groups")"
  [ "$c" = "401" ] && ok "basic with a wrong secret: GET /groups -> 401" || bad "basic wrong secret" "status $c (want 401)"
  me="$(curl -s -u registry-api:wrong-secret "$base$API/users/me")"
  me_user="$(printf '%s' "${me:-null}" | jq -r 'if type=="object" and has("username") then .username else "NOJSON" end' 2>/dev/null)"
  [ "$me_user" = "" ] && ok "basic with a wrong secret: /users/me is the anonymous user ($me)" || bad "basic wrong secret /users/me" "$me"

  if [ -z "$tok_admin" ] || [ -z "$tok_user" ]; then
    bad "keycloak tokens missing (is the keycloak service up?), bearer and role-mapping checks skipped"
  else
    c="$(code -H "Authorization: Bearer $tok_admin" "$base$API/groups")"
    [ "$c" = "200" ] && ok "bearer (client credentials): GET /groups -> 200" || bad "bearer" "status $c"
    me="$(curl -s -H "Authorization: Bearer $tok_admin" "$base$API/users/me")"
    [ "$(printf '%s' "${me:-null}" | jq -r '.admin' 2>/dev/null)" = "true" ] && ok "service account is admin via admin override ($me)" || bad "service account admin" "$me"

    # role-source=application: `user` has no mapping yet -> forbidden
    c="$(code -H "Authorization: Bearer $tok_user" "$base$API/groups")"
    [ "$c" = "403" ] && ok "user without role mapping -> 403" || bad "user without mapping" "status $c (want 403)"

    # admin maps user -> READ_ONLY, then user can read but not write
    c="$(code -X POST -H "Authorization: Bearer $tok_admin" -H 'Content-Type: application/json' \
          -d '{"principalId":"user","role":"READ_ONLY","principalName":"Registry User"}' "$base$API/admin/roleMappings")"
    [ "$c" = "204" ] && ok "POST /admin/roleMappings user=READ_ONLY -> 204" || bad "POST roleMappings" "status $c"
    c="$(code -H "Authorization: Bearer $tok_user" "$base$API/groups")"
    [ "$c" = "200" ] && ok "READ_ONLY user GET /groups -> 200" || bad "READ_ONLY user read" "status $c"
    out="$(curl -s -w '\n%{http_code}' -X POST -H "Authorization: Bearer $tok_user" -H 'Content-Type: application/json' -d '{"groupId":"smoke-forbidden"}' "$base$API/groups")"
    c="${out##*$'\n'}"; body="${out%$'\n'*}"
    [ "$c" = "403" ] && ok "READ_ONLY user POST /groups -> 403 ($(printf '%s' "${body:-null}" | jq -c . 2>/dev/null || printf '%s' "$body"))" || bad "READ_ONLY user write" "status $c (want 403)"
    curl -s -o /dev/null -X DELETE -H "Authorization: Bearer $tok_admin" "$base$API/groups/smoke-forbidden"   # in case authorization is broken
    c="$(code -X DELETE -H "Authorization: Bearer $tok_admin" "$base$API/admin/roleMappings/user")"
    [ "$c" = "204" ] && ok "DELETE role mapping -> 204" || bad "DELETE role mapping" "status $c"

    # provider-shaped CRUD with the service account
    g="smoke-auth-$$"
    c="$(code -X POST -H "Authorization: Bearer $tok_admin" -H 'Content-Type: application/json' -d "{\"groupId\":\"$g\"}" "$base$API/groups")"
    [ "$c" = "200" ] && ok "POST /groups as service account -> 200" || bad "POST /groups auth" "status $c"
    c="$(code -X DELETE -H "Authorization: Bearer $tok_admin" "$base$API/groups/$g")"
    [ "$c" = "204" ] && ok "DELETE /groups as service account -> 204" || bad "DELETE /groups auth" "status $c"
  fi
fi

echo
echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ] && [ "$pass" -gt 0 ]
