Title: Prepare protected Timestral HF repository and publishing trust

Add an explicitly selected HF owner, private-by-default model with retain and
prevent_destroy, and one publisher for the existing docker-images repo's main
branch/timestral-openvino.yaml workflow. Export the repo_id as the non-secret
HF_TIMESTRAL_OPENVINO_REPO Actions variable and output setup identifiers.

Follow-up patch: integration.patch. Depends on independent GitHub bootstrap and
an actual signed installable provider release; =0.1.0 is a target, not published.
The lockfile remains unchanged until ordinary init resolves the real release.

Validation: isolated initialized-module tofu fmt/validate with explicit local
development override. Patch applicability passed; production tracked files and
lockfile unchanged. No real plan, apply, model contents or org-wide workflow
permission changes. Select owner/visibility and prove bootstrap permissions
before a separately authorized production operation.
