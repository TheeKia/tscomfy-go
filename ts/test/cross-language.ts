// The cross-language property (docs/spec.md §9): for each set of values, tscomfy building the
// workflow with them and Go's Fill filling its template with them give byte-identical prompts.
// This module writes the fixture Go's test reads (workflow/testdata/cross-language.json): the
// template, and per case the values and the prompt tscomfy built.

import { type BuildContext, type JsonValue, stringify, toApi, workflow } from "@tscomfy/core";
import { param, template } from "@tscomfy/go";
import { documentOf } from "../src/document.ts";
import { params } from "./comfy/params/index.ts";
import type { Choice, Nodes } from "./comfy/params/nodes.ts";

/** The path of the fixture, from the repository root. */
export const FIXTURE = "workflow/testdata/cross-language.json";

interface Values {
  readonly seed: bigint;
  readonly signed: bigint;
  readonly count: number;
  readonly unbounded: bigint;
  readonly ratio: number;
  readonly huge: number;
  readonly text: string;
  readonly toggle: boolean;
  readonly choice: Choice;
}

/** The values written in the template: its defaults. */
const DEFAULTS: Values = {
  seed: 0n,
  signed: 0n,
  count: 4,
  unbounded: 0n,
  ratio: 0.5,
  huge: 0,
  text: "a cat in a hat",
  toggle: true,
  choice: "first",
};

/** The parameters, in declaration order; `optional` ones may be left unset (`null`). */
const PARAMS: readonly (readonly [keyof Values, string, boolean])[] = [
  ["seed", "seed", false],
  ["signed", "signed", false],
  ["count", "count", true],
  ["unbounded", "unbounded", true],
  ["ratio", "ratio", false],
  ["huge", "huge_float", true],
  ["text", "text", false],
  ["toggle", "toggle", true],
  ["choice", "choice", true],
];

function build({ n }: BuildContext<Nodes>, v: Values) {
  return n.MockParams(
    {
      seed: v.seed,
      signed: v.signed,
      count: v.count,
      unbounded: v.unbounded,
      ratio: v.ratio,
      huge_float: v.huge,
      text: v.text,
      toggle: v.toggle,
      choice: v.choice,
    },
    { id: "params" },
  );
}

const crossLanguage = template("cross_language", params, (context) => {
  const node = build(context, DEFAULTS);
  return Object.fromEntries(
    PARAMS.map(([name, input, optional]) => [name, param(node, input, { optional })]),
  );
});

/** A case: values to set; a value of `null` leaves an optional parameter unset. */
type Case = { readonly [K in keyof Values]?: Values[K] | null };

const CASES: readonly (readonly [string, Case])[] = [
  ["the defaults", {}],
  [
    "the largest values",
    {
      seed: 18446744073709551615n,
      signed: 9223372036854775807n,
      count: 64,
      unbounded: 9007199254740993n,
      ratio: 1,
      huge: 18446744073709549568,
      toggle: false,
      choice: "third",
    },
  ],
  [
    "the smallest values",
    {
      seed: 0n,
      signed: -9223372036854775808n,
      count: 1,
      unbounded: -9007199254740993n,
      ratio: 0,
      huge: -18446744073709549568,
      text: "",
    },
  ],
  ["negative zero", { ratio: -0, huge: -0 }],
  ["floats JavaScript writes in exponent form", { ratio: 1e-7, huge: 1e17 }],
  ["the smallest float", { ratio: 5e-324, huge: -5e-324 }],
  ["floats with long fractions", { ratio: 0.1 + 0.2, huge: 4503599627370495.5 }],
  ["integers near 2^53 as floats", { huge: 9007199254740992 }],
  [
    "strings with escapes",
    { text: 'a fox <&> ünïcode "q" \\ \n\t\r\b\f \u0000\u001f\u007f    🦊 \u{10FFFF}' },
  ],
  ["optional parameters left unset", { count: null, unbounded: null, huge: null, choice: null }],
];

/** The parameters of kind float. Their values travel as text, so -0 survives; Go parses it. */
const FLOATS: ReadonlySet<keyof Values> = new Set(["ratio", "huge"]);

function jsonOf(key: keyof Values, value: Values[keyof Values] | null): JsonValue {
  if (value === null || !FLOATS.has(key)) return value;
  return Object.is(value, -0) ? "-0" : String(value);
}

/** The fixture's text. */
export function crossLanguageFixture(): string {
  const cases = CASES.map(([name, set]) => {
    const values = { ...DEFAULTS };
    for (const [key, value] of Object.entries(set)) {
      if (value !== null) Object.assign(values, { [key]: value });
    }
    const built = workflow(params, (context) => {
      build(context, values);
    });
    return {
      name,
      values: PARAMS.map(([key, , optional]) => {
        const value = set[key];
        if (value === null && !optional) throw new Error(`${name}: ${key} is required`);
        return jsonOf(key, value === undefined ? values[key] : value);
      }),
      prompt: stringify(toApi(built)),
    };
  });
  return `${stringify({ template: documentOf(crossLanguage) ?? null, cases })}\n`;
}
