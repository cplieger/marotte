// The run bar's geometry and its motion, measured against the real assembled cascade rather than
// reasoned about.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { page as browser } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildExecSummary, type ExecSummaryView } from "./exec-view/summary.js";
import type { ExecNode, ExecRun } from "./exec-view/model.js";

/** A token's rendered length, read off a real element rather than restated. */
function lengthPx(root: HTMLElement, expr: string): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = expr;
  root.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

const MARK_STATUS: Record<string, string | undefined> = {
  running: "working",
  waiting: "waiting",
  input: "input",
};

let style: HTMLStyleElement;
let host: HTMLElement;
/** The summaries `mountRows` painted, in row order, so a test can repaint one in place. */
let summaries: ExecSummaryView[] = [];

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The composer band as `static/index.html` builds it, with the transcript above it so the
 *  shrink is measurable. `#chat-view` is the flex column both live in. */
function mountBand(states: readonly string[]): {
  bar: HTMLUListElement;
  wrap: HTMLElement;
  box: HTMLElement;
  dock: HTMLElement;
  rows: HTMLElement[];
} {
  host = document.createElement("div");
  host.id = "chat-view";
  host.className = "view";
  // A definite height, or the flex column has nothing to divide between the transcript and the bar
  // and the shrink cannot be observed.
  host.style.blockSize = "600px";
  host.style.inlineSize = "1000px";
  host.style.display = "flex";
  host.style.flexDirection = "column";

  // The real nesting: `#messages-wrap-outer` is the FLEX CHILD that the band's height is taken out
  // of, and `#messages-wrap` is `position: absolute; inset: 0` inside it, so the scroller follows
  // the wrapper exactly. Measuring the scroller measures the shrink.
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const wrap = document.createElement("div");
  wrap.id = "messages-wrap";
  outer.appendChild(wrap);

  const form = document.createElement("form");
  form.id = "prompt-form";
  form.className = "bottom-bar";

  const dock = document.createElement("div");
  dock.id = "decision-dock";
  dock.className = "decision-dock hidden";

  const bar = document.createElement("ul");
  bar.id = "run-bar";
  bar.className = "run-bar";

  const rows = states.map((state) => {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    li.dataset["state"] = state;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "run-bar-open";
    for (const cls of [
      "run-bar-glyph",
      "run-bar-name",
      "run-bar-state",
      "run-bar-steps",
      "run-bar-clock",
    ]) {
      const span = document.createElement("span");
      span.className = cls;
      // THE GLYPH IS KEYED ON ITS OWN `data-status`, in the WORKFLOW MARK's vocabulary rather than
      // the row's — the two differ for the state that matters most (`running` on the row, `working`
      // on the mark, which is the tab strip's word), and 12-tabs.css paints all three surfaces off
      // that attribute.
      if (cls === "run-bar-glyph") {
        const mark = MARK_STATUS[state];
        if (mark !== undefined) {
          span.dataset["status"] = mark;
        }
      } else {
        span.textContent = cls === "run-bar-name" ? "nightly sweep" : "x";
      }
      btn.appendChild(span);
    }
    li.appendChild(btn);
    bar.appendChild(li);
    return li;
  });

  const box = document.createElement("div");
  box.id = "prompt-box";
  box.className = "prompt-box";
  const ta = document.createElement("textarea");
  ta.id = "prompt-input";
  box.appendChild(ta);

  const barHost = document.createElement("div");
  barHost.className = "run-bar-host";
  barHost.append(bar);
  form.append(dock, barHost, box);
  host.append(outer, form);
  document.body.appendChild(host);
  return { bar, wrap, box, dock, rows };
}

afterAll(() => {
  host?.remove();
});

