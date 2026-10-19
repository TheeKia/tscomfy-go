import { describe, expect, test } from "bun:test";
import { parse } from "@tscomfy/core";
import { param, template } from "@tscomfy/go";
import { exportTemplates } from "../src/export.ts";
import { params } from "./comfy/params/index.ts";

function named(name: string) {
  return template(name, params, ({ n }) => ({
    seed: param(n.MockParams({ choice: "first" }, { id: "p" }), "seed"),
  }));
}

describe("exportTemplates", () => {
  test("prints every template, sorted by name, with big integers exact", () => {
    const text = exportTemplates({ second: named("b_second"), first: named("a_first") });
    const { value } = parse(text);
    expect(value).toMatchObject({
      format: 1,
      templates: [{ name: "a_first" }, { name: "b_second" }],
    });
    expect(text).toContain('"max":18446744073709551615');
    expect(
      text.startsWith('{"format":1,"templates":[{"format":1,"name":"a_first","params":['),
    ).toBe(true);
  });

  test("prints an empty list for a module without templates", () => {
    expect(exportTemplates({})).toBe('{"format":1,"templates":[]}');
  });

  test("refuses a default export", () => {
    expect(() => exportTemplates({ default: named("a") })).toThrow("default export");
  });

  test("refuses an export that isn't a template", () => {
    expect(() => exportTemplates({ helper: () => 1 })).toThrow('export "helper" is not a template');
  });

  test("refuses two templates of one name", () => {
    expect(() => exportTemplates({ one: named("same"), two: named("same") })).toThrow(
      'two exports are templates named "same"',
    );
  });
});
