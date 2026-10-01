import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { skeletonRows } from "./skeleton.js";

// ---------------------------------------------------------------------------
// The four shapes, and the one rule about them CSS can enforce.
//
// D4 splits the units by where a bar SITS: a bar inside prose measures in the
// text's own metrics (`em`, `lh`) so a change to the container's type carries it,
// and a bar in a chrome slot measures in `rem`. Which line class a site passes is
// what makes that testable rather than conventional, so the first two cases sweep
// each class for a unit from the other family.
//
// The rendered case is the one `1lh` exists for. Two containers whose prose runs
// at DIFFERENT line-heights (`.turn-req-text` at 1.5, `.message.assistant` at
// 1.6) are measured against each other: a bar plus its two margins occupies
// exactly one line box of whichever container it is in, which a hardcoded `em`
// cannot do. The two heights differing is that case's own non-vacuity floor.
//
// The last case is the emitted DOM of `skeletonRows`' three cell shapes, and the
// half that matters is the `bar` one: a bar carrying both `skeleton-line` and a
// site's own height class takes whichever the MANIFEST order picks, which is the
// cross-slice tie the shape vocabulary exists to keep out.
// ---------------------------------------------------------------------------

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

/** Every LENGTH UNIT a rule body declares, as a set.
 *
 *  A family membership test rather than a substring search, because `0.75rem`
 *  contains `em` and a naive `includes("em")` reads the chrome class as an offender.
 *  `%` is admitted by both families: it is container-relative, so it says nothing
 *  about which type scale a bar follows. */
function unitsIn(body: string): string[] {
  const found = new Set<string>();
  for (const m of body.matchAll(/\b\d*\.?\d+([a-z%]+)/gi)) {
    found.add(m[1]?.toLowerCase() ?? "");
  }
  return [...found].sort();
}

/** A run of three bars mounted in a real prose container, measured two ways.
 *
 *  `pitch` is the distance from one bar's top to the next's, which IS "one bar per
 *  real line box" and is what makes N bars stand on the first N lines. `interior`
 *  is the middle bar's own outer box. The middle bar rather than the first,
 *  because a prose container legitimately zeroes the outer block margins of its
 *  own first and last descendants (13-messages.css does, at a specificity this
 *  file cannot and should not beat) — a placeholder standing in for prose inherits
 *  that, so the RUN's edges are flush by design and only its interior pitch is
 *  the rule under test. */
function measureRun(containerClass: string): { line: number; pitch: number; interior: number } {
  const container = document.createElement("div");
  container.className = containerClass;
  const wrap = document.createElement("div");
  wrap.className = "skeleton-text";
  const bars: HTMLDivElement[] = [];
  for (const width of ["94%", "88%", "52%"]) {
    const bar = document.createElement("div");
    bar.className = "skeleton skeleton-line-text";
    bar.style.width = width;
    wrap.appendChild(bar);
    bars.push(bar);
  }
  container.appendChild(wrap);
  stage.appendChild(container);

  const [, middle, last] = bars as [HTMLDivElement, HTMLDivElement, HTMLDivElement];
  const cs = getComputedStyle(middle);
  return {
    line: Number.parseFloat(getComputedStyle(container).lineHeight),
    pitch: last.getBoundingClientRect().top - middle.getBoundingClientRect().top,
    interior:
      middle.getBoundingClientRect().height +
      Number.parseFloat(cs.marginBlockStart) +
      Number.parseFloat(cs.marginBlockEnd),
  };
}

describe("the shape vocabulary's unit split", () => {
  it("sizes a bar inside prose in the text's own metrics, never in rem or px", () => {
    const { body } = ruleContaining(loadCSS("30-utilities.css"), ".skeleton-line-text");
    expect(unitsIn(body), ".skeleton-line-text declares only em/lh/%").toEqual(
      expect.arrayContaining(["em", "lh"]),
    );
    expect(unitsIn(body).filter((u) => !["em", "lh", "%"].includes(u))).toEqual([]);
  });

  it("sizes a bar in a chrome slot in rem, never in em or lh", () => {
    const { body } = ruleContaining(loadCSS("30-utilities.css"), ".skeleton-line");
    expect(unitsIn(body), ".skeleton-line declares a chrome length").toContain("rem");
    expect(unitsIn(body).filter((u) => !["rem", "%"].includes(u))).toEqual([]);
  });

  it("gives INLINE FIELD a chrome-unit box and no text metric at all", () => {
    const { body } = ruleContaining(loadCSS("30-utilities.css"), ".skeleton-field");

    // The BOX is what D4's chrome rule is about, and it is the bar's own height.
    expect(body).toMatch(/block-size:\s*[\d.]+rem/);
    // `lh` is TEXT BLOCK's unit — the one that makes a bar track a container's
    // prose. A chrome bar has no prose to track, so it may not reach for it.
    expect(unitsIn(body)).not.toContain("lh");

    // The one `em` is the WIDTH's fallback, and it is deliberate: both existing
    // sites declare `width: 10em`/`12em`, so a `rem` fallback would resize them.
    // Per-site widths live in a custom property, and this asserts the fallback is
    // the only place a length appears on that axis.
    const inlineSize = /inline-size:\s*([^;]+);/.exec(body)?.[1] ?? "";
    expect(inlineSize).toBe("var(--skel-field-w, 10em)");
  });
});

