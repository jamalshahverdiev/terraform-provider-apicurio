#!/usr/bin/env bash
# Records the behaviours of a live Apicurio Registry 3.x that the plan lists
# under "to verify on a live server" (PLAN-apicurio.md, section 11), plus a few
# response shapes the client code depends on. Everything it creates lives in
# groups prefixed `probe-`; an EXIT trap deletes them (also those of an earlier
# interrupted run) and restores the deletion flags it toggles.
#
#   scripts/probe-api.sh http://localhost:8090 > findings-3.3.4.md
#   scripts/probe-api.sh http://localhost:8091 > findings-3.0.15.md
#   TOKEN="$(scripts/get-token.sh)" scripts/probe-api.sh http://localhost:8093 > findings-auth.md
#
# Exit code: 1 when the registry is unreachable or probe groups were left behind.
set -uo pipefail

BASE="${1:-http://localhost:8090}"
API="$BASE/apis/registry/v3"
AUTH=()
[ -n "${TOKEN:-}" ] && AUTH=(-H "Authorization: Bearer $TOKEN")
P="probe-$$"
created_default=0

# req METHOD PATH [curl args...]  -> prints "STATUS body" ; body compacted
req() {
  local m="$1" p="$2"; shift 2
  local out code body
  out="$(curl -s -w '\n%{http_code}' -X "$m" ${AUTH[@]+"${AUTH[@]}"} "$API$p" "$@")"
  code="${out##*$'\n'}"; body="${out%$'\n'*}"
  [ "$body" = "$out" ] && body=""
  if printf '%s' "$body" | jq -e . >/dev/null 2>&1; then body="$(printf '%s' "$body" | jq -c .)"; fi
  printf '%s %s' "$code" "$body"
}
json() { req "$@" -H 'Content-Type: application/json'; }
show() { # label, then the req output on the next line
  printf -- '- `%s`\n\n      %s\n\n' "$1" "$2"
}
h() { printf '\n### %s\n\n' "$1"; }
# ids: keep the status in front of the sorted artifact id list (errors stay visible)
ids() { local s; s="$(cat)"; printf '%s ' "${s%% *}"; printf '%s' "${s#* }" | jq -c '[.artifacts[]?.artifactId] | sort' 2>/dev/null; }
hdrs() { curl -s -D - -o /dev/null ${AUTH[@]+"${AUTH[@]}"} "$API$1" | grep -iE '^(HTTP|content-type)' | tr -d '\r' | tr '\n' ' '; }

restore() {
  local n g
  for n in group artifact artifact-version; do
    json PUT "/admin/config/properties/apicurio.rest.deletion.$n.enabled" -d '{"value":"true"}' >/dev/null
  done
  req DELETE /admin/rules/INTEGRITY >/dev/null
  for g in $(req GET '/groups?limit=500' | sed 's/^[0-9]* //' | jq -r '.groups[]?.groupId | select(startswith("probe-"))' 2>/dev/null); do
    req DELETE "/groups/$g" >/dev/null
  done
  [ "$created_default" = 1 ] && req DELETE /groups/default >/dev/null
  return 0
}
trap restore EXIT
trap 'exit 130' INT TERM   # make an interruption run the EXIT trap too

info_out="$(req GET /system/info)"
if [ "${info_out%% *}" != "200" ]; then
  echo "registry at $BASE not reachable (GET /system/info -> ${info_out%% *})" >&2
  exit 1
fi
info="${info_out#* }"

AVRO1='{"type":"record","name":"Price","namespace":"probe","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"}]}'
AVRO2='{"type":"record","name":"Price","namespace":"probe","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"},{"name":"currency","type":"string","default":"EUR"}]}'
# Adding a field WITHOUT a default (vat) breaks BACKWARD compatibility: a reader
# with the new schema cannot read data written with the previous one. Removing a
# field, or adding one with a default, is backward compatible.
AVRO_INCOMPAT='{"type":"record","name":"Price","namespace":"probe","fields":[{"name":"id","type":"string"},{"name":"price","type":"double"},{"name":"currency","type":"string","default":"EUR"},{"name":"vat","type":"double"}]}'
BAD_AVRO='{"type":"record","name":"Broken","fields":[{"name":"x"}]}'
# Uses probe.Price by name instead of redefining it, so DEREFERENCE can inline it.
AVRO_ORDER='{"type":"record","name":"Order","namespace":"probe","fields":[{"name":"orderId","type":"string"},{"name":"price","type":"probe.Price"}]}'

