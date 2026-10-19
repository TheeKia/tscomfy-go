import {
  type ApiPrompt,
  type InputDef,
  type IntValue,
  isOutputRef,
  type JsonValue,
  type NodeHandle,
  type NodeId,
  type Out,
  toApi,
  ValidationError,
  type Workflow,
} from "@tscomfy/core";
import type { Param } from "./index.ts";

/** The template format this package writes (docs/spec.md §4). */
export const FORMAT = 1;

const TEMPLATE_NAME = /^[a-z][a-z0-9_]*$/;
const PARAM_NAME = /^[A-Za-z][A-Za-z0-9_]*$/;

/** The input kinds a parameter can have. */
type ParamKind = "int" | "float" | "string" | "boolean" | "combo";

/** A parameter of a template document. */
export type ParamDocument = {
  readonly name: string;
  readonly node: NodeId;
  readonly input: string;
  readonly kind: ParamKind;
  readonly min?: IntValue;
  readonly max?: IntValue;
  readonly optional?: true;
};

/** A template document (docs/spec.md §4), as `stringify` writes it. */
export type TemplateDocument = {
  readonly format: typeof FORMAT;
  readonly name: string;
  readonly params: readonly ParamDocument[];
  readonly prompt: ApiPrompt;
};

/**
 * A template or export that breaks a rule of docs/spec.md §3.5 or §3.6. The export command
 * prints its message alone; any other error comes with its stack.
 */
export class TemplateError extends Error {
  override readonly name = "TemplateError";
}

/** The template document of each template, made when the template is. */
const documents = new WeakMap<object, TemplateDocument>();

/** Records the document of a template that `template` made. */
export function register(template: object, document: TemplateDocument): void {
  documents.set(template, document);
}

/** The template document of a value that `template` made, or `undefined`. */
export function documentOf(value: unknown): TemplateDocument | undefined {
  return typeof value === "object" && value !== null ? documents.get(value) : undefined;
}

/** The node id behind a node handle or one of its outputs. */
export function nodeIdOf(node: NodeHandle | Out<string>): NodeId {
  return isOutputRef(node) ? node.node : node.id;
}

/** Throws unless `name` is a valid template name. */
export function checkTemplateName(name: string): void {
  if (!TEMPLATE_NAME.test(name)) {
    throw new TemplateError(
      `template: the name ${JSON.stringify(name)} must match ${String(TEMPLATE_NAME)}`,
    );
  }
}

/**
 * The template document of a built workflow (docs/spec.md §4): its prompt, exported and validated
 * by tscomfy, and its parameters, each checked against the schema and the prompt. Throws a
 * {@link TemplateError} naming the template and the parameter for anything §3.6 refuses.
 */
export function templateDocument(name: string, built: Workflow): TemplateDocument {
  const fail = (message: string, options?: ErrorOptions): never => {
    throw new TemplateError(`template "${name}": ${message}`, options);
  };
  let prompt: ApiPrompt;
  try {
    prompt = toApi(built);
  } catch (error) {
    if (error instanceof ValidationError) return fail(error.message, { cause: error });
    throw error;
  }
  const illFormed = findIllFormed(prompt);
  if (illFormed !== undefined) {
    fail(`the prompt holds a string that isn't valid Unicode (a lone surrogate) at ${illFormed}`);
  }
  const handles: unknown = built.handles;
  if (typeof handles !== "object" || handles === null || Array.isArray(handles)) {
    return fail("the build function must return an object of parameters made by param()");
  }
  const taken = new Set<string>();
  const params = Object.entries(handles).map(([paramName, spec]: [string, unknown]) => {
    const failParam = (message: string): never => fail(`parameter "${paramName}": ${message}`);
    if (!PARAM_NAME.test(paramName)) failParam(`the name must match ${String(PARAM_NAME)}`);
    if (!isParam(spec)) return failParam("not made by param()");
    const key = `${spec.node}\u0000${spec.input}`;
    if (taken.has(key)) failParam(`another parameter already names ${describe(spec)}`);
    taken.add(key);
    return paramDocument(paramName, spec, built, prompt, failParam);
  });
  return { format: FORMAT, name, params, prompt };
}

