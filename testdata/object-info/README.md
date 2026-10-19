# Mock `/object_info` schemas

Complete `/object_info` responses, each with a few nodes around one theme, for the export tests
and the example's schema. Copied unchanged from tscomfy's `internal/fixtures/mocks/object-info/`
(MIT, same owner) on 2026-10-20.

**Provenance.** Hand-written in tscomfy, in the shapes ComfyUI serves; no real server dump. Node
classes are named `Mock…` unless the name itself is under test, and file names are generic
(`model-a.safetensors`, …). Integers beyond 2⁵³ are written exactly.

| File | Covers |
|---|---|
| `basic-pipeline.json` | A txt2img pipeline (checkpoint loader, CLIP text encode, empty latent, KSampler with a 2⁶⁴−1 seed max, VAE decode, save), plus a LoRA loader, latent upscale and preview |
| `combos.json` | Legacy and v2 combo forms, numeric, mixed and boolean options, upload and remote combos, multi-select flags |
| `numbers.json` | Integer bounds beyond 2⁵³, also as FLOAT bounds, defaults outside the bounds, widgets with no default |
| `autogrow.json` | `COMFY_AUTOGROW_V3` inputs, which can't be parameters |
| `dynamic-combo.json` | `COMFY_DYNAMICCOMBO_V3` inputs and their nested keys, which can't be parameters |
