---
page_title: "apicurio Provider"
description: |-
  Manage Apicurio Registry 3.x through its Core Registry API v3: groups, artifacts and their versions, branches, rules, role mappings and configuration properties.
---

# apicurio Provider

Manage an [Apicurio Registry](https://www.apicur.io/registry/) 3.x instance
from Terraform through its Core Registry API v3 (`/apis/registry/v3`).

The provider targets Apicurio Registry **3.0.15 and later** and is tested
against 3.0.15, 3.1.7, 3.2.6 and 3.3.4, with and without authentication.
The v2 API (Apicurio Registry 2.x) is not supported.

## Example Usage

```terraform
terraform {
  required_providers {
    apicurio = {
      source  = "jamalshahverdiev/apicurio"
      version = "~> 0.1"
    }
  }
}

# A registry without authentication.
provider "apicurio" {
  url = "http://localhost:8090"
}

# A registry secured with Keycloak, using the client credentials of a
# confidential OIDC client (apicurio.authn.basic-client-credentials.enabled).
#
# provider "apicurio" {
#   url      = "http://localhost:8093"
#   username = "registry-api"
#   password = var.registry_client_secret
# }
```

## Authentication

Configure at most one of the following; leave all of them unset for a registry
without authentication.

| Mode | Attributes | What the provider sends |
|---|---|---|
| none | – | nothing |
| static token | `token` | `Authorization: Bearer <token>` |
| HTTP basic / client credentials | `username`, `password` | `Authorization: Basic ...`. On a registry with `apicurio.authn.basic-client-credentials.enabled=true` the user name is an OIDC client id and the password its secret; the registry exchanges them for a token itself. |

With `validate_connection = true` (the default) the provider calls
`GET /system/info` while configuring, records the server version, and, when
credentials are configured, `GET /users/me`. Both endpoints are public on a
secured registry, and a failed HTTP basic login is answered as the anonymous
user rather than with a 401, so the provider treats an empty user name as
rejected credentials and fails the plan right away. A malformed or expired
bearer token is refused by every endpoint and reported as an authentication
error.

Without any credentials the provider configures successfully even against a
secured registry (the check only touches public endpoints); the first
protected operation then fails with HTTP 401. Likewise, a token whose user
has no role (for example a Keycloak user without a role mapping when
`apicurio.auth.role-source=application`) passes the check and gets HTTP 403
on content operations; `apicurio_current_user` (planned) shows the roles the
registry grants.

## Environment variables

Every attribute can come from the environment. A non-empty value in the
configuration wins; an empty string counts as unset. An authentication
attribute set in the configuration also hides the environment variables of the
other method, so an exported `APICURIO_USERNAME` does not interfere with a
block that configures `token`.

| Attribute | Environment variable |
|---|---|
| `url` | `APICURIO_URL` |
| `token` | `APICURIO_TOKEN` |
| `username` | `APICURIO_USERNAME` |
| `password` | `APICURIO_PASSWORD` |

## Deletions are disabled by default

Apicurio Registry 3.x refuses to delete groups, artifacts and versions until
the matching dynamic property is turned on; the API answers HTTP 405 and the
provider reports which property to set:

| Property | Environment variable form |
|---|---|
| `apicurio.rest.deletion.group.enabled` | `APICURIO_REST_DELETION_GROUP_ENABLED=true` |
| `apicurio.rest.deletion.artifact.enabled` | `APICURIO_REST_DELETION_ARTIFACT_ENABLED=true` |
| `apicurio.rest.deletion.artifact-version.enabled` | `APICURIO_REST_DELETION_ARTIFACT_VERSION_ENABLED=true` |

Deleting a group deletes the artifacts it contains even when artifact
deletion is disabled.

## Versions are immutable

Apicurio Registry never changes a stored version: every change of a schema
is a new version. `apicurio_artifact.content` follows that model: when it is
set, the provider keeps the artifact's latest version equal to the
configured document, creating a new version (numbered by the registry)
whenever the document changes, and reports `version`, `global_id` and
`content_id` of that version. Content is compared as a document: JSON and
YAML reformatting, line endings and trailing whitespace are not changes.
Without `content` the resource manages the artifact's metadata only.
A version created outside Terraform shows up as a diff of `content`, and the
next apply creates another version so that the latest one matches the
configuration again.

`apicurio_artifact_version` manages one explicit version per resource
instead: its content is immutable (a change replaces the version), drafts
(`draft = true`) can change in place and are promoted with `state`, and the
state of a version (`ENABLED`, `DISABLED`, `DEPRECATED`, `SUNSET`) is
managed there. Never manage the versions of one artifact both ways. The
`latest` branch, which `apicurio_artifact.content` and the default lookup of
the `apicurio_artifact_version` data source follow, skips drafts and disabled
versions.

## Rules

`apicurio_global_rule`, `apicurio_group_rule` and `apicurio_artifact_rule` configure the
`VALIDITY`, `COMPATIBILITY` and `INTEGRITY` rules. For one rule type the registry applies the
first rule it finds in this order: the rule on the artifact, the rule on its group, the global
rule. A rule with `config = "NONE"` counts as found, so an artifact rule set to `NONE` switches
the type off for that artifact even when a global rule exists. Rules are applied when a version
is created and when a draft leaves `DRAFT`, never to existing versions; make versions
`depends_on` the rules that must check them.

| Rule type | `config` |
|---|---|
| `VALIDITY` | `NONE`, `SYNTAX_ONLY`, `FULL` |
| `COMPATIBILITY` | `NONE`, `BACKWARD`, `BACKWARD_TRANSITIVE`, `FORWARD`, `FORWARD_TRANSITIVE`, `FULL`, `FULL_TRANSITIVE` |
| `INTEGRITY` | a comma-separated set of `REFS_EXIST`, `NO_DUPLICATES`, `NO_CIRCULAR_REFERENCES`, `ALL_REFS_MAPPED`; or `FULL` or `NONE` alone |

The registry stores any `config` string without checking it, and a stored value outside the
lists above breaks the next version creation with a server error; the provider therefore validates
the value at plan time. On a secured registry global rules need the admin role, group and artifact rules the
developer role.

Not every rule is implemented for every artifact type. The upstream maturity matrix, checked
against Apicurio Registry 3.0.15, 3.1.7, 3.2.6 and 3.3.4:

| Artifact type | `VALIDITY` | `COMPATIBILITY` | `INTEGRITY` |
|---|---|---|---|
| `AVRO`, `PROTOBUF`, `JSON`, `OPENAPI` | full | full | full |
| `XSD` | full | full since 3.1 (not enforced on 3.0) | full |
| `WSDL` | full | none | full |
| `ASYNCAPI` | full (3.0 reports fewer semantic problems) | none | full |
| `XML` | full | none | mapping detection not supported |
| `GRAPHQL`, `KCONNECT` | syntax only | none | mapping detection not supported |

A `COMPATIBILITY` rule on a type listed as "none" is accepted by the registry and never fails.

## Declaring the provider where the registry is not available

Set `validate_connection = false` when the configuration must plan on a
machine that cannot reach the registry (for example when the registry itself
is created by the same configuration). Resources and data sources still need
the registry when they are read or applied.

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `password` (String, Sensitive) HTTP basic password (or OIDC client secret, see `username`). Env: `APICURIO_PASSWORD`.
- `token` (String, Sensitive) Static bearer token sent as `Authorization: Bearer`. Conflicts with `username`/`password`. Env: `APICURIO_TOKEN`.
- `url` (String) Base URL of the registry, for example `http://localhost:8090`. The provider appends `/apis/registry/v3`; a URL that already ends with it is accepted. Env: `APICURIO_URL`.
- `username` (String) HTTP basic user name. On a registry with `apicurio.authn.basic-client-credentials.enabled=true` this is an OIDC client id and `password` its secret; the registry exchanges them for a token itself. Requires `password`. Env: `APICURIO_USERNAME`.
- `validate_connection` (Boolean) Check the registry while configuring the provider: `GET /system/info` (records the server version) and, when credentials are configured, `GET /users/me`, so a wrong URL or rejected credentials fail the plan right away. Defaults to `true`. Set it to `false` for configurations where the registry is not deployed yet.
