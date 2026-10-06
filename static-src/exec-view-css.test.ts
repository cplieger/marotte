// Real-layout guards for the exec view's disclosures and the run card's clamped step output; each fact was a measured
// defect invisible to a source read.
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
  // A definite width so a clamped line box can wrap.
  host.style.inlineSize = "480px";
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

// Releases the shared ResizeObserver per case so one case's observation stays out of the next.
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

/** One `.ev-row` as `buildRow` assembles it; `kids` decides whether it is a leaf. */
function evRow(opts: { depth: number; kids: boolean; expanded?: boolean }): HTMLElement {
  const chevron = document.createElement("span");
  chevron.className = "ev-twist";
  chevron.setAttribute("aria-hidden", "true");
  chevron.appendChild(chevronEl());

  const glyph = document.createElement("span");
  glyph.className = "ev-state";
  const kindSlot = document.createElement("span");
  kindSlot.className = "ev-kind";
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

  // `paint` hides the twist on a leaf and calls `applyCollapse` on a row with children.
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
    // `visibility: hidden` cannot collapse the row's flex `gap`, half of the 20px.
    expect(css(twist, "display")).toBe("none");
    expect(css(twist, "visibility")).not.toBe("hidden");
    expect(twist.getBoundingClientRect().width).toBe(0);
  });

  it("puts the activity mark on the row's own content edge", () => {
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const main = row.querySelector<HTMLElement>(".ev-row-main")!;
    const glyph = row.querySelector<HTMLElement>(".ev-state")!;
    // The content edge: the leading padding is the depth indent, not the defect.
    const contentEdge =
      main.getBoundingClientRect().x +
      Number.parseFloat(css(main, "border-left-width")) +
      Number.parseFloat(css(main, "padding-left"));
    expect(glyph.getBoundingClientRect().x - contentEdge).toBeCloseTo(0, 1);
  });

  it("still indents a row that HAS a disclosure by its twist", () => {
    // Collapsing the empty box must not collapse a container's real one.
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

      // The wrapper's own rotation composed with the chevron's closed -90deg.
      expect(css(twist, "transform"), `${state}: wrapper`).toBe("none");
      expect(css(glyph, "--chev-turn").trim(), `${state}: turn`).toBe(expanded ? "0deg" : "-90deg");
      expect(css(glyph, "transform"), `${state}: glyph`).toBe(
        expanded ? "matrix(1, 0, 0, 1, 0, 0)" : "matrix(0, -1, 1, 0, 0, 0)",
      );
    }
  });
});

describe("a row inside the group box is a band, not a pill", () => {
  // A row spans the box's inner width, so the box owns the corners; a rounded row mid-list reads as floating.
  it("squares the header and every row inside the box, and leaves the box its corners", () => {
    const box = evRow({ depth: 0, kids: true, expanded: true });
    box.classList.add("ev-group");
    // `tree.ts` writes `--ev-depth` 0 for depth 0 and 1.
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
    // Outside a box each row is its own box, so rounding it is correct.
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const main = row.querySelector<HTMLElement>(".ev-row-main")!;
    expect(Number.parseFloat(css(main, "border-top-left-radius"))).toBeGreaterThan(0);
  });

  // Flush: bottom padding on `.ev-kids` read as a list continuing past its last item.
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

    // The corner is the box's, through its clip (the flush-inside-a-clipping-parent exemption), which works at any depth.
    expect(css(box, "overflow")).toBe("hidden");
    expect(Number.parseFloat(css(box, "border-bottom-left-radius"))).toBeGreaterThan(0);
  });

  // A gap between rows shows the box's fill, visible once a row takes a fill.
  it("runs adjacent rows flush, so no fill shows between them", () => {
    const box = evRow({ depth: 0, kids: true, expanded: true });
    box.classList.add("ev-group");
    const kids = box.querySelector<HTMLElement>(":scope > .ev-kids")!;
    const children = [evRow({ depth: 0, kids: false }), evRow({ depth: 0, kids: false })];
    for (const child of children) {
      kids.appendChild(child);
    }
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
    // Read off `.ev-tl`: the claim is that they are the same card.
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
    // No author `[hidden]` rule here, so the UA's `[hidden]` loses to the bare class.
    const box = mount(inputs(true));
    expect(css(box, "display")).toBe("none");
    expect(box.getBoundingClientRect().height).toBe(0);
  });
});

