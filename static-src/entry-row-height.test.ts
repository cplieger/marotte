import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryRow, entryList, entrySkeleton } from "./entry-row.js";
import type { EntryRowSpec } from "./entry-row.js";

// One row height per list, over rows whose content disagrees, built by the real builder on the shipped stylesheet.

/** Hardcoded design numbers (3.5/3.75rem, 4.5/4.75rem at a 16px root), so a retune moves this table deliberately. */
const FINE_2 = 56;
const FINE_3 = 72;
const COARSE_2 = 60;
const COARSE_3 = 76;

/** Runs past four lines, so the clamp is engaged. */
const LONG =
  "A description long enough to run past four lines at any width this list is given. ".repeat(6);

const noop = (): void => undefined;

const badge = (text: string): HTMLElement => {
  const b = document.createElement("span");
  b.className = "docs-badge";
  b.textContent = text;
  return b;
};

const lead = (): HTMLElement => {
  const l = document.createElement("span");
  l.className = "entry-lead";
  return l;
};

const action = (): HTMLElement => {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "icon-btn entry-delete";
  b.textContent = "x";
  return b;
};

/** `.btn-small` declares its own `--btn-h` in a later slice, which the slot's floor must outrank. */
const textAction = (): HTMLElement => {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn-small";
  b.textContent = "Run";
  return b;
};

const SPECS: readonly EntryRowSpec[] = [
  { key: "a", title: "No subtitle at all" },
  { key: "b", title: "One word", sub: { kind: "line", text: "idle" } },
  { key: "c", title: "Clamped description", sub: { kind: "clamp", text: LONG } },
  {
    key: "d",
    title: "Two facts",
    sub: { kind: "lines", lines: [{ text: ".kiro/specs/x/tasks.md", mono: true }, { text: "x" }] },
  },
  {
    key: "e",
    title: "Lead and badges",
    lead: lead(),
    badges: [badge("fileMatch"), badge("override")],
  },
  {
    key: "f",
    title: "Everything at once",
    lead: lead(),
    badges: [badge("model"), badge("3 tools")],
    time: { ms: Date.now() - 4_000_000 },
    sub: { kind: "clamp", text: LONG },
    actions: [textAction(), action(), action()],
    open: { name: "Everything at once", onOpen: noop },
  },
  { key: "g", title: "Short description", sub: { kind: "clamp", text: "Short." } },
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
  host.style.top = "-9999px";
  document.documentElement.removeAttribute("data-pointer");
});

/** Skeleton rows are measured too: the swap to real rows must move nothing. */
function mountList(tier: "--row-h-2" | "--row-h-3", pointer: "fine" | "coarse"): number[] {
  document.documentElement.setAttribute("data-pointer", pointer);
  const list = entryList();
  list.style.setProperty("--row-h", `var(${tier})`);
  list.append(...SPECS.map((s) => entryRow(s)), ...entrySkeleton(2));
  host.appendChild(list);
  return [...list.children].map((r) => r.getBoundingClientRect().height);
}

describe("a two-line list", () => {
  it("gives every row --row-h-2 on a fine pointer", () => {
    expect(mountList("--row-h-2", "fine")).toEqual([
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
      FINE_2,
    ]);
  });

  it("gives every row --row-h-2 on a coarse pointer", () => {
    expect(mountList("--row-h-2", "coarse")).toEqual([
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
      COARSE_2,
    ]);
  });
});

describe("a three-line list", () => {
  it("gives every row --row-h-3 on a fine pointer", () => {
    expect(mountList("--row-h-3", "fine")).toEqual([
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
      FINE_3,
    ]);
  });

  it("gives every row --row-h-3 on a coarse pointer", () => {
    expect(mountList("--row-h-3", "coarse")).toEqual([
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
      COARSE_3,
    ]);
  });
});

