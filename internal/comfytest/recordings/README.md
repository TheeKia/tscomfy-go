# Protocol recordings

What a ComfyUI server sends while it runs prompts, replayed by the fake server in
`internal/comfytest`. Copied unchanged from tscomfy's `internal/fixtures/mocks/protocol/` (MIT,
same owner) on 2026-10-20.

Each file holds runs: the prompt, the answer to `POST /prompt`, the websocket events in order, and
the job record (`GET /api/jobs/{id}`) at the end. `PROMPT_ID` stands for the prompt id the client
minted; binary frames are `{ "binary": 4, "metadata", "image" }` (base64) or
`{ "binary": 3, "node", "text" }`.

**Provenance.** tscomfy's capture script recorded them from ComfyUI 0.41.0; they were then
distilled by hand: tracebacks cut to two frames, the server's paths and model names generalized
(`model-a.safetensors`, …), image bytes replaced by a tiny image. `blocked.json` is hand-written
from ComfyUI's source, since no core node blocks without a model or a partner account. They are
mocks, not raw captures, so they fall under the rule that no real ComfyUI data is committed.

The protocol rules these runs exercise were checked against ComfyUI 0.42's source (spec §8); the
live tests (Phase 6) run the model-free ones against a 0.42 server.

| File | Covers |
|---|---|
| `success.json` | A saved and a previewed image, then the same prompt again, every node cached |
| `websocket.json` | `SaveImageWebsocket`'s type-4 PNG frame, PreviewAny's text output |
| `rejected.json` | 400s: no output node, an unknown class (`node_id` in `extra_info`), an input out of bounds |
| `partial.json` | A 200 with `node_errors`: one output refused, the other run |
| `error.json` | A node raising after an output ran: `execution_error`, a failed record with that output |
| `cancel.json` | A running job cancelled: `execution_interrupted`, a cancelled record |
| `pending.json` | A job cancelled while queued: no event, then a 404 record |
| `sampler.json` | A sampler run: six latent previews as type-4 JPEG frames, step progress, a 2⁶⁴−1 seed echoed exactly |
| `blocked.json` | Progress text (type 3) and a blocked node |
