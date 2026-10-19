# Running tscomfy from Go: measurements and the template prototype

Research for decision D1 (`docs/decisions.md`) and the template format (spec §4), done 2026-10-19 and 2026-10-20 on the owner's machine:
Go 1.27.2 (linux/amd64), Bun 1.4.3, tscomfy 0.2.1 (`/home/kia/code/tscomfy`), the schema
generated from the owner's ComfyUI (1,758 nodes; ComfyUI 0.41 when generated, 0.42 running). The
scratch code lived outside the repository; what matters is reproduced here. No real server data
is in this file: model names and schema hashes are replaced by placeholders.

## 1. The workload

tscomfy's `examples/txt2img` (`txt2img.ts`): checkpoint, two prompts, an empty latent, a sampler,
decode, save. 7 nodes. Its schema module (`src/comfy/local/index.ts`, gitignored in tscomfy) is
one 4.09 MB JSON string literal that `defineSchema` parses with tscomfy's lossless `parse`.

The bundle was built with:

```sh
bun build entry.ts --conditions=@tscomfy/source --target=browser --format=iife --minify --outfile=bundle.js
# 4,140,245 bytes; 420,211 gzipped
```

`entry.ts` set `globalThis.tscomfyBuild = (request: string) => string`: parse the request with
tscomfy's `parse`, build `txt2img`, return `stringify({ prompt: toApi(wf), handles })`.

## 2. Running TypeScript per request

### 2.1 A Bun process per build

`bun build run-bun.js --compile` (94,955,488 bytes), run from Go with `os/exec`, request on stdin:

| | Wall | Peak RSS |
|---|---|---|
| An empty compiled Bun program | 6 ms | 16 MB |
| The workflows binary, one build | 64–67 ms (median 66.2 ms, p95 70.2 ms over 30 runs) | 96–100 MB |

Inside the binary (`process.memoryUsage().rss`, `performance.now()`):

| Stage | Time | RSS after |
|---|---|---|
| Runtime start | – | 17 MB |
| Import the bundle (parse 4 MB of JS, build the 1,758-node schema) | 56 ms | 96 MB |
| Build the prompt | 1.67 ms | 96 MB |

Almost all of a request's cost is reloading the schema.

### 2.2 Embedded JS engines in Go

Harness: load the bundle once, call `tscomfyBuild` 30 to 50 times, compare the output with Bun's
byte for byte.

| Engine | Load | Per build | Output |
|---|---|---|---|
| `modernc.org/quickjs` v0.25.0 (ccgo-transpiled QuickJS 2026-06-04, pure Go) | 279 ms (245 ms from bytecode) | median 0.67 ms, p95 0.69 ms | byte-identical |
| `github.com/dop251/goja` (2026-10-19 pseudo-version) | fails | – | – |
| `github.com/fastschema/qjs` v0.0.6 (QuickJS-ng on wazero) | fails | – | – |

- goja and QuickJS-ng throw tscomfy's own error: "defineSchema: this runtime has no JSON.parse
  source-text access, so the schema's big integers would lose precision". tscomfy needs it for the
  134 integer bounds beyond 2^53 in a real dump (tscomfy ADR-0013, D10).
- modernc QuickJS: RSS grew by about 85 MB for the first VM and 25–35 MB for each further one;
  4 VMs did 1,000 builds in 274 ms (3,650/s). The Go binary grew by about 9 MB. A cold build of
  the package took 4.8 s. It cross-compiles with `CGO_ENABLED=0` to linux/arm64, darwin/arm64 and
  windows/amd64. After a thrown `ValidationError` (a checkpoint not in the combo) the VM stayed
  usable, and the full diagnostic arrived as a `*quickjs.Error`. `SetEvalTimeout(50ms)` stopped
  `for(;;){}` after 50 ms, and the VM stayed usable.
