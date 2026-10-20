# AGENTS.md

Instructions for AI agents (and humans) working in this repo. Read this first, then
`docs/spec.md` (the design, and the source of truth), `docs/decisions.md` (the main decisions,
what they rejected and why) and `docs/plan.md` (the phases and their status). When a decision
changes, change the spec and its entry in `docs/decisions.md` in the same commit.

## The project

tscomfy-go lets a Go backend start ComfyUI generations, follow their events and fetch their
outputs, with the workflows written in TypeScript with tscomfy. At development time,
`go generate` runs the TypeScript once and writes each workflow as a template (the validated
prompt plus its parameter slots) and a typed Go struct with constants for its node ids; both are
committed. In production a Go struct's `Build()` fills the template, and a native Go client runs
the prompt on ComfyUI. Spec §1 and §2 describe it; decision D1 explains why, with measurements.

**Out of scope** (spec §1.3): building graphs in Go, TypeScript at run time in any form (Bun
processes, workers, embedded JS engines), pools of servers, queue and history browsing, Comfy's
hosted API, managing ComfyUI. Don't add these. Building graphs in Go and running TypeScript per
request were measured and rejected (D1); don't re-propose them.

**No backward compatibility.** Only the latest releases are supported: ComfyUI 0.42, Go 1.27,
tscomfy 0.2, Bun 1.4, TypeScript 7 (spec §1.2). Don't write code for older versions.

**Status:** v0.1.0 is released: the Go module and `@tscomfy/go` on npm. Every phase of the plan
is done. Releases come from CI on a `v<version>` tag; bump `ts/package.json` first. The plan's
status table is kept current.

## Where things are

The layout is spec §2.3.

| Path | What |
|---|---|
| `Makefile` | The gate: `make check`, one target per step (spec §9); tool versions pinned here. `make fixtures` regenerates every committed generated file |
| `comfy/`, `workflow/` | The public packages (spec §5, §7) |
| `cmd/tscomfy-go/` | The generator (spec §6); its golden output is the package `internal/golden/` |
| `internal/format/`, `internal/comfytest/` | Reading the template format; the fake ComfyUI, with `recordings/` (spec §7.11) |
| `ts/` | The npm package `@tscomfy/go` (spec §3); `ts/test/comfy/` holds generated test schemas |
| `examples/txt2img/` | The example: workflows in TypeScript, the generated Go, an SSE handler in `main.go` |
| `docs/guides/` | Getting started, writing workflows, running jobs, deploying |
| `testdata/object-info/` | Mock schemas, for the tests of `@tscomfy/go` and the example |
| `.golangci.yml`, `.oxlintrc.json`, `.oxfmtrc.json`, `tsconfig.base.json` | Lint, format and compiler settings |
| `.github/workflows/check.yml`, `release.yml` | CI: runs `make check`; on a `v*` tag, releases |
| `docs/spec.md` | The design: authoring (§3), template format (§4), `workflow` (§5), the generator (§6), the client (§7), protocol rules (§8), tests and the gate (§9), open values (§10) |
| `docs/decisions.md` | D1 to D4: the decisions someone could propose to undo, with what was rejected |
| `docs/research/runtime-and-templates.md` | The measurements behind D1, and the prototype code for template export (TypeScript) and fill (Go) that Phase 1 starts from |
| `docs/research/go-client-conventions.md` | The survey of Go remote-job APIs behind D2 |
| `docs/plan.md` | Phases, their deliverables and exit criteria, status, notes for tscomfy, history |
| `README.md` | The public overview; keep it in step with what exists |

## tscomfy, the sister project

tscomfy is at `/home/kia/code/tscomfy` (same owner, MIT). Its client, `@tscomfy/client`, already
implements what the `comfy` package needs, against the same server. Read it as the reference for
behavior, not for API shape: the Go API follows Go conventions (spec §7.1, D2). Where it and
ComfyUI 0.42 disagree, ComfyUI's source wins (spec §8 lists what was checked).

The paths below are relative to `/home/kia/code/tscomfy`.

Read before Phase 0 (setup), to mirror tscomfy's tooling:

- `package.json`: scripts and pinned tool versions.
- `tsconfig.base.json`, `.oxlintrc.json`, `.oxfmtrc.json`.
- `.github/workflows/check.yml` and `release.yml` (npm trusted publishing with provenance, the
  tag-matches-version check, the build/publish split).
- `internal/fixtures/README.md`, `internal/fixtures/mocks/object-info/` and
  `internal/fixtures/mocks/protocol/`: the mocks to copy, and the rule that fixtures are
  hand-written, never real dumps.

Read before Phase 1 (templates): `packages/core/src/index.ts` (the public API: `workflow`,
`toApi`, `stringify`, `parse`), `packages/core/src/graph/workflow.ts` (node ids: `{ id }`,
automatic `"1"`, `"2"`, …), `docs/decisions/0013-generated-schema-module.md`,
`packages/core/src/schema/types.ts` (input kinds and bounds), `packages/cli` (`tscomfy generate
--from`), `docs/guides/`, and `examples/txt2img/` and `examples/hires-fix/`. Then this repo's
`docs/research/runtime-and-templates.md` §3.

Read before Phase 2 (client):

