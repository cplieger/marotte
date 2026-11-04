import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";

import { initIconCrisp } from "./icon-crisp.js";

const SVG_NS = "http://www.w3.org/2000/svg";

const hosts: HTMLElement[] = [];

/** An `ic-ui` icon (16px on the 24-unit grid, stroke 1), which wants box phase 0.5 at DPR 1. */
function place(left: number, top: number): { host: HTMLElement; svg: SVGSVGElement } {
  const host = document.createElement("div");
  host.style.position = "absolute";
  host.style.left = `${String(left)}px`;
  host.style.top = `${String(top)}px`;
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.style.inlineSize = "16px";
  svg.style.blockSize = "16px";
  svg.style.display = "block";
  const p = document.createElementNS(SVG_NS, "path");
  p.setAttribute("d", "M6 9l6 6 6-6");
  svg.append(p);
  host.append(svg);
  document.body.append(host);
  hosts.push(host);
  return { host, svg };
}

function offTarget(svg: SVGSVGElement): number {
  const r = svg.getBoundingClientRect();
  const off = (v: number): number => Math.abs((((v % 1) + 1) % 1) - 0.5);
  return Math.max(off(r.x), off(r.y));
}

beforeAll(async () => {
  initIconCrisp();
  // The observers' first callbacks land on real frames; let them run dry before a test fakes the clock, or a pass they
  // queued on the real clock swallows the test's first one.
  await new Promise((resolve) => setTimeout(resolve, 600));
});

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
  // Runs the settle loop dry on the fake clock before it is dropped.
  vi.advanceTimersByTime(2000);
  vi.useRealTimers();
});

describe("the settle loop", () => {
  it("keeps re-asking while a finite slide runs, and snaps what it declined once the slide lands", () => {
    vi.useFakeTimers();
    const drawer = place(10.3, 20.7);
    drawer.host.style.transform = "translateX(-100.4px)";
    drawer.host.getBoundingClientRect();
    drawer.host.style.transition = "transform 10s linear";
    drawer.host.style.transform = "translateX(0)";
    const still = place(10.3, 60.7);

    initIconCrisp();
    vi.advanceTimersByTime(1000);
    expect(drawer.svg.style.translate, "declined mid-slide").toBe("");
    expect(offTarget(still.svg)).toBeLessThan(0.02);

    // A style-only move adds no node and resizes nothing, so only a re-ask can see it.
    still.host.style.left = "44.85px";
    vi.advanceTimersByTime(1000);
    expect(offTarget(still.svg), "re-asked while the slide runs").toBeLessThan(0.02);

    for (const a of drawer.host.getAnimations()) {
      a.finish();
    }
    vi.advanceTimersByTime(1000);
    expect(offTarget(drawer.svg), "snapped once the slide landed").toBeLessThan(0.02);
  });

  it("keeps re-asking through a collapse above an icon that arrived mid-collapse, and snaps it once the row is gone", async () => {
    vi.useFakeTimers();
    const list = document.createElement("div");
    list.style.position = "absolute";
    list.style.left = "10.3px";
    list.style.top = "20.7px";
    const leaving = document.createElement("div");
    leaving.style.blockSize = "48px";
    leaving.style.overflow = "hidden";
    list.append(leaving);
    document.body.append(list);
    hosts.push(list);
    const collapse = leaving.animate([{ maxHeight: "48px" }, { maxHeight: "0px" }], {
      duration: 10_000,
      fill: "forwards",
    });
    const { host, svg } = place(0, 0);
    host.style.position = "static";
    list.append(host);
    // The MutationObserver delivers on a microtask.
    await Promise.resolve();
    vi.advanceTimersByTime(1500);
    expect(svg.style.translate, "held mid-collapse").toBe("");

    // A removal adds no icon and this one resizes nothing, so only a re-ask can see the landed box.
    collapse.finish();
    leaving.remove();
    vi.advanceTimersByTime(2000);
    expect(offTarget(svg), "snapped once the collapse landed").toBeLessThan(0.02);
  });

  it("stops re-asking when only an endless animation is left, so a spinner does not make the page poll", () => {
    vi.useFakeTimers();
    const spinner = place(10.3, 20.7);
    spinner.svg.animate([{ transform: "rotate(0deg)" }, { transform: "rotate(360deg)" }], {
      duration: 900,
      iterations: Infinity,
    });
    const still = place(10.3, 60.7);

    initIconCrisp();
    vi.advanceTimersByTime(1000);
    expect(offTarget(still.svg)).toBeLessThan(0.02);
    const snapped = still.svg.style.translate;

    still.host.style.left = "44.85px";
    vi.advanceTimersByTime(2000);
    expect(still.svg.style.translate, "no pass ran after the loop converged").toBe(snapped);
    expect(offTarget(still.svg)).toBeGreaterThan(0.1);
  });
});

describe("a static ancestor scale", () => {
  /** Passes counted by the one read every snap makes per icon. */
  function countPasses(): { passes: () => number; restore: () => void } {
    const spy = vi.spyOn(SVGSVGElement.prototype, "getScreenCTM");
    return { passes: () => spy.mock.calls.length, restore: () => spy.mockRestore() };
  }

  it("stops the page re-asking, and snaps once the scale's own transform transition ends", () => {
    vi.useFakeTimers();
    // The collapsed chat-options pill holds its icons at `scale(0.4)` for as long as it stays collapsed.
    const pill = place(10.3, 20.7);
    pill.host.style.transform = "scale(0.4)";
    const still = place(10.3, 60.7);
    initIconCrisp();
    vi.advanceTimersByTime(1000);
    expect(offTarget(still.svg), "the premise: the free icon is snapped").toBeLessThan(0.02);
    const count = countPasses();
    try {
      vi.advanceTimersByTime(2000);
      const idle = count.passes();

      pill.host.style.transform = "";
      pill.host.dispatchEvent(
        new TransitionEvent("transitionend", { propertyName: "transform", bubbles: true }),
      );
      vi.advanceTimersByTime(20);
      const rearmed = count.passes();

      expect({ idle, rearmed: rearmed > 0, snapped: offTarget(pill.svg) < 0.02 }).toEqual({
        idle: 0,
        rearmed: true,
        snapped: true,
      });
    } finally {
      count.restore();
    }
  });

  it("is not re-armed by a colour transition ending on a control that holds an icon", () => {
    vi.useFakeTimers();
    const pill = place(10.3, 20.7);
    pill.host.style.transform = "scale(0.4)";
    initIconCrisp();
    vi.advanceTimersByTime(1000);
    const count = countPasses();
    try {
      pill.host.dispatchEvent(
        new TransitionEvent("transitionend", { propertyName: "color", bubbles: true }),
      );
      vi.advanceTimersByTime(1000);
      expect(count.passes()).toBe(0);
    } finally {
      count.restore();
    }
  });
});
