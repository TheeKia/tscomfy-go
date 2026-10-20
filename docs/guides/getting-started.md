# Getting started

tscomfy-go runs ComfyUI workflows from a Go backend. You write each workflow once in TypeScript
with tscomfy, typed by your server's own nodes; `go generate` turns it into Go; your backend
builds prompts and runs them with a plain Go client. TypeScript never runs in production.

You need Go 1.27, Bun 1.4 (at development time only) and ComfyUI 0.42.

## 1. The workflows project

Next to your Go code, make a small TypeScript project for the workflows:

```sh
mkdir workflows && cd workflows
bun init -y
bun add @tscomfy/core @tscomfy/go@<the tscomfy-go version>
bun add -d @tscomfy/cli
bunx tscomfy generate --url http://127.0.0.1:8188 --out comfy/local --import-extension .ts
```

`tscomfy generate` writes `comfy/local/`: your server's node definitions, typed. Commit it, and
regenerate it when the server's nodes change (`tscomfy check` in CI tells you when).

## 2. A workflow

```ts
// workflows/txt2img.ts
import { param, template } from "@tscomfy/go";
import { local } from "./comfy/local/index.ts";

export const txt2img = template("txt2img", local, ({ n }) => {
  const [model, clip, vae] = n.CheckpointLoaderSimple({ ckpt_name: "model.safetensors" });
  const positive = n.CLIPTextEncode({ clip, text: "a cat" }, { id: "positive" });
  const negative = n.CLIPTextEncode({ clip, text: "" });
  const latent = n.EmptyLatentImage({ width: 1024, height: 1024 });
  const sampled = n.KSampler(
    { model, positive, negative, latent_image: latent, seed: 0, steps: 30,
      sampler_name: "euler", scheduler: "normal" },
    { id: "sampler" },
  );
  n.SaveImage({ images: n.VAEDecode({ samples: sampled, vae }), filename_prefix: "txt2img" },
    { id: "save" });
  return {
    prompt: param(positive, "text"),
    seed: param(sampled, "seed"),
    steps: param(sampled, "steps", { optional: true }),
  };
});
```

```ts
// workflows/index.ts: every exported template becomes Go
export { txt2img } from "./txt2img.ts";
```

## 3. Generate the Go

```sh
go get -tool github.com/TheeKia/tscomfy-go/cmd/tscomfy-go
```

```go
// main.go, or any file of the package that holds the directive
//go:generate go tool tscomfy-go -workflows ./workflows -out ./internal/workflows
```

`go generate ./...` writes `internal/workflows/txt2img.json` (the template) and `txt2img.go`:

```go
type Txt2Img struct {
	Prompt string  // positive.text
	Seed   uint64  // sampler.seed, 0 to 18446744073709551615
	Steps  *int    // sampler.steps, 1 to 10000, default 30
}
func (p Txt2Img) Build() (comfy.Prompt, error)
const (
	Txt2ImgPositive comfy.NodeID = "positive"
	Txt2ImgSampler  comfy.NodeID = "sampler"
	Txt2ImgSave     comfy.NodeID = "save"
)
```

Commit both files. The output directory belongs to the generator: put nothing else in it.

## 4. Run it

```go
client, err := comfy.Dial(ctx, "http://127.0.0.1:8188", nil)
if err != nil { … }
defer client.Close()

prompt, err := workflows.Txt2Img{Prompt: "a fox in the snow", Seed: 42, Steps: new(20)}.Build()
if err != nil { … }
res, err := client.Run(ctx, prompt, nil)
if err != nil { … }
for _, f := range res.Files(workflows.Txt2ImgSave) {
	file, err := client.Open(ctx, f.FileRef)
	…
}
```

Next: [writing workflows](writing-workflows.md), [running jobs](running-jobs.md),
[deploying](deploying.md). `examples/txt2img` is a complete backend that streams progress and
previews over server-sent events.
