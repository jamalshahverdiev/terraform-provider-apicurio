# Local Apicurio Registry instances for real tests

Everything the provider does is exercised against real servers before a PR is
merged. This directory starts them.

| Service | Image | Host port | Profile | Purpose |
|---|---|---|---|---|
| `registry-33` | `apicurio/apicurio-registry:3.3.4` | 8090 | default | latest 3.x line, no authentication |
| `registry-30` | `apicurio/apicurio-registry:3.0.15` | 8091 | default | oldest supported 3.x line, no authentication |
| `keycloak` | `quay.io/keycloak/keycloak:26.4` | 8092 | `auth` | realm `registry` imported from [`keycloak/realm.json`](keycloak/realm.json) |
| `registry-33-auth` | `apicurio/apicurio-registry:3.3.4` | 8093 | `auth` | 3.3.4 secured by Keycloak, role-based authorization on |
| `registry-31` | `apicurio/apicurio-registry:3.1.7` | 8094 | `matrix` | compatibility pass before 1.0.0 |
| `registry-32` | `apicurio/apicurio-registry:3.2.6` | 8095 | `matrix` | compatibility pass before 1.0.0 |

Every registry runs on the in-memory H2 database. `docker compose down` loses
all data, which is what the tests want. The three deletions
(`apicurio.rest.deletion.{group,artifact,artifact-version}.enabled`) are turned
on through environment variables; they stay dynamic properties, so a test can
still switch them off through the API and watch the 405.

## Commands

```sh
cd docker-compose
docker compose up -d                                  # 8090 + 8091
docker compose --profile auth up -d                   # + Keycloak 8092 + secured registry 8093
docker compose --profile matrix up -d                 # + 8094 + 8095
docker compose --profile auth --profile matrix up -d  # everything

scripts/smoke.sh                                      # checks whatever is running
docker compose --profile auth --profile matrix logs -f registry-33-auth
docker compose --profile auth --profile matrix down   # stop and wipe
```

The API of every registry is at `http://localhost:<port>/apis/registry/v3`.
The health endpoint is on the management port (9000, not published) on the
3.3 line and on 8080 on the 3.0 line; the healthchecks in `compose.yaml`
accept either, and `smoke.sh` judges readiness on the API itself.

## Keycloak: realm `registry`

| What | Value |
|---|---|
| Admin console | <http://localhost:8092> , `admin` / `admin` (master realm) |
| Realm roles | `sr-admin`, `sr-developer`, `sr-readonly` |
| Confidential client | `registry-api`, secret `registry-api-secret`, service account with the realm role `sr-admin` |
| Public client | `apicurio-registry`, password grant enabled (test scripts only) |
| Users | `admin` / `admin` (`sr-admin`), `developer` / `developer` (`sr-developer`), `user` / `user` (`sr-readonly`) |
| Token endpoint | `http://localhost:8092/realms/registry/protocol/openid-connect/token` |

```sh
scripts/get-token.sh              # client credentials of registry-api
scripts/get-token.sh developer    # password grant of a user (password == username)
```

### Why the hostname settings look the way they do

The provider fetches tokens from the host (`http://localhost:8092`); the
registry verifies them from inside the compose network (`http://keycloak:8092`).
Both sides must agree on the issuer, so:

* `KC_HOSTNAME=http://localhost:8092` fixes the `iss` claim and the `issuer` of
  the discovery document to the host URL no matter where a request comes from;
* `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true` makes the token and JWKS endpoints of
  the discovery document follow the request host, so the registry reaches them
  at `http://keycloak:8092`;
* `--http-port=8092` keeps the same port inside and outside, so the two URLs
  differ only in the host name.

## The secured registry (8093)

* Authentication: OIDC bearer tokens of the realm, and HTTP basic where the
  username and password are an OIDC client id and secret
  (`apicurio.authn.basic-client-credentials.enabled=true`; the registry
  exchanges them for a token at Keycloak).
* Authorization: `apicurio.auth.role-based-authorization=true` with
  `apicurio.auth.role-source=application`, so roles come from the registry's
  own role mappings (`/admin/roleMappings`), not from the token. The admin
  override lets anyone whose token carries the realm role `sr-admin` in as an
  admin: the user `admin` and the `registry-api` service account. `developer`
  and `user` get `403` until an `apicurio_role_mapping` gives them a role.
* `GET /system/info` and `GET /users/me` are public even here (the server
  marks them `AuthorizedLevel.None`): both answer 200 without credentials, and
  HTTP basic with a wrong client id or secret is treated as the anonymous user
  (200, empty `username`). A bad bearer token is 401 everywhere, including
  `/system/info`; protected endpoints answer 401 for anonymous callers. The
  provider's connection check therefore requires a non-empty `username` from
  `/users/me` when credentials are configured, not just a 200.

### Provider configurations against this stack

```hcl
# open registries
provider "apicurio" { url = "http://localhost:8090" }   # 3.3.4
provider "apicurio" { url = "http://localhost:8091" }   # 3.0.15

# secured registry: static token
provider "apicurio" {
  url   = "http://localhost:8093"
  token = var.token   # scripts/get-token.sh
}

# secured registry: basic client credentials
provider "apicurio" {
  url      = "http://localhost:8093"
  username = "registry-api"
  password = "registry-api-secret"
}

# secured registry: OIDC client credentials, token fetched and refreshed by the provider
provider "apicurio" {
  url = "http://localhost:8093"
  oidc {
    token_url     = "http://localhost:8092/realms/registry/protocol/openid-connect/token"
    client_id     = "registry-api"
    client_secret = "registry-api-secret"
  }
}
```

## Scripts

| Script | What it does |
|---|---|
| `scripts/smoke.sh` | waits for the running services, checks versions, deletion flags, a group round trip, and on the `auth` profile every authentication mode plus the role-mapping behaviour; exit code 0 only when everything passed |
| `scripts/get-token.sh` | prints an access token (client credentials, or password grant of a realm user) |
| `scripts/probe-api.sh` | records the server behaviours the plan lists as "to verify on a live server" (section 11); run against 8090 and 8091, results in [`LIVE-SERVER-FINDINGS.md`](LIVE-SERVER-FINDINGS.md) |
