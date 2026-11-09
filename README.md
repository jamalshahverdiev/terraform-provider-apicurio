# terraform-provider-apicurio

Terraform provider for [Apicurio Registry](https://www.apicur.io/registry/)
3.x, built with the
[Terraform Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).
It talks to the Core Registry API v3 (`/apis/registry/v3`) and is tested
against real registries (3.0.15, 3.1.7, 3.2.6, 3.3.4) with and without
Keycloak.

Status: **0.1.0 in development**. See [`CHANGELOG.md`](CHANGELOG.md) for what
is there and [`PLAN-apicurio.md`](PLAN-apicurio.md) for the roadmap.

## Provider configuration

```hcl
terraform {
  required_providers {
    apicurio = {
      source  = "jamalshahverdiev/apicurio"
      version = "~> 0.1"
    }
  }
}

provider "apicurio" {
  url = "http://localhost:8090"          # or APICURIO_URL

  # one of, or none for an open registry:
  # token    = var.token                 # or APICURIO_TOKEN
  # username = "registry-api"            # or APICURIO_USERNAME (OIDC client id with
  # password = var.client_secret         # or APICURIO_PASSWORD  basic client credentials)

  # validate_connection = false          # when the registry is not deployed yet
}
```

Generated reference with examples: [`docs/`](docs/).

## Resources

- `apicurio_group`: a group with description, labels and owner; `force_destroy`
  guards against deleting a group that still contains artifacts.
- `apicurio_artifact`: an artifact's metadata and, with `content`, its latest
  version (a new version for every change of the document; reformatting is
  not a change), with `references` to other artifact versions.
- `apicurio_artifact_version`: one explicit, immutable version (GitOps
  style), with drafts, state transitions and metadata.
- `apicurio_branch`: an ordered list of versions.
- `apicurio_global_rule`, `apicurio_group_rule`, `apicurio_artifact_rule`: the
  `VALIDITY`, `COMPATIBILITY` and `INTEGRITY` rules at the three levels, with
  the `config` validated per rule type.

Role mappings and configuration properties follow the phases of the plan.

## Data sources

- `apicurio_system_info`: name, description, version and build time of the registry.
- `apicurio_group`: metadata of a group.
- `apicurio_artifact`: metadata and latest version (content, references) of an artifact.
- `apicurio_artifact_version`: one version by id or branch expression.

## Development

Requirements: Go (the version in `go.mod`; with [gvm](https://github.com/moovweb/gvm):
`gvm install go1.25.8 -B && gvm use go1.25.8`), GNU make, a C compiler for
`make test-race`, Terraform or OpenTofu, Docker with Compose, `golangci-lint` v2.

```sh
make build        # compile the provider binary
make test         # unit tests (client against httpmock, provider configure through the protocol)
make test-race    # the same under the race detector (needs a C compiler; CI runs this)
make testacc      # acceptance tests against the in-process fake registry (what CI runs)
make lint         # golangci-lint
make generate     # regenerate docs/ from the schema and examples/
```

### Real registries

`docker-compose/` starts Apicurio Registry 3.3.4 (8090) and 3.0.15 (8091),
and with the `auth` profile a Keycloak (8092) plus a secured 3.3.4 (8093);
`matrix` adds 3.1.7 (8094) and 3.2.6 (8095). Details, credentials and the
recorded server behaviours are in
[`docker-compose/README.md`](docker-compose/README.md) and
[`docker-compose/LIVE-SERVER-FINDINGS.md`](docker-compose/LIVE-SERVER-FINDINGS.md).

```sh
make stack-up                                   # start everything and run the smoke test
make testacc-live                               # acceptance tests against 3.3.4
make testacc-live APICURIO_URL=http://localhost:8091
APICURIO_USERNAME=registry-api APICURIO_PASSWORD=registry-api-secret \
  make testacc-live APICURIO_URL=http://localhost:8093
make stack-down
```

### Using a local build

Point Terraform at the binary with a `dev_overrides` block, then run
`terraform plan`/`apply` without `terraform init`:

```sh
make install   # installs into $(go env GOBIN) or $(go env GOPATH)/bin
```

```hcl
# ~/.terraformrc
provider_installation {
  dev_overrides {
    "jamalshahverdiev/apicurio" = "/home/<you>/go/bin"
  }
  direct {}
}
```

## Releasing

A `v*` tag runs `.github/workflows/release.yml`: goreleaser builds the
binaries for every platform, signs the checksums with the GPG key in the
`GPG_PRIVATE_KEY`/`PASSPHRASE` secrets and publishes a GitHub release that the
Terraform Registry picks up. The release notes are the `CHANGELOG.md` section
whose heading names the tag's version (`## [0.1.0] - ...` for `v0.1.0`); the
job fails when that section is missing, so rename `[Unreleased]` before tagging.

## License

[Mozilla Public License 2.0](LICENSE).