create_art() { # group artifact [type] [content] -> body of create
  local g="$1" a="$2" t="${3:-AVRO}" c="${4:-$AVRO1}"
  local payload
  payload="$(jq -nc --arg a "$a" --arg t "$t" --arg c "$c" '{artifactId:$a, artifactType:$t, name:("Name of "+$a), description:("Desc of "+$a), labels:{env:"probe"}, firstVersion:{version:"1", content:{content:$c, contentType:"application/json"}}}')"
  json POST "/groups/$g/artifacts?ifExists=FAIL" -d "$payload"
}
add_version() { # group artifact version content [extra-json-fields]
  local g="$1" a="$2" v="$3" c="$4" extra="${5:-{\}}"
  local payload
  payload="$(jq -nc --arg v "$v" --arg c "$c" --argjson e "$extra" '({version:$v, content:{content:$c, contentType:"application/json"}} + $e)')"
  json POST "/groups/$g/artifacts/$a/versions" -d "$payload"
}

printf '# Live findings: %s\n\n' "$(printf '%s' "$info" | jq -r '"\(.name) \(.version) built \(.builtOn)"')"
printf 'Base URL `%s`, probed %s.\n' "$BASE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# ------------------------------------------------------------------ Q1 metadata clearing
h "1. PUT of artifact and group metadata: does an empty string clear, or is null needed?"
json POST /groups -d "{\"groupId\":\"$P-meta\",\"description\":\"group desc\",\"labels\":{\"a\":\"1\",\"b\":\"2\"}}" >/dev/null
create_art "$P-meta" art >/dev/null
show 'GET artifact after create' "$(req GET "/groups/$P-meta/artifacts/art")"
show 'PUT artifact {"name":""}' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{"name":""}')"
show 'GET artifact after name=""' "$(req GET "/groups/$P-meta/artifacts/art")"
show 'PUT artifact {"description":null}' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{"description":null}')"
show 'GET artifact after description=null' "$(req GET "/groups/$P-meta/artifacts/art")"
show 'PUT artifact {"labels":{}}' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{"labels":{}}')"
show 'GET artifact after labels={}' "$(req GET "/groups/$P-meta/artifacts/art")"
show 'PUT artifact {"name":"again","labels":{"x":"y"}} then PUT {"description":"only desc"} (are omitted fields kept?)' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{"name":"again","labels":{"x":"y"}}' >/dev/null; json PUT "/groups/$P-meta/artifacts/art" -d '{"description":"only desc"}' >/dev/null; req GET "/groups/$P-meta/artifacts/art")"
show 'PUT artifact {} (empty body)' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{}')"
show 'PUT artifact {"owner":"someone"}' "$(json PUT "/groups/$P-meta/artifacts/art" -d '{"owner":"someone"}'; printf ' / GET: '; req GET "/groups/$P-meta/artifacts/art")"
show 'GET group after create' "$(req GET "/groups/$P-meta")"
show 'PUT group {"description":""}' "$(json PUT "/groups/$P-meta" -d '{"description":""}'; printf ' / GET: '; req GET "/groups/$P-meta")"
show 'PUT group {"description":null}' "$(json PUT "/groups/$P-meta" -d '{"description":"back"}' >/dev/null; json PUT "/groups/$P-meta" -d '{"description":null}'; printf ' / GET: '; req GET "/groups/$P-meta")"
show 'PUT group {"labels":{}} (clears?) then PUT {"description":"d"} (labels kept?)' "$(json PUT "/groups/$P-meta" -d '{"labels":{"k":"v"}}' >/dev/null; json PUT "/groups/$P-meta" -d '{"labels":{}}' >/dev/null; printf 'after labels={}: '; req GET "/groups/$P-meta"; json PUT "/groups/$P-meta" -d '{"labels":{"k":"v"}}' >/dev/null; json PUT "/groups/$P-meta" -d '{"description":"d"}' >/dev/null; printf ' / after description-only PUT: '; req GET "/groups/$P-meta")"
show 'PUT version metadata {"name":"","description":null,"labels":{}}' "$(json PUT "/groups/$P-meta/artifacts/art/versions/1" -d '{"name":"vn","description":"vd","labels":{"q":"1"}}' >/dev/null; json PUT "/groups/$P-meta/artifacts/art/versions/1" -d '{"name":"","description":null,"labels":{}}'; printf ' / GET: '; req GET "/groups/$P-meta/artifacts/art/versions/1")"

