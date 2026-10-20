# tscomfy-go: specification

This is the design of tscomfy-go and the source of truth for it. `docs/decisions.md` records the
main decisions (D1 to D4) and what they rejected; `docs/research/` holds the measurements;
`docs/plan.md` holds the phases and their status. A change to a decision changes this file and
its entry in `docs/decisions.md` in the same change.

"Must" and "never" are requirements. Values marked **(settle in Phase N)** are open; §10 lists
them all.

## 1. Scope

### 1.1 What it does

tscomfy-go lets a Go backend start ComfyUI generations, follow their events and fetch their
outputs. The workflows are written in TypeScript with
[tscomfy](https://github.com/TheeKia/tscomfy), which types every node from the server's
`/object_info`.

1. **At development time,** the author writes each workflow with tscomfy, names the nodes Go
   cares about, and declares which inputs are parameters (§3).
2. **`go generate`** runs that TypeScript once. Each workflow becomes a template: the prompt,
   validated by tscomfy, plus its parameter slots (§4). The generator writes a typed Go struct
   and node-id constants for it (§6). The developer commits both.
3. **In production,** the struct's `Build()` fills the template (§5), and the `comfy` client
   submits the prompt, streams its events, settles its outcome and moves files (§7).

TypeScript never runs in production (D1).

### 1.2 Supported versions

No backward compatibility: each dependency is supported at its latest release only. When a new
release changes something the project relies on, the project follows it and drops the old
behavior.

| | Version |
|---|---|
| Go | 1.27 (`go 1.27` in `go.mod`) |
| ComfyUI | 0.42 (0.42.0 and 0.42.1 serve the same protocol; `Dial` refuses older versions) |
| Bun | 1.4, at development time only, to run the export |
| TypeScript | 7 |
| tscomfy | `@tscomfy/core` 0.2 |

### 1.3 Not in v1

- Building graphs in Go (D1).
- TypeScript at run time, in any form: Bun processes, a long-lived Bun process, embedded JS
  engines (D1). A graph whose shape varies per request is written as fixed variants, each its
  own template.
- Pools of servers, queue and history browsing, Comfy's hosted API, partner sign-in (keys go in
  `SubmitOptions.ExtraData`), starting or managing ComfyUI.
- `SaveImageWebsocket` images as outputs: they arrive as `PreviewEvent`s.
- Typed outputs per node class, and result structs per workflow: `res.Files(id)` with the
  generated constants is enough.
- Go constants for combo options, and Go-side checks of combo values. The server validates them,
  and a changed server setup needs a code change anyway.
- A schema drift check from Go. `tscomfy check` in CI covers it.

## 2. Architecture

### 2.1 Development time and run time

```
Development (the developer's machine, and CI)              Production (the backend)
─────────────────────────────────────────────              ─────────────────────────────────────
workflows/            TypeScript, Bun                      internal/workflows/  generated, committed
  comfy/local/        ← tscomfy generate --url …             txt2img.json        template, embedded
  txt2img.ts          ← template("txt2img", …)                txt2img.go          Txt2Img{…}.Build(), node ids
  index.ts            ← exports the templates              handlers
        │                                                      │ Build() → comfy.Prompt
        └── go generate → go tool tscomfy-go ──writes──▶       └── comfy.Client ──HTTP + WebSocket──▶ ComfyUI
```

### 2.2 Packages

| Package | Role |
|---|---|
| `comfy` | The ComfyUI client (§7) |
| `workflow` | Loads templates and fills in parameter values (§5) |
| `cmd/tscomfy-go` | The generator, run with `go tool tscomfy-go` (§6) |
| `internal/format` | Reads and checks the template format and the export's output (§3.5, §4), for `workflow` and the generator |
| `internal/comfytest` | A fake ComfyUI for this module's tests (§7.11) |
| `ts/` (`@tscomfy/go`) | `template`, `param` and the export command (§3) |

- Dependencies point one way: `cmd/tscomfy-go` → `workflow` → `comfy`.
- `comfy` knows nothing about templates or TypeScript. `workflow` knows nothing about sockets.
- Nothing a backend imports starts Bun or a JS engine.

### 2.3 Repository layout

```
go.mod                 module github.com/TheeKia/tscomfy-go
Makefile               the gate: `make check` (§9)
package.json           private Bun workspace root: workspaces ["ts", "examples/*/workflows"], TS tooling
comfy/                 workflow/        cmd/tscomfy-go/        internal/format/
internal/comfytest/    the fake server; recordings/ holds the protocol recordings copied from
                       tscomfy, embedded with go:embed, with a README of their provenance
ts/                    the npm package @tscomfy/go
testdata/object-info/  mock schemas: basic-pipeline.json copied from tscomfy, params.json written
                       here; a README states their provenance
examples/txt2img/      workflows/ (a workspace member, schema generated from a mock), main.go (the
                       go:generate directive), internal/workflows/ (generated)
docs/                  spec.md, plan.md, decisions.md, research/, guides/
```

