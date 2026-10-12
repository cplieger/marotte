import { readdirSync, readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { expect, it } from "vitest";

it("is still needed: the installed @vitest/browser fails a file on an untrusted pagehide", () => {
  const pkg = createRequire(import.meta.url).resolve("@vitest/browser/package.json");
  const dir = join(dirname(pkg), "dist", "client", "__vitest_browser__");
  const sources = readdirSync(dir)
    .filter((name) => name.startsWith("orchestrator-") && name.endsWith(".js"))
    .map((name) => readFileSync(join(dir, name), "utf8"));
  expect(sources).not.toHaveLength(0);
  const watchesUntrusted = sources.some(
    (source) => source.includes('addEventListener("pagehide"') && !source.includes("isTrusted"),
  );
  expect(
    watchesUntrusted,
    "the installed @vitest/browser no longer fails a file on a synthetic pagehide; " +
      "delete page-lifecycle-setup.ts, this test and its setupFiles entry in vitest.config.ts",
  ).toBe(true);
});