# ------------------------------------------------------------------ Q2 last version deletion
h "2. Deleting the last version of an artifact: does the artifact survive?"
create_art "$P-last" one >/dev/null
show 'DELETE the only version (1)' "$(req DELETE "/groups/$P-last/artifacts/one/versions/1")"
show 'GET artifact afterwards' "$(req GET "/groups/$P-last/artifacts/one")"
show 'GET versions afterwards' "$(req GET "/groups/$P-last/artifacts/one/versions")"
show 'GET versions/branch=latest afterwards' "$(req GET "/groups/$P-last/artifacts/one/versions/branch=latest")"
create_art "$P-last" two >/dev/null; add_version "$P-last" two 2 "$AVRO2" >/dev/null
show 'artifact with versions 1,2: DELETE version 2, then GET branch=latest' "$(req DELETE "/groups/$P-last/artifacts/two/versions/2"; printf ' / latest: '; req GET "/groups/$P-last/artifacts/two/versions/branch=latest")"
show 'POST artifact without firstVersion (no versions at all)' "$(json POST "/groups/$P-last/artifacts" -d '{"artifactId":"empty","artifactType":"AVRO"}')"
show 'GET empty artifact' "$(req GET "/groups/$P-last/artifacts/empty")"
show 'GET empty artifact versions/branch=latest' "$(req GET "/groups/$P-last/artifacts/empty/versions/branch=latest")"
show 'GET empty artifact versions list' "$(req GET "/groups/$P-last/artifacts/empty/versions")"
show 'GET empty artifact branches' "$(req GET "/groups/$P-last/artifacts/empty/branches")"

# ------------------------------------------------------------------ Q3 role mappings
h "3. Role mappings: POST for an existing principal, and the endpoints on this instance"
show 'GET /users/me' "$(req GET /users/me)"
show 'GET /admin/roleMappings' "$(req GET /admin/roleMappings)"
show 'POST {principalId:probe-user, role:DEVELOPER}' "$(json POST /admin/roleMappings -d '{"principalId":"probe-user","role":"DEVELOPER","principalName":"Probe User"}')"
show 'POST again, same principal, role READ_ONLY (409 or overwrite?)' "$(json POST /admin/roleMappings -d '{"principalId":"probe-user","role":"READ_ONLY"}')"
show 'GET /admin/roleMappings/probe-user' "$(req GET /admin/roleMappings/probe-user)"
show 'PUT /admin/roleMappings/probe-user {role:ADMIN}' "$(json PUT /admin/roleMappings/probe-user -d '{"role":"ADMIN"}'; printf ' / GET: '; req GET /admin/roleMappings/probe-user)"
show 'PUT with an invalid role' "$(json PUT /admin/roleMappings/probe-user -d '{"role":"GOD"}')"
show 'DELETE /admin/roleMappings/probe-user' "$(req DELETE /admin/roleMappings/probe-user)"
show 'GET after delete (404 name)' "$(req GET /admin/roleMappings/probe-user)"
show 'DELETE again (404?)' "$(req DELETE /admin/roleMappings/probe-user)"
show 'PUT on an unmapped principal' "$(json PUT /admin/roleMappings/never-mapped -d '{"role":"ADMIN"}')"

# ------------------------------------------------------------------ Q4 404 names
h "4. The \`name\` values of 404 bodies"
show 'group' "$(req GET /groups/no-such-group)"
show 'artifact (group exists)' "$(req GET "/groups/$P-meta/artifacts/no-such-artifact")"
show 'artifact (group missing)' "$(req GET /groups/no-such-group/artifacts/x)"
show 'version' "$(req GET "/groups/$P-meta/artifacts/art/versions/999")"
show 'version via branch=nope' "$(req GET "/groups/$P-meta/artifacts/art/versions/branch=nope")"
show 'version content' "$(req GET "/groups/$P-meta/artifacts/art/versions/999/content")"
show 'global rule (not configured)' "$(req GET /admin/rules/VALIDITY)"
show 'global rule (unknown type)' "$(req GET /admin/rules/BOGUS)"
show 'group rule' "$(req GET "/groups/$P-meta/rules/VALIDITY")"
show 'group rule, group missing' "$(req GET /groups/no-such-group/rules/VALIDITY)"
show 'artifact rule' "$(req GET "/groups/$P-meta/artifacts/art/rules/VALIDITY")"
show 'artifact rule, artifact missing' "$(req GET "/groups/$P-meta/artifacts/nope/rules/VALIDITY")"
show 'role mapping' "$(req GET /admin/roleMappings/nobody)"
show 'branch' "$(req GET "/groups/$P-meta/artifacts/art/branches/nope")"
show 'branch versions' "$(req GET "/groups/$P-meta/artifacts/art/branches/nope/versions")"
show 'config property (unknown)' "$(req GET /admin/config/properties/no.such.property)"
show 'config property PUT (unknown)' "$(json PUT /admin/config/properties/no.such.property -d '{"value":"x"}')"
show 'config property PUT (known but not dynamic: apicurio.storage.enable-automatic-group-creation)' "$(json PUT /admin/config/properties/apicurio.storage.enable-automatic-group-creation -d '{"value":"false"}')"
show 'unknown route under the API' "$(req GET /no/such/route)"
show 'unknown route outside the API (raw status)' "$(curl -s -o /dev/null -w '%{http_code} content-type=%{content_type}' "$BASE/nope")"

