// Drift guard: `storeMock` must stay TOTAL. A missing export fails a whole file at link time, or
// worse, only the full run with an unattributed "error when mocking a module".
import { describe, it, expect } from "vitest";

import { storeMock } from "./store-mock.js";
import { makeSession } from "./model.js";
import * as store from "../store.js";
import type { Session } from "../types.js";

// Types are erased at runtime, so this compares the value surface an ESM link resolves.
const real = Object.keys(store as Record<string, unknown>).sort();
const mocked = Object.keys(storeMock).sort();

describe("the store.js mock helper stays total", () => {
  it("names every value store.ts exports", () => {
    expect(real.length, "the real module exported nothing; the import is wrong").toBeGreaterThan(0);
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `store-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing store.ts does not export, so a rename cannot hide behind it", () => {
    const extra = mocked.filter((k) => !real.includes(k));
    expect(extra, `store-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});

// The helper cannot reach `importOriginal`, so its pure entries are hand copies of production
// rules; presence says nothing about them, so this half checks their behaviour.
describe("the mock's re-derived rules agree with the real ones", () => {
  function session(fields: Partial<Session>): Session {
    return { ...makeSession({ id: "c1" }), ...fields };
  }

  it("derivedHasMore", () => {
    for (const [count, resident] of [
      [0, 0],
      [3, 3],
      [8, 3],
      [3, 8],
    ] as const) {
      expect(storeMock.derivedHasMore(count, resident)).toBe(store.derivedHasMore(count, resident));
    }
  });

  function turns(...closeAts: readonly (number | undefined)[]): Session["turns"] {
    const m = new Map<string, unknown>();
    for (const [i, closeAt] of closeAts.entries()) {
      m.set(`t-${String(i)}`, { closeAt });
    }
    return m as Session["turns"];
  }

  it("turnLive", () => {
    for (const s of [
      session({ turns: turns() }),
      // Liveness is the LOG: this term alone answers and `thinking` may not outrank it.
      session({ turns: turns(undefined), thinking: false }),
      session({ turns: turns(4), thinking: true }),
      // The header's own `live`, for a chat whose window is not resident.
      session({ turns: turns(4), turn_open: true }),
      // States neither input; without it this guard cannot see a drift in the third term.
      session({ turns: turns(4), provisional: true }),
    ]) {
      expect(storeMock.turnLive(s)).toBe(store.turnLive(s));
    }
  });

  it("steerIDFor", () => {
    for (const id of ["m-abc", "m-1-2"]) {
      expect(storeMock.steerIDFor(id)).toBe(store.steerIDFor(id));
    }
  });
});
