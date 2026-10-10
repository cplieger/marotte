import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// The four shapes, and the one rule about them CSS can enforce.

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

/** Every LENGTH UNIT a rule body declares, as a set. A family membership test rather than a
 *  substring search, because `0.75rem` contains `em` and a naive `includes("em")` reads the
 *  chrome class as an offender. */
function unitsIn(body: string): string[] {
  const found = new Set<string>();
  for (const m of body.matchAll(/\b\d*\.?\d+([a-z%]+)/gi)) {
    found.add(m[1]?.toLowerCase() ?? "");
  }
  return [...found].sort();
}

/** A run of three bars mounted in a real prose container, measured two ways. */
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
    // `lh` is TEXT BLOCK's unit — the one that makes a bar track a container's prose. A chrome bar
    // has no prose to track, so it may not reach for it.
    expect(unitsIn(body)).not.toContain("lh");

    // The one `em` is the WIDTH's fallback, and it is deliberate: both existing sites declare
    // `width: 10em`/`12em`, so a `rem` fallback would resize them. Per-site widths live in a custom
    // property, and this asserts the fallback is the only place a length appears on that axis.
    const inlineSize = /inline-size:\s*([^;]+);/.exec(body)?.[1] ?? "";
    expect(inlineSize).toBe("var(--skel-field-w, 10em)");
  });
});

describe("a prose bar tracks its container's line box", () => {
  it("stands on exactly one line box, in both of the transcript's prose containers", () => {
    const req = measureRun("turn-req-text");
    const reply = measureRun("message assistant");

    // The whole point of measuring two: the containers run at 1.5 and 1.6, so a bar sized off a
    // restated number would be right in at most one of them.
    expect(req.line, "the two containers' line boxes differ").not.toBeCloseTo(reply.line, 2);

    // Both margins are `calc((1lh - 0.7em) / 2)`, so each can round to Chromium's 1/64px layout
    // unit and the sum can sit up to 2/64 off the exact line box.
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

    // The control: without the contents wrap the two rows are ONE flex item's children, so the
    // list's gap never applies between them and they touch.
    wrap.classList.remove("skeleton-rows");
    const flush = second.getBoundingClientRect().top - first.getBoundingClientRect().bottom;
    expect(flush).toBeCloseTo(0, 1);
  });

  it("declares its display in exactly one rule, and nothing else contests it", () => {
    // A second `display` on this class in any slice would decide the shape by MANIFEST order rather
    // than by the rule above.
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
