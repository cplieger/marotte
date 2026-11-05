// A file row's name wins the width: on touch, or in a list under 30rem, its size, date and mode
// go on a second line under the name, and the row stays a 44px touch row.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { FB_NO_ENTER_ATTR } from "./files-shared.js";

const META = "1.2 KB   ·   Nov 5, 2026 08:45 AM   ·   -rw-r--r--";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  host.style.cssText = "position:fixed;top:0;left:0;";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

interface Row {
  readonly row: HTMLElement;
  readonly name: HTMLElement;
  readonly meta: HTMLElement;
  readonly letter: HTMLElement | null;
}

/** One listing row in the shape `files.ts` `entryRow` renders, in a list `width` px wide. */
function mount(
  width: number,
  tier: "fine" | "coarse",
  opts?: { letter?: boolean; meta?: string },
): Row {
  document.documentElement.dataset["pointer"] = tier;
  const wrap = document.createElement("div");
  wrap.className = "fb-list-wrap";
  wrap.style.cssText = `inline-size:${String(width)}px;block-size:400px;`;
  const list = document.createElement("div");
  list.id = "fb-list";
  const row = document.createElement("div");
  row.className = "fb-row";
  row.setAttribute("role", "listitem");
  row.setAttribute(FB_NO_ENTER_ATTR, "");
  const check = document.createElement("input");
  check.type = "checkbox";
  check.className = "fb-check";
  const icon = document.createElement("span");
  icon.className = "fb-icon";
  const name = document.createElement("span");
  name.className = "fb-name fb-name-link";
  name.textContent = "docker-compose.override.yaml";
  const meta = document.createElement("span");
  meta.className = "fb-meta";
  meta.textContent = opts?.meta ?? META;
  row.append(check, icon, name);
  let letter: HTMLElement | null = null;
  if (opts?.letter === true) {
    letter = document.createElement("span");
    letter.className = "fb-git-letter git-st-m fb-git-clickable";
    letter.setAttribute("role", "button");
    letter.textContent = "M";
    row.append(letter);
  }
  row.append(meta);
  list.append(row);
  wrap.append(list);
  host.replaceChildren(wrap);
  return { row, name, meta, letter };
}

function rect(el: Element): DOMRect {
  return el.getBoundingClientRect();
}

describe("a file row", () => {
  it.each([1024, 390])("on touch at %ipx puts the metadata under the name, 44px tall", (w) => {
    const r = mount(w, "coarse");
    const name = rect(r.name);
    const meta = rect(r.meta);
    expect(meta.top, "metadata starts below the name").toBeGreaterThanOrEqual(name.bottom - 0.5);
    expect(meta.left, "metadata starts under the name").toBeCloseTo(name.left, 0);
    expect(r.name.scrollWidth, "the name is not cut").toBeLessThanOrEqual(r.name.clientWidth);
    expect(rect(r.row).height).toBeGreaterThanOrEqual(44);
    expect(getComputedStyle(r.meta).fontSize, "the metadata keeps its mouse size").toBe("11px");
  });

  it("with a mouse on a wide list keeps the metadata beside the name", () => {
    const r = mount(1024, "fine");
    const name = rect(r.name);
    const meta = rect(r.meta);
    expect(meta.top, "one line").toBeLessThan(name.bottom);
    expect(meta.left).toBeGreaterThanOrEqual(name.right);
  });

  it("with a mouse on a phone-wide list stacks too, so the name is not cut", () => {
    const r = mount(366, "fine");
    expect(rect(r.meta).top).toBeGreaterThanOrEqual(rect(r.name).bottom - 0.5);
    expect(r.name.scrollWidth).toBeLessThanOrEqual(r.name.clientWidth);
  });

  it.each([
    [366, "fine"],
    [390, "coarse"],
    [1024, "coarse"],
  ] as const)("at %ipx on %s is as tall stacked as the one-line parent row", (w, tier) => {
    const stacked = rect(mount(w, tier).row).height;
    const parent = mount(w, tier, { meta: "" });
    expect(rect(parent.meta).top, "the parent row stays one line").toBeLessThan(
      rect(parent.name).bottom,
    );
    expect(stacked).toBe(rect(parent.row).height);
  });

  it("keeps a stacked git letter's target inside the row's paint clip", () => {
    const r = mount(390, "coarse", { letter: true });
    if (r.letter === null) {
      throw new Error("no letter");
    }
    const letter = rect(r.letter);
    const reach = -parseFloat(getComputedStyle(r.letter, "::after").right);
    const row = rect(r.row);
    expect(reach, "the expander reaches past the letter's end").toBeGreaterThan(0);
    expect(letter.right + reach).toBeLessThanOrEqual(row.right - 1);
    expect(letter.top, "centred on both lines").toBeGreaterThan(rect(r.name).top);
  });
});