describe("a prose bar tracks its container's line box", () => {
  it("stands on exactly one line box, in both of the transcript's prose containers", () => {
    const req = measureRun("turn-req-text");
    const reply = measureRun("message assistant");

    // The whole point of measuring two: the containers run at 1.5 and 1.6, so a
    // bar sized off a restated number would be right in at most one of them.
    expect(req.line, "the two containers' line boxes differ").not.toBeCloseTo(reply.line, 2);

    // Both margins are `calc((1lh - 0.7em) / 2)`, so each can round to Chromium's
    // 1/64px layout unit and the sum can sit up to 2/64 off the exact line box.
    expect(req.pitch, "one bar per line box in .turn-req-text").toBeCloseTo(req.line, 1);
    expect(reply.pitch, "one bar per line box in .message.assistant").toBeCloseTo(reply.line, 1);
    expect(req.interior).toBeCloseTo(req.line, 1);
    expect(reply.interior).toBeCloseTo(reply.line, 1);
  });
});

describe(".skeleton-rows", () => {
  it("dissolves into the real list, so the list's own gap separates its rows", () => {
    const list = document.createElement("div");
    list.style.display = "flex";
    list.style.flexDirection = "column";
    list.style.gap = "12px";
    const wrap = document.createElement("div");
    wrap.className = "skeleton-rows";
    const first = document.createElement("div");
    const second = document.createElement("div");
    for (const row of [first, second]) {
      row.style.blockSize = "10px";
      wrap.appendChild(row);
    }
    list.appendChild(wrap);
    stage.appendChild(list);

    expect(getComputedStyle(wrap).display).toBe("contents");
    const separation = second.getBoundingClientRect().top - first.getBoundingClientRect().bottom;
    expect(separation, "the real list's gap reaches the placeholder rows").toBeCloseTo(12, 1);

    // The control: without the contents wrap the two rows are ONE flex item's
    // children, so the list's gap never applies between them and they touch.
    wrap.classList.remove("skeleton-rows");
    const flush = second.getBoundingClientRect().top - first.getBoundingClientRect().bottom;
    expect(flush).toBeCloseTo(0, 1);
  });

  it("declares its display in exactly one rule, and nothing else contests it", () => {
    // A second `display` on this class in any slice would decide the shape by
    // MANIFEST order rather than by the rule above.
    const writers: string[] = [];
    for (const [path, css] of Object.entries(
      import.meta.glob<string>("./css/*.css", { query: "?raw", import: "default", eager: true }),
    )) {
      for (const { selector, body } of allRules(css)) {
        if (
          selector.split(",").some((s) => s.trim() === ".skeleton-rows") &&
          /(?:^|[;{\s])display\s*:/.test(body)
        ) {
          writers.push(path);
        }
      }
    }
    expect(writers).toEqual(["./css/30-utilities.css"]);
  });
});

describe("skeletonRows' three cell shapes", () => {
  it("wraps a cell that names its own class, with the bar inside it", () => {
    const out = skeletonRows("list-row docs-skel-row", [[{ cls: "list-row-name", w: "62%" }]]);
    const row = out.firstElementChild;
    const cell = row?.firstElementChild;

    expect(row?.className).toBe("list-row docs-skel-row");
    expect(cell?.className).toBe("list-row-name");
    expect(cell?.children).toHaveLength(1);
    const inner = cell?.firstElementChild as HTMLElement | null | undefined;
    expect(inner?.className).toBe("skeleton skeleton-line");
    expect(inner?.style.width).toBe("62%");
  });

  it("emits a named bar as a DIRECT child of the row, and NOT beside skeleton-line", () => {
    const out = skeletonRows("docs-skel-row", [[{ bar: "docs-skel-name", w: "48%" }]]);
    const bar = out.firstElementChild?.firstElementChild as HTMLElement | null | undefined;

    // Direct child: a wrapper here would be a stretched column item holding the
    // bar, one box more than the real row has.
    expect(bar?.className).toBe("skeleton docs-skel-name");
    expect(bar?.style.width).toBe("48%");
    // THE half of this case that would have caught the silent resize at /docs.
    // `.docs-skel-name` is 0.875rem and `.skeleton-line` is 0.75rem, so a bar
    // carrying both takes whichever slice comes later in the MANIFEST.
    expect(bar?.classList.contains("skeleton-line")).toBe(false);
    expect(out.querySelectorAll(".skeleton-line")).toHaveLength(0);
  });

  it("reserves a cell with neither class nor width as a bare box", () => {
    const out = skeletonRows("ev-tl-lane", [[{}]]);
    const cell = out.firstElementChild?.firstElementChild as HTMLElement | null | undefined;

    expect(cell?.tagName).toBe("DIV");
    expect(cell?.className).toBe("");
    expect(cell?.children).toHaveLength(0);
    // A reserved control column says nothing, so it must not shimmer.
    expect(out.querySelectorAll(".skeleton")).toHaveLength(0);
  });

  it("gives a width with no bar class the chrome default", () => {
    const out = skeletonRows("fb-row fb-row-skel", [[{ w: "4rem" }]]);
    const bar = out.firstElementChild?.firstElementChild as HTMLElement | null | undefined;
    expect(bar?.className).toBe("skeleton skeleton-line");
  });

  it("carries aria-hidden on the root and nowhere below it", () => {
    const out = skeletonRows("list-row", [
      [{ cls: "list-row-name", w: "62%" }, {}],
      [{ bar: "docs-skel-name", w: "48%" }],
    ]);
    expect(out.getAttribute("aria-hidden")).toBe("true");
    expect(out.querySelectorAll("[aria-hidden]")).toHaveLength(0);
    expect(out.children).toHaveLength(2);
  });
});
