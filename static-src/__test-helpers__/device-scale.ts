// The one owner of the `setDeviceScaleFactor` command's `BrowserCommands` augmentation (declared in
// vitest.config.ts), for suites that measure layout or snapping at a device pixel ratio.
import { expect } from "vitest";
import { server } from "vitest/browser";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    setDeviceScaleFactor(dpr: number | null): Promise<void>;
  }
}

const initial = window.devicePixelRatio;

/** Set the page's device pixel ratio and assert the page sees it. Clear it with `clearDeviceScale` in `afterEach`. */
export async function setDeviceScale(dpr: number): Promise<void> {
  await server.commands.setDeviceScaleFactor(dpr);
  await expect
    .poll(() => window.devicePixelRatio, { message: "the premise: the DPR took" })
    .toBeCloseTo(dpr, 4);
}

/** Clear the override and assert the ratio is back to the one this file started with. */
export async function clearDeviceScale(): Promise<void> {
  await server.commands.setDeviceScaleFactor(null);
  await expect
    .poll(() => window.devicePixelRatio, { message: "the DPR override is cleared" })
    .toBe(initial);
}