describe("the run bar's geometry", () => {
  it("ends on the same edges as the prompt box and the dock", () => {
    const { bar, box, dock } = mountBand(["running"]);
    dock.classList.remove("hidden");

    // The host draws the box; the list is its content.
    const barRect = bar.parentElement!.getBoundingClientRect();
    const boxRect = box.getBoundingClientRect();
    const dockRect = dock.getBoundingClientRect();

    expect(barRect.left).toBeCloseTo(boxRect.left, 1);
    expect(barRect.right).toBeCloseTo(boxRect.right, 1);
    expect(barRect.left).toBeCloseTo(dockRect.left, 1);
    expect(barRect.right).toBeCloseTo(dockRect.right, 1);
    host.remove();
  });

  it("shrinks the transcript by exactly its own height and covers nothing", () => {
    const { bar, wrap } = mountBand(["running", "waiting"]);
    const shown = wrap.getBoundingClientRect();
    const box = bar.parentElement!;
    const barRect = box.getBoundingClientRect();
    expect(barRect.height).toBeGreaterThan(0);
    // The MARGIN box is what the band grows by: the region carries the same `margin-block-end` the
    // dock and the steer stack do, which is the gap between them rather than part of the bar.
    const margin = Number.parseFloat(getComputedStyle(box).marginBlockEnd);
    expect(margin).toBeGreaterThan(0);

    // The `.hidden` utility is `display: none !important` and the host follows its list, so the
    // region's whole box leaves the flex column and the transcript takes the space back.
    bar.classList.add("hidden");
    const hidden = wrap.getBoundingClientRect();

    expect(hidden.height - shown.height).toBeCloseTo(barRect.height + margin, 0);
    // And while it is shown, the two boxes do not overlap: the bar is a sibling that grows the
    // band, not an overlay.
    expect(shown.bottom).toBeLessThanOrEqual(barRect.top + 0.5);
    host.remove();
  });

  it("takes its hit target from the shared floor, at whichever tier is in force", () => {
    const { rows } = mountBand(["running"]);
    const btn = rows[0]?.querySelector<HTMLElement>(".run-bar-open") ?? null;
    expect(btn).not.toBeNull();
    if (btn === null) {
      return;
    }

    // The row declares no `min-height`, so it RESOLVES 61-mcp-tools.css's `:where(button, …)`
    // token.
    expect(Number.parseFloat(getComputedStyle(btn).minHeight)).toBeCloseTo(
      lengthPx(host, "var(--hit-floor)"),
      1,
    );

    // The coarse tier is where the floor BINDS: 2.75rem is above the row's own box, so it is the
    // thing deciding the rendered target. The tier is the pointer rather than a width
    // (01-tokens.css), so `data-pointer` is how a test reaches it.
    const root = document.documentElement;
    const had = root.getAttribute("data-pointer");
    root.setAttribute("data-pointer", "coarse");
    try {
      const floor = lengthPx(host, "var(--hit-floor)");
      expect(floor, "the coarse tier's floor is above the row's own height").toBeGreaterThan(27);
      expect(btn.getBoundingClientRect().height).toBeCloseTo(floor, 1);
    } finally {
      if (had === null) {
        root.removeAttribute("data-pointer");
      } else {
        root.setAttribute("data-pointer", had);
      }
    }
    host.remove();
  });
});

describe("the run bar's state column", () => {
  it("beats the working mark and holds the waiting one still", () => {
    // The IN-FLIGHT AXIS, restated for the mark: motion means work is moving, and `waiting` is the
    // same ring standing still.
    const { rows } = mountBand(["running", "waiting"]);
    const [running, waiting] = rows;
    const glyph = (row: HTMLElement | undefined): Element | null =>
      row?.querySelector(".run-bar-glyph") ?? null;

    // `{ subtree: true }` because the animation is on the ::before overlay that closes the ring's
    // hole.
    const beating = glyph(running)?.getAnimations({ subtree: true }) ?? [];
    const names = beating.map((a) => (a as CSSAnimation).animationName).sort();
    expect(names, "the working mark closes its hole and seals it").toEqual([
      "vk-mark-close",
      "vk-mark-seal",
    ]);
    const seconds = Number.parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue("--dot-beat-dur"),
    );
    expect(seconds, "--dot-beat-dur resolves").toBeGreaterThan(0);
    for (const anim of beating) {
      expect(anim.effect?.getComputedTiming().duration).toBe(seconds * 1000);
    }

    expect(glyph(waiting)?.getAnimations({ subtree: true }).length).toBe(0);
    host.remove();
  });

  it("draws the mark itself at the dot token's size, and reserves that box with no state", () => {
    // The MARK is the element now, not a ::before ring inside it: `box-sizing: border-box` is what
    // lets a 2px band paint inside `--dot-size` rather than around it, so the element's own box is
    // the assertion.
    const { rows } = mountBand(["running", "waiting", "input", "unknown"]);
    const dot = lengthPx(host, "var(--dot-size)");
    expect(dot).toBeGreaterThan(0);
    for (const row of rows) {
      const glyph = row.querySelector(".run-bar-glyph");
      expect(glyph).not.toBeNull();
      if (glyph === null) {
        continue;
      }
      const box = glyph.getBoundingClientRect();
      expect(box.width, `${row.dataset["state"]} width`).toBeCloseTo(dot, 1);
      expect(box.height, `${row.dataset["state"]} height`).toBeCloseTo(dot, 1);
    }
    host.remove();
  });
});

