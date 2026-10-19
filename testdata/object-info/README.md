# Mock `/object_info` schemas

Complete `/object_info` responses for the tests of `@tscomfy/go` and the example's schema. No
real server dump: model names are generic (`model-a.safetensors`, …), and integers beyond 2⁵³
are written exactly.

| File | Provenance | Covers |
|---|---|---|
| `basic-pipeline.json` | Copied unchanged from tscomfy's `internal/fixtures/mocks/object-info/` (MIT, same owner) on 2026-10-20; hand-written there | A txt2img pipeline (checkpoint loader, CLIP text encode, empty latent, KSampler with a 2⁶⁴−1 seed max, VAE decode, save), plus a LoRA loader, latent upscale and preview. The example's schema |
| `params.json` | Written here by hand | `MockParams`, an output node with one input of every kind a template parameter can or can't have (64-bit and unbounded ints, a 2⁷⁰ bound, floats with bounds beyond 2⁵³, string, boolean, combos with string, numeric and multi-select options, a socket, autogrow, dynamic combo, an input with no default); `MockNumber`, to link into a widget |