describe("the row's own inset and control heights", () => {
  it("insets every row by --sp-4 and keeps its actions at the dense tier", () => {
    mountList("--row-h-2", "fine");
    const row = host.querySelector<HTMLElement>('[data-key="f"]');
    const btn = row?.querySelector<HTMLElement>(".entry-delete");
    expect(getComputedStyle(row as HTMLElement).paddingInlineStart).toBe("16px");
    expect(btn?.getBoundingClientRect().height).toBe(32);
  });

  it("grows those controls to the touch floor rather than the dense token on coarse", () => {
    mountList("--row-h-2", "coarse");
    const btn = host.querySelector<HTMLElement>('[data-key="f"] .entry-delete');
    expect(btn?.getBoundingClientRect().height).toBe(44);
  });

  it("holds a text action to the same tier as the icon beside it", () => {
    // `.btn-small` rests at 36px and comes later in the cascade, so the slot's selector leads with `.entry`.
    mountList("--row-h-2", "fine");
    const text = host.querySelector<HTMLElement>('[data-key="f"] .btn-small');
    const icon = host.querySelector<HTMLElement>('[data-key="f"] .entry-delete');
    expect(text?.getBoundingClientRect().height).toBe(32);
    expect(text?.getBoundingClientRect().height).toBe(icon?.getBoundingClientRect().height);
  });

  it("shows a long description as exactly two lines", () => {
    // The body is a flex column, so a clamp admitting four lines is shrunk and clipped while the row still measures right.
    mountList("--row-h-3", "coarse");
    expect(host.querySelector('[data-key="c"] .entry-sub-clamp')?.clientHeight).toBe(34);
  });

  it("takes the title's size from the item tier", () => {
    mountList("--row-h-2", "fine");
    const title = host.querySelector<HTMLElement>('[data-key="a"] .entry-title');
    expect(getComputedStyle(title as HTMLElement).fontSize).toBe("13px");
  });
});

function titleTop(key: string): number {
  const row = host.querySelector<HTMLElement>(`[data-key="${key}"]`) as HTMLElement;
  const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
  return title.getBoundingClientRect().top - row.getBoundingClientRect().top;
}

describe("the title column", () => {
  it("sits at one offset whether or not the row has a subtitle", () => {
    // The subtitle region reserves its line, or a lone title would centre lower than its neighbours.
    mountList("--row-h-2", "fine");
    expect(titleTop("a")).toBeCloseTo(titleTop("b"), 1);
    expect(titleTop("a")).toBeCloseTo(titleTop("e"), 1);
  });

  it("sits at one offset on a three-line list whatever fills the two-line region", () => {
    // The region is two lines whatever fills it, so the fact pair may carry no gap of its own.
    mountList("--row-h-3", "coarse");
    expect(titleTop("g")).toBeCloseTo(titleTop("c"), 1);
    expect(titleTop("d")).toBeCloseTo(titleTop("c"), 1);
  });
});

function subTop(key: string): number {
  const row = host.querySelector<HTMLElement>(`[data-key="${key}"]`) as HTMLElement;
  const sub = row.querySelector<HTMLElement>(".entry-sub, .entry-lines") as HTMLElement;
  return sub.getBoundingClientRect().top - row.getBoundingClientRect().top;
}

describe("the subtitle column", () => {
  it("sits at one offset whether or not the title line carries a badge", () => {
    // A badge is taller than the title's line box; a line that grew to hold it pushed the subtitle down.
    mountList("--row-h-3", "fine");
    expect(subTop("e")).toBeCloseTo(subTop("b"), 1);
    expect(subTop("f")).toBeCloseTo(subTop("c"), 1);
  });
});

const gitMark = (): HTMLElement => {
  const m = document.createElement("span");
  m.className = "docs-git-letter";
  m.textContent = "M";
  return m;
};

/** The ink's right edge: a growing title's box ends at the line's far end, its glyphs where the name does. */
function inkRight(node: HTMLElement): number {
  const range = document.createRange();
  range.selectNodeContents(node);
  return range.getBoundingClientRect().right;
}

const r = (row: HTMLElement): DOMRect => row.getBoundingClientRect();