# ------------------------------------------------------------------ Q5 rule violations
h "5. Rule violations: status and body on create, and on DRAFT -> ENABLED"
json POST /groups -d "{\"groupId\":\"$P-rules\"}" >/dev/null
create_art "$P-rules" comp >/dev/null
show 'POST artifact rule COMPATIBILITY=BACKWARD' "$(json POST "/groups/$P-rules/artifacts/comp/rules" -d '{"ruleType":"COMPATIBILITY","config":"BACKWARD"}')"
show 'POST rule again (409 name)' "$(json POST "/groups/$P-rules/artifacts/comp/rules" -d '{"ruleType":"COMPATIBILITY","config":"BACKWARD"}')"
show 'GET rule' "$(req GET "/groups/$P-rules/artifacts/comp/rules/COMPATIBILITY")"
show 'PUT rule config FULL' "$(json PUT "/groups/$P-rules/artifacts/comp/rules/COMPATIBILITY" -d '{"ruleType":"COMPATIBILITY","config":"FULL"}')"
show 'PUT rule with invalid config' "$(json PUT "/groups/$P-rules/artifacts/comp/rules/COMPATIBILITY" -d '{"ruleType":"COMPATIBILITY","config":"SIDEWAYS"}')"
json PUT "/groups/$P-rules/artifacts/comp/rules/COMPATIBILITY" -d '{"ruleType":"COMPATIBILITY","config":"BACKWARD"}' >/dev/null
show 'compatible new version (field with default)' "$(add_version "$P-rules" comp 2 "$AVRO2")"
show 'INCOMPATIBLE new version (field added without default): status + body' "$(add_version "$P-rules" comp 3 "$AVRO_INCOMPAT")"
show 'COMPATIBILITY still on + syntactically broken Avro' "$(add_version "$P-rules" comp 4 "$BAD_AVRO")"
show 'only VALIDITY=FULL (compatibility rule deleted) + syntactically broken Avro: status + body' "$(req DELETE "/groups/$P-rules/artifacts/comp/rules/COMPATIBILITY" >/dev/null; json POST "/groups/$P-rules/artifacts/comp/rules" -d '{"ruleType":"VALIDITY","config":"FULL"}' >/dev/null; add_version "$P-rules" comp 4 "$BAD_AVRO")"
show 'VALIDITY=FULL + semantically broken Avro (unknown type name)' "$(add_version "$P-rules" comp 4 '{"type":"record","name":"Semantic","fields":[{"name":"x","type":"NoSuchType"}]}')"
json POST "/groups/$P-rules/artifacts/comp/rules" -d '{"ruleType":"COMPATIBILITY","config":"BACKWARD"}' >/dev/null
show 'draft version with broken content (isDraft:true): created even with VALIDITY+COMPATIBILITY rules?' "$(add_version "$P-rules" comp 5 "$BAD_AVRO" '{"isDraft":true}')"
show 'GET draft version' "$(req GET "/groups/$P-rules/artifacts/comp/versions/5")"
show 'PUT draft content to an INCOMPATIBLE schema (allowed while DRAFT?)' "$(json PUT "/groups/$P-rules/artifacts/comp/versions/5/content" -d "$(jq -nc --arg c "$AVRO_INCOMPAT" '{content:$c, contentType:"application/json"}')")"
show 'PUT state ENABLED on the draft (it is the newest version, so the rule compares it with itself)' "$(json PUT "/groups/$P-rules/artifacts/comp/versions/5/state" -d '{"state":"ENABLED"}')"
show 'GET the version after the transition' "$(req GET "/groups/$P-rules/artifacts/comp/versions/5")"
show 'draft 6 incompatible, then ENABLED version 7 (compatible) so the draft is NOT the newest, then ENABLE draft 6' "$(add_version "$P-rules" comp 6 "$AVRO_INCOMPAT" '{"isDraft":true}' >/dev/null; json PUT "/groups/$P-rules/artifacts/comp/versions/5/state" -d '{"state":"DISABLED"}' >/dev/null; add_version "$P-rules" comp 7 "$AVRO2" | cut -c1-60; printf ' / ENABLE 6: '; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"ENABLED"}'; printf ' / state 6: '; req GET "/groups/$P-rules/artifacts/comp/versions/6" | sed 's/^[0-9]* //' | jq -r .state 2>/dev/null)"
show 'fix draft 6 content to compatible, PUT state ENABLED' "$(json PUT "/groups/$P-rules/artifacts/comp/versions/6/content" -d "$(jq -nc --arg c "$AVRO2" '{content:$c, contentType:"application/json"}')" >/dev/null; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"ENABLED"}'; printf ' / GET: '; req GET "/groups/$P-rules/artifacts/comp/versions/6")"
show 'PUT content on an ENABLED version (expected refusal)' "$(json PUT "/groups/$P-rules/artifacts/comp/versions/6/content" -d "$(jq -nc --arg c "$AVRO1" '{content:$c, contentType:"application/json"}')")"
show 'state transitions: ENABLED->DEPRECATED, DEPRECATED->SUNSET (3.3+), SUNSET->ENABLED, ->DRAFT, ->DISABLED' "$(json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"DEPRECATED"}'; printf ' | SUNSET: '; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"SUNSET"}'; printf ' | ENABLED: '; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"ENABLED"}'; printf ' | DRAFT: '; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"DRAFT"}'; printf ' | DISABLED: '; json PUT "/groups/$P-rules/artifacts/comp/versions/6/state" -d '{"state":"DISABLED"}'; printf ' | GET: '; req GET "/groups/$P-rules/artifacts/comp/versions/6")"
show 'create with ifExists=FAIL when the artifact exists (409 name)' "$(create_art "$P-rules" comp)"
show 'POST version with an existing version id (409 name)' "$(add_version "$P-rules" comp 1 "$AVRO1")"
show 'POST group that exists (409 name)' "$(json POST /groups -d "{\"groupId\":\"$P-rules\"}")"
out="$(json POST /groups -d '{"groupId":"default"}')"
[ "${out%% *}" = "200" ] && created_default=1
show 'POST group "default" (400 reserved on 3.1+; 3.0.15 creates it, 409 only once it exists)' "$out"
show 'POST global rule, then again (409)' "$(json POST /admin/rules -d '{"ruleType":"INTEGRITY","config":"FULL"}'; printf ' | again: '; json POST /admin/rules -d '{"ruleType":"INTEGRITY","config":"FULL"}'; printf ' | GET: '; req GET /admin/rules/INTEGRITY; printf ' | DELETE: '; req DELETE /admin/rules/INTEGRITY)"
show 'dryRun=true version POST (no version created?)' "$(json POST "/groups/$P-rules/artifacts/comp/versions?dryRun=true" -d "$(jq -nc --arg c "$AVRO2" '{version:"77", content:{content:$c, contentType:"application/json"}}')"; printf ' | versions after: '; req GET "/groups/$P-rules/artifacts/comp/versions" | sed 's/^[0-9]* //' | jq -c '{count, versions:[.versions[].version]}' 2>/dev/null)"

