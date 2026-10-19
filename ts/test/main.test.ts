import { describe, expect, test } from "bun:test";

const MAIN = `${import.meta.dir}/../src/main.ts`;

/** Runs the export command on a fixture module, from the test directory. */
async function exportOf(...args: string[]) {
  const child = Bun.spawn(["bun", "--conditions=@tscomfy/source", MAIN, ...args], {
    cwd: import.meta.dir,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { stdout, stderr, code };
}

describe("tscomfy-go-export", () => {
  test("prints the export document and a newline", async () => {
    const { stdout, stderr, code } = await exportOf("fixtures/good.ts");
    expect({ stderr, code }).toEqual({ stderr: "", code: 0 });
    expect(stdout.startsWith('{"format":1,"templates":[{"format":1,"name":"good",')).toBe(true);
    expect(stdout.endsWith("}\n")).toBe(true);
  });

  test("prints a rule a template breaks, alone", async () => {
    const { stdout, stderr, code } = await exportOf("fixtures/bad-template.ts");
    expect({ stdout, code }).toEqual({ stdout: "", code: 1 });
    expect(stderr).toStartWith('tscomfy-go-export: template: the name "Bad" must match');
    expect(stderr.split("\n")).toHaveLength(2);
  });

  test("prints any other error with its stack", async () => {
    const { stderr, code } = await exportOf("fixtures/throws.ts");
    expect(code).toBe(1);
    expect(stderr).toStartWith("tscomfy-go-export: RangeError: a helper failed");
    expect(stderr).toContain("throws.ts");
  });

  test("refuses a wrong command line", async () => {
    const runs = await Promise.all([exportOf(), exportOf("a.ts", "b.ts")]);
    for (const { stderr, code } of runs) {
      expect({ stderr, code }).toEqual({
        stderr: "tscomfy-go-export: usage: tscomfy-go-export <entry module>\n",
        code: 1,
      });
    }
  });
});