- **The recordings' provenance:** tscomfy's capture script recorded them from ComfyUI 0.41.0;
  they were then distilled by hand (tracebacks cut to two frames, the server's paths and model
  names generalized, image bytes replaced by a tiny image); `blocked.json` is hand-written
  (tscomfy's `internal/fixtures/README.md`). They are mocks under tscomfy's corpus rule, not raw
  captures, and are committed there under the same "no real data" rule as here.

- Go and TypeScript share the repository root: `go.mod` and the workspace's `package.json` sit
  side by side. The example's workflows project uses `@tscomfy/go` through `workspace:*`.
- **No real ComfyUI data is committed,** examples included. The example's schema is generated
  with `tscomfy generate --from` from a copied mock (`basic-pipeline.json`, which has every node
  txt2img needs), so its template and generated Go hold only mock data.

### 2.4 Dependencies

- **Go:** the standard library and `github.com/coder/websocket` v1.8.15.
- **TypeScript:** `@tscomfy/core` only.
- **The approved toolchain:** Go, golangci-lint v2, govulncheck, Bun, TypeScript
  7, oxlint with oxlint-tsgolint, oxfmt, `@types/bun`, `@tscomfy/cli` (the example's schema), and
  the GitHub Actions `actions/checkout`, `actions/setup-go`, `oven-sh/setup-bun`,
  `golangci/golangci-lint-action`, and for releases `actions/setup-node`,
  `actions/upload-artifact`, `actions/download-artifact`. Versions are pinned exactly.
- Any other dependency, dev tools included, needs the owner's approval and an entry in
  `docs/decisions.md`. tscomfy's `expect-type` is not approved.
- `go.mod` has `ignore node_modules`, so `./...` skips every `node_modules` directory (verified
  2026-10-20; `ignore ./node_modules` would skip only the root one).

## 3. Authoring workflows: `@tscomfy/go`

Published to npm as `@tscomfy/go` from this repository (`ts/`), with the same version as the Go
module (D4). It depends on `@tscomfy/core`'s public API only, through a peer-dependency
range (`^0.2.1`). Like tscomfy's packages, it ships built JavaScript and declarations (`dist/`,
from `tsc -b`) and exposes its source under the `@tscomfy/source` condition inside the
workspace. The export command is a `bin` that runs with Bun. A backend's workflows project
installs `@tscomfy/core` and `@tscomfy/go`, and
`@tscomfy/cli` as a dev dependency for `tscomfy generate` (all 0.2.1 or later on npm as of
2026-10-20, `@tscomfy/go` excepted).

```ts
// The public API of @tscomfy/go
export interface Template<N = unknown> {
  readonly name: string;
  readonly workflow: Workflow<N, Readonly<Record<string, Param>>>; // tscomfy's, handles = params
}
export interface Param {
  readonly node: NodeId;
  readonly input: string;
  readonly optional: boolean;
}
export interface ParamOptions {
  readonly optional?: boolean;
}
export function template<N>(
  name: string,
  schema: TypedSchema<N>,
  build: (context: BuildContext<N>) => Readonly<Record<string, Param>>,
): Template<N>;
export function param(node: NodeHandle | Out<string>, input: string, options?: ParamOptions): Param;
```

Templates are plain data built eagerly at import (tscomfy's `workflow` calls `build` at once),
and `param` records a node id and an input name. The package follows tscomfy's TypeScript conventions: strict compiler options
(`tsconfig.base.json` there), oxlint and oxfmt with tscomfy's configs, no default exports. Every
public declaration lives in `ts/src/index.ts`, so that file's diff is the package's API diff.

### 3.1 Example

```ts
// workflows/txt2img.ts
import { param, template } from "@tscomfy/go";
import { local } from "./comfy/local/index.ts";

export const txt2img = template("txt2img", local, ({ n }) => {
  const [model, clip, vae] = n.CheckpointLoaderSimple(
    { ckpt_name: "model-a.safetensors" }, { id: "checkpoint" });
  const positive = n.CLIPTextEncode({ clip, text: "a cat in a hat" }, { id: "positive" });
  const negative = n.CLIPTextEncode({ clip, text: "" }, { id: "negative" });
  const latent = n.EmptyLatentImage({ width: 1024, height: 1024 }, { id: "latent" });
  const sampled = n.KSampler(
    { model, positive, negative, latent_image: latent, seed: 0, steps: 30,
      sampler_name: "euler", scheduler: "normal" },
    { id: "sampler" });
  const image = n.VAEDecode({ samples: sampled, vae }, { id: "decode" });
  n.SaveImage({ images: image, filename_prefix: "txt2img" }, { id: "save" });

  return {
    prompt: param(positive, "text"),
    seed: param(sampled, "seed"),
    negative: param(negative, "text", { optional: true }),
    steps: param(sampled, "steps", { optional: true }),
    width: param(latent, "width", { optional: true }),
    height: param(latent, "height", { optional: true }),
    checkpoint: param(model, "ckpt_name", { optional: true }),
  };
});
```

```ts
// workflows/index.ts: every exported template is generated
export { txt2img } from "./txt2img.ts";
```

### 3.2 `template(name, schema, build)`

- Calls tscomfy's `workflow(schema, build)` and keeps `name`.
- `build` returns the parameters: an object whose keys are parameter names and whose values come
  from `param`. Its key order is the parameters' order.
- `name` matches `^[a-z][a-z0-9_]*$`; it also names the template's files (`<name>.json`,
  `<name>.go`). Parameter names match `^[A-Za-z][A-Za-z0-9_]*$`. Both must convert to distinct Go
  identifiers (§6.3).
- `<name>.go` must be a file `go build` compiles everywhere: the name can't end in `_test`, or in
  `_<GOOS>`, `_<GOARCH>` or both (`up_windows`, `x_amd64`). The generator checks it with
  `go/build`, which knows the current lists.

### 3.3 `param(node, input, options?)`

- `node` is a node handle or one of its outputs (tscomfy ADR-0025).
- `input` is the name of one of that node class's own inputs in the schema
  (`schema.nodes[classType].inputs`). Keys that dynamic inputs expand into, such as a dynamic
  combo's `format.codec`, are not inputs of the class and can't be parameters in v1.
- `options.optional`: the parameter may be left unset, keeping the value written in the graph.
  Parameters are required unless `optional` is `true`.
- The value written in the graph is the parameter's default, for optional and required ones alike.

### 3.4 Node ids

- A node's id is its key in the prompt, and ComfyUI reports that key in every event about the
  node (`display_node`, `display_node_id`). For tscomfy's flat prompts the two are the same.
- The author sets ids with tscomfy's `{ id }` option. tscomfy gives other nodes automatic ids
  `"1"`, `"2"`, … and throws on a duplicate or empty id.
- Ids made only of digits are treated as automatic: they get no Go constant (§6.4).

### 3.5 The export command

`tscomfy-go-export <entry>`, a `bin` of `@tscomfy/go` run by the generator (§6.1); not meant
for direct use.

- It imports the entry module (`index.ts` in the workflows directory) and takes every named
  export, each of which must be a template that `template` made. Anything else exported, a
  default export included, is an error, and so are two templates of one name.
- It prints one JSON document to stdout: `{"format":1,"templates":[<template>, …]}`, each
  template as in §4, written with tscomfy's `stringify`. Templates are sorted by name (byte
  order).
- On an error, and when importing the module throws, it prints the message to stderr and exits
  non-zero. A template that breaks a rule of §3.6 throws when the module defines it.
- It does no I/O besides reading the modules and writing stdout and stderr.

### 3.6 What `template` checks

`template` builds the workflow at once, exports it with `toApi` (which validates it), and makes
the template document, so a mistake fails where the workflow is defined: in the editor's test
run, in `bun test`, or in `go generate`. It throws an `Error` naming the template and the
parameter when:

- tscomfy's validation reports an error (the `Error`'s `cause` is the `ValidationError`);
- the name, or a parameter's name, breaks the rules of §3.2;
- `build` returns something other than an object of values made by `param`;
- two parameters name the same node and input;
- a parameter's node isn't in the workflow, or its class isn't in the schema;
- a parameter's input is not one of its node class's inputs in the schema;
- a parameter's input kind is not one of `int`, `float`, `string`, `boolean`, `combo`, or it is
  a multi-select combo or one with an option that isn't a string;
- a parameter's node isn't in the prompt (bypassed or muted), or its input holds a link or is
  absent from the prompt (give it a value);
