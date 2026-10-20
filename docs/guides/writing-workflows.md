# Writing workflows

A workflow is a tscomfy workflow wrapped in `template(name, schema, build)`. tscomfy types every
node from your server, so a wrong connection or an invalid value fails when you type it; the
parameters say which values Go sets per request.

## Names

- `name` (`^[a-z][a-z0-9_]*$`) names the files and the Go type: `txt2img_hires` → `Txt2ImgHires`
  in `txt2img_hires.go`. It can't end in `_test` or a Go system or architecture (`_windows`,
  `_arm64`), which would make `go build` skip the file.
- Parameter names (`^[A-Za-z][A-Za-z0-9_]*$`) become fields: `filename_prefix` →
  `FilenamePrefix`. Their order is the fields' order.
- Give the nodes Go cares about an id with tscomfy's `{ id: "sampler" }`: each becomes a constant
  (`Txt2ImgSampler`) to match events and outputs by. Ids made only of digits are tscomfy's
  automatic ones and get no constant.

## Parameters

`param(node, input, { optional })` names one input of one node: a node handle, or one of its
outputs. The value written in the workflow is the default.

- **Required** parameters are plain fields, always sent. **Optional** ones are pointers: nil keeps
  the default (`Steps: new(20)` sets it).
- Kinds: `int`, `float`, `string`, `boolean`, and single-select combos with string options. An int
  field is `int` when its bounds fit 32 bits, `uint64` for seeds up to 2⁶⁴−1, `int64` otherwise.
  Write an int beyond 2⁵³ in the workflow as a `bigint` (`seed: 2n ** 63n`).
- An input that holds a link, a dynamic input (autogrow, dynamic combo), a multi-select combo, or
  an input of a bypassed or muted node can't be a parameter.

`Build()` checks types and numeric bounds; the server validates the rest (a combo value it
doesn't have is a `*comfy.RejectedError` naming the node and input).

## When `template` refuses

`template` builds the workflow when the module loads and throws a `TemplateError` naming the
template and the parameter, so mistakes show in your editor's test run, in `bun test`, and in
`go generate`. tscomfy's own validation errors come with their `ValidationError` as the cause.

## Graphs that change shape

A template is one fixed graph. A request that needs another shape (a hires pass, a LoRA or not)
uses another template: `txt2img`, `txt2img_hires`. Shared parts are plain TypeScript functions.

## Uploaded images

Upload with `client.Upload` and pass its `Value` to a string parameter on `LoadImage`'s `image`:
make that parameter optional or required like any other.
