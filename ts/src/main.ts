// The export command tscomfy-go's generator runs (docs/spec.md §3.5): `tscomfy-go-export
// <entry module>` imports the workflows module and prints its export document on stdout.

import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { TemplateError } from "./document.ts";
import { exportTemplates } from "./export.ts";

async function main(args: readonly string[]): Promise<void> {
  const [entry, ...rest] = args;
  if (entry === undefined || rest.length > 0) {
    throw new TemplateError("usage: tscomfy-go-export <entry module>");
  }
  const module: unknown = await import(pathToFileURL(resolve(entry)).href);
  if (typeof module !== "object" || module === null) {
    throw new TemplateError(`${entry} is not a module`);
  }
  process.stdout.write(`${exportTemplates(module)}\n`);
}

try {
  await main(process.argv.slice(2));
} catch (error) {
  // A rule the workflows break says all there is to say; anything else needs its stack.
  const shown =
    error instanceof TemplateError
      ? error.message
      : error instanceof Error
        ? (error.stack ?? error.message)
        : String(error);
  process.stderr.write(`tscomfy-go-export: ${shown}\n`);
  process.exitCode = 1;
}
