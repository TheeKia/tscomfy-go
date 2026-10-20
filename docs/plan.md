# tscomfy-go: plan

How tscomfy-go gets built: the phases, what each delivers, and what each settles. The design
itself is `docs/spec.md`; the main decisions and what they rejected are in `docs/decisions.md`.
When a phase changes the design, the spec (and the decision entry, if any) changes in the same
commit; this file only tracks the work.

## Status

| Phase | State |
|---|---|
| 0. Setup | Done 2026-10-20: gate green locally and in CI |
| 1. Templates | Done 2026-10-20: TypeScript to committed Go, byte-identical to tscomfy |
| 2. Client core | Done 2026-10-20 |
| 3. Events | Done 2026-10-20 |
| 4. Robustness | Done 2026-10-20 |
| 5. Files | Done 2026-10-20 |
| 6. Live tests | Done 2026-10-20: all pass against the owner's ComfyUI 0.42 |
| 7. Release | Done 2026-10-20: v0.1.0 released (Go module and `@tscomfy/go`) |

## Rules for every phase

- A phase ends with the gate green (spec §9), run locally, and with no known TODOs.
- Every value it settles (spec §10) is written into the spec, with its reason.
- Research comes before code: run it, read the source, measure it, and record the evidence.
- Only the latest releases are supported (spec §1.2): no code for older ComfyUI, Go or tscomfy
  versions.

## Phases

### 0. Setup