# ------------------------------------------------------------------ Q6 group delete with artifacts, artifact deletion disabled
h "6. DELETE a group with artifacts when only group deletion is enabled"
show 'current deletion properties' "$(req GET /admin/config/properties | sed 's/^[0-9]* //' | jq -c '[.[] | select(.name | test("deletion|mutability|group-creation|semver|draft")) | {name, value, type, label}]' 2>/dev/null)"
show 'PUT apicurio.rest.deletion.artifact.enabled=false' "$(json PUT /admin/config/properties/apicurio.rest.deletion.artifact.enabled -d '{"value":"false"}'; printf ' / GET: '; req GET /admin/config/properties/apicurio.rest.deletion.artifact.enabled)"
json POST /groups -d "{\"groupId\":\"$P-gdel\"}" >/dev/null; create_art "$P-gdel" a1 >/dev/null
show 'DELETE artifact while disabled (405 name)' "$(req DELETE "/groups/$P-gdel/artifacts/a1")"
show 'DELETE group with artifacts inside' "$(req DELETE "/groups/$P-gdel"; printf ' / GET group: '; req GET "/groups/$P-gdel"; printf ' / GET artifact: '; req GET "/groups/$P-gdel/artifacts/a1")"
show 'PUT apicurio.rest.deletion.artifact-version.enabled=false then DELETE a version (405 name)' "$(json PUT /admin/config/properties/apicurio.rest.deletion.artifact-version.enabled -d '{"value":"false"}' >/dev/null; req DELETE "/groups/$P-meta/artifacts/art/versions/1")"
show 'PUT apicurio.rest.deletion.group.enabled=false then DELETE an empty group (405 name)' "$(json PUT /admin/config/properties/apicurio.rest.deletion.group.enabled -d '{"value":"false"}' >/dev/null; json POST /groups -d "{\"groupId\":\"$P-gdel2\"}" >/dev/null; req DELETE "/groups/$P-gdel2")"
show 'DELETE config property (reset to default = env value?)' "$(req DELETE /admin/config/properties/apicurio.rest.deletion.artifact.enabled; printf ' / GET: '; req GET /admin/config/properties/apicurio.rest.deletion.artifact.enabled)"
json PUT /admin/config/properties/apicurio.rest.deletion.artifact.enabled -d '{"value":"true"}' >/dev/null
json PUT /admin/config/properties/apicurio.rest.deletion.artifact-version.enabled -d '{"value":"true"}' >/dev/null
json PUT /admin/config/properties/apicurio.rest.deletion.group.enabled -d '{"value":"true"}' >/dev/null
show 'PUT a boolean property with a non-boolean value' "$(json PUT /admin/config/properties/apicurio.rest.deletion.group.enabled -d '{"value":"maybe"}'; printf ' / GET: '; req GET /admin/config/properties/apicurio.rest.deletion.group.enabled)"
json PUT /admin/config/properties/apicurio.rest.deletion.group.enabled -d '{"value":"true"}' >/dev/null
show 'GET /admin/config/properties: all names' "$(req GET /admin/config/properties | sed 's/^[0-9]* //' | jq -c '[.[] | .name]' 2>/dev/null)"

