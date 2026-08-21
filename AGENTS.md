# Working on oascmd

`README.md` documents the library for people *using* it: the derivation rules,
the extension table, the hook matrix, the lock file format. This file is for
people *changing* it. It records the reasoning behind the structure and the
invariants that will break quietly if you undo them.

Read the README first. Nothing here repeats it.

## What it is

A Go library that turns an OpenAPI 3.0/3.1 document into a Cobra command tree,
in two modes that must behave identically: parse the spec at runtime and get
`[]*cobra.Command` back, or generate typed Go source at build time. The
motivating consumer is the Namefi backend's oRPC-generated OpenAPI 3.1
document, but the library knows nothing about Namefi.

Three commits, and the shape of the project follows them:

| Commit | What it added |
|---|---|
| `a10e0918d` | the library: model, naming, spec parsing, runtime + gen modes |
| `d1505fbe7` | the lock file and severity-classified drift detection |
| `64b0726d1` | configurable HTTP client, body envelope wrap/unwrap, lifecycle hooks |

## Layout

```
oascmd.go     the normalized Operation model. The pivot of the whole design:
              nothing downstream of spec/ touches libopenapi types.
body.go       ResolveBody - envelope wrap/unwrap, flatness, flag collisions
naming.go     DeriveCommandPath, FlagName, the word splitter
exec.go       ExecOptions, request building, auth, retries, output printing
hooks.go      Hooks, SkipOperation, ApplyRead/ApplyBody, Confirm
validate.go   ValidateEnum - shared by runtime and by *generated* code

spec/         libopenapi -> []oascmd.Operation. The only package that imports
              libopenapi. spec.go parses operations; components.go parses
              component schemas into struct fields for gen.
runtime/      runtime.go: model -> []*cobra.Command
              verify.go:  model -> lock snapshot, for checking a live API
gen/          model -> Go source. goldenpkg/ is the checked-in output, which
              the test suite both diffs and *imports* (so it must compile).
lock/         model.go  Lock <-> Model (ToModel is the inverse of Compute)
              lock.go   Compute / Marshal / Load / Write, digests
              diff.go   the severity ladder and the report renderers
              policy.go what a policy decides, and the exit codes
cmd/oascmd-gen/  the CLI. run.go is the testable body; main.go only os.Exit's.
examples/petstore/  runnable CLI wiring both modes off one spec
testdata/petstore.yaml  the single fixture, shared by every package
```

The dependency direction is one-way and worth keeping that way:

```
spec ──> oascmd (model) <── runtime
                  ^  ^
                  |  └───── gen ──> lock
                  └──────── lock
```

`gen` depends on `lock` only through two small adapters (`LockModels`,
`ModelFromLock`) so the emitter does not have to know the lock's schema.
`lock` depends on `oascmd` but never on `gen`, which is why `additive-only`
can rebuild an old command from the lock alone.

## Design decisions worth not undoing

**One normalized model, two consumers.** `oascmd.Operation` exists so runtime
and buildtime cannot drift apart. Both call the same `Hooks.ApplyRead`, the
same `Hooks.ApplyBody`/`ResolveBody`, the same `DeriveCommandPath`, and the
same `oascmd.Execute`. If you add a feature to one mode, it belongs in the
shared layer, not in `runtime/` or `gen/`. The tests enforce this in one place
explicitly (`TestGeneratedUnwrapMatchesRuntime`), but the discipline is broader
than the one test.

**`spec/` is the only package that imports libopenapi.** That containment is
what makes the parser pin swappable and keeps `Operation` free of yaml nodes.
The pin (`libopenapi v0.25.0`) is a Go-toolchain constraint, not a preference —
see the README's "Why libopenapi".

**The generated file is checked in and compiled.** `gen/goldenpkg/generated.go`
is imported by `gen/gen_test.go`, so a golden match also proves the emitted code
compiles and its constructors have the shapes the tests call. This is why the
generator writes through `go/format` and fails loudly if the result does not
parse — a syntax error is caught at generation time with the raw buffer in the
error, not later.

**Emission order is deterministic.** `spec.Load` sorts operations by path then
method; `lock.Compute` sorts flag entries by name and hashes over a sorted key
list. Nothing downstream re-sorts. Break this and every regeneration produces a
spurious diff, which is exactly what the lock exists to prevent.

**`run` returns an exit code instead of calling `os.Exit`.** That is the only
reason `cmd/oascmd-gen` is testable at all; the whole drift matrix is tested
through it.

## Invariants

### The lock's severity classification is a promise, not a heuristic

