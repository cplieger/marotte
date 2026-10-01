// Real-layout CSS guards for the exec view's disclosures and the run card's
// clamped step output.
//
// Every fact here was reported as a defect and measured before it was fixed, and
// each one is invisible to the type checker, the linter and to a source-reading
// test: a phantom indent, a composed rotation, a missing glyph and a clamp that
// clips inside its own padding are all GEOMETRY. `mountAppCSS` assembles the
// sheet in `css/MANIFEST` order the way `cmd/bundle` concatenates it, because
// equal-specificity ties in this app are decided by that order rather than by
// the selectors, and the browser project computes real boxes.
//
// Markup is hand-built to mirror the builders (`exec-view/tree.ts` `buildRow`,
// `exec-view/page.ts` `renderInputs`, `fundamentals/run-card.ts`'s step row)
// rather than driven through them: the subject is the stylesheet, and importing
// the builders drags `chat.ts` and the run store in behind them for facts they
// do not decide.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { chevronEl } from "./chevron.js";
import { attachClamp, releaseClampsIn } from "./clamp-text.js";
import execPageSrc from "./exec-view/page.ts?raw";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // The panes are flex children of a sized page in production; a definite width
  // here is what makes a clamped line box have somewhere to wrap.
  host.style.inlineSize = "480px";
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

// The clamp cases below attach a real `attachClamp`, which registers the shared
// `ResizeObserver`; releasing per case keeps one case's observation out of the next
// one's layout. A no-op for every other case in this file.
afterEach(() => {
  releaseClampsIn(host);
});

function mount(node: HTMLElement): HTMLElement {
  host.replaceChildren(node);
  return node;
}

function css(el: Element, prop: string): string {
  return getComputedStyle(el).getPropertyValue(prop);
}

/** One `.ev-row` as `buildRow` assembles it: the twist wrapper carrying the
 *  shared chevron, the state mark, the kind slot, the text and the duration.
 *  `kids` gives the row children, which is what decides whether it is a leaf. */
function evRow(opts: { depth: number; kids: boolean; expanded?: boolean }): HTMLElement {
  const chevron = document.createElement("span");
  chevron.className = "ev-twist";
  chevron.setAttribute("aria-hidden", "true");
  chevron.appendChild(chevronEl());

  const glyph = document.createElement("span");
  glyph.className = "ev-state";
  const kindSlot = document.createElement("span");
  kindSlot.className = "ev-kind";
  kindSlot.hidden = true;
  const text = document.createElement("span");
  text.className = "ev-text";
  const label = document.createElement("span");
  label.className = "ev-label";
  label.textContent = "review_gpt";
  text.appendChild(label);
  const dur = document.createElement("span");
  dur.className = "ev-dur";

  const main = document.createElement("div");
  main.className = "ev-row-main";
  main.append(chevron, glyph, kindSlot, text, dur);

  const row = document.createElement("div");
  row.className = "ev-row";
  row.setAttribute("role", "treeitem");
  row.dataset["state"] = "running";
  row.dataset["kind"] = "step";
  row.style.setProperty("--ev-depth", String(opts.depth));
  row.appendChild(main);

  // `paint` hides the twist and removes `aria-expanded` on a leaf, and calls
  // `applyCollapse` (which writes both) on a row with children.
  if (opts.kids) {
    chevron.hidden = false;
    const collapsed = opts.expanded !== true;
    row.classList.toggle("ev-collapsed", collapsed);
    row.setAttribute("aria-expanded", String(!collapsed));
    const kids = document.createElement("div");
    kids.className = "ev-kids";
    kids.setAttribute("role", "group");
    kids.hidden = collapsed;
    row.appendChild(kids);
  } else {
    chevron.hidden = true;
    row.removeAttribute("aria-expanded");
  }
  return row;
}

/** The tree pane wrapper, so the row's own rules apply in the box they ship in. */
function evTree(...rows: HTMLElement[]): HTMLElement {
  const tree = document.createElement("div");
  tree.className = "ev-tree";
  tree.setAttribute("role", "tree");
  tree.append(...rows);
  const pane = document.createElement("div");
  pane.className = "ev-pane ev-pane-tree";
  pane.appendChild(tree);
  return pane;
}

describe("a tree row with no disclosure keeps no phantom indent", () => {
  it("removes the hidden twist from layout rather than emptying it", () => {
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const twist = row.querySelector<HTMLElement>(".ev-twist")!;
    // `visibility: hidden` was the shipped answer and it cannot collapse the
    // row's own flex `gap`, which is half of the 20px.
    expect(css(twist, "display")).toBe("none");
    expect(css(twist, "visibility")).not.toBe("hidden");
    expect(twist.getBoundingClientRect().width).toBe(0);
  });

  it("puts the activity mark on the row's own content edge", () => {
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const main = row.querySelector<HTMLElement>(".ev-row-main")!;
    const glyph = row.querySelector<HTMLElement>(".ev-state")!;
    // The content edge, not the border box: the row's leading padding IS its
    // depth indent and is not the defect. Measured at 20px of gap before the
    // fix (a 1rem twist box plus the row's 0.25rem gap). The border term is not
    // padding for the arithmetic's sake — `70-selection.css` reserves a 1px
    // edge on `.ev-row-main` so the selected border is a colour change rather
    // than a layout shift, and it sits outside the padding box.
    const contentEdge =
      main.getBoundingClientRect().x +
      Number.parseFloat(css(main, "border-left-width")) +
      Number.parseFloat(css(main, "padding-left"));
    expect(glyph.getBoundingClientRect().x - contentEdge).toBeCloseTo(0, 1);
  });

  it("still indents a row that HAS a disclosure by its twist", () => {
    // The other direction: collapsing the empty box must not collapse the real
    // one, or a container's own glyph would sit where its children's do.
    const leaf = evRow({ depth: 0, kids: false });
    const container = evRow({ depth: 0, kids: true });
    mount(evTree(container, leaf));
    const cx = container.querySelector<HTMLElement>(".ev-state")!.getBoundingClientRect().x;
    const lx = leaf.querySelector<HTMLElement>(".ev-state")!.getBoundingClientRect().x;
    expect(cx).toBeGreaterThan(lx);
  });
});