- a parameter's value doesn't fit its kind: an int that isn't a safe integer or a `bigint` (an
  int beyond 2^53 written as a number is rounded and lands in exponent form: write it as a
  `bigint`), a float that isn't a number, a string or combo that isn't a string, a boolean that
  isn't one;
- a string anywhere in the prompt isn't well-formed Unicode (a lone surrogate, which no Go string
  can hold).

The error is a `TemplateError`. The export command prints its message alone and any other error
with its stack, so a bug in the workflows' own code shows where it is. The generator checks each
template once more as `workflow.Load` does (§6.2), bounds included.

## 4. Template format v1

A template is a JSON document. One file per workflow, `<name>.json`, written by the generator and
committed.

```json
{
  "format": 1,
  "name": "txt2img",
  "params": [
    { "name": "prompt", "node": "positive", "input": "text", "kind": "string" },
    { "name": "seed", "node": "sampler", "input": "seed", "kind": "int",
      "min": 0, "max": 18446744073709551615 },
    { "name": "steps", "node": "sampler", "input": "steps", "kind": "int",
      "min": 1, "max": 10000, "optional": true }
  ],
  "prompt": {
    "positive": {
      "inputs": { "text": "a cat in a hat", "clip": ["checkpoint", 1] },
      "class_type": "CLIPTextEncode",
      "_meta": { "title": "CLIP Text Encode (Prompt)" }
    }
  }
}
```

- **`format`**: `1`. A reader must refuse any other value.
- **`name`**: the workflow's name.
- **`params`**: in declaration order, which is the generated struct's field order and `Fill`'s
  argument order (an array, since Go's map decoding loses member order). Each has `name`, `node`,
  `input` and `kind` (`int`, `float`, `string`, `boolean`, `combo`). `min` and `max` are present
  when the schema gives them, as exact JSON numbers: 64-bit integers stay integers. `optional` is
  present only when `true`. A parameter's default is its value in `prompt`; the file doesn't
  repeat it.
- **`prompt`**: tscomfy's `toApi` output. Its keys are the node ids.
- **No list of outputs:** the generated node constants cover them. **No provenance** (schema hash,
  ComfyUI version): nothing reads it, and it would rewrite every file whenever the schema is
  regenerated; `tscomfy check` covers drift.
- **Numbers** are read and written as their literal text, never through `float64`.
- **On disk** the generator writes the document indented by two spaces, with LF line endings and
  a final newline, for readable diffs. Indenting and compacting with `encoding/json/jsontext`
  changes only whitespace, so the compacted `prompt` is byte-identical to tscomfy's `stringify`
  output (verified 2026-10-20, `docs/research/runtime-and-templates.md` §3.3). The template is a
  JSON file rather than a Go string literal so that it can be reviewed and loaded on its own.
- **Deterministic:** the same workflows and schema give the same bytes.
- **Versioned:** a change a v1 reader would misread needs a new `format` number.

## 5. Filling templates: package `workflow`

The generated code uses it; a backend can also load template files itself.

```go
package workflow

func Load(data []byte) (*Template, error)
func MustLoad(data []byte) *Template // panics; for generated code, whose files the generator checked

func (t *Template) Name() string
func (t *Template) Params() []Param
func (t *Template) Fill(values ...any) (comfy.Prompt, error)

type Param struct {
	Name     string
	Node     comfy.NodeID
	Input    string
	Kind     Kind           // KindInt, KindFloat, KindString, KindBoolean, KindCombo
	Optional bool
	Default  jsontext.Value // the value in the prompt
	Min, Max jsontext.Value // nil when the schema gives none; exact literals
}

type ParamError struct {
	Workflow, Param string
	Err             error // ErrRequired, ErrOutOfRange or ErrWrongType, wrapped with detail
}
```

- **`Load`** checks the document against §4: the format, every parameter's node and input present
  in `prompt` with a value (not a link), a value that fits the kind, `min ≤ value ≤ max`. It
  compacts the prompt, records the byte span of each parameter's value, and keeps that value as
  the parameter's default.
- **`Fill`** takes one value per parameter, in `Params()` order. It copies the compacted prompt and
  replaces each parameter's value with the given one, encoded with `encoding/json/v2`. Every other
  byte is unchanged, so the result equals what tscomfy would produce for the same values.
- **Values by kind:**

  | Kind | Go values accepted | Checked |
  |---|---|---|
  | `int` | `int`, `int64`, `uint64`, or a pointer to one | within `min` and `max` |
  | `float` | `float64`, `*float64` | finite; within `min` and `max` |
  | `boolean` | `bool`, `*bool` | – |
  | `string`, `combo` | `string`, `*string` | valid UTF-8 (the server validates combo values) |

  These are exactly the types the generator emits (§6.3), so `Fill` uses a type switch, no
  reflection. Any other type is `ErrWrongType`; `float32` in particular, since its shortest
  decimal form (`0.1`) is not the value a `float64` holds, so no tscomfy build could produce it.
  A float is written as tscomfy's `stringify` writes it: as JavaScript's `JSON.stringify` does
  (`jsontext.AppendFloat`), except that a negative zero is `0` (`encoding/json/v2` alone writes
  `-0`, verified 2026-10-20) and a float from 2^53 up, every one of which is an integer, is in
  exponent form (`1e+17`; `stringify` does that so the number reads back as a float). A string
  that isn't valid UTF-8 is `ErrWrongType`.

- **`nil`** (an untyped nil, or a nil pointer) keeps the default. It is allowed only for optional
  parameters; for a required one it is `ErrRequired`.
- **Errors:** a wrong number of values is an error; a value that fails a check is a
  `*ParamError`. `Fill` never panics.
- A `Template` is immutable and safe for concurrent use.

## 6. The generator: `cmd/tscomfy-go`

### 6.1 Running it

```go
//go:generate go tool tscomfy-go -workflows ./workflows -out ./internal/workflows
```

- Installed with `go get -tool github.com/TheeKia/tscomfy-go/cmd/tscomfy-go`.
- Flags: `-workflows` (the TypeScript project; its `index.ts` is the entry) and `-out` (the Go
  package directory). The package name is the base name of `-out`, which must be a valid Go
  package name.
- It runs the export command (§3.5) in the workflows directory as `bun x --bun --no-install
  tscomfy-go-export index.ts`: `bun x` finds the bin of the `@tscomfy/go` the project installed
  (in its `node_modules/.bin`, or a parent's in a workspace), so the project's own `@tscomfy/*`
  versions are used, and `--no-install` keeps it from downloading one when there is none.
  Settled in Phase 1.
- The export command reports its `format` (§3.5). When it isn't the one the generator reads (1),
  the generator stops, changing nothing, and says to install the `@tscomfy/go` version that
  matches the Go module (D4).
- `go generate` runs the directive in the directory of the file that holds it, so `-workflows`
  and `-out` are relative to that file.
- It needs no network and no ComfyUI.

### 6.2 What it writes

For each template, `<name>.json` (§4) and `<name>.go`:

```go
// Code generated by tscomfy-go. DO NOT EDIT.

package workflows

import (
	_ "embed"

	"github.com/TheeKia/tscomfy-go/comfy"
	"github.com/TheeKia/tscomfy-go/workflow"
)

//go:embed txt2img.json
var txt2ImgJSON []byte

var txt2ImgTemplate = workflow.MustLoad(txt2ImgJSON)

// Txt2Img holds the parameters of the txt2img workflow. Optional fields left
// nil keep the value written in the workflow.
type Txt2Img struct {
	Prompt     string  // positive.text
	Seed       uint64  // sampler.seed, 0 to 18446744073709551615
	Negative   *string // negative.text, default ""
	Steps      *int    // sampler.steps, 1 to 10000, default 30
	Width      *int    // latent.width, 16 to 16384, default 1024
	Height     *int    // latent.height, 16 to 16384, default 1024
	Checkpoint *string // checkpoint.ckpt_name, default "model-a.safetensors"
}

// Build returns the txt2img prompt with p's values. It reports values outside
// an input's bounds as a *workflow.ParamError; the server validates the rest.
func (p Txt2Img) Build() (comfy.Prompt, error) {
	return txt2ImgTemplate.Fill(p.Prompt, p.Seed, p.Negative, p.Steps, p.Width, p.Height, p.Checkpoint)
}

// The txt2img nodes, by the ids given in the workflow.
const (
	Txt2ImgCheckpoint comfy.NodeID = "checkpoint"
	Txt2ImgPositive   comfy.NodeID = "positive"
	Txt2ImgNegative   comfy.NodeID = "negative"
	Txt2ImgLatent     comfy.NodeID = "latent"
	Txt2ImgSampler    comfy.NodeID = "sampler"
	Txt2ImgDecode     comfy.NodeID = "decode"
	Txt2ImgSave       comfy.NodeID = "save"
)
```

- The first line matches Go's generated-code convention, so tools skip the file.
- Each field's comment shows its node and input, its bounds (`1 to 10000`, `at least 1`,
  `at most 8`), and an optional one's default, read from the prompt; a string default longer than
  40 characters is cut, with `…`.
- A template without parameters is an empty struct whose `Build` returns the prompt as written.
- The §3.1 example's values come from the mock schema `basic-pipeline.json`, which is what this
  repository's example is generated from (§2.3).
- Output is gofmt'd and deterministic. Constants are in prompt order.
- Before writing, the generator checks the very bytes it writes and embeds with `format.Parse`,
  which is what `workflow.Load` runs, so `MustLoad` cannot fail on a file it wrote.
- `Build`'s comment names what `Fill` refuses: a value outside its input's bounds, a float that
  isn't finite, a string that isn't valid UTF-8.
- A node id or input name that isn't all printable is quoted in the field comment, so it can't
  end the comment.

### 6.3 Types and names

- **Field types:**

  | Kind | Bounds | Go type |
  |---|---|---|
  | `string`, `combo` | | `string` |
  | `boolean` | | `bool` |
  | `float` | | `float64` |
  | `int` | both present and within −2^31 … 2^31−1 | `int` |
  | `int` | both present, `min ≥ 0` and 2^63 ≤ `max` ≤ 2^64−1 | `uint64` |
  | `int` | each bound present within the `int64` range | `int64` |
  | `int` | anything else | error |

  An optional parameter's field is a pointer to its type: `Steps: new(20)` (Go 1.26's
  `new(expr)`).
