// A docs row is one height whatever it carries: `.docs-panel` sets the three-line tier once on the shared `.entry`
// builder, so titles sit at one offset. Real layout, since the claim is geometric.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryList, entryRow } from "./entry-row.js";
import type { EntryRowSpec } from "./entry-row.js";

/** Hardcoded: a retune has to move this table deliberately. */
const FINE_3 = 72;
const COARSE_3 = 76;

const LONG =
  "A description long enough to run past four lines at any width this list is given. ".repeat(6);

const badge = (text: string): HTMLElement => {
  const b = document.createElement("span");
  b.className = "docs-badge";
  b.textContent = text;
  return b;
};

const noop = (): void => undefined;

const SPECS: readonly EntryRowSpec[] = [
  {
    key: "spec",
    title: "Requirements — Something",
    sub: {
      kind: "lines",
      lines: [
        { text: "workspace/.kiro/specs/feature/requirements.md", mono: true },
        { text: "feature" },
      ],
    },
    open: { name: "r", onOpen: noop },
  },
  {
    key: "steering",
    title: "actions",
    badges: [badge("fileMatch"), badge("override")],
    sub: { kind: "clamp", text: LONG },
    open: { name: "actions", onOpen: noop },
  },
  {
    key: "hook",
    title: "guard",
    badges: [badge("PreToolUse")],
    sub: {
      kind: "lines",
      lines: [
        { text: "fsWrite|executeBash", mono: true },
        { text: "python3 scan.py", mono: true },
      ],
    },
    open: { name: "guard", onOpen: noop },
  },
  { key: "agent", title: "reviewer", sub: { kind: "clamp", text: "" } },
];

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;inline-size:800px;";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
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

function mountPanel(pointer: "fine" | "coarse"): HTMLElement {
  document.documentElement.setAttribute("data-pointer", pointer);
  const panel = document.createElement("div");
  panel.className = "docs-panel";
  const section = document.createElement("div");
  section.className = "docs-section";
  const label = document.createElement("div");
  label.className = "entry-section-label";
  label.textContent = "feature";
  const list = entryList();
  list.append(...SPECS.map((s) => entryRow(s)));
  section.append(label, list);
  panel.append(section);
  host.appendChild(panel);
  return panel;
}

const heights = (panel: HTMLElement): number[] =>
  [...panel.querySelectorAll<HTMLElement>(".entry")].map((r) => r.getBoundingClientRect().height);

function titleTops(panel: HTMLElement): number[] {
  return [...panel.querySelectorAll<HTMLElement>(".entry")].map((row) => {
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    return title.getBoundingClientRect().top - row.getBoundingClientRect().top;
  });
}

describe("a docs row", () => {
  it("is --row-h-3 on a fine pointer whatever it carries", () => {
    expect(heights(mountPanel("fine"))).toEqual([FINE_3, FINE_3, FINE_3, FINE_3]);
  });

  it("is --row-h-3 on a coarse pointer whatever it carries", () => {
    expect(heights(mountPanel("coarse"))).toEqual([COARSE_3, COARSE_3, COARSE_3, COARSE_3]);
  });

  it("sits its title at one offset across the four shapes", () => {
    const [first, ...rest] = titleTops(mountPanel("fine"));
    for (const top of rest) {
      expect(top).toBeCloseTo(first as number, 1);
    }
  });

  it("takes the tier from the panel, not from the row", () => {
    // Control: the same row outside the panel is on the default two-line rung.
    document.documentElement.setAttribute("data-pointer", "fine");
    const list = entryList();
    list.append(entryRow(SPECS[0] as EntryRowSpec));
    host.appendChild(list);
    expect(list.firstElementChild?.getBoundingClientRect().height).toBe(56);
  });

  it("keeps the section label on the page rung, above its own card", () => {
    const panel = mountPanel("fine");
    const label = panel.querySelector<HTMLElement>(".entry-section-label") as HTMLElement;
    const list = panel.querySelector<HTMLElement>(".list-container") as HTMLElement;
    expect(getComputedStyle(label).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(getComputedStyle(label).textTransform).toBe("uppercase");
    expect(label.getBoundingClientRect().bottom).toBeLessThanOrEqual(
      list.getBoundingClientRect().top,
    );
  });
});