describe("the tree's disclosure arrow follows the app's direction convention", () => {
  it("points DOWN when open and RIGHT when closed, with no wrapper transform", () => {
    for (const expanded of [true, false]) {
      const row = evRow({ depth: 0, kids: true, expanded });
      mount(evTree(row));
      const twist = row.querySelector<HTMLElement>(".ev-twist")!;
      const glyph = twist.querySelector<HTMLElement>(".disclosure-chevron")!;
      const state = expanded ? "open" : "closed";

      // The wrapper's own `rotate(-90deg)` is what COMPOSED with the chevron's
      // closed -90deg to make -180deg (UP when closed, a bare RIGHT when open).
      expect(css(twist, "transform"), `${state}: wrapper`).toBe("none");
      expect(css(glyph, "--chev-turn").trim(), `${state}: turn`).toBe(expanded ? "0deg" : "-90deg");
      expect(css(glyph, "transform"), `${state}: glyph`).toBe(
        expanded ? "matrix(1, 0, 0, 1, 0, 0)" : "matrix(0, -1, 1, 0, 0, 0)",
      );
    }
  });

  // DELETED with the shape it pinned: "flips only the row that owns the chevron,
  // never its subtree" hand-built an expanded top-level container holding a
  // COLLAPSED one, and `tree.ts` cannot produce that any more — only a top-level
  // container folds, so a nested row has no chevron in layout and no
  // `aria-expanded` to bleed from. The child combinators it defended are still in
  // `31-exec-view.css` and still correct; there is simply nothing left that a
  // descendant selector could visibly repaint.
});

describe("a row inside the group box is a band, not a pill", () => {
  // Reported: a row on the group box's dark fill took a ROUNDED hover and
  // selection fill while sitting mid-list. A row spans the box's whole inner width
  // (`.ev-kids` adds no inline padding), so the corners belong to the box and a
  // radius on the row reads as a floating pill rather than a band in a list.
  it("squares the header and every row inside the box, and leaves the box its corners", () => {
    const box = evRow({ depth: 0, kids: true, expanded: true });
    box.classList.add("ev-group");
    // A direct child of a box indents by ZERO (`tree.ts` writes `--ev-depth` 0 for
    // depth 0 AND depth 1), which is what this harness's `depth` models.
    const child = evRow({ depth: 0, kids: false });
    box.querySelector<HTMLElement>(":scope > .ev-kids")!.appendChild(child);
    mount(evTree(box));

    expect(Number.parseFloat(css(box, "border-top-left-radius"))).toBeGreaterThan(0);
    const mains: readonly [string, HTMLElement][] = [
      ["header", box.querySelector<HTMLElement>(":scope > .ev-row-main")!],
      ["child", child.querySelector<HTMLElement>(".ev-row-main")!],
    ];
    for (const [name, main] of mains) {
      for (const corner of ["top-left", "top-right", "bottom-left", "bottom-right"]) {
        expect(css(main, `border-${corner}-radius`), `${name}: ${corner}`).toBe("0px");
      }
    }
  });

  it("leaves an UN-BOXED top-level row its own radius", () => {
    // The other direction, and the reason the fix is scoped to the box: outside one,
    // each row is its own box in a gapped column, so rounding it is correct.
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const main = row.querySelector<HTMLElement>(".ev-row-main")!;
    expect(Number.parseFloat(css(main, "border-top-left-radius"))).toBeGreaterThan(0);
  });

  // Reported: the box's dark fill continued past the last selectable row. It was
  // `padding-block-end: var(--sp-1)` on `.ev-kids` — 4px measured — which reads as a
  // list that carries on after its last item. Flush is also what gives that row its
  // rounded bottom corners, since the box clips them.
  it("runs the last row to the box's inner bottom edge, and clips its corners there", () => {
    const box = evRow({ depth: 0, kids: true, expanded: true });
    box.classList.add("ev-group");
    const kids = box.querySelector<HTMLElement>(":scope > .ev-kids")!;
    for (const _ of [0, 1]) {
      kids.appendChild(evRow({ depth: 0, kids: false }));
    }
    mount(evTree(box));

    expect(css(kids, "padding-block-end")).toBe("0px");
    expect(css(kids, "padding-block-start")).toBe("0px");

    const last = kids.lastElementChild!.querySelector<HTMLElement>(".ev-row-main")!;
    const innerBottom =
      box.getBoundingClientRect().bottom - Number.parseFloat(css(box, "border-bottom-width"));
    expect(last.getBoundingClientRect().bottom).toBeCloseTo(innerBottom, 1);

    // The corner is the BOX's, taken through the clip rather than restated on the
    // row: `web.md`'s flush-inside-a-clipping-parent exemption, and the only shape
    // that works at any nesting depth, since the visually-last row can be the last
    // descendant of a nested container.
    expect(css(box, "overflow")).toBe("hidden");
    expect(Number.parseFloat(css(box, "border-bottom-left-radius"))).toBeGreaterThan(0);
  });

  // Reported as a "1 line black gap between steps". `.ev-kids` carried
  // `gap: 0.0625rem`; a row paints nothing at rest, so that gap showed the BOX's own
  // fill — the darkest rung — which is invisible until a row takes the hover wash or
  // the selected fill and then reads as a line cut across the list.
  it("runs adjacent rows flush, so no fill shows between them", () => {
    const box = evRow({ depth: 0, kids: true, expanded: true });
    box.classList.add("ev-group");
    const kids = box.querySelector<HTMLElement>(":scope > .ev-kids")!;
    const children = [evRow({ depth: 0, kids: false }), evRow({ depth: 0, kids: false })];
    for (const child of children) {
      kids.appendChild(child);
    }
    // The state the gap was visible in: one row carrying a fill of its own.
    children[0]!.classList.add("ev-selected");
    mount(evTree(box));

    const [first, second] = children.map((c) => c.querySelector<HTMLElement>(".ev-row-main")!);
    expect(second!.getBoundingClientRect().top).toBeCloseTo(
      first!.getBoundingClientRect().bottom,
      1,
    );
  });
});

