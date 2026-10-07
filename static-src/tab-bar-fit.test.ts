// Real-layout tests (Browser Mode): fitTabBar decides label-vs-icon mode by
// measuring truncation, so these tests build a genuinely constrained bar and
// assert the class the CSS keys on.

import { afterEach, describe, expect, it, vi } from "vitest";
import { fitTabBar } from "./tab-bar-fit.js";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const ICONS = "seg-bar-icons";

function buildBar(width: string, labels: readonly string[]): HTMLElement {
  const host = document.createElement("div");
  host.style.width = width;
  const bar = document.createElement("nav");
  bar.className = "seg-bar";
  bar.style.display = "flex";
  for (const label of labels) {
    const btn = document.createElement("button");
    btn.className = "seg";
    // The production rules that make overflow show as truncation rather
    // than wrapping or growth; the stylesheet is not loaded in the runner,
    // so the load-bearing declarations are inlined.
    btn.style.flex = "1";
    btn.style.minWidth = "0";
    btn.style.display = "flex";
    btn.style.whiteSpace = "nowrap";
    const span = document.createElement("span");
    span.className = "seg-label";
    span.style.minWidth = "0";
    span.style.overflow = "hidden";
    span.textContent = label;
    btn.appendChild(span);
    bar.appendChild(btn);
  }
  host.appendChild(bar);
  document.body.appendChild(host);
  return bar;
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("fitTabBar", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("keeps labels when every label fits", () => {
    const bar = buildBar("600px", ["Changes", "Pull requests", "Sources"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(false);
  });

  it("switches the whole bar to icons when any label would truncate", () => {
    const bar = buildBar("120px", ["Steering", "Skills", "Agents", "Specs", "Hooks", "Workflows"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(true);
  });

  it("returns to labels when the bar grows wide enough", async () => {
    const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
    fitTabBar(bar);
    expect(bar.classList.contains(ICONS)).toBe(true);

    // Widen the container; the ResizeObserver re-measures. RO callbacks are
    // delivered async (before paint), so poll briefly.
    (bar.parentElement as HTMLElement).style.width = "600px";
    await vi.waitFor(
      () => {
        expect(bar.classList.contains(ICONS)).toBe(false);
      },
      { timeout: FRAME_BUDGET_MS },
    );
  });

  it("re-measures in label mode so icon mode does not latch", async () => {
    // Labels hidden in icons mode never report overflow, so the measure resets to label mode first;
    // a scoped style simulates that CSS.
    const style = document.createElement("style");
    style.textContent = `.${ICONS} .seg-label { display: none; }`;
    document.head.appendChild(style);
    try {
      const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
      fitTabBar(bar);
      expect(bar.classList.contains(ICONS)).toBe(true);

      (bar.parentElement as HTMLElement).style.width = "600px";
      await vi.waitFor(
        () => {
          expect(bar.classList.contains(ICONS)).toBe(false);
        },
        { timeout: FRAME_BUDGET_MS },
      );

      // And shrink again: labels must yield back to icons.
      (bar.parentElement as HTMLElement).style.width = "120px";
      await vi.waitFor(
        () => {
          expect(bar.classList.contains(ICONS)).toBe(true);
        },
        { timeout: FRAME_BUDGET_MS },
      );
    } finally {
      style.remove();
    }
  });

  it("stays in icon mode when an icon-mode bar shrinks further", async () => {
    // The measure resets to label mode BEFORE reading: without that, a bar
    // already in icons mode measures its hidden labels (zero width, no
    // overflow) and falls back to labels at a width where they cannot fit.
    const style = document.createElement("style");
    style.textContent = `.${ICONS} .seg-label { display: none; }`;
    document.head.appendChild(style);
    try {
      const bar = buildBar("120px", ["General", "Tools", "Permissions"]);
      fitTabBar(bar);
      expect(bar.classList.contains(ICONS)).toBe(true);

      (bar.parentElement as HTMLElement).style.width = "90px";
      // Outwait RO delivery plus the deferred rAF measure (rAF runs before RO within a frame), then assert
      // ONCE: a waitFor on an already-true class would pass at t=0.
      for (let i = 0; i < 4; i++) {
        await new Promise((r) => requestAnimationFrame(r));
      }
      expect(bar.classList.contains(ICONS)).toBe(true);
    } finally {
      style.remove();
    }
  });
});