- **Identifiers:** a workflow name, parameter name or node id is split at every character that
  isn't an ASCII letter or digit, and before every letter that follows a digit; each part is
  capitalized and the parts are joined. `txt2img` → `Txt2Img`, `filename_prefix` →
  `FilenamePrefix`, `blur0` → `Blur0`, `vae_decode2x` → `VaeDecode2X`. A workflow name or
  parameter name that yields no identifier, or one starting with a digit, is an error; so is a
  node id that yields nothing (a node's part follows the workflow's, so it may start with a
  digit).
- **Collisions:** two declarations in the package with the same identifier, or a parameter whose
  field would be `Build`, are errors that name both sources.
- **Unexported names** are the type's name with its first letter lowered, plus `JSON` (the
  embedded bytes) or `Template`: `txt2ImgJSON`, `txt2ImgTemplate`.

### 6.4 Constants

One `comfy.NodeID` constant per node whose id is not made only of digits, named
`<Workflow><Node>`.

### 6.5 The output directory

- The output directory belongs to the generator. It holds only files the generator wrote (a `.go`
  file with the header of §6.2, or the `.json` next to one).
- The generator replaces the directory's generated files and deletes those of workflows that no
  longer exist; a file whose content is unchanged is left alone. It writes each file through a
  temporary one (`.tscomfy-go-*`) and a rename, and deletes temporary files a killed run left.
  It refuses to run, changing nothing, when the directory holds any other file, a subdirectory or
  a symlink.
- When the export or the generation fails, nothing is written. An I/O error while writing can
  leave some files replaced; the next run puts the directory right.

## 7. The client: package `comfy`

### 7.1 Conventions (D2)

- A `context.Context` bounds only the call it is passed to. Cancelling a job is an explicit
  `Job.Cancel`. `Run` is the one exception (§7.5).
- A job that doesn't succeed returns a typed error; the `*Result` is still returned when the
  server settled the job.
- Events are a single-use iterator with no error side channel.
- Options are struct pointers; nil means the defaults.
- `Client` and `Job` are safe for concurrent use. No goroutine outlives `Close`.
- Panic only for programmer misuse.

### 7.2 Client

The client's behavior is ported from tscomfy's `@tscomfy/client` (`packages/client/src/` there,
`channel.ts` and `job.ts` above all), which already runs prompts correctly against the same
server. Its tests are the cases to port (§9). Its API shape is not: this section is.

```go
func Dial(ctx context.Context, rawURL string, opts *Options) (*Client, error)

type Options struct {
	HTTPClient *http.Client // default: a client the package creates
	Header     http.Header  // on every HTTP request and on the websocket handshake
	Logger     *slog.Logger // default: discard
}

func (c *Client) Close() error
```

- **`rawURL`** is the server's http(s) base URL. A path prefix and a query are kept on every
  request and on the websocket URL. A URL with credentials (`user:pass@`) or a fragment is an
  error. Error messages never include the query, which may hold a token.
