// Raises `vi.waitFor`'s 1000ms default failure bound suite-wide; vitest has no config option for
// it. `__test-helpers__/frame-budget.ts` owns why: deep into a run frames arrive at 1Hz. Patching
// the default, not ~90 call sites, keeps a new call correct; a call naming its own timeout is
// untouched. A FAILURE bound only: a working poll returns on its first satisfied check.
import { vi } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";

type WaitFor = typeof vi.waitFor;

const original: WaitFor = vi.waitFor.bind(vi);

vi.waitFor = ((callback: Parameters<WaitFor>[0], options?: Parameters<WaitFor>[1]) => {
  if (options === undefined) {
    return original(callback, { timeout: FRAME_BUDGET_MS });
  }
  // A bare number is the timeout; anything else already states its own.
  if (typeof options === "number") {
    return original(callback, { timeout: options });
  }
  return original(callback, options);
}) as WaitFor;
