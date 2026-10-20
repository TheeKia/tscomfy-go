# @tscomfy/go

Write ComfyUI workflows in TypeScript with [tscomfy](https://github.com/TheeKia/tscomfy) and run
them from Go with [tscomfy-go](https://github.com/TheeKia/tscomfy-go).

```ts
import { param, template } from "@tscomfy/go";
import { local } from "./comfy/local/index.ts";

export const txt2img = template("txt2img", local, ({ n }) => {
  const [model, clip, vae] = n.CheckpointLoaderSimple({ ckpt_name: "model.safetensors" });
  const positive = n.CLIPTextEncode({ clip, text: "a cat in a hat" }, { id: "positive" });
  const sampled = n.KSampler(
    {
      model,
      positive,
      negative: n.CLIPTextEncode({ clip, text: "" }),
      latent_image: n.EmptyLatentImage({ width: 1024, height: 1024 }),
      seed: 0,
      steps: 30,
      sampler_name: "euler",
      scheduler: "normal",
    },
    { id: "sampler" },
  );
  n.SaveImage({ images: n.VAEDecode({ samples: sampled, vae }), filename_prefix: "txt2img" }, { id: "save" });
  return {
    prompt: param(positive, "text"),
    seed: param(sampled, "seed"),
    steps: param(sampled, "steps", { optional: true }),
  };
});
```

`template` builds the workflow at once and checks it: tscomfy's validation, and each parameter
against the schema and the prompt. `go generate` runs this package's `tscomfy-go-export` command
and writes each exported template as a JSON file and a typed Go struct whose `Build()` fills it.

Install the version that matches the Go module. See
[getting started](https://github.com/TheeKia/tscomfy-go/blob/main/docs/guides/getting-started.md)
and [writing workflows](https://github.com/TheeKia/tscomfy-go/blob/main/docs/guides/writing-workflows.md).

MIT.
