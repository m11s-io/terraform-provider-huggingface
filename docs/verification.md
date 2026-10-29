# Implementation handoff — 2026-10-29

Local checkout: `m11s-io-github/terraform-provider-huggingface`.
Publication branch: `feat/huggingface-provider`, based on the infrastructure-managed
GitHub main history. Development audit history is retained locally rather than
published; repository-local paths and fixture identifiers are sanitized.
The GitHub bootstrap was subsequently applied: four additions, zero changes and
zero destroys. No provider release tag, registry enrollment, HF production apply
or Timestral upload was performed. Draft PR descriptions are in `bootstrap/`.
The initial structural reference was `c4ae14054d67516c1b50582c5afbb4aeac8c739b`.
This task made no reference edits. At the final check, that checkout was clean
at `6aaabc585e3624d6a104185989a14afd34f9ff9a`; its HEAD changed independently
during the session and is not part of this provider branch.

## Deliverables and evidence

| Item | Evidence/status |
| --- | --- |
| Model repository resource | Create/read/visibility/import/drift/replacement; retain default and explicit delete |
| GitHub publisher resource | Exact rewritten claims; list by immutable ID; individual delete; drift/import/replacement |
| HTTP/configuration | Explicit unset/empty/unknown precedence; origin/TLS/bounds/retries/cancellation; redacted errors |
| Ambiguous create | No mutation replay/adoption; actionable import; second apply sends no duplicate POST |
| Combined dependency plans | Visibility/policy-only changes preserve publisher; real repo replacement changes identity |
| Unit/race/vet | Passed on Go 1.27.3, including generator and resource tests |
| Terraform protocol | 1.15.9 stateful-fake lifecycle/import/no-op/drift/error/replacement tests passed |
| OpenTofu protocol | 1.13.2 same stateful-fake matrix passed |
| Generation/docs | Reproduced without changes; pinned generator 2.8.1, runtime 1.8.0, nullable 1.1.0; docs tool 0.25.0 |
| Workflow/harness checks | actionlint 1.7.12; six Python safety tests passed |
| Snapshot | GoReleaser 2.19.0: five ZIPs, license, ZIP checksums and source manifest's renamed checksum/protocol 6 verified; signing/publishing skipped |
| Infra bootstrap patch | Isolated tofu fmt/init/validate and patch applicability passed |
| HF integration patch | Isolated initialized-module fmt/validate with explicit local override passed; lockfile unchanged |
| Direct real HF API | Temporary legacy Write PAT; disposable private repo/publisher CRUD and cleanup verified |
| Real provider/live OIDC | Code prepared, **not executed**; fake protocols and direct API CRUD do not prove OIDC |
| Registry installation | `m11s-io/huggingface` not found on registry recheck; ordinary init not claimed |

Raw upstream OpenAPI 3.1.0 SHA256:
`11306c4757273fefa2fadb093161d426d7a08cd62b36522276d8449c6dfc19c8`.
It differs from the design's earlier snapshot; old bytes were unavailable for a
semantic diff. Vendored bytes, provenance, operation allowlist, explicit overlays
and source-derived API evidence are committed. Ordinary generation does not
retrieve mutable HF schema/templates. [API contract](api-contract/api-contract.md).

The bootstrap definitions were committed separately in the infrastructure
checkout; its dependency lockfile is unchanged. No infrastructure checkout or
backend/state/tfvars file is included in this provider publication. Status:

- [Bootstrap](../bootstrap/bootstrap.patch), applied: public GitHub repo,
  infrastructure MPL-2.0 LICENSE and existing GPG secret pattern.
- [Integration](../bootstrap/integration.patch): target provider pin, required HF
  namespace, private default, retain+prevent_destroy, exact existing docker-images
  main/timestral-openvino.yaml trust, Actions variable and non-secret outputs.

## Review and decisions

Fresh scoped reviews covered feasibility, bootstrap, generation and HTTP/config.
The first HTTP review found a DELETE404 confirmation gap; a regression and focused
re-review verified its fix. A final whole-branch review found dependent trust
replacement and a snapshot-check layout assumption; both were reproduced and
fixed. A legal-branch HCL literal issue was also reproduced and fixed in the same
pass. Tests were run before and after the fixes. The scoped final fix review marked R1/R2/R3 addressed, found no new issues, and
approved the authorized local handoff. No automatic merge is performed.

