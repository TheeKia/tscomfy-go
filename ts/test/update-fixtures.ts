// Writes the cross-language fixture (docs/spec.md §9) that Go's workflow tests read. `make
// fixtures` runs it; the gate's end-to-end check fails while the committed one is stale.

import { crossLanguageFixture, FIXTURE } from "./cross-language.ts";

await Bun.write(`${import.meta.dir}/../../${FIXTURE}`, crossLanguageFixture());
