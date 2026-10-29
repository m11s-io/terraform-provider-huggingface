# Generator feasibility experiment — 2026-10-29

## Result

**Conditionally feasible for the supplied identity/create/settings subset** with
`oapi-codegen v2.8.1`, runtime `v1.8.0`, nullable `v1.1.0`, the documented
`output-options.nullable-type: true`, and a deterministic, reversible hoist of
the inline create request schema to a named component. The direct baseline is
**not semantically compatible**: it collapses nullable null/omission and its
typed create request serializes to `{}`. Neither defect was repaired by editing
generated Go.

Exact typed create request payloads and union/member round trips pass after the
hoist. Settings false/omission, valid `anyOf`/`const` values, encoded path segments,
distinct create 200/409 responses, raw unmodeled error statuses, and request-ID
header retention pass. This is a throwaway feasibility experiment, not provider
implementation or proof of complete JSON Schema validation.

The generated decoder and union constructors **do not enforce** `const`, required
fields, or `oneOf` membership. They preserve valid representations and emit
`Valid()` helpers for const-derived enums; callers must perform domain validation
or explicit schema validation. Malformed recognized JSON responses return a nil
typed response wrapper: the HTTP wrapper must retain status/headers before typed
parsing to satisfy diagnostics requirements on that error path.

## Scope and provenance

Only scratch files under
`<discovery-workspace>/generator-experiment` and this report
were created. No repository files, generated Go, credentials, or HF resources
were modified. Tests use `example.invalid`, request builders, response parsers,
and a synthetic HTTP doer; **no Hugging Face request was made**.

Input schema is the exact supplied `generator-subset.json`, containing these
operations: `GET /api/whoami-v2`, `POST /api/repos/create`, and
`PUT /api/models/{namespace}/{repo}/settings`. All generated code compiles;
behavioral tests target create/settings rather than all whoami fields. No missing
repository/publisher overlay operation was part of this experiment.

SHA256 evidence (`shasum -a 256`):

| File | SHA256 |
| --- | --- |
| supplied `upstream.json` | `11306c4757273fefa2fadb093161d426d7a08cd62b36522276d8449c6dfc19c8` |
| supplied `generator-subset.json` | `9569ca501db563bfd27d1c8704d5eef0dd0c5b441b13ba4761e97f5836ce28fc` |
| supplied `client.gen.go` and `client.second.gen.go` | `fdc5dd06ea38a5e2f2c428050a43ff53906f21443c22c122ba4b3453d1c12d72` |
| nullable-only generated client | `1856be49e825b9c3a4f61cf4566d16112781b0a24248faf174bec027968a0962` |
| `hoisted-subset.json` | `9c3f4570a3c59190ee32cd05179f2869947719b6c2e61afdbc93bdc27faedf28` |
| hoisted generated client | `ebd506b5dc686aba1f945bcf0509043b83256390c764b4bc4480d3a649ea23e0` |

The supplied upstream raw file digest differs from the design's inspected
snapshot digest `3dc5e6f9...`. It matches `provenance.json`'s
`files.upstream.json.sha256`, while that file's top-level `spec_sha256` contains
the design digest. This report does not infer the reason or silently reconcile
the values; production provenance needs one unambiguous digest definition.

Environment: `go version go1.27.3 darwin/arm64`; scratch module declares Go 1.25.0.
Generator v2.8.1's official go.mod requires Go 1.25.0; runtime v1.8.0's official
go.mod requires Go 1.24.0. Compatibility of the actual generated imports was
established by compilation and behavior, not just release dates.

## Reproduction

Work from `<discovery-workspace>/generator-experiment`.
The original generated file is copied into `baseline/client.gen.go`; its
byte-identical duplicate was **not** copied into the same Go package.

`go.mod` / `go.sum` pin:

```text
github.com/oapi-codegen/runtime v1.8.0
github.com/oapi-codegen/nullable v1.1.0
github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
github.com/google/uuid v1.6.0 // indirect
```

Generation config `hoisted-config.yaml`:

