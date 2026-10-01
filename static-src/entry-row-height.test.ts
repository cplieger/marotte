import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { entryRow, entryList, entrySkeleton } from "./entry-row.js";
import type { EntryRowSpec } from "./entry-row.js";

// ---------------------------------------------------------------------------
// ONE ROW HEIGHT PER LIST, measured over rows whose content deliberately
// disagrees.
//
// The rows are built through the REAL builder against the SHIPPED stylesheet, so
// the fixture cannot drift from either: a hand-written row would keep passing
// after the builder stopped emitting the class the height rule keys on.
//
// The two tiers matter as much as the two rungs: the pages this replaces
// measured 36px to 124px, and every one of those heights was identical on a
// finger and on a mouse because the row took its size from its font.
// ---------------------------------------------------------------------------

/** The px each tier token resolves to, hardcoded rather than read back: these
 *  are the design's own numbers (3.5/3.75rem and 4.5/4.75rem at the app's 16px
 *  root), so a retune has to move this table deliberately. */
const FINE_2 = 56;
const FINE_3 = 72;
const COARSE_2 = 60;
const COARSE_3 = 76;

/** Long enough to run past FOUR lines at this list's width, so the clamp is
 *  actually engaged: a description that wraps to two lines on its own measures
 *  the same with the clamp deleted. */
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

/** A TEXT action, the Workflows row's Schedule and Run: `.btn-small` declares
 *  its own `--btn-h` resting height in a later slice, which is the control the
 *  slot's floor has to outrank. */
const textAction = (): HTMLElement => {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "btn-small";
  b.textContent = "Run";
  return b;
};

/** Seven rows that share nothing but their list: no subtitle, a one-word one, a
 *  two-line clamp, a pair of fact lines, a lead and badges, a full row, and a
 *  clamp holding one word. */
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

/** A list at the given tier, holding the seven rows plus two skeleton rows — the
 *  skeletons are in the same measurement because the whole point of them is that
 *  the swap to real rows moves nothing. */
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
    // `.btn-small` rests at `--btn-h` (36px) everywhere else in the app and its
    // rule comes later in the cascade; measured before the slot's selector led
    // with `.entry`, Schedule and Run sat at 36px beside 32px icons.
    mountList("--row-h-2", "fine");
    const text = host.querySelector<HTMLElement>('[data-key="f"] .btn-small');
    const icon = host.querySelector<HTMLElement>('[data-key="f"] .entry-delete');
    expect(text?.getBoundingClientRect().height).toBe(32);
    expect(text?.getBoundingClientRect().height).toBe(icon?.getBoundingClientRect().height);
  });

  it("shows a long description as exactly two lines", () => {
    // The height cases above cannot see this: the body is a flex column, so a
    // clamp that admits four lines is SHRUNK back to the space the fixed row
    // leaves and the row still measures 76px — the extra line is simply clipped,
    // and the reader loses a line of the description with no other signal. 34px
    // is two lines of 13px ink at `--lh-ui` (33.8, rounded by clientHeight);
    // coarse is the tier where the ink is biggest against the row.
    mountList("--row-h-3", "coarse");
    expect(host.querySelector('[data-key="c"] .entry-sub-clamp')?.clientHeight).toBe(34);
  });

  it("takes the title's size from the item tier", () => {
    mountList("--row-h-2", "fine");
    const title = host.querySelector<HTMLElement>('[data-key="a"] .entry-title');
    expect(getComputedStyle(title as HTMLElement).fontSize).toBe("13px");
  });
});

/** The title's offset from its row's top, the number a reader's eye scans down. */
function titleTop(key: string): number {
  const row = host.querySelector<HTMLElement>(`[data-key="${key}"]`) as HTMLElement;
  const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
  return title.getBoundingClientRect().top - row.getBoundingClientRect().top;
}

describe("the title column", () => {
  it("sits at one offset whether or not the row has a subtitle", () => {
    // The body centres in the row, so a subtitle region that shrank to its
    // content would centre a lone title instead: measured 19.5px against the
    // 10.8px of its neighbours before the region reserved its line.
    mountList("--row-h-2", "fine");
    expect(titleTop("a")).toBeCloseTo(titleTop("b"), 1);
    expect(titleTop("a")).toBeCloseTo(titleTop("e"), 1);
  });

  it("sits at one offset on a three-line list whatever fills the two-line region", () => {
    // "c" is a two-line clamp, "g" a clamp holding one word and "d" a pair of
    // fact lines: the region is two lines tall whatever fills it, so the pair
    // may carry no gap of its own — with one it measured 2px taller and lifted
    // its title 1px off the column.
    mountList("--row-h-3", "coarse");
    expect(titleTop("g")).toBeCloseTo(titleTop("c"), 1);
    expect(titleTop("d")).toBeCloseTo(titleTop("c"), 1);
  });
});

