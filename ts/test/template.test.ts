import { describe, expect, test } from "bun:test";
import { ValidationError, type BuildContext } from "@tscomfy/core";
import { param, type Param, template, TemplateError } from "@tscomfy/go";
import { documentOf } from "../src/document.ts";
import { basicPipeline } from "./comfy/basic-pipeline/index.ts";
import type { Nodes as BasicNodes } from "./comfy/basic-pipeline/nodes.ts";
import { params } from "./comfy/params/index.ts";
import type { Nodes as ParamsNodes } from "./comfy/params/nodes.ts";

type Build<N> = (context: BuildContext<N>) => Readonly<Record<string, Param>>;

/** A template over the `params` mock: one MockParams node, with `choice` set, as id "p". */
function onParams(
  build: (
    p: ReturnType<ParamsNodes["MockParams"]>,
    context: BuildContext<ParamsNodes>,
  ) => Readonly<Record<string, Param>>,
) {
  return template("params", params, (context) => {
    const p = context.n.MockParams({ choice: "first" }, { id: "p" });
    return build(p, context);
  });
}

function txt2img(build?: Build<BasicNodes>) {
  return template("txt2img", basicPipeline, (context) => {
    const { n } = context;
    const [model, clip, vae] = n.CheckpointLoaderSimple(
      { ckpt_name: "model-a.safetensors" },
      { id: "checkpoint" },
    );
    const positive = n.CLIPTextEncode({ clip, text: "a cat in a hat" }, { id: "positive" });
    const negative = n.CLIPTextEncode({ clip, text: "" });
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
    const save = n.SaveImage(
      { images: n.VAEDecode({ samples: sampled, vae }), filename_prefix: "txt2img" },
      { id: "save" },
    );
    return (
      build?.(context) ?? {
        prompt: param(positive, "text"),
        seed: param(sampled, "seed"),
        steps: param(sampled, "steps", { optional: true }),
        checkpoint: param(model, "ckpt_name", { optional: true }),
        prefix: param(save, "filename_prefix", { optional: true }),
      }
    );
  });
}

describe("template", () => {
  test("records the prompt and the parameters in declaration order", () => {
    const made = txt2img();
    expect(made.name).toBe("txt2img");
    const document = documentOf(made);
    expect(document?.["format"]).toBe(1);
    expect(document?.["name"]).toBe("txt2img");
    expect(document?.["params"]).toEqual([
      { name: "prompt", node: "positive", input: "text", kind: "string" },
      {
        name: "seed",
        node: "sampler",
        input: "seed",
        kind: "int",
        min: 0,
        max: 18446744073709551615n,
      },
      {
        name: "steps",
        node: "sampler",
        input: "steps",
        kind: "int",
        min: 1,
        max: 10000,
        optional: true,
      },
      { name: "checkpoint", node: "checkpoint", input: "ckpt_name", kind: "combo", optional: true },
      { name: "prefix", node: "save", input: "filename_prefix", kind: "string", optional: true },
    ]);
    // Automatic ids for the nodes without one; the prompt is tscomfy's own export.
    expect(Object.keys(document?.["prompt"] ?? {})).toEqual([
      "1",
      "2",
      "checkpoint",
      "positive",
      "latent",
      "sampler",
      "save",
    ]);
  });

  test("takes a node's id from a multi-output node itself", () => {
    const made = template("loader", basicPipeline, ({ n }) => {
      const loader = n.CheckpointLoaderSimple({ ckpt_name: "model-a.safetensors" }, { id: "load" });
      n.PreviewImage({ images: n.VAEDecode({ samples: n.EmptyLatentImage({}), vae: loader.VAE }) });
      return { checkpoint: param(loader, "ckpt_name") };
    });
    expect(documentOf(made)?.["params"]).toEqual([
      { name: "checkpoint", node: "load", input: "ckpt_name", kind: "combo" },
    ]);
  });

  test("records float, boolean and big bounds exactly", () => {
    const made = onParams((p) => ({
      signed: param(p, "signed"),
      ratio: param(p, "ratio"),
      huge: param(p, "huge_float"),
      toggle: param(p, "toggle"),
      unbounded: param(p, "unbounded"),
    }));
    expect(documentOf(made)?.["params"]).toEqual([
      {
        name: "signed",
        node: "p",
        input: "signed",
        kind: "int",
        min: -9223372036854775808n,
        max: 9223372036854775807n,
      },
      { name: "ratio", node: "p", input: "ratio", kind: "float", min: 0, max: 1 },
      {
        name: "huge",
        node: "p",
        input: "huge_float",
        kind: "float",
        min: -18446744073709549568,
        max: 18446744073709549568,
      },
      { name: "toggle", node: "p", input: "toggle", kind: "boolean" },
      { name: "unbounded", node: "p", input: "unbounded", kind: "int" },
    ]);
  });

  test("allows no parameters", () => {
    expect(documentOf(onParams(() => ({})))?.["params"]).toEqual([]);
  });
});

