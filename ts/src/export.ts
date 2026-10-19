import { stringify } from "@tscomfy/core";
import { documentOf, FORMAT, type TemplateDocument, TemplateError } from "./document.ts";

/**
 * The export document of a workflows module (docs/spec.md §3.5): every named export, each a
 * template made by `template`, as `{"format":1,"templates":[…]}` sorted by name. Throws a
 * `TemplateError` for a default export, an export that isn't a template, and two templates of one
 * name.
 */
export function exportTemplates(module: object): string {
  const byName = new Map<string, TemplateDocument>();
  for (const exportName of Object.keys(module)) {
    if (exportName === "default") {
      throw new TemplateError("the module has a default export; export each template by name");
    }
    const document = documentOf(Reflect.get(module, exportName));
    if (document === undefined) {
      throw new TemplateError(
        `export "${exportName}" is not a template made by template() (is @tscomfy/go installed twice?)`,
      );
    }
    if (byName.has(document.name)) {
      throw new TemplateError(`two exports are templates named "${document.name}"`);
    }
    byName.set(document.name, document);
  }
  // Template names are ASCII, so code-unit order is byte order.
  const templates = [...byName.values()].toSorted((a, b) =>
    a.name < b.name ? -1 : a.name > b.name ? 1 : 0,
  );
  return stringify({ format: FORMAT, templates });
}