describe("the run's task instructions read as a card", () => {
  function inputs(hidden: boolean): HTMLElement {
    const dl = document.createElement("dl");
    dl.className = "ev-inputs";
    dl.hidden = hidden;
    const k = document.createElement("dt");
    k.className = "ev-in-k";
    k.textContent = "task";
    const v = document.createElement("dd");
    v.className = "ev-in-v";
    v.textContent = "converge the chevrons";
    dl.append(k, v);
    return dl;
  }

  it("takes the same fill, radius and padding as the timeline card", () => {
    // Read off `.ev-tl` rather than hardcoded, so the claim is that the two are
    // the SAME card and not that either is a given colour.
    const tl = document.createElement("div");
    tl.className = "ev-tl";
    const reference = mount(tl);
    const want = {
      bg: css(reference, "background-color"),
      radius: css(reference, "border-top-left-radius"),
      pad: css(reference, "padding-top"),
    };
    expect(want.bg).not.toBe("rgba(0, 0, 0, 0)");

    const box = mount(inputs(false));
    expect(css(box, "background-color")).toBe(want.bg);
    expect(css(box, "border-top-left-radius")).toBe(want.radius);
    expect(css(box, "padding-top")).toBe(want.pad);
  });

  it("disappears entirely when hidden, rather than becoming an empty card", () => {
    // There is no author `[hidden]` rule in this tree, so the UA sheet's
    // `[hidden] { display: none }` LOSES the specificity tie to the bare class
    // and a hidden `.ev-inputs` computed `display: grid` at 17px tall.
    const box = mount(inputs(true));
    expect(css(box, "display")).toBe("none");
    expect(box.getBoundingClientRect().height).toBe(0);
  });
});

// The page has TWO clamps — the header's instructions and a result box's report —
// and one opener skin between them. `run-page-layout.test.ts` asserts the shared
// selector list, which is what makes a fork unrepresentable; this is the other
// direction, that what the reader sees is in fact one control twice.
describe("both clamp openers wear one skin", () => {
  it("resolves to the same ink, size and underline", () => {
    const row = document.createElement("div");
    const instructions = document.createElement("button");
    instructions.type = "button";
    instructions.className = "ev-in-more";
    instructions.textContent = "Show more";
    const report = document.createElement("button");
    report.type = "button";
    report.className = "ev-r-more";
    report.textContent = "Show more";
    row.append(instructions, report);
    mount(row);

    // The premise: the skin really is in force, rather than both reading as the UA
    // button default — a bare `<button>` is not underlined and is not this ink.
    expect(css(instructions, "text-decoration-line")).toBe("underline");
    expect(css(instructions, "background-color")).toBe("rgba(0, 0, 0, 0)");

    for (const prop of ["color", "font-size", "text-decoration-line", "border-top-width"]) {
      expect(css(report, prop), prop).toBe(css(instructions, prop));
    }
  });
});

describe("a run step's clamped output does not paint past its own clamp", () => {
  function step(text: string): HTMLElement {
    const cap = document.createElement("div");
    cap.className = "run-step-capture";
    cap.textContent = text;

    const row = document.createElement("div");
    row.className = "run-step";
    row.dataset["status"] = "completed";
    // An ANCHOR, as the builder makes it: the row is a door into `/run/{id}`.
    const head = document.createElement("a");
    head.className = "run-step-head";
    head.href = "/run/wf_1";
    row.append(head, cap);

    const card = document.createElement("div");
    card.className = "run-card";
    const steps = document.createElement("div");
    steps.className = "run-steps";
    steps.appendChild(row);
    card.appendChild(steps);
    return card;
  }

  it("keeps a navigator row's own ink rather than taking the reset's link ink", () => {
    // The reset layer carries `a { color: var(--c-link) }`, and this app spends link
    // ink on TEXT controls; a row is a navigator, like `.tab` and `.ev-row-main`. So
    // the row has to opt out, or every step in the card reads as a hyperlink.
    const card = mount(step("captured"));
    const head = card.querySelector<HTMLElement>(".run-step-head")!;
    expect(css(head, "text-decoration-line")).toBe("none");

    // The oracle is the LIVE token, not a literal: `--c-link` is theme-split, and a
    // hard-coded swatch would pass on one theme and lie on the other.
    const probe = document.createElement("a");
    probe.href = "/run/wf_1";
    probe.textContent = "link";
    card.appendChild(probe);
    expect(css(head, "color")).not.toBe(css(probe, "color"));
    // …and it is the inherited ink, which is what `color: inherit` means.
    expect(css(head, "color")).toBe(css(card, "color"));
  });

  const overflowing = Array.from(
    { length: 40 },
    (_, i) => `captured output line fragment number ${String(i)}`,
  ).join(" ");

  it("moves the trailing space to a margin, outside the clip region", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // `overflow: hidden` clips at the PADDING box, so bottom padding on a
    // `-webkit-line-clamp` box reveals the clipped line inside that band.
    expect(css(cap, "padding-bottom")).toBe("0px");
    expect(Number.parseFloat(css(cap, "margin-bottom"))).toBeGreaterThan(0);
    // The clamp itself is untouched.
    expect(css(cap, "-webkit-line-clamp")).toBe("2");
    expect(css(cap, "overflow-y")).toBe("hidden");
  });

  /** Every line box the capture's text produced, clipped ones included. */
  function lineRects(cap: HTMLElement): DOMRect[] {
    const range = document.createRange();
    range.selectNodeContents(cap);
    return [...range.getClientRects()];
  }

  it("paints no partial line under the ellipsis", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // The clip edge IS the element's own bottom, because `overflow: hidden`
    // clips at the padding box.
    //
    // "No line below the edge" is the WRONG claim, and asserting it fails on the
    // fix: `getClientRects()` reports every line the text produced, and leaving
    // lines below the edge is the clamp's whole job. The defect is a line that
    // STRADDLES the edge — part painted, part cut. With block-end padding the
    // third line began 8px above the edge and 7px of it rendered, directly under
    // the ellipsis; with the padding at 0 that line begins exactly ON the edge,
    // so none of it paints.
    const clip = cap.getBoundingClientRect().bottom;
    const rects = lineRects(cap);
    // The text really does overflow, or this asserts nothing.
    expect(rects.length).toBeGreaterThan(2);
    const straddling = rects
      .filter((r) => r.top < clip - 0.5 && r.bottom > clip + 0.5)
      .map((r) => `${r.top - clip}..${r.bottom - clip}`);
    expect(straddling).toEqual([]);
  });

  it("keeps the visible gap the padding used to give it", () => {
    // The fix must not tighten the row, and the plan's claim is that the visible
    // gap is IDENTICAL either way — not that it is any particular number. So the
    // oracle is a second card carrying the pre-fix declaration inline, rather
    // than arithmetic over the box model, which would only restate it.
    // `.run-step` is `overflow: hidden`, so it establishes a BFC and the margin
    // cannot collapse out of the row.
    const visibleGap = (card: HTMLElement): number => {
      const row = card.querySelector<HTMLElement>(".run-step")!;
      const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
      const clip = cap.getBoundingClientRect().bottom;
      // A line box is not one rect: `white-space: pre-wrap` plus
      // `overflow-wrap: anywhere` splits a line into a rect per text fragment
      // (measured: 4 rects for the 2 visible lines), so take the lowest visible
      // EDGE rather than counting rects.
      const shown = lineRects(cap)
        .map((r) => r.bottom)
        .filter((b) => b <= clip + 0.5);
      expect(shown.length).toBeGreaterThan(0);
      return row.getBoundingClientRect().bottom - Math.max(...shown);
    };

    const fixed = visibleGap(mount(step(overflowing)));

    const legacy = step(overflowing);
    const legacyCap = legacy.querySelector<HTMLElement>(".run-step-capture")!;
    legacyCap.style.paddingBlockEnd = "var(--sp-2)";
    legacyCap.style.marginBlockEnd = "0";
    expect(visibleGap(mount(legacy))).toBeCloseTo(fixed, 1);
  });
});