# ------------------------------------------------------------------ Q7 branches
h "7. Branch version order, empty list, system branches"
create_art "$P-br" b >/dev/null; add_version "$P-br" b 2 "$AVRO2" >/dev/null; add_version "$P-br" b 3 "$AVRO2" >/dev/null
show 'POST branch {branchId:rel, versions:["3","1"]}' "$(json POST "/groups/$P-br/artifacts/b/branches" -d '{"branchId":"rel","description":"release","versions":["3","1"]}')"
show 'GET branch versions (order as given, or sorted?)' "$(req GET "/groups/$P-br/artifacts/b/branches/rel/versions")"
show 'POST append version 2' "$(json POST "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"version":"2"}'; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/rel/versions" | sed 's/^[0-9]* //' | jq -c '[.versions[].version]' 2>/dev/null)"
show 'PUT replace {"versions":["1","2"]}' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"versions":["1","2"]}'; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/rel/versions" | sed 's/^[0-9]* //' | jq -c '[.versions[].version]' 2>/dev/null)"
show 'PUT replace {"versions":["2","3","1"]} (is the list order kept, reversed, or sorted?)' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"versions":["2","3","1"]}'; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/rel/versions" | sed 's/^[0-9]* //' | jq -c '[.versions[].version]' 2>/dev/null)"
show 'PUT replace {"versions":[]} (accepted?)' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"versions":[]}'; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/rel/versions" | sed 's/^[0-9]* //' | jq -c '{count, versions:[.versions[].version]}' 2>/dev/null)"
show 'PUT replace with a missing version {"versions":["42"]}' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"versions":["42"]}' | cut -c1-300)"
show 'PUT replace with a duplicate {"versions":["1","1"]}' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel/versions" -d '{"versions":["1","1"]}' | cut -c1-300; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/rel/versions" | sed 's/^[0-9]* //' | jq -c '[.versions[].version]' 2>/dev/null)"
show 'POST branch without versions, then GET its versions' "$(json POST "/groups/$P-br/artifacts/b/branches" -d '{"branchId":"empty-branch"}' | cut -c1-120; printf ' / list: '; req GET "/groups/$P-br/artifacts/b/branches/empty-branch/versions")"
show 'GET versions/branch=empty-branch (tip of an empty branch)' "$(req GET "/groups/$P-br/artifacts/b/versions/branch=empty-branch")"
show 'POST branch with a bad id (space)' "$(json POST "/groups/$P-br/artifacts/b/branches" -d '{"branchId":"bad id"}')"
show 'PUT branch metadata {description:"x"}' "$(json PUT "/groups/$P-br/artifacts/b/branches/rel" -d '{"description":"x"}'; printf ' / GET: '; req GET "/groups/$P-br/artifacts/b/branches/rel")"
show 'GET branches list (system branches?)' "$(req GET "/groups/$P-br/artifacts/b/branches")"
show 'GET latest branch versions order' "$(req GET "/groups/$P-br/artifacts/b/branches/latest/versions" | sed 's/^[0-9]* //' | jq -c '[.versions[].version]' 2>/dev/null)"
show 'DELETE system branch latest (409 name)' "$(req DELETE "/groups/$P-br/artifacts/b/branches/latest")"
show 'POST branch latest (409 name)' "$(json POST "/groups/$P-br/artifacts/b/branches" -d '{"branchId":"latest"}')"
show 'POST branch rel again (409 name)' "$(json POST "/groups/$P-br/artifacts/b/branches" -d '{"branchId":"rel"}')"
show 'DELETE branch rel' "$(req DELETE "/groups/$P-br/artifacts/b/branches/rel")"
show 'GET versions list order (default)' "$(req GET "/groups/$P-br/artifacts/b/versions" | sed 's/^[0-9]* //' | jq -c '{count, versions:[.versions[].version]}' 2>/dev/null)"

