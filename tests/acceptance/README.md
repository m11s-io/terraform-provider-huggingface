# Acceptance evidence and opt-in live tests

Default tests use only local stateful fake APIs. Install the pinned CLIs with
`make cli-tools`, then `make acceptance`. Override `TERRAFORM_CLI` and `TOFU_CLI`
with existing binary paths if needed. Both actual protocol clients exercise
repository lifecycle/import/no-op/drift/replacement/retention, publisher graph
ordering/replacement/import, foreign binding preservation, failed writes,
permission loss and lost POST responses with no duplicate on a second apply.
Development overrides do not prove normal registry init; no release exists yet.

Real API CRUD was separately verified against unique disposable private models
with a Write PAT; all were removed. This is distinct from fake protocol tests,
provider live acceptance and a real OIDC exchange. Real provider live acceptance
and GitHub OIDC have **not yet run**.

For explicit live provider acceptance, use a dedicated namespace and temporary
credential. Install `huggingface_hub==1.19.0` in a virtual environment and ensure
pinned tofu is on PATH. Set `HF_TOKEN`, `HF_TEST_NAMESPACE`,
`HF_TEST_GITHUB_REPOSITORY`, `HF_TEST_BRANCH`, then opt in:

```sh
HF_ACC=1 go test ./internal/provider -run '^TestLiveAcceptance$' -count=1 -v
```

Only `terraform-provider-acceptance-<random>` repositories are created. The live
roundtrip applies/imports the resources, adds an unrelated binding and a second
private scope-test model, uploads a tiny marker, destroys managed ownership with
retain, verifies artifact/binding preservation, and deletes disposable models.
No billed endpoint or Timestral destination is used. The local roundtrip tests
CRUD/retention using bootstrap auth; it does not claim OIDC.

After remote bootstrap, the manual `hf-acceptance.yml` workflow requires repository
secret `HF_BOOTSTRAP_TOKEN` and an explicitly authorized namespace. It configures
trust for its **own actual GitHub repository/branch/hf-acceptance.yml**, not the
Timestral workflow. Setup and cleanup receive the bootstrap secret. Only upload
gets `id-token: write`; it exchanges a real GitHub JWT, uploads a marker, verifies
mismatched workflow claims are rejected, and checks the positive token cannot
write to the second owned model. JWT/access tokens are neither printed nor saved
as artifacts. Job outputs contain only disposable identifiers.

Cleanup runs with `always()` and has a standard-library purge fallback if SDK,
CLI setup or retention verification fails. Setup also attempts cleanup on errors.
Forced runner termination or cancellation before outputs propagate can still
require manual cleanup: inspect the unique prefix in the dedicated namespace.
Never run cleanup against production models. No workflow dispatch or credentials
configuration is performed by this handoff.