```yaml
package: hfapi
generate:
  models: true
  client: true
output-options:
  nullable-type: true
output: hoisted/client.gen.go
```

Commands actually executed:

```sh
go mod download github.com/oapi-codegen/runtime@v1.8.0
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1 -config nullable-config.yaml ../generator-subset.json
go mod tidy
go test -count=1 -v ./nullable > inline-union-failure.log
# exit 1, the retained negative control described below

python3 hoist_request_schema.py
# inverse hoist equals original subset: true
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1 -config hoisted-config.yaml hoisted-subset.json
cp hoisted/client.gen.go hoisted-first.gen.go.txt

env GOBIN=<discovery-workspace>/generator-experiment/bin go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.1
./bin/oapi-codegen -version
# github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
# v2.8.1
env GOPROXY=off GOSUMDB=off ./bin/oapi-codegen -config hoisted-config.yaml hoisted-subset.json
cmp hoisted/client.gen.go hoisted-first.gen.go.txt
# exit 0: byte-identical

go build ./baseline ./nullable ./hoisted
# exit 0
go test -count=1 -v ./baseline ./hoisted > verified-tests.log
# exit 0
```

`go test ./...` intentionally includes the retained negative-control `nullable`
package and therefore fails. Use the explicit verification command above for
the accepted transform/config; inspect `inline-union-failure.log` for the
unaltered-inline failure. No generated files were manually edited. `gofmt` was
run only on handwritten test files.

Offline detail: `GOPROXY=off go run ...@v2.8.1` failed because Go attempted a
module deprecation lookup. Building directly from the cached module directory
produced `(devel)` version metadata and changed the generated header. Those
attempts are not the accepted reproducibility result. Installing the pinned
binary with `go install ...@v2.8.1`, then invoking that binary offline, produced
the correct version header and byte-identical output. The initial install hit
sandbox DNS restrictions and succeeded on the approved escalated retry. Schema
retrieval and mutable template retrieval were never part of generation.

## Exact findings

### Nullable false and omission

The baseline `Create` model member has `Private *bool`, `Organization *string`,
and `ResourceGroupId *string` with `omitempty`. Both
`{"name":"probe"}` and
`{"name":"probe","private":null,"organization":null,"resourceGroupId":null}`
decode/reencode as `{"name":"probe"}`. Baseline characterization test passes
only when this incompatibility is observed.

With nullable enabled, the exact original 3.1 `type: [boolean, null]`,
`type: [string, null]`, and `anyOf: [string-schema, null-schema]` become
`nullable.Nullable[T]`. No 3.0 downgrade or `nullable: true` schema rewrite is
used. Hoisted typed request payloads and decode/member/union reencoding match
these exact byte strings:

```json
{"name":"probe"}
{"name":"probe","private":false}
{"name":"probe","private":true}
{"name":"probe","organization":null,"private":null,"resourceGroupId":null}
{"name":"probe","organization":"org","resourceGroupId":"0123456789abcdef01234567","type":"model"}
```

Settings' nonnullable optional `private` remains `*bool`. Its typed request
builder sends `{}`, `{"private":false}`, and `{"private":true}` distinctly.

### Inline oneOf request defect and lossless workaround

Direct generation emits:

```go
type PostApiReposCreateJSONRequestBody PostApiReposCreateJSONBody
```

The defined type does not inherit the union's `MarshalJSON`; its only field is
unexported. The generated typed request builder's `json.Marshal(body)` therefore
returns `{}` for every tested create request. Five exact-payload subtests fail,
even with nullable enabled. Direct union/member marshaling itself is correct.

The checked-in scratch transform `hoist_request_schema.py` moves only the whole
inline JSON schema to `components.schemas.CreateRepoRequest` and replaces it
with a local `$ref`. It asserts that undoing this move produces an object equal
to the parsed original subset, preserving every schema keyword/value. It neither
changes union branches nor drops `const`, null, required, enum or constraints.
Generation then emits:

```go
type PostApiReposCreateJSONRequestBody = CreateRepoRequest
```