describe("template refuses", () => {
  const cases: readonly (readonly [string, () => unknown, string])[] = [
    [
      "a name that breaks the rule",
      () => template("Txt2Img", params, () => ({})),
      'the name "Txt2Img" must match',
    ],
    ["an empty name", () => template("", params, () => ({})), 'the name "" must match'],
    [
      "a parameter name that breaks the rule",
      () => onParams((p) => ({ "2nd": param(p, "count") })),
      'parameter "2nd": the name must match',
    ],
    [
      "a value not made by param",
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a look-alike on purpose
      () => onParams(() => ({ x: { node: "p" } as unknown as Param })),
      'parameter "x": not made by param()',
    ],
    [
      "two parameters on one input",
      () => onParams((p) => ({ a: param(p, "count"), b: param(p, "count") })),
      'parameter "b": another parameter already names p.count',
    ],
    [
      "an input the class doesn't have",
      () => onParams((p) => ({ x: param(p, "mode.factor") })),
      'MockParams has no input "mode.factor"',
    ],
    [
      "a socket input",
      () => onParams((p) => ({ x: param(p, "image") })),
      '"image" is of kind socket',
    ],
    [
      "an autogrow input",
      () => onParams((p) => ({ x: param(p, "layers") })),
      '"layers" is of kind autogrow',
    ],
    [
      "a dynamic combo",
      () => onParams((p) => ({ x: param(p, "mode") })),
      '"mode" is of kind dynamicCombo',
    ],
    [
      "a multi-select combo",
      () => onParams((p) => ({ x: param(p, "tags") })),
      '"tags" is a multi-select combo',
    ],
    [
      "a combo with options that aren't strings",
      () => onParams((p) => ({ x: param(p, "numeric") })),
      "options that aren't strings",
    ],
    [
      "an input without a value",
      () => onParams((p) => ({ x: param(p, "note") })),
      "p.note has no value in the prompt",
    ],
    [
      "a linked input",
      () =>
        template("params", params, ({ n }) => {
          const p = n.MockParams(
            { choice: "first", count: n.MockNumber({ value: 3 }) },
            { id: "p" },
          );
          return { x: param(p, "count") };
        }),
      "p.count is linked",
    ],
    [
      "a build function that returns an array",
      () =>
        template("params", params, ({ n }) => {
          n.MockParams({ choice: "first" });
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the wrong shape on purpose
          return [] as unknown as Record<string, Param>;
        }),
      "must return an object of parameters",
    ],
    [
      "a node of another workflow",
      () =>
        template("params", params, ({ n }) => {
          n.MockParams({ choice: "first" }, { id: "p" });
          return { x: param({ id: "elsewhere" }, "count") };
        }),
      'node "elsewhere" is not in the workflow',
    ],
    [
      "an int beyond 2^53 given as a number",
      () =>
        template("params", params, ({ n }) => ({
          x: param(n.MockParams({ choice: "first", seed: 1e17 }, { id: "p" }), "seed"),
        })),
      "p.seed is 100000000000000000, beyond 2^53: write it as a bigint",
    ],
    [
      "a fraction for an int",
      () =>
        template("params", params, ({ n }) => ({
          x: param(n.MockParams({ choice: "first", unbounded: 1.5 }, { id: "p" }), "unbounded"),
        })),
      "p.unbounded is not an integer",
    ],
    [
      "a lone surrogate anywhere in the prompt",
      () =>
        template("params", params, ({ n }) => {
          n.MockParams({ choice: "first", text: "a\uD800b" }, { id: "p" });
          return {};
        }),
      "isn't valid Unicode (a lone surrogate) at prompt.p.inputs.text",
    ],
    [
      "a muted node",
      () =>
        template("params", params, ({ n }) => {
          n.MockParams({ choice: "first" }, { id: "p" });
          const muted = n.MockNumber({ value: 3 }, { id: "muted", mode: "mute" });
          return { x: param(muted, "value") };
        }),
      'node "muted" is not in the prompt',
    ],
  ];
  for (const [what, make, message] of cases) {
    test(what, () => {
      expect(make).toThrow(message);
      expect(make).toThrow(TemplateError);
    });
  }

  test("a workflow that fails tscomfy's validation, with its ValidationError as the cause", () => {
    let thrown: unknown;
    try {
      template("params", params, ({ n }) => {
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- an invalid value on purpose
        n.MockParams({ choice: "fourth" as "first" });
        return {};
      });
    } catch (error) {
      thrown = error;
    }
    if (!(thrown instanceof Error)) throw new Error("template didn't throw an Error");
    expect(thrown.message).toStartWith('template "params": The workflow has 1 error');
    expect(thrown.cause).toBeInstanceOf(ValidationError);
  });
});
