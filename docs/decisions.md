# tscomfy-go: decisions

The decisions someone could plausibly propose to undo, with why they were made and what was
rejected. The design itself is `docs/spec.md`; the measurements are in `docs/research/`. An entry
is added only for such a decision or for a new dependency; values and details live in the spec,
with their reason next to them.

## D1. Workflows are TypeScript, turned into templates at development time

2026-10-19.

**Decision.** Workflows are written in TypeScript with tscomfy. `go generate` runs that TypeScript
once and writes each workflow as a template (the validated prompt plus its parameter slots) and a
typed Go struct with constants for its node ids; both are committed. Nothing a backend imports
runs TypeScript, Bun or a JS engine. A graph whose shape varies per request is written as fixed
variants, each its own template (spec §1, §3 to §6).

**Why.** tscomfy types every node from `/object_info`, so a wrong connection or value fails at
compile time; Go has no literal types, required fields or sum types to match it. What a request
changes is values (prompt text, seed, size, an uploaded image), not the graph's shape, so the
TypeScript only has to run when a workflow changes. Production stays plain Go: no Bun in the
image, no process handling, no IPC. Go checks parameter types and numeric bounds; the server
validates the rest.

**Rejected** (measured 2026-10-19 on the real 1,758-node schema, Go 1.27.2, Bun 1.4.3;
`docs/research/runtime-and-templates.md`):

- **Building graphs in Go** from generated types. Of three encodings, only a typestate builder
  (Go 1.27 generic methods) enforced as much as tscomfy, at about 2 GB of compiler memory, a 4.3 s
  cold build, and gopls unable to find references to generic methods. The cheaper encodings
  checked far less.
- **A Bun process per request:** 64–70 ms and about 100 MB each, nearly all of it reloading the
  schema to build a 7-node graph; plus a 95 MB binary to ship.
- **A long-lived Bun process:** about 2 ms per build, but supervision, framing and restarts move
  into Go, and Bun still ships.
- **An embedded JS engine:** `modernc.org/quickjs` is fast (0.67 ms per build, byte-identical)
  but brings `modernc.org/libc` and its tree into every user's binary and isn't a runtime tscomfy
  is tested on; goja and QuickJS-ng can't load tscomfy's schema at all (no `JSON.parse`
  source-text access, tscomfy ADR-0013).
- **Exporting from ComfyUI's UI and patching in Go:** untyped, and node ids are numbers the UI
  changes.

The owner's private pages comparing these: https://claude.ai/artifact/5JtK5bB9VdaCVrhTWBZT59 (Go
graph building) and https://claude.ai/artifact/3q78Ds2kY64K1H7Pt1YDgH (ways to run workflows).

## D2. The client follows Go conventions for remote jobs

2026-10-19.

**Decision.** A context bounds only the call it is passed to; cancelling a job is an explicit
`Job.Cancel`, and `Run` is the one call whose context governs the job, as in
`exec.CommandContext`. A job that doesn't succeed is a typed error with its data attached, and
`Wait` still returns the result when the server settled the job, as `exec.Cmd.Output` returns
output with an `*ExitError`. Events are a single-use `iter.Seq[Event]` with no error side
channel. Options are struct pointers (spec §7.1).

**Why.** Every remote-job library surveyed (Temporal, Google Cloud long-running operations,
Replicate, Docker, River) starts a job, waits with a context that bounds only the wait, and
cancels with a separate call; failures are typed errors reached with `errors.As`. Survey with
signatures: `docs/research/go-client-conventions.md`.

**Rejected:** tscomfy's API ported literally (a signal that owns the job, an outcome union with
an `Err()` method: two levels of error handling); two channels for result and error (callers
select on both, an abandoned one leaks); `iter.Seq2[Event, error]` or `Next`/`Err` streams (there
is no error to report mid-stream); callbacks in the options (a slow one stalls delivery);
functional options.

## D3. What `Submit` does when the POST fails or its context ends

2026-10-19.

**Decision.** `Submit` returns as soon as its context ends; a POST in flight continues in a
goroutine the client owns. Unless the server answered 400, any failure after the request may have
been sent ends with a best-effort `POST /api/jobs/{id}/cancel` for the minted prompt id. A 200
naming another prompt id cancels that id and is a `*ProtocolError`. `Close` aborts in-flight POSTs
without the cancel (spec §7.4).

**Why.** Only the answer to `POST /prompt` says whether a prompt was queued, and a queued prompt
nobody waits for keeps the GPU busy. The client mints the prompt id, so it can name a job it never
heard back about, and the per-job cancel answers 200 for unknown ids too.

**Rejected:** running the POST under `context.WithoutCancel` (`Submit` could block forever on a
hung server); returning without a cancel (a queued prompt runs unwatched); cancelling only after a
200 (misses a POST whose answer was lost).

## D4. `@tscomfy/go` lives in this repository; the template format is the contract

2026-10-20; the pairing rule revised the same day.

**Decision.** The TypeScript package lives in `ts/` and depends only on `@tscomfy/core`'s public
API. One `v<version>` tag publishes the Go module and `@tscomfy/go` with the same version, from
CI. The generator checks the template `format` number the export reports, not the package
version (spec §3.5, §6.1).

**Why.** The export writes the template format and Go reads it: one contract, changed in one
commit and tested end to end in one gate. Checking the format rather than the version lets a
backend update the Go module for a client fix without touching its workflows project, while a
format change, which needs a new number anyway, is still caught.

**Rejected:** the package in the tscomfy monorepo (the contract would span two repositories and
releases, and tscomfy would carry Go-specific code); requiring equal versions on both sides
(lockstep upgrades for changes that don't touch the format).

## Dependencies

Go uses the standard library and `github.com/coder/websocket`; TypeScript uses `@tscomfy/*`. The
approved toolchain is listed in spec §2.4. Any other dependency, dev tools included, needs the
owner's approval and an entry here.