# ------------------------------------------------------------------ Q8 search labels
h "8. Label, name and description filters of /search/artifacts"
json POST /groups -d "{\"groupId\":\"$P-search\"}" >/dev/null
json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"s1", artifactType:"AVRO", name:"Prices", description:"about s1", labels:{env:"prod", team:"payments"}, firstVersion:{content:{content:$c, contentType:"application/json"}}}')" >/dev/null
json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"s2", artifactType:"AVRO", name:"Prices dev", description:"about s2", labels:{env:"dev", team:"payments"}, firstVersion:{content:{content:$c, contentType:"application/json"}}}')" >/dev/null
json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"s3", artifactType:"AVRO", name:"Other", description:"about s3", labels:{env:"prod"}, firstVersion:{content:{content:$c, contentType:"application/json"}}}')" >/dev/null
show 'labels=env:prod' "$(req GET "/search/artifacts?groupId=$P-search&labels=env:prod" | ids)"
show 'labels=env (key only)' "$(req GET "/search/artifacts?groupId=$P-search&labels=env" | ids)"
show 'labels=team (key only)' "$(req GET "/search/artifacts?groupId=$P-search&labels=team" | ids)"
show 'labels=env:prod&labels=team:payments (two filters: AND?)' "$(req GET "/search/artifacts?groupId=$P-search&labels=env:prod&labels=team:payments" | ids)"
show 'labels=env:nope' "$(req GET "/search/artifacts?groupId=$P-search&labels=env:nope" | ids)"
show 'name=Prices (exact or substring?)' "$(req GET "/search/artifacts?groupId=$P-search&name=Prices" | ids)"
show 'name=Pric (substring?)' "$(req GET "/search/artifacts?groupId=$P-search&name=Pric" | ids)"
show 'name=prices (case?)' "$(req GET "/search/artifacts?groupId=$P-search&name=prices" | ids)"
show 'name=prices&nameCaseInsensitive=true (3.3+ parameter)' "$(req GET "/search/artifacts?groupId=$P-search&name=prices&nameCaseInsensitive=true" | ids)"
show 'description=about (substring?)' "$(req GET "/search/artifacts?groupId=$P-search&description=about" | ids)"
show 'artifactId=s1 (exact?)' "$(req GET "/search/artifacts?groupId=$P-search&artifactId=s1" | ids; printf ' | artifactId=s: '; req GET "/search/artifacts?groupId=$P-search&artifactId=s" | ids)"
show 'artifactType=AVRO&groupId' "$(req GET "/search/artifacts?groupId=$P-search&artifactType=AVRO" | ids)"
show 'raw first result (shape)' "$(req GET "/search/artifacts?groupId=$P-search&limit=1" | cut -c1-600)"
show 'GET /groups?limit=2 (paging shape, count)' "$(req GET "/groups?limit=2&offset=0" | cut -c1-400)"
show 'GET /search/groups?groupId=<prefix> (exact match, not prefix)' "$(req GET "/search/groups?groupId=$P" | sed 's/^[0-9]* //' | jq -c '[.groups[]?.groupId]' 2>/dev/null; printf ' | exact: '; req GET "/search/groups?groupId=$P-search" | sed 's/^[0-9]* //' | jq -c '[.groups[]?.groupId]' 2>/dev/null)"
show 'GET /admin/config/artifactTypes' "$(req GET /admin/config/artifactTypes)"