The following records the implementation rulings and their practical costs:

| Ruling, in execution order | Reason and cost/limit |
| --- | --- |
| Gate scaffolding on API/generator feasibility | Avoid unsupported provider claims; costs discovery time before code |
| Duplicate publisher POST requires preflight and explicit import recovery | HF creates distinct IDs for identical POSTs; costs manual recovery after ambiguous writes and cannot provide distributed uniqueness |
| Publisher reads use list plus immutable ID | Individual GET is not implemented; costs complete bounded collection reads |
| Exclude account CI/CD identities | They are distinct from repo publishing trust; account identity management is unavailable in v1 |
| Preserve union/const schema and validate domain inputs | Generated constructors are not validators; costs a maintained reversible schema hoist and wrapper validation |
| Vendor the newly retrieved digest honestly | Previous bytes unavailable; semantic differences from the older snapshot remain unknown |
| Treat Write PAT as sufficient, not minimum permission proof | Fine-grained and non-owner scopes remain unverified |
| Confirm private absence only for owning user plus legacy Write authority | Other ambiguous 404s preserve state/error; may require operator intervention instead of automatic removal |
| Introduce the CLI harness with resources before the CI task | Satisfies each resource's actual protocol checks; some test files arrived earlier than the plan's final CI stage |
| Keep bootstrap and integration as separate patches | No authorized remote apply or installable release; production init/plan remain pending |
| Continue inline after the implementer usage limit | Preserves existing tests/commits; tasks 5–8 rely on the final fresh branch review rather than per-task agent reviews |
| Fix literal HCL claim rewriting in the final pass | Exact workflow claims must remain literal; costs a small test-harness encoder |

Final reviewer set aside the following explicitly bounded behaviors; the executor
accepts these limits rather than silently treating them as proven:

- Arbitrary-scale upstream list completeness is not established. Known pagination
  and truncation signals fail closed; unusually large collections need upstream
  contract verification before relying on absence detection.
- Independent simultaneous identical creates cannot be made unique without server
  support. Coordinate state ownership; no local mutex is claimed to solve this.
- Unsupported issuer/claim drift errors while preserving state. Operator correction
  or explicit recovery is required; automatic coercion/deletion could alter trust.
- Fine-grained/org/non-owner access models are not proven. Conservative ambiguity
  may block refresh/destroy until access is restored or ownership is verified.
- Real provider live acceptance and real GitHub OIDC remain unrun. Their manual
  workflow is not production acceptance evidence until actually executed.
- Actual signing, enrollment, normal registry installation, remote bootstrap and
  a production plan are separate operations requiring real prerequisites.
- Forced runner termination may defeat cleanup despite finally/always/fallbacks.
  Disposable names and dedicated-namespace manual cleanup remain necessary.

No unresolved Critical/Important code finding is intentionally deferred.

## Remaining operational sequence

1. Bootstrap is complete. Preserve the infrastructure-owned LICENSE and keep
   infrastructure credentials outside this public repository.
2. Publish the reviewed, privacy-sanitized source on a clean branch rooted in
   the remote main history; keep development-only history local.
3. Run opt-in real provider acceptance and manual GitHub OIDC smoke in a dedicated
   namespace with a fresh temporary bootstrap token; verify and remove test objects.
4. Configure GPG and registry enrollment, then explicitly request a signed release.
   `0.1.0` is a target pin, not a published version. Verify normal OpenTofu init.
5. Review the follow-up integration patch and a real newly resolved lockfile.
   Select `huggingface_namespace` explicitly and keep private visibility unless a
   public release is chosen. `m11s-io` was the tested user account, not an assumed
   production organization or a permission guarantee for another owner.
6. Supply fresh HF/bootstrap and existing App/backend credentials through the
   environment, review an actual infra plan, and separately authorize apply.
   Model export/upload, OVMS images and deployments are outside this task.

The temporary token used for direct API discovery is no longer needed and can be
revoked. It was not committed or stored in discovery/test artifacts.