`lock/diff.go` answers one question per field: *can something that works today
stop working?* The answer, not the size of the textual change, decides the
severity. Two rulings look wrong until you apply that test, and both are load
bearing:

- **A changed default is `breaking`.** The flag is still there, still the same
  type, still optional. But a script that never mentioned it now sends a
  different value and gets different behaviour with no error anywhere. Silent
  is worse than loud, so it ranks at the top.
- **Adding `x-cli-confirm` is `breaking`; removing it is `additive`.** Adding it
  makes the command block on a prompt, so unattended callers hang rather than
  fail. Removing a prompt cannot break anyone.

The same test explains the rest: a reordered enum is `cosmetic` (nothing a user
can observe), a new *optional* flag is `additive`, a new *required* flag with no
default is `breaking`, a body envelope move is `breaking` because the flags are
unchanged while the payload the API receives is not.

If you add a field to `lock.Operation`, you must also classify it in
`diffOperation`. An unclassified field still changes the operation `Digest`, so
the operation is reported as changed with no field lines explaining why — the
report becomes "something moved, good luck".

### The lock is computed after the hooks

`OnReadOperation` and `OnEmitOperation` can skip, rename and mutate operations.
The lock is computed from the post-hook models (`gen.GenerateWithModels`
returns them for exactly this reason) so it describes what was generated, not
what the spec said. `TestLockComputedFromPostHookModel` pins it.

### `ToModel` must stay the faithful inverse of `Compute`

`additive-only` re-emits a breaking operation from its lock entry alone. That
only works if the entry carries everything the emitter needs. Two details exist
solely to serve it:

- `Flag.APIName` records the underlying spec name **only when it differs** from
  the flag name. `ToModel` reconstructs the `x-cli-flag-name` override by
  comparing the two. Drop `APIName` and a renamed flag silently starts sending
  the wrong parameter name on replay.
- `Body.Wrap` records the resolved envelope path, so a replayed command nests
  the payload where it always did.

If you add anything to `CommandModel` that affects emitted code, add it to the
lock schema and to `ToModel`, or `additive-only` will quietly emit a *different*
command than the one it claims to be preserving.

### No `generatedAt`, ever

A timestamp would change on every run, so every regeneration would diff even
when the surface is identical. It is deliberately absent, documented as absent
in the `Lock` doc comment, and a test asserts it stays absent.

### The lock schema version is a one-way door

`lock.Unmarshal` rejects a file whose `lockVersion` is *lower* than `Version`
with "delete it and regenerate", not just a higher one. Bumping `Version`
therefore forces every consumer to discard their baseline and lose the drift
history it encoded. Only bump it for a genuinely incompatible shape change.

`GeneratorVersion` is the softer lever: changing it is classified `additive`
drift, which flags "the emitter moved, the output may differ" without
invalidating anything.

### Body envelopes: the flags are the inner properties

Wrapped bodies (`{"json": {…}}`, `{"data": {"attributes": {…}}}`) exist because
the API's transport shape leaked into its schema. `--payload.arg1` would be a
terrible thing to type, so `ResolveBody` descends into the envelope, makes the
inner properties the flags, and records the path it took in `Body.WrapPath`.
`NestBody` re-wraps at submit time. `--data` is unaffected: it is always the
whole raw body, envelope included.

The rules that keep this honest:

- **Automatic unwrapping only descends through an *unambiguous* chain** — one
  property, and that property is an object. `{"json": {…}, "meta": …}` is
  ambiguous, so nothing is unwrapped. Loosening this would silently relocate
  flags for any spec with a two-property body.
- **An explicit path that is missing or not an object is a hard error**, not a
  fallback. Silently ignoring a bad `x-cli-body-unwrap` would produce a CLI that
  looks right and posts to the wrong shape.
- **Colliding flag names are a hard error.** Unwrapping can bring `petId` and
  `pet_id` into the same namespace; both kebab to `--pet-id`. `checkFlagCollisions`
  refuses rather than letting one win.
- **Non-flat means `--data` only.** If any resolved property is still an object,
  `ResolveBody` clears `Body.Props` entirely. Emitting a partial flag set for a
  half-representable body would be worse than emitting none.
- **Wrap is outermost, unwrap is inside it.** `x-cli-body-wrap: envelope` plus an
  auto-detected `json` envelope gives `{"envelope": {"json": {…}}}`. The order is
  fixed in `ResolveBody`; both modes read the resulting `WrapPath`, so they
  cannot disagree.

## Gotchas