// Two clamps, one opener skin; `run-page-layout.test.ts` pins the shared selector, this pins what it paints.
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

    // Premise: the skin is in force, not the UA button default.
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
    // The reset layer inks every `a` as a link, and a row is a navigator, so it opts out.
    const card = mount(step("captured"));
    const head = card.querySelector<HTMLElement>(".run-step-head")!;
    expect(css(head, "text-decoration-line")).toBe("none");

    // The live token, not a literal: `--c-link` is theme-split.
    const probe = document.createElement("a");
    probe.href = "/run/wf_1";
    probe.textContent = "link";
    card.appendChild(probe);
    expect(css(head, "color")).not.toBe(css(probe, "color"));
    expect(css(head, "color")).toBe(css(card, "color"));
  });

  const overflowing = Array.from(
    { length: 40 },
    (_, i) => `captured output line fragment number ${String(i)}`,
  ).join(" ");

  it("moves the trailing space to a margin, outside the clip region", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // `overflow: hidden` clips at the padding box, so bottom padding reveals the clipped line.
    expect(css(cap, "padding-bottom")).toBe("0px");
    expect(Number.parseFloat(css(cap, "margin-bottom"))).toBeGreaterThan(0);
    expect(css(cap, "-webkit-line-clamp")).toBe("2");
    expect(css(cap, "overflow-y")).toBe("hidden");
  });

  function lineRects(cap: HTMLElement): DOMRect[] {
    const range = document.createRange();
    range.selectNodeContents(cap);
    return [...range.getClientRects()];
  }

  it("paints no partial line under the ellipsis", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // The clip edge is the element's bottom. Lines below it are the clamp's job; none may straddle it.
    const clip = cap.getBoundingClientRect().bottom;
    const rects = lineRects(cap);
    // The text really overflows, or this asserts nothing.
    expect(rects.length).toBeGreaterThan(2);
    const straddling = rects
      .filter((r) => r.top < clip - 0.5 && r.bottom > clip + 0.5)
      .map((r) => `${r.top - clip}..${r.bottom - clip}`);
    expect(straddling).toEqual([]);
  });

  it("keeps the visible gap the padding used to give it", () => {
    // The visible gap must be identical either way; the oracle is a second card carrying the naive declaration inline.
    const visibleGap = (card: HTMLElement): number => {
      const row = card.querySelector<HTMLElement>(".run-step")!;
      const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
      const clip = cap.getBoundingClientRect().bottom;
      // `pre-wrap` + `overflow-wrap: anywhere` splits a line into fragment rects, so take the lowest visible edge.
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

// The run page's report is the only clamp over markdown block children, each with its own line metric and margins,
// so no fixed cap lands on a line boundary.
describe("the run page's collapsed report ends on a whole line", () => {
  /** The opener is a sibling of the clamped text: inside the `overflow: hidden` box it would be clipped. */
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
    // After mounting: detached, `attachClamp` falls back to its character guess.
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

  /** A valid PNG: Firefox refused the malformed one Chromium and WebKit decoded. */
  const PNG16 =
    "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGNwaDhAEmIY1TCqYfhqAACldYAQpGTU2QAAAABJRU5ErkJggg==";

  interface Band {
    readonly top: number;
    readonly bottom: number;
  }

  /** Every line band, rects merged by vertical overlap; a single `Range` returns a bounding box. */
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

  /** `.ev-r-text` has no padding or border, so its bottom is the clip edge. */
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
    // Both reproduce in all three engines with a fixed cap.
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
      // Premise: a report that fits has nothing to clip.
      expect(
        text.scrollHeight - text.clientHeight,
        "the report overflows the clamp",
      ).toBeGreaterThan(1);
      expect(more.hidden, "so the opener is offered").toBe(false);
      expect(straddling(text)).toEqual([]);
    });

    it(`keeps every whole line the cap showed: ${what}`, () => {
      // The snap may discard only the partial line: fully visible lines match the stylesheet's own cap.
      const { text, handle } = resultBox(...blocks());
      const snapped = textBands(text).filter((b) => b.bottom <= clipEdge(text) + 0.5).length;

      text.style.removeProperty("--clamp-h");
      const nominal = textBands(text).filter((b) => b.bottom <= clipEdge(text) + 0.5).length;
      handle.sync();

      expect(snapped).toBe(nominal);
      expect(snapped).toBeGreaterThan(1);
    });
  }

  /** The stylesheet's clip, measured with the snap withdrawn and restored through the handle. */
  function nominalClip(text: HTMLElement, handle: ReturnType<typeof attachClamp>): number {
    text.style.removeProperty("--clamp-h");
    const at = text.getBoundingClientRect().bottom;
    handle.sync();
    return at;
  }

  // A box that renders no text registers no rect, so a text-only snap over-trims content that fitted.
  const atomic: readonly [string, number, () => HTMLElement | Promise<HTMLElement>][] = [
    // Word counts that leave the box just under the cap with the trailing paragraph past it.
    ["a horizontal rule", 77, () => document.createElement("hr")],
    [
      "an image",
      70,
      async () => {
        const img = document.createElement("img");
        img.src = PNG16;
        img.alt = "";
        // A data-URI image decodes asynchronously, so decode before attaching or this measures the race.
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
      // A trailing paragraph keeps the report overflowing while the box is the last thing that fits.
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
      // A fix for over-trim must not cut mid-line.
      expect(straddling(text)).toEqual([]);
    });
  }

  it("does not move the clip onto a box that renders NOTHING", () => {
    // An empty paragraph is 0px tall and must not be awarded a clip position in a margin gap.
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
    // An inline replaced box shares a line box with text, which the direct-child cases cannot reach.
    const img = document.createElement("img");
    img.src = PNG16;
    img.alt = "";
    await img.decode();
    const line = document.createElement("p");
    line.append(document.createTextNode("lead "), img, document.createTextNode(" trail"));
    // The bait window is the font's descent; the negative margin slides the line into it.
    line.style.marginBlockStart = "-3.6px";
    // A rule as the tail, so the walk does not close the cut band early on text below the clip.
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
    // A non-positive clip height falls back to `none` and shows the whole report.
    const high = para(3, "high");
    high.style.marginTop = "-400px";
    const low = para(40, "low");
    low.style.marginTop = "700px";
    const { text, handle } = resultBox(high, low);
    // Far enough down that the band above is at positive y, or `fits <= 0` answers first.
    text.closest<HTMLElement>(".ev-results")?.style.setProperty("margin-block-start", "600px");
    handle.sync();

    const boxTop = text.getBoundingClientRect().top;
    const bandBottom = high.getBoundingClientRect().bottom;
    // The overflow premise is read with the snap withdrawn, so it describes the report.
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
    // Source assertion: deleting `snapToLine` from `RESULT_CLAMP` leaves every constructed-clamp case green.
    const decl = /const RESULT_CLAMP\s*=\s*\{([^}]*)\}/.exec(execPageSrc);
    expect(decl?.[1], "exec-view/page.ts declares RESULT_CLAMP").toBeDefined();
    expect(
      decl?.[1] ?? "",
      "the run page's results clamp opts in to the line snap; without it the " +
        "collapsed report clips mid-glyph again and no other case here can see it",
    ).toMatch(/\bsnapToLine\s*:\s*true\b/);
  });

  /** A markdown table row: short headers, a wrapped cell and a centred one, and an image cell. */
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
    await img.decode();
    shot.appendChild(img);
    row.append(wide, short, shot);
    tbody.appendChild(row);
    table.append(thead, tbody);
    return table;
  }

  /** Unmerged and unpruned: the specification the model is checked against. */
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

  /** The model's own atom predicate: an NBSP-only element has a real line box. */
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

  /** A sweep of filler counts and positive gaps, so every straddle found is reachable. */
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
    // A table row's cell rects overlap vertically, so document order is not sorted by position.
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
        // The band oracle is a superset and diagnostic; where they disagree the rect reading rules.
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
    // One-directional: the clip is never above the straddle-free ideal (equality fails on the refusal arm).
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
    // The walk's stop bound: a long tail after the row is what makes it fire.
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
    // A report under the cap must not snap: a written height would offer Show more over nothing.
    const { text, more } = resultBox(para(6, "short"));
    expect(text.scrollHeight - text.clientHeight, "the report fits").toBeLessThanOrEqual(1);
    expect(more.hidden, "so no opener is offered").toBe(true);
    expect(text.style.getPropertyValue("--clamp-h"), "and nothing is snapped").toBe("");
    expect(straddling(text)).toEqual([]);
  });
});