- `docs/research/comfyui-execution.md`: the execution protocol, with citations into ComfyUI.
- tscomfy's `docs/plan.md` §17, and its ADR-0023 to ADR-0025: the client's design.
- `docs/guides/running-workflows.md` and `docs/api/client.md`: how the client behaves.
- `packages/client/src/` and its tests in `packages/client/test/` (port their cases).
- `internal/fixtures/src/fake-server.ts` and `protocol.ts`, and the recordings in
  `internal/fixtures/mocks/protocol/`: the fake server to port and the runs it replays.

Don't change tscomfy from here. If tscomfy-go needs something from it, write it down in the plan
and ask the owner.

## Working agreements

These come from the project owner. Follow them unless told otherwise.

- **Quality bar is very high.** Clean architecture, idiomatic Go, strict types, no shortcuts. A
  phase is not done while it has known TODOs or the gate is red.
- **Simple over clever.** Every type, option, file and document earns its place; cut what has no
  user. Ask "do we need this?" before adding machinery.
- **Research before deciding; don't assume.** Verify claims hands-on (run it, read the source,
  measure it) before they go into the spec or code, and record the evidence there.
- **Modern tooling, newest stable versions, verified to work:** Go 1.27, Bun 1.4, TypeScript 7,
  oxlint and oxfmt for TypeScript, golangci-lint v2 for Go.
- **Dependencies:** Go uses only the standard library and `github.com/coder/websocket`.
  TypeScript uses only `@tscomfy/*`. The approved toolchain is listed in spec §2.4. Any other
  dependency, dev tools included, needs the owner's approval and an entry in
  `docs/decisions.md`: tscomfy's `expect-type`, for one, is not approved.
- **CI runs the local gate** (spec §9); it never replaces running the gate locally before
  declaring work done.
- **Releases come from CI.** A `v<version>` tag publishes the Go module (the tag itself) and the
  npm package with the same version. Don't publish from a machine.
- **Never commit real ComfyUI data.** `/object_info` dumps, prompts and outputs from a real server
  stay out of git, examples included. Committed tests use hand-written mocks, and tscomfy's
  protocol recordings, which were distilled by hand from captures (spec §2.3), never raw
  captures.
- **License hygiene:** the project is MIT. ComfyUI's frontend and backend are GPL-3.0: read them
  as a specification only. Never copy their code or text.
- **Git:** never add Claude as a co-author (no `Co-Authored-By: Claude` trailer) and no Claude
  attribution in pull request descriptions. Commit only when the owner asks.

## Architecture and API rules

The rules are in the spec: dependency direction and no TypeScript at run time (§2.2),
deterministic templates (§4), the client's conventions (§7.1), opaque prompts (§7.3), errors
(§7.10). Beyond those:

- **Public API:** every exported symbol has a doc comment, and every main entry point an
  `Example` function. Keep the surface small: an option or a type is added with a user.
- Panic only for programmer misuse.

## Go conventions

- `context.Context` is the first parameter of anything that blocks or does I/O. No goroutine
  outlives `Close()`.
- Wrap errors with `%w` so `errors.Is` and `errors.As` work; sentinel errors are `Err…`, error
  types `…Error`.
- JSON through `encoding/json/v2` and `encoding/json/jsontext`; logging through `log/slog`;
  iteration through `iter.Seq`.
- Tests: table-driven where it reads better, `t.Context()`, `-race -shuffle=on`. Anything with
  time or goroutines runs inside `synctest.Test` over the in-memory fake server (spec §7.11),
  which also catches leaks; never real sleeps. Fuzz the decoders.

## Gotchas

The protocol's are in spec §8. Others:

- Sort strings by byte order (`slices.Sort` in Go, `toSorted()` with no comparator in
  TypeScript), never locale-aware.
- In TypeScript, `JSON.parse` rounds integers beyond 2^53: use tscomfy's `parse` and `stringify`.
  In Go, never decode a prompt into `any`.
- Python servers send `null` for unset fields: treat it as absent.
- In API JSON a bare list is a link; list-valued widget values are sent as
  `{"__value__": [...]}`.

## Local environment

Installed on the owner's machine (2026-10-20): Go 1.27.2, Bun 1.4.3, golangci-lint 2.15.0 and
govulncheck (through mise), gopls, make. Check for newer stable versions before pinning.

- **Git:** the repository's default branch is `main`; its remote is
  `github.com/TheeKia/tscomfy-go`.
- **npm:** `@tscomfy/go` has a trusted publisher for this repository's `release.yml`; no npm
  token exists anywhere.
- **ComfyUI's source** is at `~/ai-tools/ComfyUI` (a git checkout of 0.42), readable as a
  specification only.

The owner's ComfyUI runs at `http://127.0.0.1:8188` (0.42.0), and agents may use it for testing:

- Read-only requests (`/object_info`, `/system_stats`, `/api/jobs`, `/api/jobs/{id}`, `/view`)
  and websockets, any time.
- The live tests (`TSCOMFY_GO_LIVE_URL`), which queue only model-free prompts and may upload
  small test images. They cancel only jobs they queued themselves.
- Never `POST /interrupt` or `/queue` (they act on whatever is running or queued, the owner's
  work included), never queue a prompt that loads a model, and never start, stop or restart
  ComfyUI (it lives in `~/ai-tools/ComfyUI`); ask the owner.