**`SkipOperation` is compared two different ways.** `Hooks.ApplyRead` uses
`errors.Is`, so a wrapped `SkipOperation` from `OnReadOperation` works. But
`gen.GenerateWithModels` (for `OnEmitOperation`) and `runtime.buildCommand` (for
`OnBeforeCreateCommand`) use `err == oascmd.SkipOperation`. A wrapped sentinel
from those two hooks is treated as a fatal error instead of a veto. Worth
unifying on `errors.Is`; until then, do not wrap it there.

**`NameFunc` must be pure.** `runtime.BuildFromOperations` calls it twice per
operation (once for the command, once to resolve the group). A `NameFunc` with
side effects or a counter will produce a tree that does not match itself.

**Required bodies are enforced in runtime mode only.** Runtime returns "a
request body is required: pass --data or the body flags" when a required body is
empty; the generated command sends the request with no body and lets the server
reject it. Verified against the example:

```
$ go run ./examples/petstore       orders create   # -> a request body is required: …
$ go run ./examples/petstore typed orders create   # -> POST is sent, server decides
```

Likewise, a *required body property* is never enforced in either mode —
`MarkFlagRequired` is emitted only for path/query params, and runtime's
`registerFlag` is called for body props without `required`. The lock does record
`required` for body flags, so `diff` will classify adding one as `breaking`
while nothing at runtime actually demands it. If you make required-ness real,
change both modes together.

**`ExecOptions.Out` defaults differently by layer.** `oascmd.Execute` falls back
to `io.Discard` when `Out` is nil; the command layers (runtime `RunE` and the
emitted `RunE`) substitute `os.Stdout` first. Call `Execute` directly without
setting `Out` and the response goes nowhere.

**`ExecOptions.Headers` overwrite, they do not add.** `Execute` does
`Header.Del` then `Add` per key, so they win over the executor's own `Accept`
and `Content-Type`. That is intentional; it is also how you break JSON bodies if
you set `Content-Type` carelessly.

**The caller's `*http.Client` is never mutated.** `CookieJar` is installed on a
shallow copy. Keep it that way — a shared client picking up a jar is an
action-at-a-distance bug.

**Retries replay through `GetBody`.** `SetRequestBody` is the only supported way
for an `OnBeforeExecute` hook to change the payload, because it updates
`ContentLength` and `GetBody` together. Assigning `req.Body` directly leaves a
retry sending an empty body. `maxRequestAttempts` (100) caps a hook that always
asks to retry.

**Duplicate constructor names get a numeric suffix, silently.** `gen` appends
`2`, `3`, … when two operations derive the same `FuncName`. `lock.Compute` does
the same for duplicate keys (`key#2`). Both are defensive last resorts; if you
see them in a lock file, the real fix is `x-cli-name` or a `NameFunc`.

## Build and test

Every command below was run in `projects/oascmd` and passed. `GOTOOLCHAIN=local`
is required — the repo's toolchain is go1.23.5 and the module declares the same.

```bash
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local gofmt -l .        # prints nothing
GOTOOLCHAIN=local go test ./...     # all packages ok
```

Golden regeneration, which is a no-op when the emitter has not changed:

```bash
GOTOOLCHAIN=local UPDATE_GOLDEN=1 go test -count=1 ./gen/
```

The example, both modes:

```bash
GOTOOLCHAIN=local go run ./examples/petstore --help
GOTOOLCHAIN=local go run ./examples/petstore orders create --help   # shows --pet-id/--quantity, the unwrapped envelope
GOTOOLCHAIN=local go run ./examples/petstore typed orders create --help
```

The checked-in example output is reproducible: generating from
`testdata/petstore.yaml` into a scratch directory produces a file byte-identical
to `examples/petstore/petstoregen/generated.go`, and running `-diff` against the
committed `oascmd.lock.json` reports "Up to date" with exit 0.

## Deliberately not done

From the code and the README's "Known limits":

- **Only `application/json` request bodies.** `spec.convertBody` returns early
  for anything else, so form and multipart operations get no flags at all.
- **Header and cookie parameters are not flags.** `convertParam` drops anything
  that is not `in: path` or `in: query`. They belong to `ExecOptions.Headers` /
  `Cookies` / `Auth`, which are per-client concerns rather than per-invocation
  ones.
- **Responses are printed, not decoded.** `gen` emits response-shaped structs so
  a caller *can* unmarshal, but no command does it. Keeping the executor
  response-agnostic is what lets one `Execute` serve both modes.
- **Validation stops at required-ness, scalar type and enum.** No `pattern`,
  `minimum`, `oneOf` or nested object checking; `--data` is only checked for
  being syntactically valid JSON. The server is still the authority.
- **Runtime mode does not detect command-name collisions.** Two operations
  deriving the same path silently produce two sibling commands with the same
  `Use`. Buildtime mode at least suffixes the constructor.