describe("a run step's transcript spaces its blocks the way a chat turn does", () => {
  function blocks(): HTMLElement[] {
    const summary = document.createElement("summary");
    summary.className = "reasoning-summary";
    summary.textContent = "Thinking";
    const reasoning = document.createElement("details");
    reasoning.className = "reasoning-block msg-reasoning";
    reasoning.appendChild(summary);

    const code = document.createElement("code");
    code.textContent = "work/after";
    const p = document.createElement("p");
    p.append("Step 1: confirm ", code, " equals wt.");
    const message = document.createElement("div");
    message.className = "message assistant";
    message.appendChild(p);
    const prose = document.createElement("div");
    prose.className = "msg-row";
    prose.appendChild(message);

    const tool = (): HTMLElement => {
      const card = document.createElement("div");
      card.className = "tool-call";
      card.textContent = "shell";
      return card;
    };
    return [reasoning, tool(), prose, tool()];
  }

  function runBody(): { body: HTMLElement; ordered: HTMLElement[] } {
    const first = document.createElement("div");
    first.className = "ev-d-turn";
    const firstBlocks = blocks();
    first.append(...firstBlocks);
    const second = document.createElement("div");
    second.className = "ev-d-turn";
    const lastTool = document.createElement("div");
    lastTool.className = "tool-call";
    lastTool.textContent = "shell";
    second.appendChild(lastTool);
    const body = document.createElement("div");
    body.className = "ev-d-body";
    body.append(first, second);
    return { body, ordered: [...firstBlocks, lastTool] };
  }

  function gaps(ordered: readonly HTMLElement[]): number[] {
    return ordered
      .slice(1)
      .map(
        (next, i) => next.getBoundingClientRect().top - ordered[i]!.getBoundingClientRect().bottom,
      );
  }

  it("puts the body's own gap between every block, within a turn and across turns", () => {
    const { body, ordered } = runBody();
    mount(body);
    expect(parseFloat(getComputedStyle(body).rowGap)).toBe(12);
    expect(gaps(ordered)).toEqual([12, 12, 12, 12]);
  });

  it("sets the prose in the chat's own type", () => {
    const { body } = runBody();
    const chat = document.createElement("div");
    chat.className = "turn-body";
    chat.append(...blocks());
    const pair = document.createElement("div");
    pair.append(body, chat);
    mount(pair);
    const runP = body.querySelector<HTMLElement>(".message.assistant p")!;
    const chatP = chat.querySelector<HTMLElement>(".message.assistant p")!;
    expect(css(runP, "font-size")).toBe("14px");
    expect(css(runP, "font-size")).toBe(css(chatP, "font-size"));
    expect(css(runP, "line-height")).toBe(css(chatP, "line-height"));
  });

  it("sets the prose's inline code as the chat's chip", () => {
    const { body } = runBody();
    const chat = document.createElement("div");
    chat.className = "turn-body";
    chat.append(...blocks());
    const pair = document.createElement("div");
    pair.append(body, chat);
    mount(pair);
    const runCode = body.querySelector(".message.assistant code")!;
    const chatCode = chat.querySelector(".message.assistant code")!;
    expect(css(runCode, "padding-inline-start")).toBe("4px");
    expect(css(runCode, "padding-inline-start")).toBe(css(chatCode, "padding-inline-start"));
    expect(css(runCode, "background-color")).toBe(css(chatCode, "background-color"));
    expect(css(runCode, "font-family")).toBe(css(chatCode, "font-family"));
    expect(css(runCode, "font-size")).toBe(css(chatCode, "font-size"));
  });
});