- **`Dial`**:
  1. opens `/ws?clientId=<fresh UUID>` and raises the socket's read limit (§8, rule 12);
  2. sends `{"type":"feature_flags","data":{"supports_preview_metadata":true}}` as the first
     message and waits up to 10 s for the server's flags, which ComfyUI sends only in answer to
     that message; no answer is `ErrUnsupportedServer`;
  3. checks `/system_stats`: a `system.comfyui_version` that is missing, doesn't parse as
     `major.minor.patch`, or is below 0.42.0 is `ErrUnsupportedServer`.

  On failure nothing stays open.
- **The client id** is internal. It changes with every socket, and a second socket opened with it
  would silence the client's own (both §8, rule 6). The client logs each socket's id at debug
  level.
- **One client per ComfyUI server.** Any number of jobs share its socket, told apart by prompt id.
- **A dropped socket** reconnects while jobs are open (§8, rule 6). With no job open it stays
  closed, and the next `Submit` reopens it, flags first. A backend therefore survives ComfyUI
  restarts; one that starts before ComfyUI retries `Dial`.
- **`Close`** stops everything the client started: sockets, goroutines, timers, polls, in-flight
  POSTs. Open jobs' `Wait` and `Run` return `ErrClosed`; the jobs keep running on the server.
  Later calls return `ErrClosed`. `Close` is idempotent.

### 7.3 Prompts and node ids

```go
type Prompt jsontext.Value // an API-format prompt, raw
type NodeID string         // a node's key in the prompt
```

- Go never decodes a prompt into `any` (a seed of 2^64−1 would become
  `1.8446744073709552e+19`). It is sent byte for byte.
- Generated `Build()` methods return a `Prompt`; raw JSON from elsewhere converts with
  `comfy.Prompt(raw)`.

### 7.4 Submit

```go
func (c *Client) Submit(ctx context.Context, prompt Prompt, opts *SubmitOptions) (*Job, error)

type SubmitOptions struct {
	Previews  PreviewMethod             // zero: not sent, the server's default (none unless started with --preview-method)
	Targets   []NodeID                  // partial_execution_targets; nil: every output
	Front     bool                      // queue ahead of the waiting jobs
	ExtraData map[string]jsontext.Value // more extra_data, such as api_key_comfy_org
}

type PreviewMethod string // sent as extra_data.preview_method

const (
	PreviewAuto       PreviewMethod = "auto" // latent2rgb on 0.41 and 0.42
	PreviewLatent2RGB PreviewMethod = "latent2rgb"
	PreviewTAESD      PreviewMethod = "taesd" // needs the TAESD decoder model; falls back to latent2rgb
	PreviewNone       PreviewMethod = "none"
)
```

- **`ctx` bounds the submission only.** Once `Submit` returns a `*Job`, the job runs until it ends
  or `Cancel`.
- A prompt that isn't a JSON object is an error. `ExtraData` keys the client sets itself
  (`client_id`, `preview_method`) are an error. Nothing is sent in either case, or when `ctx` is
  already done.
- **Steps:**
  1. Makes sure the socket is open, within `ctx`. If it is reconnecting (rule 6 of §8), `Submit`
     waits for that, and returns the transport error if its budget runs out. If it is closed and
     nothing is reconnecting (no job was open), `Submit` makes one attempt to open it, flags
     first, and returns the error if that fails. Nothing is posted in either failure.
  2. Mints the prompt id: a version-4 UUID from `crypto/rand`, canonical lowercase.
  3. Registers the job under it, so no event can arrive for an unknown job.
  4. Posts `{prompt, client_id, prompt_id, extra_data, front?, partial_execution_targets?}` to
     `/prompt`.
- **Answers:**
  - 200 returns the `*Job`, whatever the body: a body that isn't JSON or has another shape only
    loses its detail. Its `node_errors`, when any (a partial acceptance: the server runs the
    outputs that validated), become the result's `NodeErrors` (§7.8).
  - 400 returns a `*RejectedError` and no job.
  - A 200 whose `prompt_id` isn't the minted one (a proxy that ignores it) cancels the job under
    the id the server returned (the minted id was never queued) and returns a `*ProtocolError`.
  - Anything else returns an `*HTTPError`.
- **Failures** (D3):
  - When `ctx` ends while the POST is in flight, `Submit` returns `context.Cause(ctx)` at once. The
    POST continues in a goroutine the client owns.
  - Unless the server answered 400, any failure after the request may have been sent ends with a
    best-effort `POST /api/jobs/{id}/cancel` for the minted id. A prompt that was queued is
    cancelled; for one that wasn't, the cancel does nothing.
  - `Submit` never blocks past `ctx`.

### 7.5 Run

```go
func (c *Client) Run(ctx context.Context, prompt Prompt, opts *SubmitOptions) (*Result, error)
```

`Submit` then `Wait`. `ctx` governs the job, as in `exec.CommandContext`: when it ends before the
job, `Run` cancels the job on the server (best effort, within 10 s) and returns
`context.Cause(ctx)`.

### 7.6 Job

```go
func (j *Job) ID() string                                 // the prompt id
func (j *Job) Done() <-chan struct{}                      // closed when the job settles
func (j *Job) Wait(ctx context.Context) (*Result, error)
func (j *Job) Cancel(ctx context.Context) error
func (j *Job) Events(ctx context.Context) iter.Seq[Event] // §7.7
```

