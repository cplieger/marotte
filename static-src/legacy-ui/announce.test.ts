import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { announce, mountLiveRegions, _resetForTest } from "./announce.js";

beforeEach(() => {
  vi.useFakeTimers();
  mountLiveRegions(document.body);
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

function regions(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".uip-visually-hidden")];
}

function region(politeness: "polite" | "assertive"): HTMLElement {
  const found = document.querySelector<HTMLElement>(
    `.uip-visually-hidden[aria-live="${politeness}"]`,
  );
  expect(found).not.toBeNull();
  return found!;
}

describe("announce", () => {
  it("sets the polite region's text after the delay, clearing it first", () => {
    region("polite").textContent = "old";
    announce("hello");
    expect(region("polite").textContent).toBe("");
    vi.advanceTimersByTime(99);
    expect(region("polite").textContent).toBe("");
    vi.advanceTimersByTime(1);
    expect(region("polite").textContent).toBe("hello");
    expect(region("polite").getAttribute("aria-atomic")).toBe("true");
    expect(region("polite").getAttribute("role")).toBe("status");
  });

  it("reuses the same region across polite announcements", () => {
    const polite = region("polite");
    announce("one");
    vi.advanceTimersByTime(100);
    announce("two");
    vi.advanceTimersByTime(100);
    expect(regions()).toHaveLength(2);
    expect(region("polite")).toBe(polite);
    expect(polite.textContent).toBe("two");
  });

  it("uses the separate assertive region with role=alert", () => {
    announce("polite msg", "polite");
    announce("assertive msg", "assertive");
    vi.advanceTimersByTime(100);
    expect(region("polite").textContent).toBe("polite msg");
    expect(region("assertive").textContent).toBe("assertive msg");
    expect(region("assertive").getAttribute("role")).toBe("alert");
  });

  it("re-announces an identical message by clearing then re-setting after the delay", () => {
    announce("same");
    vi.advanceTimersByTime(100);
    expect(region("polite").textContent).toBe("same");
    announce("same");
    expect(region("polite").textContent).toBe("");
    vi.advanceTimersByTime(100);
    expect(region("polite").textContent).toBe("same");
  });

  it("cancels a pending announce so a rapid second message wins (first never lands)", () => {
    announce("first");
    vi.advanceTimersByTime(50);
    announce("second");
    vi.advanceTimersByTime(50);
    expect(region("polite").textContent).toBe("");
    vi.advanceTimersByTime(50);
    expect(region("polite").textContent).toBe("second");
  });

  it("keeps the two politeness levels independent: an assertive message never cancels a polite one", () => {
    announce("polite msg");
    vi.advanceTimersByTime(50);
    announce("assertive msg", "assertive");
    vi.advanceTimersByTime(50);
    expect(region("polite").textContent).toBe("polite msg");
  });
});

describe("announce — _resetForTest really resets", () => {
  it("cancels a pending announcement instead of leaving it armed", () => {
    announce("pending");
    expect(vi.getTimerCount()).toBe(1);

    _resetForTest();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("removes the root regions and forgets the root, so announce writes nothing until a new mount", () => {
    const first = region("polite");
    _resetForTest();
    expect(regions()).toHaveLength(0);

    announce("dropped");
    expect(regions()).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(0);

    mountLiveRegions(document.body);
    const second = region("polite");
    expect(second).not.toBe(first);
    announce("fresh");
    vi.advanceTimersByTime(100);
    expect(second.textContent).toBe("fresh");
  });
});
