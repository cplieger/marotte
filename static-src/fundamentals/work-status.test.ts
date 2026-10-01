// ---------------------------------------------------------------------------
// Tests for fundamentals/work-status.ts: the one status glyph table.
//
// Three properties, each the reason the module exists: one status class per
// paint and never two, the queued tint as a flag over pending only, and no
// character in the DOM (the glyph is CSS-drawn).
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { STATUS, STATUS_CLASS, QUEUED_CLASS, paintStatus } from "./work-status.js";
import type { PlanStatus } from "../types.js";

const ALL: readonly PlanStatus[] = ["pending", "in_progress", "completed"];

function paintedClasses(el: HTMLElement): string[] {
  return ALL.map((s) => STATUS[s].className).filter((c) => el.classList.contains(c));
}

describe("paintStatus", () => {
  it.each(ALL)("paints exactly one status class for %s and names it", (status) => {
    const el = document.createElement("span");
    paintStatus(el, status);
    expect(el.classList.contains(STATUS_CLASS)).toBe(true);
    expect(paintedClasses(el)).toEqual([STATUS[status].className]);
    expect(el.getAttribute("aria-label")).toBe(STATUS[status].word);
    expect(el.getAttribute("role")).toBe("img");
  });

  it("repainting swaps the status class rather than stacking a second one", () => {
    const el = document.createElement("span");
    paintStatus(el, "pending");
    paintStatus(el, "completed");
    expect(paintedClasses(el)).toEqual([STATUS.completed.className]);
    expect(el.getAttribute("aria-label")).toBe("completed");
  });

  it("toggles the queued tint on a pending glyph and announces it", () => {
    const el = document.createElement("span");
    paintStatus(el, "pending", { queued: true });
    expect(el.classList.contains(QUEUED_CLASS)).toBe(true);
    expect(el.getAttribute("aria-label")).toBe("queued");
    paintStatus(el, "pending");
    expect(el.classList.contains(QUEUED_CLASS)).toBe(false);
    expect(el.getAttribute("aria-label")).toBe("pending");
  });

  it("refuses the queued tint on a status that is not pending", () => {
    const el = document.createElement("span");
    paintStatus(el, "in_progress", { queued: true });
    expect(el.classList.contains(QUEUED_CLASS)).toBe(false);
    expect(el.getAttribute("aria-label")).toBe("in progress");
  });

  it("puts no character in the DOM", () => {
    for (const status of ALL) {
      const el = document.createElement("span");
      paintStatus(el, status, { queued: true });
      expect(el.textContent).toBe("");
      expect(el.childNodes.length).toBe(0);
    }
  });

  it("the table carries words, not glyph characters", () => {
    for (const status of ALL) {
      expect(STATUS[status].word).toMatch(/^[a-z ]+$/);
      expect(STATUS[status].className).toMatch(/^work-status-[a-z-]+$/);
    }
  });
});