- **`Wait`**: `ctx` bounds only the wait.

  | The job | `*Result` | error |
  |---|---|---|
  | succeeded | yes | nil |
  | failed in a node | yes, outputs of the nodes that finished first | `*ExecutionError` |
  | was cancelled or interrupted, by anyone | yes, outputs so far | `ErrCancelled` |
  | lost its record, and wasn't known to be cancelled (§8, rule 4) | nil | `ErrJobLost` |
  | still running when `ctx` ended | nil | `context.Cause(ctx)` |
  | unknown: client closed, socket down through the reconnect budget | nil | `ErrClosed`, a transport error |
  | unknown: its record answered in a way the protocol doesn't expect | nil | `*HTTPError`, `*ProtocolError` |

  A record read that fails for a while (a restart, a proxy's 502) doesn't end the job: the next
  poll reads again (§8, rule 5).

  The result is non-nil exactly when the server settled the job.
- **A job ends exactly once,** in one of the table's rows other than "still running": settled,
  lost, closed, or unreachable. When it ends, `Done` closes and `Events` ends once its buffer is
  drained. From then on every `Wait` returns the same result and error. `Close` doesn't change
  the outcome of a job that had already ended. A `Wait` whose `ctx` ends first returns
  `context.Cause(ctx)` without ending the job; a later `Wait` can still get its outcome.
- **`Cancel`** posts `/api/jobs/{id}/cancel`. The job then settles from its record, normally as
  `ErrCancelled`; one that was finishing may still succeed, since the server's
  `{"cancelled":true}` only means the interrupt was signalled. Cancelling a job the server
  settled does nothing; one that ended otherwise (`ErrClosed`, a transport error) may still run,
  so the cancel is sent. The job counts as cancelled by this client from before the request, so a
  404 read meanwhile is this cancel's. After `Close`, `Cancel` returns `ErrClosed`.

### 7.7 Events

```go
type Event interface{ isEvent() }
```

| Type | From | Fields |
|---|---|---|
| `StartedEvent` | `execution_start` | |
| `CachedEvent` | `execution_cached` | `Nodes []NodeID` |
| `ExecutingEvent` | `executing` with a node | `Node NodeID` |
| `ProgressEvent` | `progress_state`: each node whose value or max changed | `Node NodeID; Value, Max float64` (ComfyUI sends floats, `comfy_execution/progress.py`) |
| `PreviewEvent` | binary type 4 | `Node NodeID; MIME string; Data []byte` |
| `OutputEvent` | `executed` with a non-null `output` (a cached output node's is null) | `Node NodeID; Output NodeOutput` |
| `TextEvent` | binary type 3, while this job's prompt runs (§8, rule 7) | `Node NodeID; Text string` |
| `BlockedEvent` | `execution_error` with `exception_type` `ExecutionBlocked` | `Node NodeID; Message string` |

- **Single-use**, documented as "It returns a single-use iterator." It yields the events buffered
  since `Submit`, then new ones. It ends when the job has settled and the buffer is drained, or
  when `ctx` is done. Called again after a `break`, it continues the stream; called after it
  ended, it yields nothing. Iterating from two goroutines at once panics.
- **No outcome.** It never reports how the job ended; that is `Wait`'s.
- **Bounded buffer.** While the reader lags, a newer `ProgressEvent`, or JPEG `PreviewEvent` (a
  sampler's preview), of a node replaces its unread one in place. PNG `PreviewEvent`s are
  `SaveImageWebsocket`'s images, one per image of a batch and nowhere else, so they are all kept,
  up to 64 unread; at most 256 unread `TextEvent`s are kept (partner nodes send one per status
  change). Beyond those caps the oldest go first. The other events come at most once per node the
  server runs: the prompt's, plus a copy of a loop's body per iteration (ComfyUI 0.42's
  `StartLoop`, which sets no limit on iterations; a 100-iteration loop of a 4-node prompt sent 403
  `executing` messages, measured 2026-10-20). The doc comment states this.
- **Node ids** are display ids (`display_node` in `executed`, `display_node_id` in
  `progress_state` and preview metadata), mapped through `executed` for nodes expanded at run
  time (ids such as `"5.0.0.1"`).
- Unknown message types and fields are ignored, and so are frames about other prompts. Numbers
  that ComfyUI sends as floats or integers (`value`, `max`) decode as `float64`. Everything the
  server sends decodes with invalid UTF-8 allowed: Python's `json` writes a string it read with
  `surrogateescape` as a lone surrogate (`"\udc80"`), which becomes U+FFFD instead of a lost
  message.

### 7.8 Results and outputs

```go
func (r *Result) Files(nodes ...NodeID) []OutputFile    // in prompt order; every node's when none are given
func (r *Result) Output(node NodeID) (NodeOutput, bool) // the last, for a node that ran more than once
func (r *Result) All() iter.Seq[NodeOutput]             // prompt order, one per run
func (r *Result) NodeErrors() []NodeError               // outputs the server skipped (partial acceptance)
func (r *Result) Blocked() []BlockedEvent               // nodes an ExecutionBlocker stopped, as seen live

type NodeOutput struct {
	Node  NodeID
	Files []OutputFile
	Text  []string       // the strings under "text"
	UI    jsontext.Value // the node's UI output, raw
}
type OutputFile struct {
	FileRef
	Key string // the output key it was listed under: "images", "audio", "gifs", …
}
type FileRef struct {
	Filename, Subfolder string
	Type                FolderType // FolderOutput, FolderTemp, FolderInput
}
type NodeError struct {
	Node      NodeID
	ClassType string
	Input     string // extra_info.input_name; "" when the error is about the whole node
	Type      string // the server's error type, such as "value_not_in_list"
	Message   string
	Details   string
}

type FolderType string // FolderOutput "output", FolderTemp "temp", FolderInput "input"
```

- **Outputs** come from the job record, except a node's output seen live in `executed`, which wins:
  the record rewrites some entries (research §5 in tscomfy).
- **One output per node the server ran.** Nodes expanded at run time can share a displayed id:
  `StartLoop` runs a copy of its body per iteration (`start.0.0.0_save`, `start.0.0.1_save`, …),
  each displayed as the original node, and the record lists each copy's output under its own id
  (`execution.py` builds `outputs` by `unique_id`). The result keeps them all, in the order they
  ran (the record's member order, which is ComfyUI's), so `Files(node)` has every iteration's
  files; `Output(node)` returns the last. A copy whose `executed` was missed (a socket drop) keeps
  the server's id, and comes after the prompt's nodes.
- **Files** are recognized by shape under any key: an object with a non-empty `filename`, and a
  `type` of `output`, `temp` or `input`. The client doesn't classify them: the generated node
  constant says which node made a file, and the server's own `metadata` (`kind`, `width`,
  `height`, and for video `duration`, `fps`, `frame_count`) and `animated` flags are in `UI`.
- **`NodeErrors`** are the server's `node_errors`, one entry per error, in prompt order.
- **`Blocked`** comes only from the socket: ComfyUI sends `ExecutionBlocked` errors live and
  doesn't record them, and the job ends `completed`. A blocked node missed during a socket drop
  is not listed.

### 7.9 Files

```go
func (c *Client) Upload(ctx context.Context, name string, r io.Reader, opts *UploadOptions) (UploadedFile, error)
func (c *Client) Open(ctx context.Context, ref FileRef) (*File, error)
func (c *Client) FileURL(ref FileRef) string

type UploadOptions struct {
	Subfolder string
	Type      FolderType // zero: FolderInput
	Overwrite bool
}
type UploadedFile struct {
	FileRef
	Value string // the widget value a loader takes: "subfolder/name", plus " [temp]" or " [output]" outside input
}
type File struct {
	io.ReadCloser
	ContentType string
	Size        int64 // -1 when unknown
}
```

- `Upload` streams a multipart `POST /upload/image`. It returns when the request ends or its
  context does; like `net/http` with a request body, it may leave a goroutine in a `Read` of the
  caller's reader that hasn't returned yet. The stored name can differ from `name`:
  without `Overwrite`, the server renames a file whose name is taken by different content
  (`name (1).ext`, …) and reuses the name for identical content. The server ignores the part's
  content type, so there is no option for it. A `Type` other than the three `FolderType`
  constants is an error and nothing is sent (ComfyUI answers 500 for one).
- A backend passes `UploadedFile.Value` to a string parameter (a `LoadImage` `image` input).
- `Open` streams `GET /view`; the caller closes the file. Its context bounds the download,
  reading included, as an HTTP request's does.
- `Close` aborts uploads and downloads in flight; afterwards both return `ErrClosed`.
- `FileURL` keeps the base URL's query, so a token in it is in the result.

### 7.10 Errors

| Error | When |
|---|---|
| `ErrClosed` | the client was closed |
| `ErrUnsupportedServer` | `Dial`: no flag answer, a version missing or below 0.42.0 (wrapped with detail) |
| `ErrJobLost` | the job's record vanished and the job wasn't known to be cancelled (§8, rule 4) |
| `ErrCancelled` | the job was cancelled or interrupted |
| `*RejectedError{Type, Message, Details string; Node NodeID; NodeErrors []NodeError}` | the server refused the prompt (400); `Node` is set when the top-level error names one (`missing_node_type`) |
| `*ExecutionError{Node NodeID; ClassType, ExceptionType, Message string; Traceback []string; Executed []NodeID}` | a node raised an exception |
| `*HTTPError{Op string; StatusCode int; Body []byte}` | an answer the protocol doesn't expect; `Body` is the first 4 KiB |
| `*ProtocolError` | the server broke the protocol |

Network errors are wrapped with `%w`, so `errors.Is` and `errors.As` work.

### 7.11 `internal/comfytest`

A fake ComfyUI: an `http.Handler` with a websocket endpoint that replays protocol recordings
(`internal/comfytest/recordings/`, embedded; `PROMPT_ID` stands for the minted id, binary frames
are `{binary, …}`) and follows ComfyUI's routing, including the same-id hazard (§8, rule 6). It
injects faults: a socket dropped mid-run, a socket that goes silent without closing, a 404 record,
delays, a refused handshake, large binary frames, events before the POST's answer, a hung run.

- **In memory.** It serves over a `net.Pipe` listener and hands out an `*http.Client` whose
  transport dials it, for `Options.HTTPClient`. A server on a real socket can't be used: inside a
  `testing/synctest` bubble, fake time doesn't advance while a goroutine waits on real network
  I/O, so the test hangs (verified 2026-10-20, Go 1.27.2; the same test over `net.Pipe` ran 30 s
  of fake time at once).
- **Leak checks come with it.** `synctest.Test` fails when a goroutine of the bubble is still
  blocked at the end ("blocked goroutines remain", verified), so every client test checks that
  `Close` stopped everything, with no helper library. The fake's own `Close` shuts its HTTP
  server down gracefully, since `net/http` lingers 500 ms over a connection after an error
  answer.
- **Writes are queued.** A socket's messages go through a queue that a writer goroutine drains,
  as a real server's do: over an unbuffered pipe, the server writing its first status while the
  client writes its feature flags would otherwise deadlock.
- **Faults:** `Behavior` per run (events before the answer, a drop before an event, the same-id
  hazard, a server that forgets the job, holding until a cancel, a queued job, a hang, gaps, a
  slow answer), and on the server: `DropSockets`, `Silence` (the server's side of the
  connections stops reading and swallows writes, so pings go unanswered: a socket dead behind a
  proxy), `RefuseSockets`, `BreakRecordReads` (a dropped connection, a body cut off, or a proxy's
  status), `RefuseUploads` (an answer before the file is all read, as for a file over ComfyUI's
  size limit), `Options.PathPrefix`, `Options.Version`, `Options.NoFeatureFlags`.
- **Internal in v0.1** (decided in Phase 7): its fault controls follow this project's tests and
  would freeze as API. Making it public later adds API and breaks nothing; a backend's own tests
  can meanwhile run its handlers against an `httptest` server of their own.

## 8. Protocol rules

Rules 1 to 11 come from tscomfy's research (`docs/research/comfyui-execution.md` there, with
citations into ComfyUI) and its client (plan §17, ADR-0023 to ADR-0025 there). Rules 12 to 14 are
this project's. All were checked against ComfyUI 0.42's source on 2026-10-20.

1. **Routing.** Events reach only the socket whose `clientId` equals the prompt's `client_id`.
   Progress text and `status` are broadcast to every socket, as is everything about a prompt
   submitted without a `client_id`; the client always sets one and ignores frames about prompts
   that aren't its jobs.
2. **Prompt ids.** The client mints them: canonical lowercase UUIDs (`str(uuid.UUID(v)) == v`,
   else 400). The server doesn't check that an id is unused; a version-4 UUID from `crypto/rand`
   makes a collision negligible.
3. **The end of a run** is `executing` with `node: null` and the job's prompt id, sent after the
   history write. `execution_success`, `execution_error` and `execution_interrupted` come before
   it and decide nothing.
4. **Settling.** On the end event, the client reads `GET /api/jobs/{id}`:
   - `completed`: success, outputs from the record;
   - `failed`: `*ExecutionError` from the record's `execution_error`;
   - `cancelled`: `ErrCancelled`;
   - `pending` or `in_progress`: not settled yet;
   - 404: a job cancelled while pending leaves no record and sends no event, and a server restart
     empties the queue and loses running jobs. The client can't tell these apart after a socket
     drop, so a 404 is `ErrCancelled` when this client cancelled the job, or when the job never
     started (rule 14) and its socket never dropped; any other 404 is `ErrJobLost`. ComfyUI's 404
     is JSON (`{"error":"Job not found"}`); one that isn't comes from something else, a proxy that
     doesn't route the job API, and is an `*HTTPError`;
   - 429 or 5xx, an answer cut off partway through its body, or no answer after the retries
     below: read again at the next poll;
   - any other answer: an `*HTTPError`.
5. **Reading the record at other times:**
   - **Jobs that haven't started:** on a `status` message, at most once a second (a change inside
     the second is read at its end), the client reads `GET /api/jobs?status=pending,in_progress`
     once for all of them, and reads the own record of each job missing from that list. One read
     per window, however many jobs are queued.
   - after every reconnect: at once, then at 1, 2, 5 and then every 10 s until settled, in place
     of the 30 s poll;
   - every 30 s while the job is open;
   - after `Cancel`.

   A record read that can't reach the server is retried after 0.5, 1, 2, 4 and 8 s, then once
   more. Each read has its own 10 s timeout, so one hung request can't stall settling. (Go's
   `http.Transport` may also retry a GET by itself on a reused connection that drops.) Ported from tscomfy, except the list read, which replaces its per-job reads on
   `status`.
