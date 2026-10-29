.PHONY: fmt vet test race build generate generate-check update-openapi

fmt:
	gofmt -w main.go generate.go internal/client internal/provider tools/openapi internal/hfapi/*_test.go

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build -trimpath -o bin/terraform-provider-huggingface .

# Module/tool downloads may be needed once. These commands never fetch HF's
# schema or mutable templates. All external refs are rejected before generation.
generate:
	go run ./tools/openapi
	go tool oapi-codegen -config openapi/oapi-codegen.yaml -o internal/hfapi/client.gen.go openapi/provider.json

# Rebuild into temporary files, leaving the checkout unchanged even on drift.
generate-check:
	@task_tmp=$$(mktemp -d); trap 'rm -rf "$$task_tmp"' EXIT; \
	go run ./tools/openapi -output "$$task_tmp/provider.json" && \
	go tool oapi-codegen -config openapi/oapi-codegen.yaml -o "$$task_tmp/client.gen.go" "$$task_tmp/provider.json" && \
	diff -u openapi/provider.json "$$task_tmp/provider.json" && \
	diff -u internal/hfapi/client.gen.go "$$task_tmp/client.gen.go"

# Writes candidate.json, candidate-provenance.json and candidate.diff only.
# Review and replace inputs manually; this target never overwrites upstream.json.
update-openapi:
	go run ./tools/openapi -update

.PHONY: docs docs-check cli-tools acceptance release-check actionlint
TERRAFORM_CLI ?= .tools/cli/terraform
TOFU_CLI ?= .tools/cli/tofu
GORELEASER ?= goreleaser
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name huggingface
docs-check:
	$(MAKE) docs
	git diff --exit-code -- docs examples
	test -z "$$(git ls-files --others --exclude-standard -- docs examples)"
cli-tools:
	python3 tests/acceptance/install_cli.py terraform .tools/cli
	python3 tests/acceptance/install_cli.py tofu .tools/cli
acceptance:
	python3 tests/acceptance/run.py --publisher --cli $(TERRAFORM_CLI) --cli $(TOFU_CLI)
actionlint:
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
release-check:
	python3 tools/release/check.py --goreleaser $(GORELEASER) $(RELEASE_CHECK_ARGS)
