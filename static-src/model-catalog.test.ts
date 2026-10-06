import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { readVerdict, refreshCatalog } from "./model-catalog.js";
import type { CatalogAnswer } from "./model-catalog.js";
import type { CatalogPhase } from "./picker.js";
import type { CatalogState } from "./wire/types.gen.js";

function answer(catalog: CatalogState): CatalogAnswer {
  return { catalog };
}

interface Recorder {
  readonly deps: Parameters<typeof refreshCatalog<CatalogAnswer>>[0];
  readonly applied: CatalogAnswer[];
  readonly phases: CatalogPhase[];
  reads: number;
}

/** Scripted reads; the last entry repeats so a looping caller never runs off the end. */
function recorder(script: readonly (CatalogAnswer | null)[]): Recorder {
  const applied: CatalogAnswer[] = [];
  const phases: CatalogPhase[] = [];
  const r: Recorder = {
    applied,
    phases,
    reads: 0,
    deps: {
      read: () => {
        const at = Math.min(r.reads, script.length - 1);
        r.reads += 1;
        return Promise.resolve(script[at] ?? null);
      },
      apply: (a) => {
        applied.push(a);
      },
      setPhase: (p) => {
        phases.push(p);
      },
    },
  };
  return r;
}

describe("the catalog verdict mapping", () => {
  it("treats an empty catalog as a real answer, not a failure to retry", () => {
    expect(readVerdict("empty")).toBe("usable");
  });

  it("maps a populated catalog to usable and an unavailable one to retry", () => {
    expect(readVerdict("ready")).toBe("usable");
    expect(readVerdict("unavailable")).toBe("retry");
  });
});

describe("refreshCatalog", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("applies a usable first answer and issues no second read", async () => {
    const r = recorder([answer("ready")]);

    await refreshCatalog(r.deps);

    expect(r.reads).toBe(1);
    expect(r.applied).toEqual([answer("ready")]);
    expect(r.phases).toEqual(["ready"]);
  });

  it("issues no second read for an EMPTY catalog", async () => {
    const r = recorder([answer("empty")]);

    await refreshCatalog(r.deps);

    expect(r.reads).toBe(1);
    expect(r.applied).toEqual([answer("empty")]);
    expect(r.phases).toEqual(["ready"]);
  });

  it("retries an unavailable answer and applies the one that converges", async () => {
    const r = recorder([answer("unavailable"), answer("ready")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(3_000);
    await done;

    expect(r.reads).toBe(2);
    expect(r.applied).toEqual([answer("ready")]);
    expect(r.phases).toEqual(["ready"]);
  });

  it("never applies an unavailable answer, so a degraded read replaces nothing", async () => {
    const r = recorder([answer("unavailable")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.applied).toEqual([]);
  });

  it("settles unavailable once the retry budget is spent, and stops reading", async () => {
    const r = recorder([answer("unavailable")]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.phases).toEqual(["unavailable"]);
    const settled = r.reads;
    await vi.advanceTimersByTimeAsync(200_000);
    expect(r.reads).toBe(settled);
  });

  it("bounds the retry: a transient failure cannot exceed the attempt ceiling", async () => {
    const r = recorder([null]);

    const done = refreshCatalog(r.deps);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    expect(r.reads).toBeLessThanOrEqual(7);
    expect(r.phases).toEqual(["unavailable"]);
  });

  it("refuses a second caller while a loop is running", async () => {
    const first = recorder([answer("unavailable")]);
    const second = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(second.deps);
    expect(second.reads).toBe(0);

    await vi.advanceTimersByTimeAsync(200_000);
    await done;
  });

  it("lets a RESET restart a live loop instead of being refused by it", async () => {
    const first = recorder([answer("unavailable")]);
    const login = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(login.deps, { reset: true });

    expect(login.applied).toEqual([answer("ready")]);
    expect(login.phases).toEqual(["ready"]);
    await vi.advanceTimersByTimeAsync(200_000);
    await done;
    expect(first.phases).toEqual([]);
    expect(first.applied).toEqual([]);
  });

  it("releases the slot for the next caller after a reset", async () => {
    const first = recorder([answer("unavailable")]);
    const login = recorder([answer("ready")]);
    const later = recorder([answer("ready")]);

    const done = refreshCatalog(first.deps);
    await refreshCatalog(login.deps, { reset: true });
    await vi.advanceTimersByTimeAsync(200_000);
    await done;

    await refreshCatalog(later.deps);
    expect(later.reads).toBe(1);
  });
});
