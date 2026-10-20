# tscomfy-go

Run ComfyUI workflows from a Go backend, with the workflows written in TypeScript with
[tscomfy](https://github.com/TheeKia/tscomfy). Supports the latest ComfyUI (0.42).

> **Status: v0.1.0.** `go get -tool github.com/TheeKia/tscomfy-go/cmd/tscomfy-go` and
> `bun add @tscomfy/go`; start with the [guides](docs/guides/getting-started.md). The design is in
> [`docs/spec.md`](docs/spec.md), the decisions behind it in [`docs/decisions.md`](docs/decisions.md),
> and the roadmap in [`docs/plan.md`](docs/plan.md).

## How it works

```
development:  workflows/*.ts ──go generate──▶ internal/workflows/*.json + *.go   (committed)
production:   Txt2Img{…}.Build() ──▶ comfy.Client ──HTTP + WebSocket──▶ ComfyUI ──▶ events, image
```

- **Workflows are written in TypeScript, once.** tscomfy types every node from your server's
  `/object_info`, so wiring and value mistakes fail at compile time. You name the nodes you care
  about and declare which inputs are parameters, with `template` and `param` from the
  `@tscomfy/go` npm package.
- **`go generate` turns them into Go.** It runs the TypeScript once and writes each workflow as a
  template (the validated prompt and its parameter slots) plus a typed Go struct and constants for
  its node ids. You commit both.
- **Production is plain Go.** No TypeScript, Bun or JS engine runs in your backend. `Build()` fills
  the template; the `comfy` package submits the prompt, streams progress and previews, settles the
  outcome from the server's job record, cancels, and uploads and downloads files.

```go
prompt, err := workflows.Txt2Img{Prompt: req.Prompt, Seed: seed}.Build()
if err != nil { … }
job, err := client.Submit(ctx, prompt, &comfy.SubmitOptions{Previews: comfy.PreviewAuto})
if err != nil { … }
for ev := range job.Events(ctx) {
	if p, ok := ev.(comfy.ProgressEvent); ok && p.Node == workflows.Txt2ImgSampler {
		/* forward p.Value of p.Max */
	}
}
res, err := job.Wait(ctx)
if err != nil { … }
for _, f := range res.Files(workflows.Txt2ImgSave) { /* open and store the images */ }
```

## Packages

| Package | What |
|---|---|
| `comfy` | The ComfyUI client: submit, events, results, cancel, files |
| `workflow` | Loads templates and fills in parameter values |
| `cmd/tscomfy-go` | The generator, run by `go generate` |
| `ts/` (`@tscomfy/go`) | `template` and `param`, and the export command the generator runs |

## License

[MIT](LICENSE).
