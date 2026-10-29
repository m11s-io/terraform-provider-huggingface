# Hugging Face API contract verified on 2026-10-29

Repository and repo trusted-publisher CRUD were exercised against disposable
private models owned by `m11s-io`, using a temporary legacy Write personal access
token. All test repositories and bindings were removed. The Timestral destination
was not created or modified. No credentials are present in these artifacts.

## Sources and schema

- Upstream: https://huggingface.co/.well-known/openapi.json
- Snapshot: OpenAPI 3.1.0, metadata version 0.0.1, 307 paths, 1,210,635 bytes.
- SHA256: `11306c4757273fefa2fadb093161d426d7a08cd62b36522276d8449c6dfc19c8`.
- This differs from the design's digest. Earlier bytes were unavailable, so a
  semantic comparison to that snapshot has not been performed.
- Official SDK commit: `db254a305a29370d2e08859000d0d40c2897c8db`,
  `src/huggingface_hub/hf_api.py`.
- Official docs: https://huggingface.co/docs/hub/trusted-publishers
- Public model settings component:
  https://huggingface.co/front/build/kube-cbf67c2/ViewerSettingsContent-Cdh3M705.js
- Public publisher component:
  https://huggingface.co/front/build/kube-cbf67c2/TrustedPublishers-DP3H7WBD.js
- HTTP helper:
  https://huggingface.co/front/build/kube-cbf67c2/ViewUtils-CuZOzPtE.js

OpenAPI includes identity, repository create and settings, but not the model
info/delete or publisher operations below. Model info/delete are supported by the
pinned SDK and verified live. Publisher operations are **undocumented UI APIs**:
the frontend supplied exact paths and request shape, then a machine token
independently authenticated their live API requests. A browser session was used
only for authorized read-only HTML inspection via HTTP. No browser automation or
session credential is required for the provider API.

Docker Terraform MCP capability recheck: `huggingface/huggingface` absent;
`sebps/huggingface` v0.0.1 has endpoint resource/endpoints data source;
`issamemari/huggingface` v1.5.0 has endpoint resource. Neither covers this contract.

## Operations

All API requests use `Authorization: Bearer <token>`. JSON mutations send
`Content-Type: application/json`; successful deletes return text `OK`.

| Operation | Method and path | Verified behavior |
| --- | --- | --- |
| Identity | GET `/api/whoami-v2` | 200; account username and credential role |
| Create model | POST `/api/repos/create` | 200; duplicate name returns 409 |
| Read model | GET `/api/models/{namespace}/{name}` | 200; includes id, private and artifact metadata |
| Visibility | PUT `/api/models/{namespace}/{name}/settings` | 200; explicit false is honored |
| Delete model | DELETE `/api/repos/delete` | 200; subsequent model info returns 404 |
| List publishers | GET `/api/models/{namespace}/{name}/settings/trusted-publishers` | 200 JSON array |
| Create publisher | POST same collection | 200 JSON publisher; duplicates are allowed |
| Delete publisher | DELETE collection`/{publisher_id}` | 200; deleting again returns 404 `Publisher not found` |

There is no verified individual publisher GET: collection`/{publisher_id}` returns
a generic route 404 even for an existing binding. Read by immutable `_id` from the
collection. No update contract is established; changes require replacement.

Create model body: `name`, `organization` (namespace), `type: "model"`,
`private: true`. Observed response: `url`, `name` (namespace/name), `id` (remote
database identifier), `filesCopyPending: false`. Terraform identity remains the
specified `model/namespace/name`, independent of the remote database identifier.
Delete model body: `name`, `organization`, `type: "model"`.
Visibility body: `{ "private": false }` or `{ "private": true }`.

## GitHub publisher payload

The frontend rewrites logical branch/workflow inputs; they are not literal API
claim keys. The following shape was accepted and read back unchanged, using
unique disposable branch/workflow values for tests:

```json
{
  "issuerUrl": "https://token.actions.githubusercontent.com",
  "claims": {
    "repository": {"kind": "eq", "value": "m11s-io/docker-images"},
    "ref": {"kind": "eq", "value": "refs/heads/main"},
    "workflow_ref": {"kind": "prefix", "value": "m11s-io/docker-images/.github/workflows/timestral-openvino.yaml@"}
  }
}
```

Observed response fields: `_id` (24-character hex string), `issuerUrl`, `claims`,
`configuredBy` (public account object), and `createdAt` (timestamp). Model a
binding by `_id`; configuredBy and telemetry are not managed configuration.

## Ownership, duplicates and list semantics

Identical publisher POSTs both returned 200 with distinct IDs. The server does
not provide uniqueness. Never blindly replay a create, silently adopt an existing
binding, or assume duplicate POST returns 409. Check the complete collection for
an existing matching binding before create. A lost create response must report
the ambiguity and require explicit import after a read-only lookup. Concurrent
identical creates by independent callers remain an upstream limitation.

Deleting one binding preserved both another binding and a deliberately duplicated
binding. No whole-collection replacement was needed. Test duplicates were also
removed when their disposable parent was deleted; a second test explicitly
deleted every binding and verified an empty array before parent deletion.

Lists returned 0, 1 and 2 objects, with no Link header. With two objects, `limit=1`,
`limit=1&cursor=invalid` and `limit=1&offset=1` each still returned both objects:
those parameters were ignored. No larger-list limit is proven. Treat the observed
array as the complete list while honoring any future pagination Link; fail closed
on unknown envelopes, malformed responses or truncation signals. Do not invent
cursor semantics. The live evidence does not establish arbitrary-scale behavior.

## Permission and absence limits

A legacy Write PAT is sufficient for owner repo/publisher CRUD. Minimum
fine-grained permissions and non-owner behavior are untested. Public documentation
requires a Write role on the repo to manage trust. A browser cookie succeeding
would not establish machine authentication; the PAT tests do.

Anonymous reads of the private model and its publishers returned 401. After
deletion, the authorized PAT received 404 for model info and publisher list.
These controlled cases do not establish that every 404 distinguishes deleted
objects from lost permissions. Refresh must preserve state with a diagnostic
whenever absence cannot be confirmed. Do not interpret individual GET's generic
404 as binding absence.

## Evidence and remaining validation

Sanitized live event fixtures: `sanitized-fixtures/*.json`. Credentials and request
headers are excluded; configuredBy is replaced with a synthetic account.
[probe.py](probe.py) is an opt-in disposable test. With `HF_TOKEN` and a dedicated
`HF_TEST_NAMESPACE` already set in the environment, run from the checkout root:

```sh
python3 docs/api-contract/probe.py
```

It creates a uniquely named private repository and publisher bindings, then
attempts cleanup in `finally`; cleanup failures identify the disposable repository
for manual removal. It writes `probe-events.json` in the current working directory,
excluding request headers and configuredBy and redacting the token. The script
contains no saved credentials and reads credentials only from the environment.
Its syntax was checked without executing it; equivalent earlier live probes
produced the recorded fixtures.

Generator feasibility is recorded separately in `generator-report.md`. No real
GitHub OIDC exchange/upload, mismatched-claim or second-repo scope smoke test has
run. CRUD and claim round-trips do not prove those behaviors. No registry release,
GitHub repository apply, billed endpoint or Timestral upload has occurred.

Publication privacy pass: fixture namespaces, repository names, remote database identifiers, timestamps and request IDs are synthetic. Field shapes, statuses, claim operators and cross-event identity relationships are preserved; upstream schema bytes are unchanged.
