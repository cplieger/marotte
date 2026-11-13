// vitest's own split sorts files by path hash and slices the list, which put every frame-bound file
// on one shard: 1029s of file time against 438s, and that shard near the suite-duration bound.
import { relative } from "node:path";
import { BaseSequencer, type TestSpecification } from "vitest/node";

/** CI seconds per file (mean of five dev and PR runs) for each file over `check-suite-duration.ts`'s
 *  SLOW_FILE_MS. Re-measure from a CI log when that guard names a slow file this table lacks. */
export const FILE_SECONDS: Readonly<Record<string, number>> = {
  "scroll.visual.test.ts": 297,
  "touch-targets.test.ts": 224,
  "user-input-layout.pointer.test.ts": 212,
  "tab-close-optimistic.test.ts": 102,
  "button-size.test.ts": 100,
  "scroll.holds.test.ts": 47,
  "scroll.motion.test.ts": 42,
  "scroll.test.ts": 38,
  "residency-anchor.test.ts": 23,
  "settings-sync.test.ts": 19,
  "block-virtualization.test.ts": 16,
  "tab-close-draft.test.ts": 16,
  "turn-footer-closed-row-css.test.ts": 13,
  "find-in-chat.test.ts": 12,
  "actions/still-applying-chat.test.ts": 11,
};

/** The mean of every other file in the same runs. */
const DEFAULT_SECONDS = 0.4;

/** A file's cost by its path from the vitest root. */
export function fileSeconds(path: string): number {
  return FILE_SECONDS[path] ?? DEFAULT_SECONDS;
}

interface Costed<T> {
  item: T;
  key: string;
  cost: number;
}

/** Shard `index` (1-based) of `count`: costliest first, each onto the least-loaded shard, ties by
 *  key, so every shard job computes the same split from the same file list. */
function shardByCost<T>(
  items: readonly T[],
  keyOf: (item: T) => string,
  costOf: (item: T) => number,
  index: number,
  count: number,
): T[] {
  const costed: Costed<T>[] = items.map((item) => ({ item, key: keyOf(item), cost: costOf(item) }));
  costed.sort((a, b) => b.cost - a.cost || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
  const loads = Array.from({ length: count }, () => 0);
  const mine: T[] = [];
  for (const entry of costed) {
    const target = loads.indexOf(Math.min(...loads));
    loads[target] = (loads[target] ?? 0) + entry.cost;
    if (target === index - 1) {
      mine.push(entry.item);
    }
  }
  return mine;
}

/** Splits shards by {@link fileSeconds} and starts the costliest files first inside a shard, so a
 *  long file does not begin after the rest of its shard. */
export class CostSequencer extends BaseSequencer {
  private seconds(spec: TestSpecification): number {
    return fileSeconds(relative(this.ctx.config.root, spec.moduleId).replaceAll("\\", "/"));
  }

  override shard(files: TestSpecification[]): Promise<TestSpecification[]> {
    const { index, count } = this.ctx.config.shard ?? { index: 1, count: 1 };
    return Promise.resolve(
      shardByCost(
        files,
        (spec) => `${spec.project.name}\0${spec.moduleId}`,
        (spec) => this.seconds(spec),
        index,
        count,
      ),
    );
  }

  override async sort(files: TestSpecification[]): Promise<TestSpecification[]> {
    const base = await super.sort(files);
    return base.toSorted((a, b) => this.seconds(b) - this.seconds(a));
  }
}
