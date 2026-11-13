import { describe, it, expect, afterEach } from "vitest";

import { acquireRegions, keepRegions, regionsOf } from "./live-regions.js";

afterEach(() => {
  document.body.innerHTML = "";
});

function host(): HTMLElement {
  const h = document.createElement("div");
  document.body.appendChild(h);
  return h;
}

const ORDERS = [
  [0, 1, 2],
  [0, 2, 1],
  [1, 0, 2],
  [1, 2, 0],
  [2, 0, 1],
  [2, 1, 0],
] as const;

describe("acquireRegions", () => {
  it.each(ORDERS)(
    "keeps one pair until the last of three owners releases (%i, %i, %i)",
    (...order) => {
      const h = host();
      const releases = [acquireRegions(h), acquireRegions(h), acquireRegions(h)];
      const pair = regionsOf(h);
      expect(pair).toBeDefined();
      expect(h.querySelectorAll("[aria-live]")).toHaveLength(2);

      const [first, second, last] = order;
      releases[first]!();
      releases[second]!();
      expect(regionsOf(h)).toBe(pair);
      expect(h.querySelectorAll("[aria-live]")).toHaveLength(2);

      releases[last]!();
      expect(regionsOf(h)).toBeUndefined();
      expect(h.querySelectorAll("[aria-live]")).toHaveLength(0);
    },
  );

  it("drops only its own owner however often it is called", () => {
    const h = host();
    const first = acquireRegions(h);
    const second = acquireRegions(h);
    first();
    first();
    expect(regionsOf(h)).toBeDefined();
    second();
    expect(regionsOf(h)).toBeUndefined();
  });

  it("gives a new owner a fresh pair after the last release", () => {
    const h = host();
    const release = acquireRegions(h);
    const before = regionsOf(h)!;
    release();
    const again = acquireRegions(h);
    release();

    const after = regionsOf(h);
    expect(after).toBeDefined();
    expect(after!.polite).not.toBe(before.polite);
    again();
    expect(regionsOf(h)).toBeUndefined();
  });

  it("remounts a cleared pair for every owner still holding it", () => {
    const h = host();
    const first = acquireRegions(h);
    h.innerHTML = "";
    expect(regionsOf(h)).toBeUndefined();
    const second = acquireRegions(h);
    const remounted = regionsOf(h);
    expect(remounted).toBeDefined();

    second();
    expect(regionsOf(h)).toBe(remounted);
    first();
    expect(h.querySelectorAll("[aria-live]")).toHaveLength(0);
  });

  it("appends the pair as the host's last two children", () => {
    const h = host();
    h.appendChild(document.createElement("p"));
    acquireRegions(h);
    const pair = regionsOf(h)!;

    expect(h.children[1]).toBe(pair.polite);
    expect(h.children[2]).toBe(pair.assertive);
    expect(pair.polite.getAttribute("aria-live")).toBe("polite");
    expect(pair.assertive.getAttribute("aria-live")).toBe("assertive");
  });
});

describe("keepRegions", () => {
  it("keeps the pair through every other owner's release", () => {
    const h = host();
    const release = acquireRegions(h);
    keepRegions(h);
    const pair = regionsOf(h);
    release();

    expect(regionsOf(h)).toBe(pair);
    expect(h.querySelectorAll("[aria-live]")).toHaveLength(2);
  });

  it("keeps a pair still inside the host, and puts back one the app removed", () => {
    const h = host();
    keepRegions(h);
    const first = regionsOf(h);
    keepRegions(h);
    expect(regionsOf(h)).toBe(first);

    h.replaceChildren(document.createElement("p"));
    keepRegions(h);
    const back = regionsOf(h);
    expect(back).toBeDefined();
    expect(back).not.toBe(first);
    expect(h.children[1]).toBe(back!.polite);
    expect(h.children[2]).toBe(back!.assertive);
  });
});
