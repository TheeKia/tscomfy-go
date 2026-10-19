import { param, template } from "@tscomfy/go";
import { params } from "../comfy/params/index.ts";

export const good = template("good", params, ({ n }) => ({
  count: param(n.MockParams({ choice: "first" }, { id: "p" }), "count"),
}));
