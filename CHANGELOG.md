# Changelog

All notable changes to this provider are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

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
