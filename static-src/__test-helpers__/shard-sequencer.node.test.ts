import { readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { TestSpecification, Vitest } from "vitest/node";

import { CostSequencer, FILE_SECONDS, fileSeconds } from "./shard-sequencer.js";

const ROOT = join(import.meta.dirname, "..");

/** The suite's test files by path from the vitest root, as CI's shard jobs collect them. */
function suiteFiles(): string[] {
  return readdirSync(ROOT, { recursive: true, encoding: "utf8" })
    .map((p) => p.replaceAll("\\", "/"))
    .filter((p) => p.endsWith(".test.ts"))
    .filter((p) => !/(^|\/)(node_modules|e2e-sse|\.stryker-tmp)\//.test(p));
}

function projectOf(path: string): string {
  if (path.endsWith(".node.test.ts")) return "node";
  if (path.endsWith(".pointer.test.ts")) return "browser-pointer";
  if (path.endsWith(".touch.test.ts")) return "browser-touch";
  return "browser";
}

function spec(path: string): TestSpecification {
  return {
    moduleId: join(ROOT, path),
    project: { name: projectOf(path), config: { sequence: { groupOrder: 0 }, isolate: true } },
  } as unknown as TestSpecification;
}

function sequencer(index: number, count: number): CostSequencer {
  const ctx = {
    config: { root: ROOT, shard: { index, count } },
    cache: { getTestSpecificationResult: () => ({ failed: false, duration: 0 }) },
  };
  return new CostSequencer(ctx as unknown as Vitest);
}

const pathOf = (s: TestSpecification): string => s.moduleId.slice(ROOT.length + 1);
const cost = (specs: readonly TestSpecification[]): number =>
  specs.reduce((sum, s) => sum + fileSeconds(pathOf(s)), 0);

describe("CostSequencer", () => {
  const files = suiteFiles();
  const heaviest = Math.max(...Object.values(FILE_SECONDS));

  it("collects the files the cost table names", () => {
    for (const path of Object.keys(FILE_SECONDS)) {
      expect(files).toContain(path);
    }
  });

  for (const count of [2, 3, 4]) {
    it(`splits the suite into ${String(count)} shards no further apart than its costliest file`, async () => {
      const shards = await Promise.all(
        Array.from({ length: count }, (_, i) => sequencer(i + 1, count).shard(files.map(spec))),
      );
      expect(shards.flat().map(pathOf).toSorted()).toEqual(files.toSorted());
      const costs = shards.map(cost);
      expect(Math.max(...costs) - Math.min(...costs)).toBeLessThanOrEqual(heaviest);
    });
  }

  it("gives each file the same shard whatever order the files arrive in", async () => {
    const forward = await sequencer(2, 3).shard(files.map(spec));
    const backward = await sequencer(2, 3).shard(files.toReversed().map(spec));
    expect(backward.map(pathOf).toSorted()).toEqual(forward.map(pathOf).toSorted());
  });

  it("starts a shard's costliest file first", async () => {
    const shard = await sequencer(1, 1).shard(files.map(spec));
    const order = (await sequencer(1, 1).sort(shard)).map(pathOf);
    expect(order[0]).toBe("scroll.visual.test.ts");
    const costs = order.map(fileSeconds);
    expect(costs).toEqual(costs.toSorted((a, b) => b - a));
  });
});