- Its dependencies: `modernc.org/libc`, `libquickjs`, `mathutil`, `memory`, `golang.org/x/sys`,
  `github.com/google/uuid`, `dustin/go-humanize`, `remyoudompheng/bigfft`, `mattn/go-isatty`,
  `ncruces/go-strftime`.
- Where its load time goes: on the 4 MB schema string, plain `JSON.parse` took 94 ms and
  `JSON.parse` with a source-text reviver 165 ms (Bun: 5 ms and 102 ms).

### 2.3 Conclusion

A request only changes values, so tscomfy can run once at development time (D1). The
owner ruled out TypeScript at run time in any form.

## 3. The template prototype

### 3.1 Export (TypeScript)

The sketch that resolved parameters against the real schema. `@tscomfy/go` will do this behind
`template` and `param` (spec §3); the logic is the same.

```ts
import { isOutputRef, stringify, toApi, workflow, type JsonValue, type NodeHandle, type Out } from "@tscomfy/core";
import { local } from "./comfy/local/index.ts";

type Ref = NodeHandle | Out<string>;
interface Param { readonly node: Ref; readonly input: string }
const param = (node: Ref, input: string): Param => ({ node, input });
const idOf = (ref: Ref): string => (isOutputRef(ref) ? ref.node : ref.id);

const txt2img = workflow(local, ({ n }) => {
  const [model, clip, vae] = n.CheckpointLoaderSimple({ ckpt_name: "<checkpoint>.safetensors" }, { id: "checkpoint" });
  const positive = n.CLIPTextEncode({ clip, text: "a cat in a hat" }, { id: "positive" });
  const negative = n.CLIPTextEncode({ clip, text: "" }, { id: "negative" });
  const latent = n.EmptyLatentImage({ width: 1024, height: 1024 }, { id: "latent" });
  const sampled = n.KSampler({ model, positive, negative, latent_image: latent, seed: 0, steps: 30,
    sampler_name: "euler", scheduler: "normal" }, { id: "sampler" });
  n.SaveImage({ images: n.VAEDecode({ samples: sampled, vae }, { id: "decode" }),
    filename_prefix: "txt2img" }, { id: "save" });
  return {
    checkpoint: param(model, "ckpt_name"),
    prompt: param(positive, "text"),
    seed: param(sampled, "seed"),
    steps: param(sampled, "steps"),
  };
});

const prompt = toApi(txt2img); // validates; throws ValidationError
const params = Object.entries(txt2img.handles).map(([name, p]) => {
  const node = idOf(p.node);
  const def = local.nodes[prompt[node]!.class_type]!.inputs.find((input) => input.name === p.input)!;
  return { name, node, input: p.input, kind: def.kind, default: prompt[node]!.inputs[p.input] as JsonValue,
    ...("min" in def && def.min !== undefined ? { min: def.min } : {}),
    ...("max" in def && def.max !== undefined ? { max: def.max } : {}) };
});
console.log(stringify({ format: 1, name: "txt2img", params, prompt }));
```

Findings:

- `toApi` writes default values the author didn't set (`cfg: 8`, `denoise: 1`), so they are in the
  prompt and could be parameters.
- The prompt's keys were the ids given (`checkpoint`, `positive`, …, `save`).
- The seed's bounds came out as `0` and `18446744073709551615` (an `IntValue` that is a `bigint`),
  and `stringify` wrote them exactly.
- Schema input definitions are in `local.nodes[classType].inputs`, each with `name` and `kind`
  (`int`, `float`, `string`, `boolean`, `combo`, `value`, `socket`, …); numeric kinds carry `min`,
  `max`, `default`, `step` (`packages/core/src/schema/types.ts`). `ComboOption` is
  `string | number | boolean`.
- Parameter handles: `n.CLIPTextEncode(…)` returns an `Out`, which has `.node`; a node without
  outputs returns a `NodeHandle`, which has `.id`. `isOutputRef` tells them apart.

### 3.2 Fill (Go)

