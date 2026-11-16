import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryRow, entryList, entrySkeleton } from "./entry-row.js";
import type { EntryRowSpec } from "./entry-row.js";

// A row is its title plus the lines it carries inside a fixed block padding, floored at the tap height; built by the
// real builder on the shipped stylesheet.

/** Hardcoded design numbers: a 13px title and 12px lines at a 1.3 line height, a 2px gap and 12px block padding.
 * A retune moves this table deliberately. Touch floors the title-only row at 44px and leaves the taller ones. */
const TITLE_ONLY_FINE = 40.9;
const TITLE_ONLY_COARSE = 44;
const ONE_LINE = 58.5;
const TWO_LINES = 74.1;

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
function mountList(pointer: "fine" | "coarse"): Map<string, number> {
  document.documentElement.setAttribute("data-pointer", pointer);
  const list = entryList();
  list.append(...SPECS.map((s) => entryRow(s)), ...entrySkeleton(2));
  host.appendChild(list);
  return new Map(
    [...list.children].map((r, i) => [
      (r as HTMLElement).dataset["key"] ?? `skel${String(i - SPECS.length)}`,
      r.getBoundingClientRect().height,
    ]),
  );
}

function expectHeights(got: Map<string, number>, want: Record<string, number>): void {
  expect([...got.keys()]).toEqual(Object.keys(want));
  for (const [key, h] of Object.entries(want)) {
    expect(got.get(key), key).toBeCloseTo(h, 0);
  }
}

describe("a row fits what it carries", () => {
  it("on a fine pointer", () => {
    expectHeights(mountList("fine"), {
      a: TITLE_ONLY_FINE,
      b: ONE_LINE,
      c: TWO_LINES,
      d: TWO_LINES,
      e: TITLE_ONLY_FINE,
      f: TWO_LINES,
      g: ONE_LINE,
      skel0: ONE_LINE,
      skel1: ONE_LINE,
    });
  });

  it("on a coarse pointer, where a title-only row takes the tap floor", () => {
    expectHeights(mountList("coarse"), {
      a: TITLE_ONLY_COARSE,
      b: ONE_LINE,
      c: TWO_LINES,
      d: TWO_LINES,
      e: TITLE_ONLY_COARSE,
      f: TWO_LINES,
      g: ONE_LINE,
      skel0: ONE_LINE,
      skel1: ONE_LINE,
    });
  });
});

describe("the row's own inset and control heights", () => {
  it("insets every row by --sp-4 and keeps its actions at the dense tier", () => {
    mountList("fine");
    const row = host.querySelector<HTMLElement>('[data-key="f"]');
    const btn = row?.querySelector<HTMLElement>(".entry-delete");
    expect(getComputedStyle(row as HTMLElement).paddingInlineStart).toBe("16px");
    expect(btn?.getBoundingClientRect().height).toBe(32);
  });

  it("paints those controls at the dense token on coarse too, with a 44px target", () => {
    host.style.top = "0";
    mountList("coarse");
    const btn = host.querySelector<HTMLElement>('[data-key="f"] .entry-delete') as HTMLElement;
    const r = btn.getBoundingClientRect();
    expect(r.height).toBe(32);
    const cx = r.left + r.width / 2;
    const cy = r.top + r.height / 2;
    for (const [x, y] of [
      [cx, cy - 21],
      [cx, cy + 21],
      [cx - 21, cy],
      [cx + 21, cy],
    ] as const) {
      const hit = document.elementFromPoint(x, y);
      expect(
        btn.contains(hit),
        `${String(x - cx)},${String(y - cy)} reached ${hit?.className ?? "null"}`,
      ).toBe(true);
    }
  });

  it("holds a text action to the same tier as the icon beside it", () => {
    // `.btn-small` rests at 36px and comes later in the cascade, so the slot's selector leads with `.entry`.
    mountList("fine");
    const text = host.querySelector<HTMLElement>('[data-key="f"] .btn-small');
    const icon = host.querySelector<HTMLElement>('[data-key="f"] .entry-delete');
    expect(text?.getBoundingClientRect().height).toBe(32);
    expect(text?.getBoundingClientRect().height).toBe(icon?.getBoundingClientRect().height);
  });

  it("shows a long description as exactly two lines", () => {
    // The body is a flex column, so a clamp admitting four lines is shrunk and clipped while the row still measures right.
    mountList("coarse");
    const clamp = host.querySelector<HTMLElement>('[data-key="c"] .entry-sub-clamp');
    expect(clamp, "the fixture must render a clamped description").not.toBeNull();
    const line = Number.parseFloat(getComputedStyle(clamp as HTMLElement).lineHeight);
    expect(clamp?.clientHeight).toBe(Math.round(2 * line));
  });

  it("takes the title's size from the item tier", () => {
    mountList("fine");
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
  it("sits one block padding below the row's top whatever the row carries", () => {
    // The padding is the body's, so every title shares one offset however many lines follow it.
    mountList("fine");
    for (const key of ["a", "b", "c", "d", "e", "f", "g"]) {
      expect(titleTop(key), key).toBeCloseTo(12, 1);
    }
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
    mountList("fine");
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
    const cx = box.left + box.width / 2;
    const cy = box.top + box.height / 2;
    const track = (label.querySelector(".toggle-slider") as HTMLElement).getBoundingClientRect();
    expect(track.left + track.width / 2).toBeCloseTo(cx, 1);
    // The switch paints its own box; its target is the 44px square centred on it, every corner of
    // which reaches the label or its input.
    for (const [dx, dy] of [
      [-21, -21],
      [21, -21],
      [-21, 21],
      [21, 21],
      [0, 0],
    ] as const) {
      const hit = document.elementFromPoint(cx + dx, cy + dy);
      expect(label.contains(hit), `${String(dx)},${String(dy)}`).toBe(true);
    }
    // The row's open control ends before the target starts, and keeps its own last pixel.
    const open = host.querySelector<HTMLElement>(".entry-open")!.getBoundingClientRect();
    expect(open.right).toBeLessThanOrEqual(cx - 22 + 0.5);
    const beside = document.elementFromPoint(open.right - 1, cy);
    expect(beside?.closest(".toggle"), `beside reached ${beside?.className ?? "null"}`).toBeNull();
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