# ------------------------------------------------------------------ extra: create/response shapes
h "9. Response shapes the client relies on"
show 'POST artifact (CreateArtifactResponse)' "$(json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"shape", firstVersion:{content:{content:$c, contentType:"application/json"}}}')")"
show 'artifactType detected when omitted (see above) / GET version content headers' "$(hdrs "/groups/$P-search/artifacts/shape/versions/branch=latest/content")"
show 'GET version content body' "$(req GET "/groups/$P-search/artifacts/shape/versions/branch=latest/content")"
show 'POST version with references to another artifact' "$(json POST "/groups/$P-search/artifacts/shape/versions" -d "$(jq -nc --arg c "$AVRO_ORDER" '{content:{content:$c, contentType:"application/json", references:[{groupId:"'"$P-search"'", artifactId:"s1", version:"1", name:"probe.Price"}]}}')")"
show 'GET version references' "$(req GET "/groups/$P-search/artifacts/shape/versions/branch=latest/references")"
show 'GET content (default = PRESERVE)' "$(req GET "/groups/$P-search/artifacts/shape/versions/branch=latest/content" | cut -c1-300)"
show 'GET content?references=DEREFERENCE' "$(req GET "/groups/$P-search/artifacts/shape/versions/branch=latest/content?references=DEREFERENCE" | cut -c1-400)"
show 'GET content?references=REWRITE' "$(req GET "/groups/$P-search/artifacts/shape/versions/branch=latest/content?references=REWRITE" | cut -c1-400)"
show 'POST version whose reference points to a missing artifact (INTEGRITY default?)' "$(json POST "/groups/$P-search/artifacts/shape/versions" -d "$(jq -nc --arg c "$AVRO_ORDER" '{content:{content:$c, contentType:"application/json", references:[{groupId:"'"$P-search"'", artifactId:"missing", version:"1", name:"probe.Price"}]}}')" | cut -c1-400)"
show 'POST artifact rule INTEGRITY=FULL then the same bad reference' "$(json POST "/groups/$P-search/artifacts/shape/rules" -d '{"ruleType":"INTEGRITY","config":"FULL"}' >/dev/null; json POST "/groups/$P-search/artifacts/shape/versions" -d "$(jq -nc --arg c "$AVRO_ORDER" '{content:{content:$c, contentType:"application/json", references:[{groupId:"'"$P-search"'", artifactId:"missing", version:"1", name:"probe.Price"}]}}')" | cut -c1-500)"
show 'YAML OpenAPI content: detected type and stored content-type' "$(json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c $'openapi: 3.0.3\ninfo:\n  title: Probe\n  version: "1.0"\npaths: {}\n' '{artifactId:"yaml-api", firstVersion:{content:{content:$c, contentType:"application/x-yaml"}}}')" | cut -c1-250; printf ' / content headers: '; hdrs "/groups/$P-search/artifacts/yaml-api/versions/branch=latest/content")"
show 'PROTOBUF content with the wrong declared type (JSON): what happens?' "$(json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c $'syntax = "proto3";\nmessage Probe { string id = 1; }\n' '{artifactId:"proto-as-json", artifactType:"JSON", firstVersion:{content:{content:$c, contentType:"application/x-protobuf"}}}')" | cut -c1-300)"
show 'PROTOBUF content, type omitted (detection)' "$(json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c $'syntax = "proto3";\nmessage Probe { string id = 1; }\n' '{artifactId:"proto", firstVersion:{content:{content:$c, contentType:"application/x-protobuf"}}}')" | sed 's/^[0-9]* //' | jq -c '{type:.artifact.artifactType, v:.version.version}' 2>/dev/null)"
show 'unknown artifactType' "$(json POST "/groups/$P-search/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"badtype", artifactType:"COBOL", firstVersion:{content:{content:$c, contentType:"application/json"}}}')" | cut -c1-300)"
show 'POST artifact with ifExists=CREATE_VERSION on existing' "$(json POST "/groups/$P-search/artifacts?ifExists=CREATE_VERSION" -d "$(jq -nc --arg c "$AVRO2" '{artifactId:"shape", firstVersion:{content:{content:$c, contentType:"application/json"}}}')" | cut -c1-400)"
show 'POST artifact into a missing group (auto creation on by default)' "$(json POST "/groups/$P-autogroup/artifacts" -d "$(jq -nc --arg c "$AVRO1" '{artifactId:"auto", firstVersion:{content:{content:$c, contentType:"application/json"}}}')" | cut -c1-200; printf ' / GET group: '; req GET "/groups/$P-autogroup" | cut -c1-200)"
show 'POST version with a bad version id (space)' "$(json POST "/groups/$P-search/artifacts/shape/versions" -d "$(jq -nc --arg c "$AVRO1" '{version:"bad id", content:{content:$c, contentType:"application/json"}}')")"
show 'PUT version state SUNSET directly from ENABLED' "$(json PUT "/groups/$P-search/artifacts/shape/versions/1/state" -d '{"state":"SUNSET"}')"
show 'GET version (VersionMetaData shape)' "$(req GET "/groups/$P-search/artifacts/shape/versions/1")"
show 'POST group rule + GET group rules list' "$(json POST "/groups/$P-search/rules" -d '{"ruleType":"VALIDITY","config":"SYNTAX_ONLY"}'; printf ' / list: '; req GET "/groups/$P-search/rules"; printf ' / GET: '; req GET "/groups/$P-search/rules/VALIDITY")"

# ------------------------------------------------------------------ cleanup
h "Cleanup"
for g in "$P-meta" "$P-last" "$P-rules" "$P-gdel" "$P-gdel2" "$P-br" "$P-search" "$P-autogroup"; do printf -- '- DELETE /groups/%s -> %s\n' "$g" "$(req DELETE "/groups/$g" | cut -c1-80)"; done
[ "$created_default" = 1 ] && printf -- '- DELETE /groups/default (created by this probe on this server) -> %s\n' "$(req DELETE /groups/default | cut -c1-80)"
req DELETE /admin/rules/INTEGRITY >/dev/null
left="$(req GET '/groups?limit=500' | sed 's/^[0-9]* //' | jq -c '[.groups[]?.groupId | select(startswith("probe-"))]' 2>/dev/null)"
printf -- '- groups left with prefix probe- (any run): %s\n' "$left"
[ "$left" = "[]" ] || exit 1