The prototype that matched tscomfy byte for byte. Package `workflow` will index value spans at
`Load` instead of re-tokenizing on every `Fill` (spec §5); the encoding behavior is the same.

```go
type input struct{ node, name string }

// fill copies prompt, replacing the value of each node's input listed in values.
func fill(prompt jsontext.Value, values map[input]any) (jsontext.Value, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(prompt))
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	var node, section string
	for {
		depth, name := dec.StackDepth(), ""
		if depth > 0 {
			if _, n := dec.StackIndex(depth); n%2 == 1 { // the next token is a member's value
				name = string(dec.StackPointer().LastToken())
			}
		}
		switch depth {
		case 1:
			node = name
		case 2:
			section = name
		}
		if depth == 3 && section == "inputs" && name != "" {
			if v, ok := values[input{node, name}]; ok {
				if err := dec.SkipValue(); err != nil {
					return nil, err
				}
				raw, err := json.Marshal(v) // encoding/json/v2
				if err != nil {
					return nil, err
				}
				if err := enc.WriteValue(raw); err != nil {
					return nil, err
				}
				delete(values, input{node, name})
				continue
			}
		}
		tok, err := dec.ReadToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := enc.WriteToken(tok); err != nil {
			return nil, err
		}
	}
	if len(values) > 0 {
		return nil, fmt.Errorf("inputs not in the template: %v", values)
	}
	return bytes.TrimSpace(out.Bytes()), nil
}
```

The check: tscomfy built the workflow with `seed: 18446744073709551615n`, `steps: 20` and the text
`"a fox <&> ünïcode \"q\"\n  🦊"` and wrote it with `stringify(toApi(…))`; Go filled the
template (built with other values) with `uint64(18446744073709551615)`, `int64(20)` and the same
string. The outputs were **byte-identical**. `encoding/json/v2` doesn't escape `<`, `>` or `&` by
default, and neither does `stringify`.

### 3.3 Indented template files

`jsontext.Value.Indent(jsontext.WithIndent("  "))` then `Compact()` on tscomfy's output above gave
the original bytes back, and the indented form kept `"seed": 18446744073709551615` as written
(spec §4).

## 4. Facts checked along the way

- `encoding/json/v2` and `jsontext` are on by default in Go 1.27 (`internal/buildcfg/exp.go`:
  `JSONv2: true`; listed in `api/go1.27.txt`). `new(1024)` compiles (Go 1.26's `new(expr)`).
- coder/websocket v1.8.15 limits a message to 32,768 bytes by default and closes the connection
  with `StatusMessageTooBig` beyond it (`read.go:88-107`). `SetReadLimit(-1)` disables the limit.
- ComfyUI 0.42 (`v0.42.0-3`): `GET /api/jobs` accepts `status` (comma-separated), `workflow_id`,
  `sort_by`, `sort_order`, `limit` and `offset` (`server.py:911-990`).
- ComfyUI's `progress_state` values are floats (`comfy_execution/progress.py:29-30`).
- tscomfy's automatic node ids are `String(n)` from a counter that skips taken ids, and a
  duplicate or empty `{ id }` throws a `TypeError` (`packages/core/src/graph/workflow.ts:152-165`).
- tscomfy's live tests queue prompts with node ids `blur0` … `blur11` on the owner's server
  (`packages/client/test/live.ts`). ComfyUI's validation has no check that ids are numeric.
- `tscomfy generate --from <object_info.json>` works offline. tscomfy's mock
  `internal/fixtures/mocks/object-info/basic-pipeline.json` has CheckpointLoaderSimple, VAELoader,
  CLIPTextEncode, EmptyLatentImage, KSampler, VAEDecode, MockSeed, SaveImage, LoraLoader,
  LatentUpscaleBy and PreviewImage.
- On npm as of 2026-10-20: `@tscomfy/core` 0.2.1, `@tscomfy/cli` 0.2.1; `@tscomfy/go` is not taken.