describe("the name group", () => {
  function mountMarked(title: string): HTMLElement {
    document.documentElement.setAttribute("data-pointer", "fine");
    const list = entryList();
    list.style.setProperty("--row-h", "var(--row-h-3)");
    list.appendChild(
      entryRow({
        key: "m",
        title,
        mark: gitMark(),
        badges: [badge("always")],
        sub: { kind: "clamp", text: "Short." },
        open: { name: title, onOpen: noop },
      }),
    );
    host.appendChild(list);
    return host.querySelector<HTMLElement>('[data-key="m"]') as HTMLElement;
  }

  it("seats the mark one gap after a short title's last glyph", () => {
    // The git letter sits beside the name, not at the line's far end.
    const row = mountMarked("environment");
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    const mark = (row.querySelector(".docs-git-letter") as HTMLElement).getBoundingClientRect();
    const line = (row.querySelector(".entry-line") as HTMLElement).getBoundingClientRect();
    // `--sp-2`, the name group's gap.
    expect(mark.left - inkRight(title)).toBeCloseTo(8, 0);
    expect(mark.right).toBeLessThan(line.left + line.width / 2);
  });

  it("centres the badges on the row beside the actions, right of the title and description", () => {
    const row = mountMarked("environment");
    const box = r(row);
    const body = (row.querySelector(".entry-open") as HTMLElement).getBoundingClientRect();
    const badges = (row.querySelector(".entry-badges") as HTMLElement).getBoundingClientRect();
    expect(badges.left).toBeGreaterThanOrEqual(body.right);
    expect(badges.top + badges.height / 2).toBeCloseTo(box.top + box.height / 2, 0);
  });

  it("keeps the mark whole and the badges in place when the title overflows", () => {
    const row = mountMarked("a-name-long-enough-to-run-off-the-line-".repeat(6));
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    const mark = (row.querySelector(".docs-git-letter") as HTMLElement).getBoundingClientRect();
    const body = (row.querySelector(".entry-open") as HTMLElement).getBoundingClientRect();
    const badges = (row.querySelector(".entry-badges") as HTMLElement).getBoundingClientRect();
    expect(title.scrollWidth).toBeGreaterThan(title.clientWidth);
    expect(mark.width).toBe(16);
    expect(mark.left - title.getBoundingClientRect().right).toBeCloseTo(8, 0);
    expect(badges.left).toBeGreaterThanOrEqual(body.right);
    expect(badges.right).toBeCloseTo(r(row).right - 16, 0);
  });
});

const toggle = (): HTMLElement => {
  const input = document.createElement("input");
  input.type = "checkbox";
  input.className = "hook-toggle";
  const slider = document.createElement("span");
  slider.className = "toggle-slider";
  const label = document.createElement("label");
  label.className = "toggle toggle-inline";
  label.append(input, slider);
  return label;
};

describe("a switch in the actions slot", () => {
  it("is the target on both axes, and its hidden input reaches nothing beside it", () => {
    // The universal checkbox expander (61-mcp-tools.css) is centred on the hidden input at the label's corner, so on a
    // coarse pointer it reached into the row's open control.
    host.style.top = "0";
    document.documentElement.setAttribute("data-pointer", "coarse");
    const list = entryList();
    list.style.setProperty("--row-h", "var(--row-h-3)");
    list.appendChild(
      entryRow({
        key: "t",
        title: "greet",
        sub: {
          kind: "lines",
          lines: [
            { text: "", mono: true },
            { text: "echo hello", mono: true },
          ],
        },
        actions: [toggle(), action()],
        open: { name: "greet", onOpen: noop },
      }),
    );
    host.appendChild(list);
    const label = host.querySelector<HTMLElement>(".toggle-inline") as HTMLElement;
    const box = label.getBoundingClientRect();
    expect([box.width, box.height]).toEqual([44, 44]);
    const track = (label.querySelector(".toggle-slider") as HTMLElement).getBoundingClientRect();
    expect(track.left + track.width / 2).toBeCloseTo(box.left + box.width / 2, 1);
    // The input is the box.
    expect(document.elementFromPoint(box.left + 2, box.top + 2)).toBe(label.querySelector("input"));
    expect(document.elementFromPoint(box.left + 22, box.top + 22)).toBe(
      label.querySelector("input"),
    );
    const beside = document.elementFromPoint(box.left - 16, box.top + box.height / 2);
    expect(beside?.closest(".toggle")).toBeNull();
    expect(beside?.closest(".entry-open")).not.toBeNull();
  });

  it("paints the focus ring on the track, because the input it lands on is invisible", () => {
    // `.toggle` holds the input at opacity 0, so the ring must reach the painted track.
    host.style.top = "0";
    document.documentElement.setAttribute("data-pointer", "fine");
    const list = entryList();
    list.appendChild(entryRow({ key: "t", title: "greet", actions: [toggle()] }));
    host.appendChild(list);
    const input = host.querySelector<HTMLInputElement>(".hook-toggle") as HTMLInputElement;
    const track = host.querySelector<HTMLElement>(".toggle-slider") as HTMLElement;
    expect(getComputedStyle(track).outlineStyle).toBe("none");
    input.focus();
    expect(input.matches(":focus-visible")).toBe(true);
    expect(getComputedStyle(track).outlineStyle).toBe("solid");
    expect(getComputedStyle(track).outlineWidth).toBe("2px");
  });
});