function paramDocument(
  name: string,
  spec: Param,
  built: Workflow,
  prompt: ApiPrompt,
  fail: (message: string) => never,
): ParamDocument {
  const node = built.graph.nodes.get(spec.node);
  if (node === undefined) return fail(`node "${spec.node}" is not in the workflow`);
  const definition = built.schema.nodes[node.classType];
  if (definition === undefined) return fail(`class ${node.classType} is not in the schema`);
  const input = definition.inputs.find((each) => each.name === spec.input);
  if (input === undefined) {
    return fail(
      `${node.classType} has no input "${spec.input}" (keys that dynamic inputs expand into can't be parameters)`,
    );
  }
  const kind = paramKind(input, fail);
  const apiNode = prompt[spec.node];
  if (apiNode === undefined) {
    return fail(`node "${spec.node}" is not in the prompt (is it bypassed or muted?)`);
  }
  const value = apiNode.inputs[spec.input];
  if (value === undefined) return fail(`${describe(spec)} has no value in the prompt: give it one`);
  if (Array.isArray(value)) return fail(`${describe(spec)} is linked; a parameter needs a value`);
  const misfit = misfitOf(kind, value);
  if (misfit !== undefined) return fail(`${describe(spec)} ${misfit}`);
  return {
    name,
    node: spec.node,
    input: spec.input,
    kind,
    ...bounds(input),
    ...(spec.optional ? { optional: true } : {}),
  };
}

function paramKind(input: InputDef, fail: (message: string) => never): ParamKind {
  switch (input.kind) {
    case "int":
    case "float":
    case "string":
    case "boolean":
      return input.kind;
    case "combo":
      if (input.multiselect) return fail(`"${input.name}" is a multi-select combo`);
      if (input.options.some((option) => typeof option !== "string")) {
        return fail(`"${input.name}" is a combo with options that aren't strings`);
      }
      return "combo";
    case "socket":
    case "value":
    case "autogrow":
    case "dynamicCombo":
    case "dynamicGroup":
    case "matchType":
    case "unknown":
      return fail(`"${input.name}" is of kind ${input.kind}, which can't be a parameter`);
  }
}

/**
 * Why a value doesn't fit its kind, as Go's `workflow.Load` reads it, or `undefined`. An int beyond
 * 2^53 must be a bigint: as a number it is rounded, and written in exponent form.
 */
function misfitOf(kind: ParamKind, value: JsonValue): string | undefined {
  switch (kind) {
    case "int":
      if (typeof value === "bigint" || Number.isSafeInteger(value)) return undefined;
      return typeof value === "number" && Number.isInteger(value)
        ? `is ${String(value)}, beyond 2^53: write it as a bigint`
        : "is not an integer";
    case "float":
      return typeof value === "number" ? undefined : "is not a number";
    case "string":
    case "combo":
      return typeof value === "string" ? undefined : "is not a string";
    case "boolean":
      return typeof value === "boolean" ? undefined : "is not a boolean";
  }
}

function bounds(input: InputDef): { readonly min?: IntValue; readonly max?: IntValue } {
  if (input.kind !== "int" && input.kind !== "float") return {};
  return {
    ...(input.min === undefined ? {} : { min: input.min }),
    ...(input.max === undefined ? {} : { max: input.max }),
  };
}

/** The path of the first string or key that isn't well-formed UTF-16, or `undefined`. */
function findIllFormed(value: JsonValue, path = "prompt"): string | undefined {
  if (typeof value === "string") return value.isWellFormed() ? undefined : path;
  if (typeof value !== "object" || value === null) return undefined;
  for (const [key, item] of Object.entries(value)) {
    const at = `${path}.${key}`;
    if (!key.isWellFormed()) return at;
    const found = findIllFormed(item, at);
    if (found !== undefined) return found;
  }
  return undefined;
}

function isParam(value: unknown): value is Param {
  return (
    typeof value === "object" &&
    value !== null &&
    "node" in value &&
    typeof value.node === "string" &&
    "input" in value &&
    typeof value.input === "string" &&
    "optional" in value &&
    typeof value.optional === "boolean"
  );
}

function describe(spec: Param): string {
  return `${spec.node}.${spec.input}`;
}
