/**
 * `@tscomfy/go`: write ComfyUI workflows with tscomfy and run them from Go.
 *
 * A workflow is declared with {@link template}, its parameters with {@link param}. tscomfy-go's
 * generator runs this package's export command and turns each template into a Go struct
 * (https://github.com/TheeKia/tscomfy-go).
 *
 * @module
 */

import {
  type BuildContext,
  type NodeHandle,
  type NodeId,
  type Out,
  type TypedSchema,
  type Workflow,
  workflow,
} from "@tscomfy/core";
import { checkTemplateName, nodeIdOf, register, templateDocument } from "./document.ts";

/** A workflow that tscomfy-go's generator turns into Go: its name and the tscomfy workflow. */
export interface Template<N = unknown> {
  /** Names the generated files (`<name>.json`, `<name>.go`) and the Go type. */
  readonly name: string;
  /** The workflow; its handles are the parameters, in declaration order. */
  readonly workflow: Workflow<N, Readonly<Record<string, Param>>>;
}

/** A parameter: one input of one node, whose value Go fills in. Made by {@link param}. */
export interface Param {
  readonly node: NodeId;
  readonly input: string;
  readonly optional: boolean;
}

/** Options of {@link param}. */
export interface ParamOptions {
  /** Go may leave the parameter unset, keeping the value written in the workflow. */
  readonly optional?: boolean;
}

/**
 * Declares a workflow for Go. Builds it at once with tscomfy's `workflow(schema, build)`;
 * `build` returns the parameters, made by {@link param}, under their names, in the order Go's
 * struct fields follow.
 *
 * `name` matches `^[a-z][a-z0-9_]*$` and parameter names `^[A-Za-z][A-Za-z0-9_]*$`. Throws when
 * tscomfy's validation fails (`cause` is its `ValidationError`) and when a parameter can't be
 * one: its node isn't in the prompt, its input isn't one of its class's inputs, holds a link or
 * has no value, is not an int, float, string, boolean or single-select string combo, or another
 * parameter names it too.
 */
export function template<N>(
  name: string,
  schema: TypedSchema<N>,
  build: (context: BuildContext<N>) => Readonly<Record<string, Param>>,
): Template<N> {
  checkTemplateName(name);
  const built = workflow(schema, build);
  const document = templateDocument(name, built);
  const result: Template<N> = Object.freeze({ name, workflow: built });
  register(result, document);
  return result;
}

/**
 * A parameter: input `input` of `node`, a node handle or one of its outputs. The value written
 * in the workflow is the parameter's default. Parameters are required unless `optional` is set.
 */
export function param(
  node: NodeHandle | Out<string>,
  input: string,
  options: ParamOptions = {},
): Param {
  return Object.freeze({ node: nodeIdOf(node), input, optional: options.optional === true });
}
