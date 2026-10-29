# Hugging Face Terraform/OpenTofu provider

Manage model-repository identity/visibility and one GitHub Actions trusted
publisher per resource. Files, model cards, licenses, weights and inference
endpoints are outside this provider's scope.

**Unreleased experimental provider.** The proposed address is
`registry.terraform.io/m11s-io/huggingface`; `0.1.0` in examples is a target,
not an available release. The public GitHub repository is bootstrapped. No registry enrollment, provider
release, HF production apply or Timestral upload has been performed.

```hcl
provider "huggingface" {} # HF_TOKEN from the apply environment

resource "huggingface_repository" "model" {
  namespace       = var.huggingface_namespace
  name            = "example-model"
  private         = true
  deletion_policy = "retain"
  lifecycle { prevent_destroy = true }
}

resource "huggingface_trusted_publisher" "github" {
  repo_id           = huggingface_repository.model.repo_id
  github_repository = "your-owner/training"
  branch            = "main"
  workflow          = "publish.yml"
}
```

## Authentication and ownership

Supply a bootstrap token as `HF_TOKEN` (or sensitive provider `token`). Only
unset inputs fall back to environment; explicit empty/unknown values fail.
`endpoint` falls back to `HF_ENDPOINT`, then `https://huggingface.co`; HTTPS is
required except for loopback tests. Timeout defaults to 30 seconds.

A legacy Write PAT on an owning user account was proven sufficient for real
repo/publisher CRUD. Minimum fine-grained scopes and non-owner/org permissions
have not been established. Bootstrap credentials manage repo/trust; the resulting
repo-scoped workflow token cannot bootstrap its own trust. Publishing workflows
use `HF_OIDC_RESOURCE` and job-scoped `id-token: write`, without a stored bootstrap
secret. Never commit tokens to configuration, state inputs or workflows.

## Lifecycle and imports

Repository identity changes replace; visibility updates in place. Destroy
**retains** the repository by default, including every artifact commit. Explicit
`deletion_policy = "delete"` deletes it. Replacement also retains the old model
unless delete was explicitly selected. Imports default to retain:

```sh
tofu import huggingface_repository.model model/namespace/name
tofu import huggingface_trusted_publisher.github model/namespace/name/publisher_id
```

A binding owns one immutable ID, never its collection. Claim/repo changes replace
with destroy before create by default; avoid create-before-destroy for trust.
Other bindings are preserved. Reads report supported claim drift and diagnose
unsupported/ambiguous drift. A private 404 removes state only after the limited
owning-user legacy-Write authority check; other access ambiguity returns an error.

The upstream publisher API **accepts duplicates**. The provider checks the
complete list before create. It never retries an ambiguous mutation or silently
adopts an object. If creation may have succeeded, inspect the reported import ID
and explicitly import before applying again. Independent concurrent callers
remain subject to the server's lack of uniqueness/CAS.

Publisher management is an **undocumented HF UI API**, independently verified
with a machine token. Its list is an array; future pagination links are bounded
and followed, and malformed/truncated/ambiguous responses fail closed. Unlimited
upstream collection completeness is not proven. See
[verified contract and limitations](docs/api-contract/api-contract.md) and
[generator feasibility](docs/api-contract/generator-report.md).

## Development and validation

Go 1.27.3; Framework 1.19.0; oapi-codegen 2.8.1; runtime 1.8.0; nullable 1.1.0.
Schema SHA256: `11306c4757273fefa2fadb093161d426d7a08cd62b36522276d8449c6dfc19c8`.
The schema is vendored unmodified. Explicit subsetting/overlays and a reversible
request-schema hoist preserve null/false/union semantics. Never edit generated Go.

```sh
make fmt vet test race build generate-check
make cli-tools
make acceptance
make docs-check # pinned Terraform must be on PATH
make actionlint
make release-check # pinned GoReleaser 2.19.0 and infrastructure LICENSE required
```

Generation never retrieves mutable HF schema/templates. `make update-openapi`
writes a candidate, provenance and diff for review; it does not replace upstream.
Fake protocol acceptance runs with Terraform 1.15.9 and OpenTofu 1.13.2 using
explicit development overrides, not registry installation. No HF secrets are
needed by normal CI. [Acceptance instructions](tests/acceptance/README.md).

## Bootstrap and release

Apply the separately reviewed [GitHub bootstrap patch](bootstrap/bootstrap.patch)
only when authorized, then reconcile the local branch with the new remote
history while preserving the infrastructure-owned MPL-2.0 LICENSE. Repository bootstrap is managed separately from provider installation. [Bootstrap procedure](bootstrap/README.md).

Signing uses `GPG_PRIVATE_KEY`/`GPG_PASSPHRASE`; register the public signing key and
provider separately with the registry before a requested release. GoReleaser
builds five CGO-disabled ZIP targets: Linux/macOS amd64+arm64, Windows amd64;
checksums and registry protocol-6 manifest are included. A snapshot skips signing
and publication. Before remote bootstrap, local packaging validation can use
`make release-check RELEASE_CHECK_ARGS="--license-template /path/to/infra/files/LICENSE-mpl-2.0.txt"`;
this stages the exact template in a temporary checkout, never writes LICENSE here.

Prepare the provider PR after remote bootstrap. The infra HF integration remains
a separate follow-up patch until an actual signed, installable provider version
exists. Production namespace is a required input, not an assumed organization.