6. **Reconnecting** (ported from tscomfy):
   - only while jobs are open: the loop stops at its next attempt once none is;
   - after 0.25, 0.5, 1, 2, 4, 8, 15, 30, 30, 30 s, each scaled by a random factor in [0.5, 1)
     (about two minutes in all);
   - **a fresh client id for every socket.** When the server closes an old socket late, it
     unregisters whatever has that id, leaving a reused id's new socket open but deaf;
   - flags re-sent first;
   - jobs that were running keep the old client id, so their remaining events are lost; they
     settle from the record polls of rule 5;
   - when the budget is spent, open jobs' `Wait` returns the transport error.
7. **Binary frames** start with a big-endian `uint32` type:
   - 1, a legacy preview without ids: the server sends it only to clients that don't announce
     `supports_preview_metadata`, so this client never gets it; ignored;
   - 3, progress text (`uint32` length, node id, UTF-8 text): it carries no prompt id and is
     broadcast. ComfyUI runs one prompt at a time, so it belongs to the job whose prompt is
     running, if any. (A prompt-id prefix exists only on an unmerged ComfyUI branch,
     `progress-text-prompt-id`; the client doesn't announce it.)
   - 4, a preview with metadata (`uint32` length, JSON metadata with `prompt_id`,
     `display_node_id` and `image_type`, then the image bytes);
   - anything else, or a frame whose lengths don't fit: ignored.
8. **Previews are off by default.** `extra_data.preview_method` turns them on per prompt.
9. **A blocked node** is not a failure: the run goes on, and the record decides the outcome.
10. **No default timeout for a job.** A dead worker thread leaves a job `in_progress` forever;
    only the caller's context ends the wait.
11. **Origins.** The client sends neither `Origin` nor `Sec-Fetch-Site`, so ComfyUI's default
    origin check passes.
12. **The read limit.** coder/websocket closes a connection on a message over 32 KiB by default
    (`read.go:91`). Previews and `SaveImageWebsocket` images are larger. The client raises the
    limit to 128 MiB, a full-size PNG of any image a GPU makes today; a test sends an 8 MiB frame.
13. **Pings.** ComfyUI answers websocket pings (aiohttp's autoping) but never sends any. A socket
    that dies without closing (a proxy's idle timeout) would cost every later job its events
    until the 30 s record read. The client pings while jobs are open, and before a `Submit` on a
    socket that has been idle; a failed ping drops the socket and starts rule 6. It pings every
    30 s while jobs are open, with a 10 s timeout, and before a `Submit` on a socket that has
    received nothing for 30 s. A silent socket is so found within 40 s, sooner than the 30 s
    record poll would settle a job, and a `Submit` never posts on a dead one.
14. **"Started"** comes from `execution_start` or `execution_cached` for the job, or from a record
    that says `in_progress`.

## 9. Testing and the gate

- **The gate** is `make check`, run locally before any work is called done; CI runs the same:
  - Go: `gofmt` check, `go vet`, golangci-lint v2 (staticcheck included), `go test -race
    -shuffle=on ./...`, short fuzz runs (10 s per target, listed in the Makefile), `govulncheck`.
  - TypeScript: `tsc -b` (TypeScript 7), `oxlint --type-aware`, `oxfmt --check`, `bun test`.
  - **Tool versions** are pinned in the Makefile. govulncheck runs as `go run
    golang.org/x/vuln/cmd/govulncheck@<version>`, which leaves `go.mod` alone. golangci-lint is a
    binary: CI installs the pinned version with `golangci/golangci-lint-action`, and the Makefile
    refuses a local binary of another version.
  - End to end (`make e2e`): regenerate every committed generated file (`make fixtures`: the test
    and example schemas from their mocks with `tscomfy generate --from`, the cross-language
    fixture, and the example's Go with `go generate`), build the example, and fail when anything
    differs from before. It compares against a copy rather than git, so it also works on
    uncommitted changes.
  - The steps run in order (`.NOTPARALLEL`), since the end-to-end check rewrites files.
- **The cross-language property:** for a set of parameter values, tscomfy building the workflow
  with them and `Fill` filling its template with them give byte-identical prompts.
  `ts/test/cross-language.ts` builds the workflow with each set and writes the template and the
  prompts tscomfy built to `workflow/testdata/cross-language.json`; Go's test fills the template
  with the same values and compares bytes. The values cover 64-bit seeds, the int64 extremes,
  integers beyond 2^53, floats (negative zero, the smallest, ones JavaScript writes in exponent
  form, ones beyond 2^53), and strings with escapes, control characters, emoji and U+2028.
- **Generator:** golden tests on a hand-written export (`cmd/tscomfy-go/testdata/export.json`).
  The golden output is a package of the module, `cmd/tscomfy-go/internal/golden`, so the gate
  compiles and vets it and a test imports it and calls its `Build` methods;
  `go test ./cmd/tscomfy-go -update` rewrites it. Tests for every error of §3.2's file-name rule,
  §6.3 and §6.5; the command run against a fake `bun`.
- **`@tscomfy/go`:** tests on the mock schemas in `testdata/object-info/`: `basic-pipeline.json`
  (copied from tscomfy) and `params.json` (written here: an output node with one input of every
  kind a parameter can or can't have, so that a prompt of it alone validates); one test per
  failure of §3.5 and §3.6. Their typed modules are generated into `ts/test/comfy/`. The export
  command is run as a process on fixture modules (`ts/test/fixtures/`). `tsc -b` type-checks the
  package, its tests and the example's workflows.
- **Client:** fake-server tests replaying the recordings, with the cases of tscomfy's
  `packages/client/test/` (`job.test.ts`, `lifecycle.test.ts`, `parts.test.ts`) ported.
- **Time and leaks:** every client test runs inside `synctest.Test` over `comfytest`'s in-memory
  transport (§7.11). Backoff, polls, pings and record checks run on fake time, never real sleeps,
  and a goroutine that survives `Close` fails the test.
- **Fuzzing:** the binary-frame decoder, the message decoder, output parsing, `Load` and `Fill`.
- **Examples:** an `Example` function for every main entry point.
- **Live tests** are opt-in with `TSCOMFY_GO_LIVE_URL` and queue only model-free prompts. They run
  against the owner's local ComfyUI, by the owner or by an agent working on this repository.
- **No real ComfyUI data in git:** tests use hand-written mocks and the copied recordings
  (distilled, §2.3).

## 10. Open values

None. Settled: `Run`'s cancel timeout (10 s, §7.5), `HTTPError.Body` (4 KiB, §7.10), the record read
timeout (10 s, rule 5), the read limit (128 MiB, rule 12), the `TextEvent` cap (256, §7.7), the
pings (30 s, 10 s, 30 s idle, rule 13), `comfytest` internal in v0.1 (§7.11).
