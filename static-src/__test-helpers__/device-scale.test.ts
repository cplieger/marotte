// A DPR restore must leave the top page at its viewport: Playwright tests a hover or click target against the top
// page's size, so a shrunk top page fails a later file's pointer action, far from the file that shrank it.
import { describe, expect, it } from "vitest";
import { page } from "vitest/browser";

import { restoreDeviceScale, setDeviceScale } from "./device-scale.js";

describe("a DPR restore", () => {
  it("leaves the top page at its viewport across a phone-size viewport cycle", async () => {
    const viewport = { width: innerWidth, height: innerHeight };
    await setDeviceScale(1.5);
    await restoreDeviceScale();
    await page.viewport(390, 844);
    await page.viewport(viewport.width, viewport.height);
    await setDeviceScale(1.5);
    await restoreDeviceScale();
    const top = window.top;
    if (top === null) {
      throw new Error("the premise: the test runs in the tester frame");
    }
    expect({ width: top.innerWidth, height: top.innerHeight }).toEqual(viewport);
  });
});