- **Done:** `docs/spec.md`; `docs/decisions.md` (D1 workflows as templates, D2 the client's Go
  conventions, D3 Submit's failure rule, D4 the npm package in this repository). The npm name
  `@tscomfy/go` is confirmed. A review on 2026-10-20 checked the spec against ComfyUI 0.42,
  tscomfy and Go 1.27, corrected it and cut what had no use (History below).
- **Repository setup, done 2026-10-20** (`make check` green locally):
  - git (`main`), `.gitignore`. Fuzzing needs no ignore rule: its cache is in `$GOCACHE`, and
    `testdata/fuzz/` holds regression inputs, which are committed.
  - **Pinned, newest on 2026-10-20:** Go 1.27.2 (`go 1.27`), golangci-lint 2.15.0, govulncheck
    1.9.0, Bun 1.4.3, TypeScript 7.0.2, oxlint 1.89.0, oxlint-tsgolint 7.0.2004, oxfmt 0.74.0;
    the Actions `actions/checkout` 7.0.1, `actions/setup-go` 7.0.0, `oven-sh/setup-bun` 2.2.0,
    `golangci/golangci-lint-action` 9.3.0.
  - **Go:** `go.mod` (`go 1.27`, `ignore node_modules`); `comfy/`, `workflow/`,
    `internal/format/`, `internal/comfytest/`, each with only its package doc comment;
    `.golangci.yml` with each linter justified, and `depguard` enforcing the allowed dependencies
    and the packages' direction (checked with a violating file).
  - **TypeScript:** the workspace `package.json`, `tsconfig.json` and `tsconfig.base.json`,
    tscomfy's `.oxlintrc.json` (without its package-specific overrides) and `.oxfmtrc.json`, and
    `ts/` as `@tscomfy/go` 0.0.0 with its module doc comment.
  - **The gate:** the `Makefile`, one target per step (spec §9). `bun test` runs with
    `--pass-with-no-tests` until there are tests, instead of a placeholder test.
  - **CI:** `.github/workflows/check.yml` runs `make check`.
  - **Fixtures:** `internal/comfytest/recordings/` (tscomfy's nine protocol recordings) and
    `testdata/object-info/` (five of its mock schemas; Phase 1 copies any other the export tests
    need), each with a README of its provenance.
  - **Left to the phase that first uses them,** so nothing unused is declared:
    `cmd/tscomfy-go/` (a `main` package can't be empty) and the `@tscomfy/core`, `@tscomfy/cli`
    and `@types/bun` dev dependencies in Phase 1; `github.com/coder/websocket` in `go.mod` in
    Phase 2 (`go mod tidy` drops a requirement nothing imports).
- **GitHub:** `github.com/TheeKia/tscomfy-go` (public); CI green on the first commit.
- **Exit:** `make check` is green on the empty packages, locally and in CI.

### 1. Templates

No ComfyUI needed. **Done 2026-10-20.** The export runs as `bun x --bun --no-install
tscomfy-go-export index.ts` (spec §6.1). `template` checks everything it can when it is defined
(§3.6). The tests replaced four of tscomfy's mock schemas with `params.json`, written here,
because none of the four has an output node, so tscomfy's validation refused every prompt of
them. An independent review then found and fixed: template names that `go build` skips
(`_test`, `_<GOOS>`), node ids that broke out of comments, defaults that `template` accepted and
`Load` refused, golden output that nothing compiled, and the example's TypeScript missing from
`tsc -b`.

- **`@tscomfy/go`:** `template`, `param`, the export command and its checks (spec §3).
- **The template format** types in `internal/format` (spec §4).
- **Package `workflow`:** `Load`, `Fill`, the checks, `ParamError` (spec §5).
- **The generator** in `cmd/tscomfy-go/`: export invocation, the format check, types, names,
  constants, the output directory rules (spec §6), and the `go tool` setup.
- **Dev dependencies:** `@tscomfy/core` (for `ts/`, matching its peer range), `@tscomfy/cli` (the
  example's schema) and `@types/bun` (the tests), pinned.
- **`examples/txt2img/workflows`:** a workspace member with the txt2img template of spec §3.1.
  Its schema is generated, and committed, by a package script running
  `tscomfy generate --from ../../../testdata/object-info/basic-pipeline.json --out comfy/local
  --import-extension .ts` (the import extension because §3.1 imports `./comfy/local/index.ts`).
  Its generated Go is committed too. The handler comes in Phase 7.
- **Tests:** export tests on the mock schemas, golden tests, the cross-language property, fuzzing
  of `Load` and `Fill`. The gate gains short fuzz runs and the end-to-end check (spec §9).
- **Start from:** `docs/research/runtime-and-templates.md` §3, the prototype export and fill.
- **Settles:** the export command's invocation.
- **Exit:** a workflow in TypeScript becomes committed Go whose `Build()` output matches tscomfy's
  own, byte for byte.

### 2 to 6. The client, done 2026-10-20

Built together, since each part's tests run through the same fake: `comfy` (spec §7, §8) and
`internal/comfytest`, the fake ComfyUI over in-memory connections that every client test runs
inside `synctest.Test` (fake time, and a goroutine that outlives `Close` fails the test). The
settled values are in spec §10. An independent review found, and the fixes and their tests
cover: record reads that gave up during outages the reconnect budget should ride out; an idle
ping under the caller's context that could kill the shared socket; a job registered on a socket
that had just dropped; `Cancel` doing nothing for jobs that ended unsettled; `Dial` calling its
own deadline an unsupported server; coalescing that lost `SaveImageWebsocket` images; Python's
lone surrogates failing to decode; and fake-server faults that let tests pass for the wrong
reason. Every rule of §8 that a test guards was checked by breaking it (the test then fails).
The live tests run a model-free prompt, a 63-bit integer round trip, `SaveImageWebsocket`, a
rejection, a node error, cancels of a running and a queued job, and an upload into `LoadImage`.

The phases as planned:

### 2. Client core

Read first: the tscomfy files listed in AGENTS.md for Phase 2.

- **`github.com/coder/websocket`** v1.8.15 (or newer) added to `go.mod`.
- **`Dial`:** URL rules, the socket, flags, the read limit, the version check.
- **`Submit`:** minting, the POST, rejection and partial acceptance, the failure rule (D3).
- **The socket reader** and the dispatch of `status`, `execution_start`, `execution_cached` and the
  end event.
- **Settling from the record,** the list read for jobs that haven't started (spec §8, rules 4
  and 5); `Wait`, `Done`, `Cancel`, `Run`, `Close`.
- **`comfytest`,** minimal and in memory (spec §7.11): replaying the success, rejected, partial,
  error, cancel and pending recordings, embedded from `internal/comfytest/recordings/`.
- **Tests:** those recordings' cases ported from tscomfy, inside `synctest.Test`; a live `Dial`
  test.
- **Settles:** `Run`'s cancel timeout, `HTTPError.Body` truncation, the record read timeout, the
  websocket read limit.
- **Exit:** a recorded run submits, settles and returns its result through the fake server, and
  no goroutine outlives `Close`.

### 3. Events

- JSON and binary decoding, `progress_state` differences, live outputs, blocked and text events.
- The bounded, coalescing buffer and the iterator's semantics (spec §7.7).
- Fuzz tests of the decoders; the sampler, websocket and blocked recordings.
- **Settles:** the `TextEvent` cap.

### 4. Robustness

- Reconnects (fresh id, backoff, budget), pings, the record-reading rules, "started" from
  records, close semantics.
- Fault-injection tests: sockets dropped mid-run, silent sockets, 404 records, a slow reader's
  memory bound, server restarts. All on fake time, with leak checks (spec §9).
- **Settles:** ping interval, timeout and idle threshold.

### 5. Files

`Upload`, `Open`, `FileURL`, output parsing (spec §7.8, §7.9).

### 6. Live tests

Against the owner's local ComfyUI, with model-free prompts, opt-in (`TSCOMFY_GO_LIVE_URL`).

### 7. Release

- `comfytest` made public if its API is settled (**settles** that). **Settled:** internal in
  v0.1 (spec §7.11).
- Guides: getting started, writing workflows, running jobs, deploying. **Done:** `docs/guides/`.
- `examples/txt2img`: workflows, generated Go, an HTTP handler that streams progress over SSE.
  **Done**, with a test against the fake server.
- `release.yml`; v0.1.0 tagged and published from CI. **`release.yml` done:** on a `v*` tag it
  checks the tag against `ts/package.json`, runs the gate, packs `@tscomfy/go`, installs the
  tarball in a fresh project and runs the generator on it, then publishes with npm trusted
  publishing. **Remaining, the owner's:** the trusted publisher for `@tscomfy/go` on npm (it names
  this repository and `release.yml`), then the tag. npm adds a trusted publisher only to a package
  that exists (`npm trust` answers 404 otherwise, tried 2026-10-20), so 0.1.0 is the one version
  published by hand, as tscomfy's first release was: the tarball `bun pm pack` made after a green
  gate, checked as `release.yml` checks it. The `v0.1.0` run of `release.yml` then finds 0.1.0 on
  npm and skips it; every later version comes from CI, with provenance. **Released 2026-10-20:**
  0.1.0 published by hand (its shasum, `843128f1…`, equals the local pack), the trusted
  publisher set, and the `v0.1.0` run of `release.yml` (39348091120) passed: the gate, the pack,
  the install check, and a publish job that skipped the version already on npm. The module proxy
  serves `github.com/TheeKia/tscomfy-go v0.1.0`.
- API compatibility checks (`apidiff`): **not for 0.x.** The project supports only the latest
  releases and promises no compatibility before 1.0; the check comes with 1.0, with its entry in
  `docs/decisions.md`.

## Requests to tscomfy

None needed. Noted for the owner, not blocking:

- `@tscomfy/client` announces `supports_progress_text_metadata` and decodes a prompt-id prefix on
  progress text, but ComfyUI 0.42 has neither (the flag exists only on its unmerged
  `progress-text-prompt-id` branch).
- `param(node, input)` can't type-check `input`, because `Out` and `NodeHandle` don't carry their
  node's class (`packages/core/src/graph/types.ts`). The export catches a wrong input name.

tscomfy is not changed from this repository.

## History

- **2026-10-19, first design session** (in the tscomfy repo): a Bun binary that Go ran once per
  request. Superseded the same day.
- **2026-10-19, second session:** measured running TypeScript per request and its alternatives,
  and replaced it with templates generated at development time (D1). Surveyed Go libraries for
  remote jobs (D2).
- **2026-10-20:** the plan split into this roadmap, `docs/spec.md` and six ADRs.
- **2026-10-20, review:** the spec was checked against ComfyUI 0.42's source, tscomfy and Go
  1.27, and the owner approved these changes:
  - Corrected: progress text never carries a prompt id (`supports_progress_text_metadata` isn't
    in ComfyUI 0.42); the job API dates from ComfyUI 0.6, so the version check, not a probe of
    `/api/jobs`, is what guards the per-job cancel; the fake server can't run on a real socket
    under `testing/synctest`, so it runs in memory.
  - Only the latest releases are supported (ComfyUI 0.42).
  - The generator checks the template format, not the package version.
  - Templates lost `default` (read from the prompt) and `source` (unused, churned every file).
  - The client lost `ClientID`, `Features`, `Job.Progress`, tscomfy's diagnostics model (now
    `NodeError` and `RejectedError` fields), output kind guessing, and `UploadOptions.ContentType`;
    `workflow` lost `Template.Nodes`; the generator lost `-pkg` and default exports.
  - One list read replaced per-job reads for jobs that haven't started; the 404 rule was settled.
  - `comfytest` starts internal; the npm API report and `apidiff` were dropped for now.
  - The six ADRs became `docs/decisions.md`.
- **2026-10-20, audit:** a codebase audit found and fixed a loop's outputs collapsing into one (a
  result keeps one output per node the server ran), `Upload` blocking after an early answer,
  cut-off record reads ending jobs, unhelpful generator errors for stray stdout and byte-order
  marks, and `TemplateError` missing from `@tscomfy/go`'s exports. Version 0.2.0.
