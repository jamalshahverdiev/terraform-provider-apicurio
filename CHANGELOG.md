# Changelog

All notable changes to this provider are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-09

### Added

- `apicurio_global_rule`, `apicurio_group_rule` and `apicurio_artifact_rule`
  resources: the `VALIDITY`, `COMPATIBILITY` and `INTEGRITY` rules at the three
  levels, `config` validated against `rule_type` at plan time (the registry
  stores any string), in-place config updates, import by `RULE_TYPE`,
  `group_id/RULE_TYPE` and `group_id/artifact_id/RULE_TYPE`. The provider
  documentation carries the rule precedence and the maturity matrix per
  artifact type.
- `apicurio_artifact_version` resource: explicit, immutable versions with
  `version` (or server numbering), `content`/`content_type`/`references`, name,
  description and labels, `draft = true` for drafts whose content changes in
  place and whose promotion (`state = "ENABLED"`) applies the rules, `state`
  transitions (`ENABLED`, `DISABLED`, `DEPRECATED`, `SUNSET` on 3.3+), import by
  `group_id/artifact_id/version`. A content change of a non-draft version
  replaces it.
- `apicurio_branch` resource: description and the ordered `versions` list
  (replaced as a whole, validated against the artifact's versions), import by
  `group_id/artifact_id/branch_id`; the system branches are refused.
- `apicurio_artifact_version` data source: one version by id or `branch=...`
  expression (default `branch=latest`), with `references_handling`.
- Client: version metadata, state and draft content updates; branches.
- `apicurio_group` resource: description, labels, owner, `force_destroy`
  (refuses to delete a group that still contains artifacts otherwise), import
  by group id, a clear error when the group already exists (groups are also
  created implicitly by their first artifact) and when deletion is disabled.
- `apicurio_artifact` resource: metadata (name, description, labels, owner),
  `artifact_type` validated against the registry's type list or detected from
  the content, and optionally `content`/`content_type`/`references` managing
  the latest version: the first version on create, a new version whenever the
  content stops being the same document (reformatting is not a change), rule
  violations reported with their causes, `version`/`global_id`/`content_id`
  computed. Import by `group_id/artifact_id`.
- `apicurio_group` and `apicurio_artifact` data sources (the latter with the
  latest version's content and references).
- Client: groups, artifacts, versions (content, references), rules and
  configuration properties.
- Provider configuration: `url`, `token`, `username`/`password` (HTTP basic;
  on a registry with `apicurio.authn.basic-client-credentials.enabled` these
  are an OIDC client id and secret), `validate_connection`, and the
  `APICURIO_URL`, `APICURIO_TOKEN`, `APICURIO_USERNAME`, `APICURIO_PASSWORD`
  environment variables. The connection check reads `/system/info` and, with
  credentials, `/users/me`; the anonymous answer the registry gives to a failed
  basic login is reported as rejected credentials.
- `apicurio_system_info` data source (`name`, `description`, `version`,
  `built_on`).
- HTTP client for the Core Registry API v3 with typed errors: object not found
  (only for the specific `*NotFoundException` names, so a wrong URL is never
  mistaken for a deleted object), already exists, deletion disabled (naming the
  `apicurio.rest.deletion.*` property to enable), rule violations with their
  causes (HTTP 400 on 3.3, 409 before, 422 for unparsable content), and
  offset/limit paging.
- Local test stack in `docker-compose/`: Apicurio Registry 3.3.4, 3.0.15,
  3.1.7, 3.2.6 and a Keycloak-secured 3.3.4, with smoke and probe scripts and
  the recorded live-server behaviours the client relies on.
