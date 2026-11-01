// `announce()` writes only into a live region mounted before it runs and drops the message
// otherwise, so boot mounts the root pair ahead of every initializer that could announce.

import { describe, it, expect } from "vitest";
import appSrc from "./app.ts?raw";

/** The calls `init()` makes before `marker`, comments stripped. */
function callsBefore(marker: string): string[] {
  const body = appSrc.slice(appSrc.indexOf("function init(): void {"));
  const at = body.indexOf(marker);
  expect(at, `init() must contain ${marker}`).toBeGreaterThan(-1);
  const code = body.slice(0, at).replace(/\/\/.*$/gm, "");
  return [...code.matchAll(/\b([A-Za-z_]\w*)\(/g)].map((m) => m[1] ?? "");
}

describe("boot mounts the root live regions", () => {
  it("on document.body, before everything but the pointer tier", () => {
    expect(new Set(callsBefore("mountLiveRegions(document.body);"))).toEqual(
      new Set([
        "init",
        "initPointerTier",
        "revealPointerModeToggle",
        "revealShellKeys",
        "initPointerModeToggle",
      ]),
    );
  });
});
