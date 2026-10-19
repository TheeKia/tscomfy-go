import { param, template } from "@tscomfy/go";
import { local } from "./comfy/local/index.ts";

/**
 * Text to image: a checkpoint, two prompts, an empty latent, a sampler, decode and save. Go sets
 * the prompt and the seed, and may override the rest.
 */
export const txt2img = template("txt2img", local, ({ n }) => {
  const [model, clip, vae] = n.CheckpointLoaderSimple(
    { ckpt_name: "model-a.safetensors" },
    { id: "checkpoint" },
  );
  const positive = n.CLIPTextEncode({ clip, text: "a cat in a hat" }, { id: "positive" });
  const negative = n.CLIPTextEncode({ clip, text: "" }, { id: "negative" });
  const latent = n.EmptyLatentImage({ width: 1024, height: 1024 }, { id: "latent" });
  const sampled = n.KSampler(
    {
      model,
      positive,
      negative,
      latent_image: latent,
      seed: 0,
      steps: 30,
      sampler_name: "euler",
      scheduler: "normal",
    },
    { id: "sampler" },
  );
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
