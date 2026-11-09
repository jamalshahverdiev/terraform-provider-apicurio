HOSTNAME  = registry.terraform.io
NAMESPACE = jamalshahverdiev
NAME      = apicurio
BINARY    = terraform-provider-$(NAME)
VERSION  ?= 0.1.0
OS_ARCH  ?= linux_amd64
PLUGINDIR = $(HOME)/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

# Registry the live acceptance tests run against (see docker-compose/README.md).
APICURIO_URL ?= http://localhost:8090

default: build

build:
	go build -o $(BINARY) .

# Install into GOBIN (use with a dev_overrides block, see README.md).
install:
	go install .

# Install into the CLI filesystem mirror (use with required_providers + terraform init).
install-mirror: build
	mkdir -p $(PLUGINDIR)
	cp $(BINARY) $(PLUGINDIR)/

fmt:
	gofmt -s -w .
	terraform fmt -recursive ./examples/

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Unit tests: client against httpmock, provider configure through the protocol.
test:
	go test ./... -count=1

# The same under the race detector (needs cgo and a C compiler; what CI runs).
test-race:
	go test ./... -race -count=1

# Acceptance tests against the in-process fake registry (what CI runs).
testacc:
	TF_ACC=1 go test ./internal/provider -v -run 'TestAcc' -count=1 -timeout 30m

# Acceptance tests against a real registry, by default the compose stack's 3.3.4.
#   make testacc-live                                    # 8090, no auth
#   make testacc-live APICURIO_URL=http://localhost:8091 # 3.0.15
#   APICURIO_USERNAME=registry-api APICURIO_PASSWORD=registry-api-secret make testacc-live APICURIO_URL=http://localhost:8093
testacc-live:
	APICURIO_URL=$(APICURIO_URL) TF_ACC=1 go test ./internal/provider -v -run 'TestAcc' -count=1 -timeout 30m

stack-up:
	cd docker-compose && docker compose --profile auth --profile matrix up -d && scripts/smoke.sh

stack-down:
	cd docker-compose && docker compose --profile auth --profile matrix down

# Regenerate docs/ from the schema and examples/ (tfplugindocs, pinned in main.go).
generate:
	go generate ./...

# --intent-to-add makes files that go generate created (but nobody added) show up in the diff.
docs-check: generate
	git add --intent-to-add docs/ examples/
	git diff --exit-code docs/ examples/ || { echo "docs/ is stale: run make generate and add the result"; exit 1; }

.PHONY: default build install install-mirror fmt vet lint test test-race testacc testacc-live stack-up stack-down generate docs-check