// One box for every run count, its rows plain lines on one column grid.
describe("the run bar's box", () => {
  const step = (id: string, state: ExecNode["state"]): ExecNode => ({
    path: id,
    label: id,
    kind: "step",
    state,
    children: [],
  });
  const runOf = (label: string, done: number, total: number): ExecRun => ({
    label,
    state: "running",
    live: true,
    nodes: Array.from({ length: total }, (_, i) =>
      step(`${label}-${String(i)}`, i < done ? "ok" : i === done ? "running" : "pending"),
    ),
  });

  /** The chat's band with real summary rows, and the fold row first when `fold` is set. */
  function mountRows(runs: readonly ExecRun[], fold: boolean, width = 1000): HTMLUListElement {
    host = document.createElement("div");
    host.style.inlineSize = `${String(width)}px`;
    const form = document.createElement("form");
    form.className = "bottom-bar";
    const barHost = document.createElement("div");
    barHost.className = "run-bar-host";
    const bar = document.createElement("ul");
    bar.className = "run-bar";
    if (fold) {
      const foldEl = document.createElement("div");
      foldEl.className = "run-bar-fold";
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "run-bar-open run-bar-toggle";
      btn.setAttribute("aria-expanded", "true");
      btn.textContent = `${String(runs.length)} workflows running`;
      foldEl.append(btn);
      barHost.append(foldEl);
    }
    summaries = [];
    for (const r of runs) {
      const li = document.createElement("li");
      li.className = "run-bar-row";
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "run-bar-open";
      const glyph = document.createElement("span");
      glyph.className = "run-bar-glyph";
      glyph.dataset["status"] = "working";
      const summary = buildExecSummary({ alertHost: btn });
      btn.append(glyph, summary.root);
      summary.paint(r);
      summaries.push(summary);
      li.append(btn);
      bar.append(li);
    }
    barHost.append(bar);
    form.append(barHost);
    host.append(form);
    document.body.append(host);
    return bar;
  }

  const RUNS = [
    runOf("fix-wake-turn-send-cancels", 1, 3),
    runOf("investigate-run-dock-waiting-redesign", 8, 27),
    runOf("feat-forgeapi-stored-token-read", 6, 12),
  ];

  for (const [count, fold] of [
    [1, false],
    [2, false],
    [3, true],
  ] as const) {
    it(`draws ${String(count)} run${count > 1 ? "s" : ""} as lines of one bordered box`, () => {
      const bar = mountRows(RUNS.slice(0, count), fold);
      const box = bar.parentElement!;
      expect(getComputedStyle(box).borderTopWidth).toBe("1px");
      for (const btn of bar.querySelectorAll<HTMLElement>(".run-bar-open")) {
        expect(getComputedStyle(btn).borderTopWidth, "a line, not a box").toBe("0px");
        const inner = box.getBoundingClientRect();
        const line = btn.getBoundingClientRect();
        expect(line.left).toBeGreaterThan(inner.left);
        expect(line.right).toBeLessThan(inner.right);
      }
      host.remove();
    });
  }

  it("starts and ends every bar at one x, whatever the titles and phrases", () => {
    const bar = mountRows(RUNS, false);
    const minis = [...bar.querySelectorAll<HTMLElement>(".ev-mini")].map((m) =>
      m.getBoundingClientRect(),
    );
    expect(new Set(minis.map((r) => Math.round(r.left))).size).toBe(1);
    expect(new Set(minis.map((r) => Math.round(r.right))).size).toBe(1);
    const titles = [...bar.querySelectorAll<HTMLElement>(".ev-sum-label")];
    expect(titles.every((t) => getComputedStyle(t).textOverflow === "ellipsis")).toBe(true);
    host.remove();
  });

  it("caps the title column, so a very long title ellipsizes", () => {
    const long = runOf(
      "a-workflow-title-that-is-far-longer-than-any-column-should-ever-hold",
      1,
      4,
    );
    const bar = mountRows([long, RUNS[0]!], false);
    const title = bar.querySelector<HTMLElement>(".ev-sum-label")!;
    const rem = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
    expect(title.getBoundingClientRect().width).toBeLessThanOrEqual(18 * rem + 0.5);
    expect(title.scrollWidth).toBeGreaterThan(title.clientWidth);
    host.remove();
  });

  it("keeps a row's hover clear of its neighbour", () => {
    const bar = mountRows(RUNS, false);
    const [a, b] = [...bar.querySelectorAll<HTMLElement>(".run-bar-open")].map((x) =>
      x.getBoundingClientRect(),
    );
    expect(b!.top - a!.bottom).toBeGreaterThan(0);
    host.remove();
  });

  it("lays a phone row out as two lines, at or above the hit floor", () => {
    document.documentElement.setAttribute("data-pointer", "coarse");
    try {
      const bar = mountRows(RUNS.slice(0, 1), false, 360);
      const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
      const title = btn.querySelector<HTMLElement>(".ev-sum-label")!.getBoundingClientRect();
      const state = btn.querySelector<HTMLElement>(".ev-sum-state")!.getBoundingClientRect();
      expect(state.top).toBeGreaterThanOrEqual(title.bottom - 1);
      const height = btn.getBoundingClientRect().height;
      expect(height).toBeGreaterThanOrEqual(lengthPx(host, "var(--hit-floor)"));
      expect(height).toBeLessThanOrEqual(56);
    } finally {
      document.documentElement.removeAttribute("data-pointer");
      host.remove();
    }
  });

  const SEVEN = Array.from({ length: 7 }, (_, i) => runOf(`run-${String(i)}`, 1, 4));

  /** The open list's lines, and how many sit wholly inside the list's own box, once each row's
   *  entry rise has landed. */
  function visibleLines(bar: HTMLElement): { all: number; whole: number } {
    for (const a of bar.getAnimations({ subtree: true })) {
      if (a instanceof CSSTransition) {
        a.finish();
      }
    }
    const box = bar.getBoundingClientRect();
    const lines = [...bar.querySelectorAll<HTMLElement>(":scope > li")];
    const whole = lines.filter((l) => l.getBoundingClientRect().bottom <= box.bottom + 0.5);
    return { all: lines.length, whole: whole.length };
  }

  describe("on a phone, an open list", () => {
    afterAll(async () => {
      await browser.viewport(1280, 720);
    });

    it("shows every run while they fit in half the viewport", async () => {
      await browser.viewport(390, 844);
      document.documentElement.setAttribute("data-pointer", "coarse");
      try {
        const bar = mountRows(SEVEN.slice(0, 4), true, 374);
        const { all, whole } = visibleLines(bar);
        expect(whole).toBe(all);
        expect(bar.scrollHeight).toBeLessThanOrEqual(bar.clientHeight + 1);
      } finally {
        document.documentElement.removeAttribute("data-pointer");
        host.remove();
      }
    });

    it("grows to half the viewport and scrolls only past it", async () => {
      await browser.viewport(360, 740);
      document.documentElement.setAttribute("data-pointer", "coarse");
      try {
        const bar = mountRows([...SEVEN, ...SEVEN], true, 344);
        expect(bar.scrollHeight).toBeGreaterThan(bar.clientHeight);
        // The cap is the box's content: the fold row, its gap and the list together.
        const box = bar.parentElement!;
        const cs = getComputedStyle(box);
        const chrome =
          parseFloat(cs.paddingTop) +
          parseFloat(cs.paddingBottom) +
          parseFloat(cs.borderTopWidth) +
          parseFloat(cs.borderBottomWidth);
        expect(box.getBoundingClientRect().height - chrome).toBeCloseTo(0.5 * 740, 0);
      } finally {
        document.documentElement.removeAttribute("data-pointer");
        host.remove();
      }
    });
  });

  it("keeps the desktop cap at a quarter of the viewport, 16rem at most", async () => {
    await browser.viewport(1280, 900);
    try {
      const bar = mountRows(SEVEN, true);
      const box = bar.parentElement!;
      const cs = getComputedStyle(box);
      const chrome = 2 * parseFloat(cs.paddingTop) + 2 * parseFloat(cs.borderTopWidth);
      expect(parseFloat(cs.maxBlockSize) - chrome).toBe(225);
    } finally {
      host.remove();
      await browser.viewport(1280, 720);
    }
  });

  describe("a run that failed", () => {
    const failed: ExecRun = {
      ...runOf("broke-on-step-two", 1, 3),
      state: "fail",
      live: false,
      alert: { kind: "failed", text: "Step two failed" },
    };

    it("draws no wash: its red mark and state word say it failed, on the box's own surface", () => {
      const bar = mountRows([failed, RUNS[0]!], false);
      const btn = bar.querySelector<HTMLElement>(".run-bar-open[data-alert='failed']")!;
      const cs = getComputedStyle(btn);
      expect(cs.backgroundImage).toBe("none");
      expect(cs.backgroundColor).toBe("rgba(0, 0, 0, 0)");
      const mark = btn.querySelector<HTMLElement>(".ev-alert-icon")!;
      const red = document.createElement("div");
      red.style.color = "var(--c-red)";
      host.append(red);
      expect(getComputedStyle(mark).color).toBe(getComputedStyle(red).color);
      expect(btn.querySelector(".ev-sum-state")!.textContent).toBe("failed");
      host.remove();
    });
  });

  describe("a run that waits on the reader", () => {
    const waiting: ExecRun = {
      ...runOf("needs-an-answer", 1, 3),
      state: "input",
      alert: { kind: "input", text: "Waiting for answer: ship it?" },
    };

    it("draws no wash: its yellow mark says it waits, on the box's own surface", () => {
      const bar = mountRows([waiting, RUNS[0]!], false);
      const box = bar.parentElement!;
      const btn = bar.querySelector<HTMLElement>(".run-bar-open[data-alert]")!;
      const cs = getComputedStyle(btn);
      expect(cs.backgroundImage).toBe("none");
      expect(cs.backgroundColor).toBe("rgba(0, 0, 0, 0)");
      const glyph = btn.querySelector<HTMLElement>(".run-bar-glyph")!;
      glyph.dataset["status"] = "input";
      const mark = document.createElement("div");
      mark.style.color = "var(--c-dot-input)";
      host.append(mark);
      expect(getComputedStyle(glyph).borderTopColor).toBe(getComputedStyle(mark).color);
      expect(getComputedStyle(box).borderTopLeftRadius).toBe("12px");
      expect(getComputedStyle(btn).borderTopLeftRadius).toBe("7px");
      host.remove();
    });

    const QUESTION = `Waiting for answer: ${"which branch should the release be cut from? ".repeat(8)}`;
    // A start time, so the row has an elapsed clock for the question to stop short of.
    const started = (r: ExecRun): ExecRun => ({
      ...r,
      nodes: r.nodes.map((n, i) =>
        i === 0 ? { ...n, start: new Date(Date.now() - 46 * 3_600_000).toISOString() } : n,
      ),
    });
    const asking = started({ ...waiting, alert: { kind: "input", text: QUESTION } });
    const answered = started(runOf("needs-an-answer", 1, 3));
    const plain = started(RUNS[0]!);

    const rect = (btn: HTMLElement, sel: string): DOMRect =>
      btn.querySelector<HTMLElement>(sel)!.getBoundingClientRect();
    const shown = (btn: HTMLElement, sel: string): boolean =>
      getComputedStyle(btn.querySelector<HTMLElement>(sel)!).display !== "none";

    it("puts the question in the progress and bar's place on the title's one line", () => {
      const bar = mountRows([asking, plain], false);
      const [btn, other] = [...bar.querySelectorAll<HTMLElement>(".run-bar-open")] as [
        HTMLElement,
        HTMLElement,
      ];
      expect(shown(btn, ".ev-sum-progress")).toBe(false);
      expect(shown(btn, ".ev-mini")).toBe(false);
      expect(shown(other, ".ev-sum-progress")).toBe(true);
      expect(shown(other, ".ev-mini")).toBe(true);

      const line = rect(btn, ".ev-alert-line");
      const title = rect(btn, ".ev-sum-label");
      const state = rect(btn, ".ev-sum-state");
      const clock = rect(btn, ".ev-sum-clock");
      // One line: the sentence sits level with the title, between the state word and the clock,
      // starting where the other row's phrase starts.
      expect(line.top + line.height / 2).toBeCloseTo(title.top + title.height / 2, 0);
      expect(line.height).toBeLessThan(title.height * 1.5);
      expect(line.left).toBeGreaterThanOrEqual(state.right);
      expect(line.right).toBeLessThanOrEqual(clock.left);
      expect(line.left).toBeCloseTo(rect(other, ".ev-sum-progress").left, 0);
      expect(btn.getBoundingClientRect().height).toBeCloseTo(
        other.getBoundingClientRect().height,
        0,
      );
      // A long question is clipped to that line rather than wrapped or widening a column.
      const text = btn.querySelector<HTMLElement>(".ev-alert-text")!;
      expect(text.scrollWidth).toBeGreaterThan(text.clientWidth);
      expect(text.scrollHeight).toBeLessThanOrEqual(text.clientHeight);
      expect(btn.scrollWidth).toBeLessThanOrEqual(btn.clientWidth);
      host.remove();
    });

    it("brings the phrase and bar back once answered, with no reflow of the title or clock", () => {
      const bar = mountRows([asking], false);
      const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
      const cols = (): number[] =>
        [".ev-sum-label", ".ev-sum-state", ".ev-sum-clock"].flatMap((sel) => {
          const r = rect(btn, sel);
          return [Math.round(r.left), Math.round(r.right), Math.round(r.top)];
        });
      const before = cols();
      summaries[0]!.paint(answered);
      expect(btn.dataset["alert"]).toBeUndefined();
      expect(shown(btn, ".ev-sum-progress")).toBe(true);
      expect(shown(btn, ".ev-mini")).toBe(true);
      expect(btn.querySelector(".ev-alert-line")).toBeNull();
      // The state word changes with the answer, so only the title and the clock hold still.
      const after = cols();
      expect([before[0], before[1], before[2]]).toEqual([after[0], after[1], after[2]]);
      expect([before[6], before[7], before[8]]).toEqual([after[6], after[7], after[8]]);
      host.remove();
    });

    it("leaves a failed or paused alert as a second line under the columns", () => {
      const failed: ExecRun = { ...asking, alert: { kind: "failed", text: "Step two failed" } };
      const bar = mountRows([failed], false);
      const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
      expect(shown(btn, ".ev-sum-progress")).toBe(true);
      expect(shown(btn, ".ev-mini")).toBe(true);
      expect(rect(btn, ".ev-alert-line").top).toBeGreaterThanOrEqual(
        rect(btn, ".ev-sum-label").bottom - 1,
      );
      host.remove();
    });

    describe("on a phone", () => {
      afterAll(async () => {
        await browser.viewport(1280, 720);
      });

      for (const [w, h] of [
        [390, 844],
        [360, 740],
      ] as const) {
        it(`${String(w)}px: the question takes the phrase's place and the row stays two lines`, async () => {
          await browser.viewport(w, h);
          document.documentElement.setAttribute("data-pointer", "coarse");
          try {
            const bar = mountRows([asking, plain], false, w - 16);
            const [btn, other] = [...bar.querySelectorAll<HTMLElement>(".run-bar-open")] as [
              HTMLElement,
              HTMLElement,
            ];
            expect(shown(btn, ".ev-sum-progress")).toBe(false);
            expect(shown(btn, ".ev-mini")).toBe(false);
            const line = rect(btn, ".ev-alert-line");
            const state = rect(btn, ".ev-sum-state");
            expect(line.left).toBeGreaterThanOrEqual(state.right);
            expect(line.top).toBeGreaterThanOrEqual(rect(btn, ".ev-sum-label").bottom - 1);
            expect(line.right).toBeLessThanOrEqual(btn.getBoundingClientRect().right);
            // One clipped line, not a wrapped one cut short of the room it was given.
            const text = btn.querySelector<HTMLElement>(".ev-alert-text")!;
            expect(text.scrollWidth).toBeGreaterThan(text.clientWidth);
            expect(text.scrollHeight).toBeLessThanOrEqual(text.clientHeight);
            expect(btn.getBoundingClientRect().height).toBeCloseTo(
              other.getBoundingClientRect().height,
              0,
            );
            expect(btn.scrollWidth).toBeLessThanOrEqual(btn.clientWidth);
          } finally {
            document.documentElement.removeAttribute("data-pointer");
            host.remove();
          }
        });
      }
    });
  });
});
