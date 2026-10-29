# GitHub repository bootstrap

`bootstrap.patch` is an unapplied patch against `infra/github` commit
`d6d4fcaddcf715e77056a21cadb2af7208ef64b2`. It changes only
`repositories.tf`, `licenses.tf`, and `secrets.tf`:

- Public `terraform-provider-huggingface`, initialized by GitHub, with issues,
  provider topics, and the proposed registry homepage; wiki/projects disabled.
- Infrastructure-managed MPL-2.0 `LICENSE` from the existing template.
- `GPG_PRIVATE_KEY` from the existing sensitive `gpg_private_key` input and an
  empty `GPG_PASSPHRASE`, matching the qBittorrent convention.

It adds no Hugging Face provider requirement, API credentials, resources, or
model-publishing configuration. It does not change organization permissions.

## Review and validation

From the intended infrastructure checkout, review and check the patch before
applying it locally:

```sh
git apply --check /path/to/terraform-provider-huggingface/bootstrap/bootstrap.patch
```

Validation was performed in
`$DISCOVERY_WORKSPACE/infra-bootstrap`, using tracked
nonsecret module `.tf` files, license templates, and `.terraform.lock.hcl`.
`backend.tf` was omitted to isolate validation from production state; all
tfvars and state files were omitted. The original infrastructure checkout was
unchanged.

```sh
tofu fmt -check
tofu init -backend=false -input=false -lockfile=readonly \
  -plugin-dir=/path/to/infra/github/.terraform/providers
tofu validate -no-color
```

All three checks passed with OpenTofu v1.13.2 and `integrations/github` v6.12.1.
The local plugin directory initialization reported `unauthenticated`; this
does not claim a fresh registry download or verify backend connectivity.
The existing committed lock file was kept unchanged.

No real plan was attempted: GitHub App and AWS credentials were absent from
the environment, and `secrets.auto.tfvars` values were not read or copied.
Validation does not prove App authorization, signing key validity, repository
name availability, backend access, or the changes a stateful plan will propose.

## Pending authorized bootstrap

1. Review/apply this patch to the infrastructure branch and commit it separately
   from later HF integration.
2. Supply the existing GitHub App and S3 backend credentials securely, following
   `infra/github/CLAUDE.md`. Initialize normally and review a saved plan without
   exposing secret values or plan artifacts.
3. Apply only after separate authorization. The GitHub repository and its
   infrastructure-owned `LICENSE` do not exist as a result of this local work.
4. Fetch the initialized remote and reconcile this independent local bootstrap
   history with its initial commits, preserving the infrastructure-managed
   `LICENSE`, before any separately authorized first push. This repository has
   no remote configured and does not inherit qBittorrent history.
5. Verify registry namespace/enrollment, then add the separate release-readiness
   configuration and signing checks before any provider release.

Later infrastructure integration can require an installable HF provider after
this bootstrap. Keeping that requirement out of this patch prevents a
provider-install/repository-create dependency cycle.

## Follow-up HF configuration patch

`integration.patch` is intentionally separate from bootstrap. It targets the
current infrastructure files and adds the proposed exact `0.1.0` provider pin,
required owner input, private-by-default protected Timestral repo, exact
`m11s-io/docker-images` / `main` / `timestral-openvino.yaml` binding, non-secret
Actions variable and setup outputs. It must wait for a real installable release.

1. Review/apply the bootstrap patch using existing GitHub App and S3 backend auth.
2. Reconcile the provider branch with the remote without overwriting its LICENSE;
   review a provider PR, configure signing/registry enrollment, and explicitly
   request the signed release. No release is performed by this handoff.
3. Verify the actual source/version with ordinary `tofu init` in an isolated
   module. The patch's `= 0.1.0` is a target until published; adjust it to the
   reviewed real version if needed. Commit the resulting real dependency lock.
4. Review/apply `integration.patch` in infra, set `huggingface_namespace`
   deliberately and keep visibility private unless public release is selected.
   Supply `HF_TOKEN` in the apply environment alongside existing App/backend auth.
5. Run `tofu fmt -check`, `tofu validate`, then review a real `tofu plan` before
   any separately authorized apply. No model export/upload or workflow permission
   expansion is part of this patch.

Existing objects require explicit import before applying:

```sh
tofu import huggingface_repository.timestral_openvino model/OWNER/Timestral-5M-OpenVINO-experimental
tofu import huggingface_trusted_publisher.timestral_openvino model/OWNER/Timestral-5M-OpenVINO-experimental/PUBLISHER_ID
```

Imports read remote trust without changing it. Never retry a possibly successful
create blindly: identical publisher POSTs are accepted as distinct objects by HF.
Repo imports default to retain; the target also adds prevent_destroy.

Local evidence: isolated initialized-module validation with the actual built
provider and an explicit development override, plus fmt/patch applicability.
This does not prove normal registry init, production backend/App auth, actual
owner permissions or a real plan. The production lockfile is unchanged and no
development override is included in either patch.

Publication status: the four GitHub bootstrap resources were applied successfully (4 added, 0 changed, 0 destroyed). The unrelated docker-images branch-protection change in a full plan was excluded. The provider publication branch is based on the infrastructure-managed main history and preserves LICENSE exactly. The HF integration patch remains unapplied and still requires an actual provider release.
