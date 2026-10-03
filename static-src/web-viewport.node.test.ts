import { describe, expect, it } from "vitest";
import {
  frameGeometry,
  parseStoredPick,
  resolvePick,
  scaleReadout,
  type ViewportPick,
} from "./web-viewport.js";
import type { PreviewHint } from "./wire/types.gen.js";

const phoneHint: PreviewHint = { preset: "phone", source: "meta" };
const desktopHint: PreviewHint = { preset: "desktop", source: "meta" };
const width1280: PreviewHint = { width: 1280, source: "meta" };
const width1024: PreviewHint = { width: 1024, source: "viewport" };
const tablet: ViewportPick = { mode: "tablet", fit: true };

describe("resolvePick", () => {
  it("prefers this device's stored pick over the page's hint", () => {
    expect(resolvePick(tablet, phoneHint, false)).toEqual({
      mode: "tablet",
      width: 820,
      fit: true,
    });
  });

  it("follows a preset hint when nothing is stored", () => {
    expect(resolvePick(undefined, phoneHint, false)).toEqual({
      mode: "phone",
      width: 390,
      fit: true,
    });
  });

  it("turns a numeric hint into the page mode at that width", () => {
    expect(resolvePick(undefined, width1280, false)).toEqual({
      mode: "page",
      width: 1280,
      fit: true,
    });
  });

  it("falls back to fill with neither", () => {
    expect(resolvePick(undefined, undefined, false)).toEqual({
      mode: "fill",
      width: null,
      fit: true,
    });
  });

  it("forces fill on a phone-shaped screen whatever is stored", () => {
    expect(resolvePick(tablet, desktopHint, true)).toEqual({
      mode: "fill",
      width: null,
      fit: true,
    });
  });

  it("drops a stored page pick that has no numeric hint to size it", () => {
    const page: ViewportPick = { mode: "page", fit: false };
    expect(resolvePick(page, phoneHint, false)).toEqual({ mode: "phone", width: 390, fit: false });
  });

  it("moves with the hint when nothing is stored", () => {
    expect(resolvePick(undefined, desktopHint, false).mode).toBe("desktop");
    expect(resolvePick(undefined, width1024, false).width).toBe(1024);
  });

  it("keeps a stored pick when the hint changes", () => {
    expect(resolvePick(tablet, desktopHint, false).mode).toBe("tablet");
    expect(resolvePick(tablet, width1280, false).mode).toBe("tablet");
  });
});

describe("frameGeometry", () => {
  it("fills the stage in fill mode", () => {
    expect(frameGeometry(800, 600, null, true)).toEqual({
      frameW: 800,
      frameH: 600,
      boxW: 800,
      boxH: 600,
      scale: 1,
      overflows: false,
    });
  });

  it("shows a width that fits at 100%", () => {
    expect(frameGeometry(800, 600, 390, true)).toEqual({
      frameW: 390,
      frameH: 600,
      boxW: 390,
      boxH: 600,
      scale: 1,
      overflows: false,
    });
  });

  it("scales a too-wide width down to the stage", () => {
    const g = frameGeometry(720, 600, 1440, true);
    expect(g.scale).toBe(0.5);
    expect(g.frameW).toBe(1440);
    expect(g.frameH).toBe(1200);
    expect(g.boxW).toBe(720);
    expect(g.boxH).toBe(600);
    expect(g.overflows).toBe(false);
  });

  it("never scales below a tenth", () => {
    expect(frameGeometry(50, 600, 1440, true).scale).toBe(0.1);
  });

  it("lets the stage scroll at 100% when fit is off", () => {
    expect(frameGeometry(720, 600, 1440, false)).toEqual({
      frameW: 1440,
      frameH: 600,
      boxW: 1440,
      boxH: 600,
      scale: 1,
      overflows: true,
    });
  });
});

describe("parseStoredPick", () => {
  it("accepts a valid pick", () => {
    expect(parseStoredPick({ mode: "desktop", fit: false })).toEqual({
      mode: "desktop",
      fit: false,
    });
  });

  it("rejects an unknown mode", () => {
    expect(parseStoredPick({ mode: "watch", fit: true })).toBeUndefined();
  });

  it("rejects a non-boolean fit", () => {
    expect(parseStoredPick({ mode: "fill", fit: "yes" })).toBeUndefined();
  });

  it("rejects a non-object", () => {
    expect(parseStoredPick("fill")).toBeUndefined();
  });
});

describe("scaleReadout", () => {
  it("rounds to a whole percent", () => {
    expect(scaleReadout(0.5)).toBe("50%");
    expect(scaleReadout(0.4567)).toBe("46%");
    expect(scaleReadout(1)).toBe("100%");
  });
});
