Title: Add Hugging Face model repository and GitHub trusted-publisher provider

Provision model repositories and individual GitHub Actions trust bindings with a
native protocol-6 Terraform/OpenTofu provider. Visibility updates preserve artifact
commits; destroys retain models by default. Import, drift and ambiguous write
recovery avoid silent adoption or duplicate publisher POSTs.

The focused client is generated reproducibly from vendored OpenAPI 3.1 plus
provenance-backed overlays for independently verified undocumented operations.
The publisher API has no server-side uniqueness; its limitations and conservative
private-404 handling are documented.

Validation: unit/race/vet; byte-identical generation/docs; both Terraform1.15.9
and OpenTofu1.13.2 stateful-fake protocol matrices; actionlint; five-platform
snapshot packaging. Real API CRUD was proven separately and all disposable
resources removed. Provider live/OIDC tests are opt-in and remain unrun.

Prerequisites: authorized remote repository bootstrap, preserve the
infrastructure-owned MPL-2.0 LICENSE while reconciling history, configure signing
and registry enrollment. No release, infrastructure apply or Timestral upload.