// The run page's collapsed report is the app's ONLY clamp over a markdown bubble's
// block children, and that is what makes it the only one a fixed cap cannot serve:
// each child carries its own line metric AND its own margins, so `N × lh` lands on
// a line boundary for a single-block report and mid-glyph for every structured one.
// Reported as the last visible row being sliced through the middle of its letters.
//
// THE ASSERTION IS THAT NO LINE BOX STRADDLES THE CLIP EDGE, deliberately not that
// the box's height is an integer multiple of its line-height: the latter is FALSE of
// correct output here, since one report renders 13, 16 and 17px ink boxes plus 8px
// inter-block margins, so a test asserting it would be red on a working fix.
// Straddling is what the defect violates and what the fix guarantees.
describe("the run page's collapsed report ends on a whole line", () => {
  /** `exec-view/page.ts` `resultItem`'s box: the key row, then a body holding the
   *  clamped text and its opener as SIBLINGS (a clamped box is `overflow: hidden`, so
   *  a button inside it is clipped away exactly when it becomes needed). */
  function resultBox(...blocks: HTMLElement[]): {
    text: HTMLElement;
    more: HTMLButtonElement;
    handle: ReturnType<typeof attachClamp>;
  } {
    const bubble = document.createElement("div");
    bubble.className = "message assistant";
    bubble.append(...blocks);

    const text = document.createElement("div");
    text.className = "ev-r-text";
    text.appendChild(bubble);
    const more = document.createElement("button");
    more.type = "button";
    more.className = "ev-r-more";

    const body = document.createElement("div");
    body.className = "ev-r-item-body";
    body.append(text, more);
    const key = document.createElement("span");
    key.className = "ev-r-item-key";
    key.textContent = "Output";
    const item = document.createElement("div");
    item.className = "ev-r-item";
    item.append(key, body);

    const results = document.createElement("div");
    results.className = "ev-results";
    const rBody = document.createElement("div");
    rBody.className = "ev-r-body";
    rBody.appendChild(item);
    results.appendChild(rBody);
    mount(results);
    // AFTER mounting: a detached element measures 0 on both sides, so `attachClamp`
    // would answer with its character guess and snap nothing.
    const handle = attachClamp(text, more, { lines: 12, fallbackChars: 900, snapToLine: true });
    return { text, more, handle };
  }

  function para(words: number, seed: string): HTMLElement {
    const p = document.createElement("p");
    p.textContent = Array.from({ length: words }, (_, i) => `${seed}${String(i)}`).join(" ");
    return p;
  }

  function fence(lines: number): HTMLElement {
    const pre = document.createElement("pre");
    const code = document.createElement("code");
    code.textContent = Array.from(
      { length: lines },
      (_, i) => `  captured := line(${String(i)})`,
    ).join("\n");
    pre.appendChild(code);
    return pre;
  }

  function bullets(items: number): HTMLElement {
    const ul = document.createElement("ul");
    for (let i = 0; i < items; i++) {
      const li = document.createElement("li");
      li.textContent = `finding number ${String(i)} in the captured report`;
      ul.appendChild(li);
    }
    return ul;
  }

  /** A 16x16 opaque PNG, so an `<img>` has a real box with no network fetch. It is a
   *  VALID one, which is load-bearing: the probe that found the over-trim first used a
   *  malformed PNG that Chromium and WebKit decoded anyway while Firefox refused it,
   *  so the image shape silently measured 0px tall and read as a no-op. */
  const PNG16 =
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGNwaDhAEmIY1TCqYfhqAACldYAQpGTU2QAAAABJRU5ErkJggg==";

  interface Band {
    readonly top: number;
    readonly bottom: number;
  }

  /** Every LINE BAND the box renders, clipped ones included: rects merged by
   *  overlapping vertical extent, so one line split across several rects (an inline
   *  element, a wrapped fragment) counts once.
   *
   *  A single `Range` over the box cannot answer this — with block-level children
   *  `selectNodeContents(box).getClientRects()` returns the ONE bounding rect where
   *  this walk returns twenty, which is why the run-step case above may use one and
   *  this one may not. */
  function textBands(box: HTMLElement): Band[] {
    const bands: Band[] = [];
    const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
    const range = document.createRange();
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      range.selectNodeContents(node);
      for (const rect of range.getClientRects()) {
        if (rect.height <= 0) {
          continue;
        }
        const open = bands[bands.length - 1];
        if (open !== undefined && rect.top < open.bottom - 0.5) {
          bands[bands.length - 1] = {
            top: Math.min(open.top, rect.top),
            bottom: Math.max(open.bottom, rect.bottom),
          };
          continue;
        }
        bands.push({ top: rect.top, bottom: rect.bottom });
      }
    }
    return bands;
  }

  /** `overflow: hidden` clips at the padding box, and `.ev-r-text` declares neither
   *  padding nor border, so the element's own bottom IS the clip edge. */
  function clipEdge(text: HTMLElement): number {
    const styles = getComputedStyle(text);
    expect(styles.paddingBottom, "the clip-edge arithmetic assumes no padding").toBe("0px");
    return text.getBoundingClientRect().bottom;
  }

  function straddling(text: HTMLElement): string[] {
    const clip = clipEdge(text);
    return textBands(text)
      .filter((b) => b.top < clip - 0.5 && b.bottom > clip + 0.5)
      .map((b) => `${((clip - b.top) / (b.bottom - b.top)).toFixed(3)} of a line painted`);
  }

  const shapes: readonly [string, () => HTMLElement[]][] = [
    // Both reproduce in all three engines against a fixed cap; a fence and a list are
    // the ordinary structured report rather than a contrived one.
    [
      "three paragraphs then a fenced block",
      () => [para(28, "alpha"), para(26, "beta"), para(30, "gamma"), fence(6)],
    ],
    [
      "a paragraph, a bulleted list, a paragraph",
      () => [para(24, "delta"), bullets(12), para(22, "epsilon")],
    ],
  ];

  for (const [what, blocks] of shapes) {
    it(`cuts no line box through its glyphs: ${what}`, () => {
      const { text, more } = resultBox(...blocks());
      // The premise: a report that fits its cap has nothing to clip, so without this
      // the case asserts nothing.
      expect(
        text.scrollHeight - text.clientHeight,
        "the report overflows the clamp",
      ).toBeGreaterThan(1);
      expect(more.hidden, "so the opener is offered").toBe(false);
      expect(straddling(text)).toEqual([]);
    });

    it(`keeps every whole line the cap showed: ${what}`, () => {
      // The other direction, and what stops the snap degenerating into "show less":
      // the measured clip may only discard the PARTIAL line, so the count of fully
      // visible lines is the same as under the stylesheet's own cap. Measured by
      // withdrawing the snap and re-taking it through the handle.
      const { text, handle } = resultBox(...blocks());
      const snapped = textBands(text).filter((b) => b.bottom <= clipEdge(text) + 0.5).length;

      text.style.removeProperty("--clamp-h");
      const nominal = textBands(text).filter((b) => b.bottom <= clipEdge(text) + 0.5).length;
      handle.sync();

      expect(snapped).toBe(nominal);
      expect(snapped).toBeGreaterThan(1);
    });
  }

  /** The clip edge the STYLESHEET's own cap gives, measured with the snap withdrawn
   *  and then restored through the handle, so the box is left as production leaves it.
   *  Content positions are unaffected by the cap — the box CLIPS, it does not reflow —
   *  so every other rect in a case may be read in either state. */
  function nominalClip(text: HTMLElement, handle: ReturnType<typeof attachClamp>): number {
    text.style.removeProperty("--clamp-h");
    const at = text.getBoundingClientRect().bottom;
    handle.sync();
    return at;
  }

  // A BOX THAT RENDERS NO TEXT registers no rect in the band walk, so a snap over text
  // alone trims back to the last band ABOVE it and hides content that FITTED. That is
  // the OVER-trim direction, which is why the straddle assertion above cannot see it:
  // measured before the fix at 700px in three engines, a rule cost 16.4/14.7/15.7px, a
  // 16px image 27.4/25.7/26.7px and an empty fence 35.4/33.7/34.7px (Chromium/Firefox/
  // WebKit) — over a line of report each. It matters because the clamped content is a
  // markdown bubble, and a step report genuinely carries rules, images and fences.
  //
  // Two shapes the same sweep found do NOT over-trim and are deliberately absent: an
  // empty paragraph renders 0px tall in all three engines, so it can never be the last
  // VISIBLE box, and a table row spanning two lines is covered by its own cells' text
  // bands.
  const atomic: readonly [string, number, () => HTMLElement | Promise<HTMLElement>][] = [
    // The filler word count puts the box just under the cap with the trailing
    // paragraph past it, which is the only arrangement where the box can be the last
    // one that FITS. Each was measured at this host's 480px with ±3 words of slack,
    // and the premise assertions below fail loudly rather than passing vacuously if a
    // type-scale change moves the window.
    ["a horizontal rule", 77, () => document.createElement("hr")],
    [
      "an image",
      70,
      async () => {
        const img = document.createElement("img");
        img.src = PNG16;
        img.alt = "";
        // A data-URI image still decodes ASYNCHRONOUSLY, so the decode has to land
        // BEFORE the clamp is attached: a snap taken while the image is still 0px tall
        // correctly awards it nothing, and the case would then measure the decode race
        // rather than the walk. That race is real in production too and is recorded as
        // a residual — see the report.
        await img.decode();
        return img;
      },
    ],
    [
      "an empty fenced block",
      70,
      () => {
        const pre = document.createElement("pre");
        pre.appendChild(document.createElement("code"));
        return pre;
      },
    ],
  ];

  for (const [what, fillerWords, build] of atomic) {
    it(`clips on the last box that FITS even when it carries no text: ${what}`, async () => {
      const box = await build();
      box.dataset["atomicTarget"] = "1";
      // A TRAILING paragraph is required rather than decoration: the box has to be the
      // last thing that fits WHILE the report still overflows, and with the box last
      // those two are mutually exclusive.
      const { text, more, handle } = resultBox(para(fillerWords, "alpha"), box, para(60, "omega"));

      expect(
        text.scrollHeight - text.clientHeight,
        "the report overflows the clamp",
      ).toBeGreaterThan(1);
      expect(more.hidden, "so the opener is offered").toBe(false);

      const nominal = nominalClip(text, handle);
      const boxBottom = box.getBoundingClientRect().bottom;
      const lastTextBand = Math.max(
        0,
        ...textBands(text)
          .filter((b) => b.bottom <= nominal + 0.5)
          .map((b) => b.bottom),
      );

      expect(
        boxBottom - nominal,
        `${what} fits under the stylesheet's own cap`,
      ).toBeLessThanOrEqual(0.5);
      expect(
        boxBottom - lastTextBand,
        `${what} sits below every text band that fits, so a text-only snap over-trims`,
      ).toBeGreaterThan(0.5);

      expect(
        text.getBoundingClientRect().bottom - boxBottom,
        `so the clip lands on ${what}'s own bottom edge rather than the band above it`,
      ).toBeCloseTo(0, 1);
      // The other direction, so a fix for the over-trim cannot buy it with a mid-line
      // cut.
      expect(straddling(text)).toEqual([]);
    });
  }

  it("does not move the clip onto a box that renders NOTHING", () => {
    // The third direction, and the one that makes the walk's zero-height test a live
    // guard rather than a formality. An empty paragraph renders 0px tall, so it can never
    // be the last VISIBLE box and cannot over-trim — which is why the table above records
    // it as not reproducing — but it still has a POSITION, and a position between the last
    // painted line and the cap is one an unguarded walk awards: the clip then lands in the
    // margin gap below the last line, showing a strip of blank space where the snap's
    // whole job is to end on the last whole line. Measured with the guard removed, over
    // the same sweep at 700px: the clip moves down 11.4px in Chromium, 9.7 in Firefox and
    // 10.7 in WebKit, on 20 of 44 samples in each — and NO band straddles, so the straddle
    // oracle is silent about it in both directions.
    const empty = document.createElement("p");
    const { text, more, handle } = resultBox(para(76, "alpha"), empty, para(60, "omega"));

    expect(text.scrollHeight - text.clientHeight, "the report overflows the clamp").toBeGreaterThan(
      1,
    );
    expect(more.hidden, "so the opener is offered").toBe(false);

    const nominal = nominalClip(text, handle);
    const box = empty.getBoundingClientRect();
    const lastFitting = Math.max(
      0,
      ...textBands(text)
        .filter((b) => b.bottom <= nominal + 0.5)
        .map((b) => b.bottom),
    );
    expect(box.height, "the paragraph renders nothing at all").toBe(0);
    expect(
      box.bottom - nominal,
      "its position is under the stylesheet's own cap, so an unguarded walk awards it",
    ).toBeLessThanOrEqual(0.5);
    expect(
      box.bottom - lastFitting,
      "and BELOW the last line that paints, so awarding it would move the clip",
    ).toBeGreaterThan(0.5);

    expect(
      text.getBoundingClientRect().bottom - lastFitting,
      "so the clip stays on the last line that paints something",
    ).toBeCloseTo(0, 1);
    expect(straddling(text)).toEqual([]);
  });

  it("refuses an atomic box that rides the line the cap cuts, rather than clipping inside it", async () => {
    // THE OTHER DIRECTION OF THE ATOMIC WALK, and the one every case above structurally
    // cannot reach: each target in that table is a direct child of the bubble, so it
    // forms its own anonymous line box and can never SHARE one with text. An INLINE
    // replaced box does share one — `.message.assistant`'s `& img` rule declares no
    // `display`, so an `![](…)` inside a paragraph is inline — and under
    // `vertical-align: baseline` its own bottom is the line's BASELINE, which sits above
    // the text band's bottom by the font's descent. So a box whose own bottom fits under
    // the cap can ride a line that does NOT, and awarding it writes the clip inside that
    // line: everything above the baseline painted, the descenders cut. Measured against a
    // build that awards at the pop, over a sub-line sweep at 700px: 0.788 of a line
    // painted in Chromium and WebKit and 0.758 in Firefox, on 8 / 10 / 8 samples each.
    const img = document.createElement("img");
    img.src = PNG16;
    img.alt = "";
    // Awaited for the reason the standalone-image case above states: an undecoded image
    // is 0px tall, and the case would then measure the decode race rather than the walk.
    await img.decode();
    const line = document.createElement("p");
    line.append(document.createTextNode("lead "), img, document.createTextNode(" trail"));
    // The bait window is the font's DESCENT inside a 22.4px line — 4px at this host's
    // type scale — and a filler word count moves the target by a WHOLE line, so no count
    // lands in it. The negative margin slides the line into the window; it is the only
    // arranged number here, and the premises below fail loudly rather than vacuously if a
    // type-scale change moves it.
    line.style.marginBlockStart = "-3.6px";
    // The TAIL is a rule rather than a paragraph, and that is the second half of the
    // shape rather than decoration: with text below the clip the walk breaks on it and
    // the cut band is closed on the way out, so the band's verdict happens to be in hand
    // early. With no text below the clip the cut band is judged only by the walk's LAST
    // close, which is what makes deferring the candidates to after it load-bearing — and
    // a report ending on an image paragraph followed by a rule is an ordinary shape.
    const { text, more, handle } = resultBox(para(74, "alpha"), line, document.createElement("hr"));

    expect(text.scrollHeight - text.clientHeight, "the report overflows the clamp").toBeGreaterThan(
      1,
    );
    expect(more.hidden, "so the opener is offered").toBe(false);

    const nominal = nominalClip(text, handle);
    const imgBottom = img.getBoundingClientRect().bottom;
    const imgTop = img.getBoundingClientRect().top;
    const ridden = textBands(text).find((b) => imgTop < b.bottom - 0.5 && imgBottom > b.top + 0.5);
    expect(ridden, "the image shares a line box with text, so a band stands for it").toBeDefined();
    expect(
      imgBottom - nominal,
      "the image's OWN bottom fits under the stylesheet's cap, so the bait is live",
    ).toBeLessThanOrEqual(0.5);
    expect(
      (ridden?.bottom ?? 0) - nominal,
      "while the band it rides does NOT fit, which is the whole of the bait",
    ).toBeGreaterThan(0.5);

    // The killing assertion: the clip may not land inside that line.
    expect(straddling(text)).toEqual([]);
    expect(
      text.getBoundingClientRect().bottom - imgBottom,
      "so the clip is NOT written at the image's own bottom, which is inside the cut line",
    ).toBeLessThan(-0.5);
    // And the retreat is exactly to the last line that fits, so the straddle is not
    // bought with an over-trim: the two directions are asserted together or a fix can
    // trade one for the other.
    const lastFitting = Math.max(
      0,
      ...textBands(text)
        .filter((b) => b.bottom <= nominal + 0.5)
        .map((b) => b.bottom),
    );
    expect(
      text.getBoundingClientRect().bottom - lastFitting,
      "the clip lands on the last band that fits, one line above the cut one",
    ).toBeCloseTo(0, 1);
  });

  it("keeps clipping rather than opening fully when no height could be positive", () => {
    // A clip height at or below zero is invalid at computed-value time, so
    // `max-block-size: var(--clamp-h, 12lh)` falls back to its initial `none` and the
    // collapsed box renders the WHOLE report — the outcome `31-exec-view.css`'s own
    // comment cites against `-webkit-line-clamp`. Failing open is the right DIRECTION
    // for this surface, so the guard raises nothing; it just makes that state
    // unreachable.
    //
    // The shape is SYNTHETIC and says so: the written height works out to the last
    // fitting band's bottom RELATIVE TO THE BOX'S OWN TOP, so reaching a non-positive
    // one needs a fitting band at or above that top, which no markdown report produces.
    // A negative top margin puts one there and a large one on the next block pushes
    // every other band below the clip, so that band is the only candidate.
    const high = para(3, "high");
    high.style.marginTop = "-400px";
    const low = para(40, "low");
    low.style.marginTop = "700px";
    const { text, handle } = resultBox(high, low);
    // AND the box has to sit far enough down the page that the band above it is still at
    // a POSITIVE viewport y — otherwise the earlier `fits <= 0` guard answers first and
    // this case measures that one instead. Bait the unguarded code would really take.
    text.closest<HTMLElement>(".ev-results")?.style.setProperty("margin-block-start", "600px");
    handle.sync();

    const boxTop = text.getBoundingClientRect().top;
    const bandBottom = high.getBoundingClientRect().bottom;
    // The overflow premise is read with the snap WITHDRAWN, so it states a fact about
    // the REPORT rather than about the snap's own output: a snap that opened the box
    // fully would make its own premise false, and the case would then fail on this line
    // instead of on the assertion that names the cause.
    text.style.removeProperty("--clamp-h");
    const overflow = text.scrollHeight - text.clientHeight;
    handle.sync();
    expect(overflow, "the premise: the box overflows, so the snap runs at all").toBeGreaterThan(1);
    expect(
      bandBottom - boxTop,
      "the only fitting band is ABOVE the box's own top, so the height works out non-positive",
    ).toBeLessThan(0);
    expect(
      bandBottom,
      "and it is at a positive viewport y, so the earlier `fits <= 0` guard does not answer first",
    ).toBeGreaterThan(0);

    expect(text.style.getPropertyValue("--clamp-h"), "no clip height is written").toBe("");
    const cap = Number.parseFloat(getComputedStyle(text).maxBlockSize);
    expect(
      Number.isFinite(cap),
      `so the stylesheet's own cap still clips, rather than resolving to none ` +
        `(read ${getComputedStyle(text).maxBlockSize})`,
    ).toBe(true);
  });

  it("has the production results clamp opting IN to the line snap", () => {
    // Every other case in this describe constructs its own clamp, so deleting
    // `snapToLine` from `RESULT_CLAMP` restores the mid-glyph clip with this whole file
    // green — measured. A SOURCE assertion because the opt-in is an authored literal,
    // and importing the builder drags `chat.ts` and the run store in behind it for a
    // fact they do not decide.
    const decl = /const RESULT_CLAMP\s*=\s*\{([^}]*)\}/.exec(execPageSrc);
    expect(decl?.[1], "exec-view/page.ts declares RESULT_CLAMP").toBeDefined();
    expect(
      decl?.[1] ?? "",
      "the run page's results clamp opts in to the line snap; without it the " +
        "collapsed report clips mid-glyph again and no other case here can see it",
    ).toMatch(/\bsnapToLine\s*:\s*true\b/);
  });

  /** A markdown table's row, the shape that had NO case in this file and so went unseen
   *  through three passes: SHORT header labels, one long body cell that wraps to two lines,
   *  one short body cell centred in the row, and one cell holding the 16x16 PNG. Short
   *  headers are what stop auto table layout squeezing the other columns to one character.
   *  Built with `createElement` + `textContent` like every other fixture here. */
  async function tableRow(): Promise<HTMLElement> {
    const table = document.createElement("table");
    const thead = document.createElement("thead");
    const headRow = document.createElement("tr");
    for (const label of ["Step", "Result", "Shot"]) {
      const th = document.createElement("th");
      th.textContent = label;
      headRow.appendChild(th);
    }
    thead.appendChild(headRow);

    const tbody = document.createElement("tbody");
    const row = document.createElement("tr");
    const wide = document.createElement("td");
    wide.textContent =
      "the first cell carries a genuinely long sentence so that it wraps onto a second " +
      "rendered line inside this one row";
    const short = document.createElement("td");
    short.textContent = "ok";
    const shot = document.createElement("td");
    const img = document.createElement("img");
    img.src = PNG16;
    img.alt = "";
    // Awaited for the reason the atomic cases above state: an undecoded image is 0px
    // tall, and the case would measure the decode race rather than the model.
    await img.decode();
    shot.appendChild(img);
    row.append(wide, short, shot);
    tbody.appendChild(row);
    table.append(thead, tbody);
    return table;
  }

  /** Every rendered text rect in the box, UNMERGED. The three helpers below walk every text
   *  node unbounded and un-pruned, so they are the model's SPECIFICATION rather than a copy
   *  of its implementation — which is what lets them contradict it when it is wrong. */
  function allRects(box: HTMLElement): DOMRect[] {
    const out: DOMRect[] = [];
    const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
    const range = document.createRange();
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      range.selectNodeContents(node);
      for (const rect of range.getClientRects()) {
        if (rect.height > 0) {
          out.push(rect);
        }
      }
    }
    return out;
  }

  function cuttingRects(text: HTMLElement): string[] {
    const clip = clipEdge(text);
    return allRects(text)
      .filter((r) => r.top < clip - 0.5 && r.bottom > clip + 0.5)
      .map((r) => `${((clip - r.top) / r.height).toFixed(3)} of a rect painted`);
  }

  function rectBottoms(text: HTMLElement): number[] {
    return allRects(text).map((r) => r.bottom);
  }

  function straddlesRect(text: HTMLElement, y: number): boolean {
    return allRects(text).some((r) => r.top < y - 0.5 && r.bottom > y + 0.5);
  }

  /** Does this element render a TEXT RECT anywhere beneath it? The MODEL's own atom
   *  predicate, so the over-trim oracle's candidate set is the model's: an element whose
   *  only text is an NBSP has a real line box and is text-bearing, where a non-blank-TEXT
   *  predicate would call it an atom and hand the oracle a candidate the model refuses. */
  function rendersText(el: Element): boolean {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    const range = document.createRange();
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      range.selectNodeContents(node);
      for (const rect of range.getClientRects()) {
        if (rect.height > 0) {
          return true;
        }
      }
    }
    return false;
  }

  interface RowSample {
    readonly words: number;
    readonly gap: number;
    readonly text: HTMLElement;
    readonly handle: ReturnType<typeof attachClamp>;
    readonly row: HTMLElement;
    readonly nominal: number;
    readonly wrote: boolean;
  }

  /** A SWEEP rather than one arranged offset, at this host's own width: a handful of filler
   *  word counts crossed with POSITIVE gaps, which can only ever widen the space above the
   *  row, so every straddle it finds is reachable by a report nobody arranged. Each sample
   *  releases its clamp BEFORE the next mount, because `releaseClampsIn` can only reach an
   *  element still inside `host` and `mount` replaces the tree. */
  async function sweepRow(
    trailing: () => HTMLElement[],
    each: (s: RowSample) => void,
  ): Promise<RowSample[]> {
    const seen: RowSample[] = [];
    for (const words of [40, 50, 60, 70, 80, 90]) {
      for (const gap of [8, 12, 16, 18, 20, 22, 24, 28, 32]) {
        const table = await tableRow();
        table.style.marginBlockStart = `${String(gap)}px`;
        releaseClampsIn(host);
        const { text, handle } = resultBox(para(words, "alpha"), table, ...trailing());
        if (text.scrollHeight - text.clientHeight <= 1) {
          continue;
        }
        const wrote = text.style.getPropertyValue("--clamp-h") !== "";
        const row = table.querySelector("tbody tr");
        expect(row, "the fixture renders its body row").not.toBeNull();
        const sample: RowSample = {
          words,
          gap,
          text,
          handle,
          row: row as HTMLElement,
          nominal: nominalClip(text, handle),
          wrote,
        };
        seen.push(sample);
        each(sample);
      }
    }
    expect(seen.length, "the sweep reached the overflowing population at all").toBeGreaterThan(0);
    return seen;
  }

  it("cuts no line box through its glyphs: a table row whose cells share a line", async () => {
    // THE REGRESSION CASE FOR THIS DEFECT. A table row is the one ordinary markdown shape
    // where two cells' rects OVERLAP vertically — a cell wrapping to two lines beside a
    // single-line cell the UA centres in the row — so the rect list in document order is
    // NOT sorted by `top`, which is exactly the assumption the single-pass band machine
    // made. Measured on the shipped build over a 3,384-sample sweep at 700px: 67 / 66 / 66
    // samples clipped up to 9.91 / 10.00 / 10.00px into a 16px line box, 83-95% of that
    // line's ink lost, in Chromium / Firefox / WebKit.
    let overlapping = 0;
    let unsorted = 0;
    let insideRow = 0;
    let wrote = 0;
    const worst: string[] = [];

    const samples = await sweepRow(
      () => [para(40, "omega")],
      (s) => {
        const rects = allRects(s.text);
        if (
          rects.some((a) =>
            rects.some(
              (b) =>
                a !== b && a.left !== b.left && a.top < b.bottom - 0.5 && a.bottom > b.top + 0.5,
            ),
          )
        ) {
          overlapping++;
        }
        for (let i = 1; i < rects.length; i++) {
          if ((rects[i]?.top ?? 0) < (rects[i - 1]?.top ?? 0) - 0.5) {
            unsorted++;
            break;
          }
        }
        const clip = clipEdge(s.text);
        const box = s.row.getBoundingClientRect();
        if (box.top < clip - 0.5 && box.bottom > clip + 0.5) {
          insideRow++;
        }
        if (s.wrote) {
          wrote++;
        }
        const cuts = cuttingRects(s.text);
        if (cuts.length > 0) {
          worst.push(`filler ${String(s.words)} gap ${String(s.gap)}px: ${cuts.join(", ")}`);
        }
        // The BAND oracle is asserted too and is the cheap gross check; it is a SUPERSET of
        // the rect one and, being computed from the model that was replaced, is a
        // DIAGNOSTIC — where the two ever disagree the rect reading rules.
        expect(straddling(s.text)).toEqual([]);
      },
    );

    // The premises. Each fails loudly rather than passing vacuously if a type-scale change
    // moves the window this fixture reaches.
    expect(
      overlapping,
      "at least one sample renders two cells' rects that OVERLAP",
    ).toBeGreaterThan(0);
    expect(unsorted, "at least one sample's rect list is NOT sorted by top").toBeGreaterThan(0);
    expect(
      insideRow,
      "at least one sample puts the clip inside the row's own span",
    ).toBeGreaterThan(0);
    expect(wrote, "at least one sample is in the WRITE population").toBeGreaterThan(0);

    expect(worst, `${String(samples.length)} samples swept`).toEqual([]);
  });

  it("clips no higher than the last position no rendered rect straddles", async () => {
    // The OVER-TRIM direction, one-directional on purpose: the clip is never ABOVE the
    // straddle-free ideal. Equality is FALSE of correct output on the refusal arm, where
    // the stylesheet's own cap stands up to `SNAP_EPSILON` below the ideal, so an equality
    // assertion here would be red on a working fix. The candidate set is the model's own —
    // rect bottoms AND the bottoms of elements that render no text — because an oracle
    // built on a wider atom predicate hands it a candidate the model refuses and the
    // one-directional assertion then fails on correct output.
    await sweepRow(
      () => [para(40, "omega")],
      (s) => {
        const cap = s.nominal;
        const atoms = [...s.text.querySelectorAll("*")].filter(
          (el) => !rendersText(el) && el.getBoundingClientRect().height > 0,
        );
        const cands = [
          ...rectBottoms(s.text),
          ...atoms.map((el) => el.getBoundingClientRect().bottom),
        ].filter((y) => y <= cap + 0.5);
        const safeIdeal = Math.max(0, ...cands.filter((y) => !straddlesRect(s.text, y)));

        expect(
          clipEdge(s.text),
          `filler ${String(s.words)} gap ${String(s.gap)}px: the clip is not above the ` +
            `last straddle-free content bottom`,
        ).toBeGreaterThanOrEqual(safeIdeal - 0.5);
      },
    );
  });

  it("reads the cells that come after a rect below the clip", async () => {
    // THE BOUND's own case, and nothing else in this file pins it. The walk stops a child
    // list at the first element whose box starts below the deepest ink read so far, so a
    // long tail after the row is what makes it fire at all — and the cell that refuses the
    // cutting clip is the row's LAST, so a bound that stopped one element early would
    // reproduce the defect with every other case here still green.
    await sweepRow(
      () => Array.from({ length: 60 }, (_, i) => para(12, `tail${String(i)}`)),
      (s) => {
        expect(
          cuttingRects(s.text),
          `filler ${String(s.words)} gap ${String(s.gap)}px, 60 paragraphs after the row`,
        ).toEqual([]);
        expect(straddling(s.text)).toEqual([]);
      },
    );
  });

  it("leaves a report that fits unclipped, with no opener", () => {
    // A report shorter than the cap has nothing to clip, so the snap must not run at
    // all: the written property is what this owns, because an always-snap whose value
    // is the content's own last line makes `scrollHeight > clientHeight` true by a
    // fraction and offers a Show more over nothing to reveal. Red-checked by dropping
    // the overflow conjunct, which writes a height here and fails on that assertion.
    const { text, more } = resultBox(para(6, "short"));
    expect(text.scrollHeight - text.clientHeight, "the report fits").toBeLessThanOrEqual(1);
    expect(more.hidden, "so no opener is offered").toBe(true);
    expect(text.style.getPropertyValue("--clamp-h"), "and nothing is snapped").toBe("");
    expect(straddling(text)).toEqual([]);
  });
});
