// Drift guard: `tabsMock()` must stay TOTAL, or every spreading file fails without naming the
// missing export. Browser, not node: `tabs.ts` reaches `router.ts`, which needs a window.
import { describe, it, expect } from "vitest";

import { tabsMock } from "./tabs-mock.js";
import * as tabs from "../tabs.js";

describe("the tabs.js mock helper stays total", () => {
  it("names every value tabs.ts exports", () => {
    // Types are erased at runtime, so this compares the value surface an ESM link resolves.
    const real = Object.keys(tabs as Record<string, unknown>).sort();
    const mocked = Object.keys(tabsMock()).sort();
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `tabs-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing tabs.ts does not export, so a rename cannot hide behind it", () => {
    const real = Object.keys(tabs as Record<string, unknown>);
    const extra = Object.keys(tabsMock()).filter((k) => !real.includes(k));
    expect(extra, `tabs-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});
