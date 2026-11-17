// A docs row fits what it carries: a card with nothing to say has no second line and is shorter, a described one is
// at most a title plus two lines. Real layout, since the claim is geometric.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryList, entryRow } from "./entry-row.js";
import type { EntryRowSpec } from "./entry-row.js";

/** Hardcoded: a 13px title and two 12px lines at a 1.3 line height, a 2px gap and 12px block padding. */
const TITLE_PLUS_TWO = 74.1;

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
  {
    key: "prompt",
    title: "Remember",
    sub: { kind: "clamp", text: "" },
    open: { name: "Remember", onOpen: noop },
  },
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

function titleTops(panel: HTMLElement): number[] {
  return [...panel.querySelectorAll<HTMLElement>(".entry")].map((row) => {
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    return title.getBoundingClientRect().top - row.getBoundingClientRect().top;
  });
}

function rowOf(panel: HTMLElement, key: string): HTMLElement {
  return panel.querySelector<HTMLElement>(`[data-key="${key}"]`) as HTMLElement;
}

describe("a docs row", () => {
  it("draws no subtitle for a prompt card with nothing to say, and is shorter than a described card", () => {
    for (const pointer of ["fine", "coarse"] as const) {
      const panel = mountPanel(pointer);
      const bare = rowOf(panel, "prompt");
      expect(bare.querySelector(".entry-sub"), pointer).toBeNull();
      expect(bare.getBoundingClientRect().height, pointer).toBeLessThan(
        rowOf(panel, "steering").getBoundingClientRect().height,
      );
      host.replaceChildren();
    }
  });

  it("is at most a title plus two lines however long its description", () => {
    for (const pointer of ["fine", "coarse"] as const) {
      const panel = mountPanel(pointer);
      for (const key of ["spec", "steering", "hook"]) {
        expect(rowOf(panel, key).getBoundingClientRect().height, `${pointer} ${key}`).toBeCloseTo(
          TITLE_PLUS_TWO,
          0,
        );
      }
      host.replaceChildren();
    }
  });

  it("sits its title at one offset across the four shapes", () => {
    const [first, ...rest] = titleTops(mountPanel("fine"));
    for (const top of rest) {
      expect(top).toBeCloseTo(first as number, 1);
    }
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