/** The subtitle region's offset from its row's top. */
function subTop(key: string): number {
  const row = host.querySelector<HTMLElement>(`[data-key="${key}"]`) as HTMLElement;
  const sub = row.querySelector<HTMLElement>(".entry-sub, .entry-lines") as HTMLElement;
  return sub.getBoundingClientRect().top - row.getBoundingClientRect().top;
}

describe("the subtitle column", () => {
  it("sits at one offset whether or not the title line carries a badge", () => {
    // A badge is taller than the title's line box (19px against 16.9px on the
    // fine tier), and a line that grew to hold it pushed the subtitle 1.07px
    // down on every badged row while the centred title stayed put — so the
    // descriptions on a list drifted between neighbours by one pixel.
    mountList("--row-h-3", "fine");
    expect(subTop("e")).toBeCloseTo(subTop("b"), 1);
    expect(subTop("f")).toBeCloseTo(subTop("c"), 1);
  });
});

/** The git status letter as docs.ts builds it: a 16px chip beside the name. */
const gitMark = (): HTMLElement => {
  const m = document.createElement("span");
  m.className = "docs-git-letter";
  m.textContent = "M";
  return m;
};

/** The right edge of an element's INK rather than its box: a title that grows to
 *  fill its line has a box ending at the far end while its glyphs end where the
 *  name does, and the mark's seat is measured against the glyphs. */
function inkRight(node: HTMLElement): number {
  const range = document.createRange();
  range.selectNodeContents(node);
  return range.getBoundingClientRect().right;
}

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

  it("seats the mark one gap after a short title's last glyph, badges on the trailing edge", () => {
    // The design keeps the git letter BESIDE the name. With the title as the
    // line's grower the mark measured at the far end of the line, beside the
    // badges, 700px from the name it belongs to.
    const row = mountMarked("environment");
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    const mark = (row.querySelector(".docs-git-letter") as HTMLElement).getBoundingClientRect();
    const line = (row.querySelector(".entry-line") as HTMLElement).getBoundingClientRect();
    const badges = (row.querySelector(".entry-badges") as HTMLElement).getBoundingClientRect();
    // `--sp-2`, the name group's gap.
    expect(mark.left - inkRight(title)).toBeCloseTo(8, 0);
    expect(mark.right).toBeLessThan(line.left + line.width / 2);
    expect(badges.right).toBeCloseTo(line.right, 0);
  });

  it("keeps the mark whole and the badges in place when the title overflows", () => {
    const row = mountMarked("a-name-long-enough-to-run-off-the-line-".repeat(6));
    const title = row.querySelector<HTMLElement>(".entry-title") as HTMLElement;
    const mark = (row.querySelector(".docs-git-letter") as HTMLElement).getBoundingClientRect();
    const line = (row.querySelector(".entry-line") as HTMLElement).getBoundingClientRect();
    const badges = (row.querySelector(".entry-badges") as HTMLElement).getBoundingClientRect();
    // The title is what yields: ellipsised, with the 16px chip whole beside it.
    expect(title.scrollWidth).toBeGreaterThan(title.clientWidth);
    expect(mark.width).toBe(16);
    expect(mark.left - title.getBoundingClientRect().right).toBeCloseTo(8, 0);
    expect(badges.left - mark.right).toBeCloseTo(8, 0);
    expect(badges.right).toBeCloseTo(line.right, 0);
  });
});

/** A hook's enable switch as docs.ts builds it: a label around a hidden checkbox
 *  and the painted track. */
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
    // The universal checkbox expander (61-mcp-tools.css) is centred on the hidden
    // input, which sits at the label's corner, so on a coarse pointer it reached
    // 10px past the slot's gap into the row's open control: a tap on the row's
    // trailing edge flipped the hook. The label is the target instead, floored
    // on both axes with the track centred in it.
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
    // The input answers at the box's corner as well as its centre: it IS the box.
    expect(document.elementFromPoint(box.left + 2, box.top + 2)).toBe(label.querySelector("input"));
    expect(document.elementFromPoint(box.left + 22, box.top + 22)).toBe(
      label.querySelector("input"),
    );
    const beside = document.elementFromPoint(box.left - 16, box.top + box.height / 2);
    expect(beside?.closest(".toggle")).toBeNull();
    expect(beside?.closest(".entry-open")).not.toBeNull();
  });

  it("paints the focus ring on the track, because the input it lands on is invisible", () => {
    // The universal ring reaches the input, which `.toggle` holds at opacity 0, so
    // a keyboard user tabbing onto a switch saw nothing.
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