That alias retains JSON methods. All five previously failing typed payloads
pass with the same pinned generator. This transform must remain explicit,
deterministic and regression-tested if adopted.

### Union and const semantics

Valid representations tested: `gated: false`, `gated: "auto"`,
`gated: "manual"`; sharing `{email:false,username:true}` and
`{email:false,username:false}`; model create with omitted default type and
explicit `"type":"model"`. Gated bool/string members round-trip through
generated union decoding/accessors. Model create nullable values round-trip
through both generated member and root union decoding/accessors.

Explicit negative characterization proves generated validation limits:

- `Gated0(true).Valid()` is false, but its union constructor still serializes
  `true` without error.
- Model-branch type `"bogus"` has `Valid() == false`, but construction and
  marshaling still succeed.
- `{"type":"model"}` lacking required name is accepted by root unmarshal.
- A dataset accessor can decode a model or bogus-type payload without rejecting
  the branch mismatch.

These are recorded limitations, not passing semantic-rejection claims. A v1
provider restricted to model repositories must select the model constructor,
set/validate its const type and validate domain inputs. Generated `As*` methods
must never be used as proof that a response matches exactly one branch.

### Status and path behavior

Synthetic create responses retain raw body, HTTP status text/code and
`X-Request-Id` for 200, 409, 403, 429, 503, and a 409 HTML response. JSON 409 has
a distinct `JSON409` field; it is not decoded as success. `filesCopyPending:false`
survives typed success parsing. Unmodeled errors remain available as raw body.

Malformed JSON labeled `application/json` on a recognized 409 returns a nil
typed wrapper and a JSON error. Wrapper code must capture HTTP metadata before
calling the generated parser; it cannot depend only on `*WithResponse` results
for this error case. Bounded reads/diagnostic sanitization remain handwritten
wrapper responsibilities, outside this experiment.

Using a synthetic transport verifies URL `EscapedPath()` exactly equals the
path built with `url.PathEscape` per segment. Inputs containing slash, space,
question mark, fragment marker, literal `%2F`, and Unicode remain path data;
the host stays `example.invalid`, with no query or fragment. Examples:

```text
org/child + a/b -> /api/models/org%2Fchild/a%2Fb/settings
org?query#fragment + r%2Fs -> /api/models/org%3Fquery%23fragment/r%252Fs/settings
```

This establishes encoding behavior, not legal HF identifier validation. Dot
segments, real proxies, redirects, credential forwarding, cancellation, retry
policy and a full provider lifecycle are outside this feasibility result.

## Primary sources checked

- [Pinned generator README and nullable configuration](https://github.com/oapi-codegen/oapi-codegen/blob/v2.8.1/README.md#generating-nullable-types)
- [Generator v2.8.1 module requirements](https://github.com/oapi-codegen/oapi-codegen/blob/v2.8.1/go.mod)
- [Runtime v1.8.0 official release](https://github.com/oapi-codegen/runtime/releases/tag/v1.8.0)
- [Runtime v1.8.0 module requirements](https://github.com/oapi-codegen/runtime/blob/v1.8.0/go.mod)
- [Nullable v1.1.0 implementation](https://github.com/oapi-codegen/nullable/blob/v1.1.0/nullable.go)

Go module download provenance pins generator commit
`068f1bcd16e4724c72d78d6774008a877b8297b1`, runtime commit
`c0b2ca256c04a843e2ee0fea9d6a7ac40ca6fc19`, and nullable commit
`5c8aab1f58cdf062d9fc3c9169af229c7c9d1d09`.

No alternative generator was needed after the semantics-preserving hoist passed.
Any newly verified operation/overlay still requires the same payload and
response checks before this feasibility finding is extended to it.

Post-review provenance cleanup: design_snapshot_sha256 and downloaded_schema_sha256 now distinguish the old reference digest from the downloaded bytes; generator status is current. The experimental findings above are unchanged.

Publication note: discovery workspace paths are normalized placeholders; account and disposable identifiers in separate API fixtures are synthetic. No private development history is required to reproduce the checked-in generator tests.
