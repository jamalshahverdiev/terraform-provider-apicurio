# What the live servers actually do

Answers to section 11 of `PLAN-apicurio.md` ("to verify on a live server before
coding the resource that depends on it"), plus the response shapes the client
code relies on. Recorded on 2026-11-09 with `scripts/probe-api.sh` against
Apicurio Registry 3.3.4 (8090), 3.0.15 (8091), 3.1.7 (8094), 3.2.6 (8095) and
the Keycloak-secured 3.3.4 (8093). Unless a version is named, every version
behaved the same.

The raw transcripts can be regenerated at any time:

```sh
scripts/probe-api.sh http://localhost:8090 > /tmp/findings-3.3.4.md
TOKEN="$(scripts/get-token.sh)" scripts/probe-api.sh http://localhost:8093 > /tmp/findings-auth.md
```

## 1. Editing metadata (`PUT` of artifact, group, version)

* A property that is **present** is written, including the empty string:
  `{"name": ""}` stores `""`, `{"labels": {}}` removes every label,
  `{"owner": "someone"}` changes the owner. A property that is **`null`** is
  treated as absent (nothing changes). `{}` is accepted (204) and changes nothing.
* **3.3.4, 3.2.6, 3.1.7**: properties that are omitted keep their value
  (`PUT {"description": "x"}` leaves `name` and `labels` alone).
* **3.0.15 (groups only)**: `PUT /groups/{g}` replaces the whole editable set.
  `PUT {"description": ""}` also removed the labels; `PUT {"labels": {}}` also
  removed the description. Artifact and version `PUT` kept omitted properties
  on 3.0.15 too.
* Consequence for the provider: **always send the complete editable set**
  (`name`, `description`, `labels`, and `owner` when configured) on every
  update, never a partial body. Then every version behaves identically and
  "clear" is simply the empty string / empty map. There is no way to clear
  `owner` (an empty owner is written as `""`, which the server shows as `""`).
* On the open registries `owner`, `modifiedBy` and `createdBy` are `""`. On the
  secured registry they carry the principal (`service-account-registry-api`),
  so `owner` must be Optional+Computed and not compared when unset.
* **3.0.15 does not store the owner of a group**: `PUT /groups/{g}` with
  `{"owner": "x"}` answers 204 and the group keeps its old owner. 3.1.7 and
  later store it. The artifact update stores the owner on every version
  (verified on 3.0.15 and 3.3.4). The provider reports a clear error when a
  configured group owner is not stored.
* `{"owner": ""}` is refused with 400 `MissingRequiredParameterException:
  Request is missing a required parameter: Owner cannot be empty` (groups and
  artifacts, every version).

## 2. Deleting the last version

* `DELETE .../versions/1` on an artifact with a single version answers 204 and
  **the artifact survives**: `GET .../artifacts/{a}` is 200, the versions list is
  `{"count":0,"versions":[]}`, `GET .../versions/branch=latest` is
  404 `VersionNotFoundException`.
* An artifact created without `firstVersion` looks exactly like that (200
  metadata, empty versions, `branch=latest` 404, branches list empty).
* So `apicurio_artifact` with `content` must treat a 404 of `branch=latest` as
  "no content yet", not as a deleted artifact, and `apicurio_artifact_version`
  can delete its version without side effects on the artifact.

## 3. Role mappings

* On a registry **without** `apicurio.auth.role-source=application`, every
  `/admin/roleMappings` call answers **403**
  `ForbiddenException: Application RBAC not enabled.` (3.0.15 answers 403 with
  an empty body). The resource must turn that into a clear error naming the
  two server properties; it cannot be tested on the open registries.
* On the secured registry (8093, role source `application`):
  * `GET /admin/roleMappings` → `{"roleMappings":[],"count":0}`.
  * `POST` for a new principal → 204.
  * `POST` for an existing principal → **409 `RoleMappingAlreadyExistsException`**
    (the message quotes the *new* role: "A mapping for principal 'x' and role
    'READ_ONLY' already exists."). The existing mapping is not changed.
  * `GET /admin/roleMappings/{p}` → `{"principalId","role","principalName"}`.
  * `PUT /admin/roleMappings/{p}` with `{"role": "ADMIN"}` → 204; an unknown role
    → 400 `BadRequestException: Not able to deserialize data provided.`
  * `PUT` on an unmapped principal → 404 `RoleMappingNotFoundException`.
  * `DELETE` → 204; a second `DELETE` and a `GET` → 404 `RoleMappingNotFoundException`.
* A user whose token carries no mapped role gets 403 on every content endpoint
  (`ForbiddenException: User is not authorized to perform the requested
  operation.`), but still 200 on `GET /users/me`, which then shows
  `admin/developer/viewer` all `false`. After `POST {"principalId":"user",
  "role":"READ_ONLY"}` the same user reads (200) and still cannot write (403).
* With the admin override (`apicurio.auth.admin-override.enabled=true`,
  type `role`, role `sr-admin`) the `registry-api` service account is admin
  without any mapping, which is what the provider's own credentials rely on.

## 4. The `name` of 404 bodies

| Missing object | `name` | Message |
|---|---|---|
| group | `GroupNotFoundException` | `No group 'x' was found.` |
| artifact (group exists or not) | `ArtifactNotFoundException` | `No artifact with ID 'a' in group 'g' was found.` |
| version (id or `branch=` expression) | `VersionNotFoundException` | `No version '1' found for artifact with ID 'a' in group 'g'.` / `No version '<tip of the branch 'x'>' ...` |
| version content | `VersionNotFoundException` | same |
| rule (global, group, artifact) | `RuleNotFoundException` | `No rule named 'VALIDITY' was found.` |
| group rule, group missing | `GroupNotFoundException` | |
| artifact rule, artifact missing | `ArtifactNotFoundException` | |
| branch | `BranchNotFoundException` | `No branch 'x' was found in g/a.` |
| role mapping (secured registry) | `RoleMappingNotFoundException` | `No role mapping for principal 'x' was found.` |
| config property, unknown **or known but not dynamic** | `ConfigPropertyNotFoundException` | `No configuration property named 'x' was found.` |
| unknown rule type in the path (`/admin/rules/BOGUS`) | `NotFoundException` | `RESTEASY003870: Unable to extract parameter ...` |
| unknown route under `/apis/registry/v3` | `NotFoundException` | `RESTEASY003210: Could not find resource for full path: ...` |
| unknown route outside the API | 404, JSON body | |

Consequence: `ErrNotFound` must be derived from the **specific** names
(`GroupNotFoundException`, `ArtifactNotFoundException`, `VersionNotFoundException`,
`RuleNotFoundException`, `BranchNotFoundException`, `RoleMappingNotFoundException`,
`ConfigPropertyNotFoundException`). A bare `NotFoundException` is a wrong URL
or an invalid enum in the path and must stay an error, otherwise a typo in the
base URL would silently remove every resource from state.

## 5. Rule violations

* Body shape (every version): `{"causes":[{"description","context"}],
  "detail","title","status","name":"RuleViolationException"}`. The causes are
  **top level**, there is no `ruleViolation` wrapper and no `ruleType` field;
  the rule can be inferred from `title`/`detail` ("Incompatible artifact ...",
  "Syntax violation for Avro artifact.", "Referenced artifact does not exist.").
  `causes` may be empty (syntax violations).
* **Status differs by version**: 3.3.4 answers **400**, 3.2.6 / 3.1.7 / 3.0.15
  answer **409**. Map on `name == "RuleViolationException"`, never on the status.
* Examples recorded:
  * COMPATIBILITY BACKWARD, Avro field added without a default:
    `causes: [{"description":"vat","context":"/fields/3"}]`,
    detail `Incompatible artifact: comp [AVRO], num of incompatible diffs: {1}, list of diff types: [vat at /fields/3] Causes: vat at /fields/3`.
  * VALIDITY FULL, unparsable Avro: `causes: []`, detail `Syntax violation for Avro artifact.`
  * INTEGRITY FULL, reference to a missing artifact: `causes: [{"description":"Referenced artifact (g/missing @ 1) does not yet exist in the registry.","context":"probe.Price"}]`.
* When a COMPATIBILITY rule is configured and the new content does not even
  parse, the answer is **422 `UnprocessableSchemaException`** ("Could not execute
  compatibility rule on invalid Avro schema"), not a `RuleViolationException`.
  The client should present 422 as a content error too.
* `PUT .../rules/{type}` accepts **any** config string (`"SIDEWAYS"` was stored
  and echoed back with 200). Validation of `config` against `rule_type` has to
  happen in the provider; the server will not refuse it. More rule API facts
  (identical on 3.0.15 and 3.3.4): `POST` with an unknown `ruleType` is 400
  `BadRequestException: Not able to deserialize data provided.` (3.0.15 sends
  that as a plain-text body); a missing or empty `config` is 400
  `MissingRequiredParameterException: Request is missing a required parameter:
  config`; `PUT .../rules/{type}` ignores the `ruleType` of the body (the path
  wins) and echoes the stored rule; an `INTEGRITY` config is stored verbatim,
  spaces included (`"REFS_EXIST, NO_DUPLICATES"`); `GET`/`PUT`/`DELETE` of an
  unconfigured rule is 404 `RuleNotFoundException`; `DELETE .../rules` (no
  type) removes every rule of the scope (204); group rules need the group to
  exist (`POST`/`GET` on a missing group is 404 `GroupNotFoundException`, no
  implicit creation), artifact rules the artifact (404
  `ArtifactNotFoundException`), an artifact without versions accepts rules;
  deleting the group or the artifact deletes its rules (a recreated group has
  none).
* A stored config outside the enum (`full`, `SIDEWAYS`, `refs_exist`) is
  accepted by `POST`/`PUT` but breaks the next `POST .../artifacts` or
  `.../versions` with a **500** `IllegalArgumentException: No enum constant
  io.apicurio.registry.rules.validity.ValidityLevel.full` (same for the other
  enums), so the provider's plan-time validation is the only guard. The
  server trims the tokens of an `INTEGRITY` set, so `"REFS_EXIST, NO_DUPLICATES"`
  is enforced like the unspaced form.
* Avro `VALIDITY SYNTAX_ONLY` is the Avro parser: a record without `fields`
  is refused like under `FULL` (`Syntax violation for Avro artifact.`); only
  `NONE`/no rule accept it. `VALIDITY FULL` on `ASYNCAPI` is semantic on
  every version (`The AsyncAPI artifact is not semantically valid`, e.g. a
  missing `info.version`; unresolved message `$ref`s are flagged from 3.1).
  `GRAPHQL` validity stays syntax-only (an unknown type is accepted).
* Compatibility enforcement per type (BACKWARD rule, breaking change, dry
  run): `OPENAPI` and `JSON` are refused on every version, `XSD` on 3.1.7, 3.2.6
  and 3.3.4 but not on 3.0.15, `ASYNCAPI` is never refused.
* Removing an Avro field, or adding one with a default, is BACKWARD compatible
  (test fixtures must add a field *without* a default to trigger a violation).
* `dryRun=true` on `POST .../versions` runs the rules and returns the version
  metadata that would be created (even a `version` id) **without storing it**:
  the versions count did not change. Usable for a plan-time check.

### Drafts

* `isDraft: true` creates the version in state `DRAFT` **without applying any
  rule** (unparsable content is accepted). Works on 3.0.15 too; the three
  deletion flags plus `apicurio.rest.mutability.artifact-version-content.enabled`
  are the only switches needed, and the compose sets them.
* `PUT .../versions/{v}/content` is allowed only while `DRAFT`; on an `ENABLED`
  version it answers 409 `ConflictException: Requested artifact version is not
  in DRAFT state. Update disallowed.` No rules are applied to the draft content.
* `PUT .../state {"state":"ENABLED"}` from `DRAFT` applies the rules with
  `RuleApplicationType.UPDATE`. The comparison target is the **newest version
  by creation order that is not `DISABLED`, drafts included** (verified on
  3.0.15 and 3.3.4: a newer incompatible draft refuses the promotion, a newer
  `DISABLED` version is skipped in favour of the one before it, a `DEPRECATED`
  one is compared). It is **not** the tip of the `latest` branch: a promoted
  draft joins `latest` at promotion time and becomes its tip, but a later
  rule check still compares with the newest version by creation. When the
  draft itself is the newest version it is compared with itself, so
  incompatible content gets enabled. When a newer version exists, the
  transition fails: 400 (409 before 3.3) `RuleViolationException` for
  compatibility, 422 `UnprocessableSchemaException` for unparsable content,
  and the version stays `DRAFT`. The documentation of `draft = true` must say
  that rules are only effective against a newer version. `POST .../versions`
  uses the same target, so a new version is compared with the newest
  non-disabled version, which may be a draft.
* `PUT .../state?dryRun=true` is accepted (204) and does not change the state.
* A draft does not use the real content hash: on 3.2.6 and 3.3.4 promoting a
  draft (any state other than `DRAFT`) changes its `contentId` to the
  deduplicated id of its bytes (`globalId` stays); 3.0.15 and 3.1.7 keep the
  draft's `contentId`. Updating a draft's content changes its `contentId` and
  `modifiedOn`, never its `globalId`. Any state change updates the version's
  `modifiedOn`.
* `DRAFT` can move to `DISABLED`, `DEPRECATED` and `SUNSET` directly, not
  only to `ENABLED` (the plan assumed otherwise). No state can move back to
  `DRAFT`.
* The content of a `DISABLED` version is not served: `GET .../content` is
  404 `VersionNotFoundException` while the metadata stays readable; the
  references endpoint answers `[]`. `branch=latest` skips drafts and
  disabled versions (its tip is the newest `ENABLED`/`DEPRECATED`/`SUNSET`
  version), while the `latest` branch listing contains every non-draft
  version in the order they joined it: a promoted draft is appended at
  promotion and becomes the tip (`[d, 3, 1]` after promoting `d`, created
  between `1` and `3`). The system branches are created lazily: `drafts`
  appears with the first draft (and stays after it is promoted), `latest`
  with the first non-draft version; before that `GET .../branches/{b}` and
  `.../versions` are 404 `BranchNotFoundException` and the listing omits
  them.
* `PUT .../versions/{v}/content` with
  `apicurio.rest.mutability.artifact-version-content.enabled=false` is 405
  `NotAllowedException: Artifact version content update operation is not
  enabled.`; on a non-draft it is 409 `ConflictException`.

### Other state transitions

* `ENABLED → DEPRECATED → ENABLED → DISABLED` all 204. Any state `→ DRAFT`:
  400 `BadRequestException: Illegal state transition: cannot transition to DRAFT state.`
* `SUNSET`: 3.3.4 accepts it from `DEPRECATED` **and directly from `ENABLED`**
  (204). 3.2.6 / 3.1.7 / 3.0.15 answer 400 `Not able to deserialize data
  provided.` (unknown enum). The provider gates `SUNSET` on server version ≥ 3.3.
* A `PUT state` with the current state is a no-op 204.

## 6. Group deletion with artifacts inside

* With `apicurio.rest.deletion.group.enabled=true` and
  `apicurio.rest.deletion.artifact.enabled=false`, `DELETE /groups/{g}` on a
  group that contains artifacts answers **204 and deletes the artifacts too**.
  The artifact flag only guards `DELETE .../artifacts/{a}`. `force_destroy`
  on `apicurio_group` is therefore a provider-side guard, not a server one.
* 405 bodies: `NotAllowedException` with `Artifact deletion operation is not
  enabled.`, `Artifact version deletion operation is not enabled.`,
  `Group deletion operation is not enabled.`
* Dynamic properties: `PUT /admin/config/properties/{name}` with `{"value":"false"}`
  takes effect immediately; `DELETE` resets to the configured default (the
  environment variable value, `true` in the compose), not to the shipped
  default. A non-boolean value for a boolean property → 400
  `InvalidPropertyValueException`, the stored value is unchanged.
* `GET /admin/config/properties` lists on 3.3.4: `apicurio.ccompat.legacy-id-mode.enabled`,
  `apicurio.ccompat.max-subjects`, `apicurio.ccompat.use-canonical-hash`,
  `apicurio.download.href.ttl.seconds`, `apicurio.log.level`,
  `apicurio.rest.compression.enabled`, the three deletion flags,
  `apicurio.rest.draft.production-mode.enabled`,
  `apicurio.rest.mutability.artifact-version-content.enabled`,
  `apicurio.semver.branching.enabled`, `apicurio.semver.validation.enabled`,
  `apicurio.storage.read-only.enabled`, `apicurio.ui.features.agents.enabled`.
  3.0.15 lists the same minus `log.level`, `rest.compression.enabled`,
  `rest.draft.production-mode.enabled`, `ui.features.agents.enabled`
  (3.1.7 has `log.level`; 3.2.6 adds `draft.production-mode` and `ui.features.agents`).
  The three deletion properties are dynamic on every version. Each entry has
  `name`, `value` (string), `type` (`java.lang.Boolean`, `java.lang.Integer`,
  `java.lang.String`, `java.lang.Long`), `label`, `description`.
* `apicurio.storage.enable-automatic-group-creation` is **not** a dynamic
  property (404 on GET/PUT); it can only be set through the environment.

## 7. Branches

* `GET .../branches/{b}/versions` returns the versions **newest-added first**
  (the reverse of the order given): created with `["3","1"]` → `["1","3"]`,
  after `POST {"version":"2"}` → `["2","1","3"]`, after
  `PUT {"versions":["2","3","1"]}` → `["1","3","2"]`. The `latest` branch lists
  `["3","2","1"]` for versions created 1, 2, 3. The provider compares the
  reversed server list with the configured list.
* `PUT .../branches/{b}/versions` takes **`{"versions": [...]}`**, not a bare
  array (a bare array is 400 `Not able to deserialize data provided.`).
  `{"versions": []}` is accepted (204) and empties the branch. `POST
  .../branches` with a version that does not exist is 404
  `VersionNotFoundException` and creates nothing (only the `PUT` replace is a
  500). `PUT .../branches/{b}` with `{"description": ""}` clears the
  description. `POST .../versions` with `"branches": ["x"]` creates branch
  `x` when needed and appends the version. Deleting a version removes it
  from every branch.
* A version that does not exist, or a duplicate in the list, is a **500**
  (`JdbcSQLIntegrityConstraintViolationException`), on every version. The
  provider validates existence and uniqueness itself before calling `PUT`.
* `GET .../versions/branch={b}` on an empty branch → 404 `VersionNotFoundException`
  ("tip of the branch").
* System branches: the list shows `latest` with `systemDefined: true` (and
  `drafts` once a draft exists). `DELETE .../branches/latest` → 409
  `NotAllowedException: System generated branches cannot be deleted.`;
  `POST` a branch named `latest` → 409 `BranchAlreadyExistsException`.
* `POST .../branches` with an invalid id → 400 `ValidationException: Branch ID
  'bad id' is invalid. It must consist of alphanumeric characters or '._-+',
  and have length 1..256 (inclusive).` Same text for version ids.
* `BranchMetaData` on 3.0.15 lacks `groupId` and `artifactId`; 3.1.7 and later
  include them. The branches list has `branches` and `count`.

## 8. Search

* `labels=env:prod` matches artifacts with that exact label pair;
  `labels=env` (key only) matches every artifact that has the key;
  two `labels=` parameters are ANDed (`env:prod` + `team:payments` → one hit).
  `labels=env:nope` → empty list. Same on every version.
* `name=` on 3.3.4 is an **exact, case-sensitive** match (`Prices` did not
  match `Prices dev`, `Pric` and `prices` matched nothing; adding
  `nameCaseInsensitive=true` still matched nothing in this probe). On 3.0.15
  `name=` is a **case-sensitive substring** match (`Pric` matched both
  `Prices` and `Prices dev`). `description=` is a substring match on every
  version (`about` matched all three). `artifactId=` is exact. The data source
  documents that the text filters are evaluated by the server and that the
  `name` semantics changed between 3.0 and 3.3.
* `GET /search/artifacts` entries: `groupId`, `artifactId`, `name`,
  `artifactType`, `labels`, `owner`, `createdOn`, `modifiedOn`, `modifiedBy`,
  plus `count` at the top level. `GET /groups?limit=2&offset=0` returns
  `groups` and the total `count`.
* `POST /groups {"groupId":"default"}`: 3.1.7 and later answer 400
  `BadRequestException: The group name 'default' is reserved and cannot be
  used.` and never list `default`. **3.0.15 accepts it (200) and creates a real
  `default` group** that then shows up in `GET /groups` (a second `POST` is
  409 `GroupAlreadyExistsException`). The provider refuses `default` as a
  `group_id` on every version, so the difference never reaches a user.
* `GET /search/groups?groupId=x` is an exact match on every version, not a
  prefix search.

## 9. Shapes and behaviours the client relies on

* `POST /groups/{g}/artifacts` → 200 `{"artifact": ArtifactMetaData, "version": VersionMetaData}`;
  `version` is absent when no `firstVersion` was sent. `ifExists=CREATE_VERSION`
  on an existing artifact returns the same shape with the new version.
* `POST /groups/{g}/artifacts` into a missing group creates the group
  (automatic group creation is on by default on every version tested).
* `VersionMetaData`: `version`, `globalId`, `contentId`, `state`, `artifactType`,
  `groupId`, `artifactId`, `owner`, `createdOn`, `modifiedBy`, `modifiedOn`,
  optional `name`, `description`, `labels`.
* `GET .../versions/{v}/content` returns the raw bytes with the stored
  `Content-Type` (`application/json` for Avro, `application/x-yaml` for a YAML
  OpenAPI document, `text/plain;charset=UTF-8` for text/plain). **Content is
  deduplicated by hash across the whole registry**: a version created with the
  same bytes under another `contentType` gets a new version id but the same
  `contentId`, and `GET .../content` reports the media type of the first
  upload of those bytes. The provider therefore keeps a configured
  `content_type` in state as the upload hint it is. The default `references`
  handling is `PRESERVE`;
  `DEREFERENCE` inlines a referenced Avro record when the content uses it by
  name; `REWRITE` returns the content unchanged for Avro (it rewrites URL
  references of OpenAPI/JSON Schema).
* `GET .../versions/{v}/references` → `[{"groupId","artifactId","version","name"}]`.
* A reference to a missing artifact is accepted (200) unless an INTEGRITY
  rule is configured (then `RuleViolationException`, see 5).
* `artifactType` detection works for Avro, Protobuf and YAML OpenAPI content.
  A **wrong declared type is accepted** (Protobuf text stored as `JSON`, 200)
  when no VALIDITY rule is configured: the server does not validate content
  against the type by default.
* An unknown `artifactType` → 3.3.4: **500 `IllegalStateException: No such
  artifact type provider: COBOL`**; 3.2.6 and earlier: 400
  `InvalidArtifactTypeException`. The provider validates the type against
  `GET /admin/config/artifactTypes` before creating.
* `GET /admin/config/artifactTypes` → `[{"name":"AVRO"}, ...]`. 3.0.15 has the
  ten classic types; 3.1.7 adds `AGENT_CARD`, `MODEL_SCHEMA`, `PROMPT_TEMPLATE`;
  3.2.6 adds `ICEBERG_TABLE`, `ICEBERG_VIEW`, `OPENRPC`; 3.3.4 adds
  `MCP_TOOL`, `ODCS_CONTRACT`, `THRIFT`.
* Version and branch ids: `[a-zA-Z0-9._\-+]{1,256}`, enforced with 400
  `ValidationException`. A version id numbered by the server is
  `max(existing numeric ids) + 1`, so ids are reused after deletions (after
  deleting every version the next one is `1` again).
* Type detection depends on the `contentType` hint: `application/x-protobuf`,
  `application/x-yaml`, `application/graphql`, `application/xml` and
  `application/json` enable the matching detectors; with `text/plain` Protobuf,
  GraphQL and YAML content answer 400 `InvalidArtifactTypeException: Failed to
  discover artifact type from content.` Content whose named types live in other
  artifacts (`references`) cannot be detected either: send `artifactType`.
* `GET /users/me` on an open registry → `{"username":"","displayName":"",
  "admin":true,"developer":false,"viewer":false}` (anonymous is admin when
  authorization is off).
* Lists: `GET .../versions` returns `{"count", "versions": [...]}` in creation
  order; branches `{"branches", "count"}`; groups `{"groups", "count"}`;
  role mappings `{"roleMappings", "count"}`; `GET .../rules` → `["VALIDITY", ...]`.
* 409 names: `GroupAlreadyExistsException`, `ArtifactAlreadyExistsException`
  (`ifExists=FAIL`), `VersionAlreadyExistsException`, `RuleAlreadyExistsException`,
  `BranchAlreadyExistsException`, `RoleMappingAlreadyExistsException`.
* 400 names seen: `BadRequestException` (reserved group id, bad JSON/enum,
  illegal state transition), `ValidationException` (ids),
  `InvalidPropertyValueException`, `InvalidArtifactTypeException` (≤ 3.2).

## 10. Authentication (secured 3.3.4 at 8093, Keycloak 26.4 at 8092)

* Keycloak with `KC_HOSTNAME=http://localhost:8092`,
  `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` and `--http-port=8092` issues tokens
  with `iss = http://localhost:8092/realms/registry` whichever host asked, and
  the registry (configured with `QUARKUS_OIDC_AUTH_SERVER_URL=http://keycloak:8092/realms/registry`)
  accepts them. No `quarkus.oidc.token.issuer` override was needed.
* `GET /system/info` is public even on the secured registry (the server marks
  it `AuthorizedLevel.None`): it answers 200 without a token. `GET /users/me`
  is also `None`-level and answers 200 for an anonymous caller with
  `username: ""`. Protected endpoints answer 401
  `UnauthorizedException: User is not authenticated.` without a token.
* A garbage bearer token → 401 everywhere (including `/users/me`).
* **Basic client credentials**: `registry-api:registry-api-secret` → 200 and
  `/users/me` shows the service account as admin. A **wrong secret is not a
  401 on its own**: the registry logs "OIDC token request returned 401" and
  continues as the anonymous user, so `/system/info` and `/users/me` answer
  200 (with an empty `username`) and every protected endpoint answers 401.
  The provider's connection check must therefore require a non-empty
  `username` from `/users/me`, not just a 200.
* Basic with an unknown client id, with a user's password, or with the public
  client behaves the same way (anonymous).
* Role source `application`: a realm user with `sr-developer` in the token but
  no mapping is treated as having no role (403 on content, 200 on `/users/me`
  with everything `false`).
* The service account gets admin through the admin override on the realm role
  `sr-admin` in its token; the `admin` user too.

## 11. Health endpoints

* 3.3.4 (Quarkus 3.33): `/health/ready` is served on the **management port
  9000** only; `/health/ready` on 8080 is 404. 3.0.15: `/health/ready` on 8080.
  The compose healthchecks try 9000 then 8080; the provider never needs the
  health endpoint (it uses `/system/info`).
