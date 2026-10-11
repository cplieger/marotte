import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { binTurns } from "./rail-select.js";
import type { TurnSummary } from "./rail-merge.js";

/** Wide enough that the map is shown and the resume control docks in its column: both turn on
 *  `@container chat-area (width >= 57.5rem)`. */
const WIDE_PX = 1120;
const NARROW_PX = 800;
/** Sub-pixel slack. Every number here is a used value the engine rounds. */
const EPS = 0.5;

function near(actual: number, expected: number, what: string): void {
  expect(
    Math.abs(actual - expected),
    `${what}: ${String(actual)} vs ${String(expected)}`,
  ).toBeLessThanOrEqual(EPS);
}

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

let area: HTMLElement | undefined;

afterEach(() => {
  area?.remove();
  area = undefined;
  delete document.documentElement.dataset["pointer"];
});

function lengthOf(host: HTMLElement, token: string): number {
  const probe = document.createElement("div");
  probe.style.cssText = `position:absolute;visibility:hidden;block-size:var(${token})`;
  host.appendChild(probe);
  const px = parseFloat(getComputedStyle(probe).blockSize);
  probe.remove();
  return px;
}

interface Map {
  outer: HTMLElement;
  nav: HTMLElement;
  track: HTMLElement;
  stack: HTMLElement;
  buttons: HTMLElement[];
  resume: HTMLElement;
}

function turn(n: number): TurnSummary {
  return { id: `m${String(n)}`, n, outcome: "completed", ts: n * 60_000 };
}

/** The real nesting: `#chat-area` is the container the queries key on, and the map and the resume
 *  control position against `#messages-wrap-outer`. The rows are laid out the way `render()` lays
 *  them out, from `binTurns` over the measured track. */
function buildMap(opts: { turns: number; width?: number; height?: number }): Map {
  area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = `position:fixed;top:0;left:0;width:${String(opts.width ?? WIDE_PX)}px;height:${String(opts.height ?? 600)}px;display:flex;flex-direction:column`;
  const toolbar = document.createElement("div");
  toolbar.className = "chat-toolbar";
  const view = document.createElement("div");
  view.id = "chat-view";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const scroller = document.createElement("div");
  scroller.id = "messages-wrap";
  outer.appendChild(scroller);

  const nav = document.createElement("nav");
  nav.className = "turn-map";
  nav.toggleAttribute("data-shown", true);
  const prev = document.createElement("button");
  prev.className = "turn-map-step";
  const stack = document.createElement("ol");
  stack.className = "turn-map-stack";
  const track = document.createElement("div");
  track.className = "turn-map-track";
  track.appendChild(stack);
  const next = document.createElement("button");
  next.className = "turn-map-step";
  nav.append(prev, track, next);
  outer.appendChild(nav);

  const resume = document.createElement("button");
  resume.id = "scroll-bottom";
  resume.type = "button";
  resume.textContent = "Latest";
  outer.appendChild(resume);

  view.appendChild(outer);
  area.append(toolbar, view);
  document.body.appendChild(area);

  const set = Array.from({ length: opts.turns }, (_, i) => turn(i + 1));
  const layout = binTurns(set, track.clientHeight);
  stack.style.setProperty("--turn-map-slots", String(layout.slots));
  stack.style.setProperty("--turn-map-total", String(layout.total));
  for (const bin of layout.bins) {
    const li = document.createElement("li");
    li.className = "turn-pill";
    li.style.setProperty("--rail-at", String(bin.at));
    const a = document.createElement("a");
    a.className = "turn-pill-link";
    a.href = `#turn-${String(bin.first.n)}`;
    li.appendChild(a);
    stack.appendChild(li);
  }
  return { outer, nav, track, stack, buttons: [prev, next], resume };
}

function pills(m: Map): HTMLElement[] {
  return [...m.stack.querySelectorAll<HTMLElement>(".turn-pill")];
}

describe("the stack is only as tall as its turns need", () => {
  it("draws a 5-turn chat 40px tall, not track-tall", () => {
    const m = buildMap({ turns: 5 });
    near(m.stack.getBoundingClientRect().height, 40, "stack height");
  });

  it("puts each row at --rail-at times the stack's travel", () => {
    const m = buildMap({ turns: 5 });
    const top = m.stack.getBoundingClientRect().top;
    expect(pills(m).map((p) => Math.round(p.getBoundingClientRect().top - top))).toEqual([
      0, 8, 16, 24, 32,
    ]);
    expect(pills(m).map((p) => Math.round(p.getBoundingClientRect().height))).toEqual([
      8, 8, 8, 8, 8,
    ]);
  });

  it("centres the stack in its track", () => {
    const m = buildMap({ turns: 5 });
    const track = m.track.getBoundingClientRect();
    const stack = m.stack.getBoundingClientRect();
    expect(track.height).toBeGreaterThan(stack.height);
    near(stack.top - track.top, track.bottom - stack.bottom, "space above against space below");
  });

  it("measures a track whose height does not follow the row count", () => {
    // `render()` bins against this box, so a height that followed the rows would feed the bin
    // count back into the next layout.
    const few = buildMap({ turns: 3 }).track.getBoundingClientRect().height;
    area?.remove();
    const many = buildMap({ turns: 1000 }).track.getBoundingClientRect().height;
    near(few, many, "track height for 3 turns against 1000");
  });

  it("paints no axis line down the track", () => {
    const m = buildMap({ turns: 5 });
    expect(getComputedStyle(m.nav, "::before").content).toBe("none");
    expect(getComputedStyle(m.stack, "::before").content).toBe("none");
  });

  it("never gains a scrollbar of its own", () => {
    const m = buildMap({ turns: 300 });
    expect(getComputedStyle(m.nav).overflowY).toBe("visible");
    expect(getComputedStyle(m.stack).overflowY).toBe("visible");
  });
});

describe("a long chat fills the track at no less than 2px a row", () => {
  it("tiles 200 rows with no overlap and no gap", () => {
    const m = buildMap({ turns: 200, height: 900 });
    const rows = pills(m).map((p) => p.getBoundingClientRect());
    expect(rows).toHaveLength(200);
    for (let i = 1; i < rows.length; i++) {
      const prev = rows[i - 1];
      const cur = rows[i];
      if (prev === undefined || cur === undefined) {
        continue;
      }
      expect(cur.height).toBeGreaterThanOrEqual(2 - EPS);
      expect(cur.height).toBeLessThanOrEqual(8 + EPS);
      near(cur.top, prev.bottom, `row ${String(i)} meets the one above`);
    }
  });

  it("draws 500 binned turns at the 2px pitch, the stack shorter than its track", () => {
    const m = buildMap({ turns: 500, height: 800 });
    const track = m.track.getBoundingClientRect().height;
    expect(track, "a track that bins 500 turns in pairs").toBeGreaterThanOrEqual(500);
    expect(track, "a track that bins 500 turns in pairs").toBeLessThan(1000);
    const rows = pills(m).map((p) => p.getBoundingClientRect().height);
    expect(rows).toHaveLength(250);
    for (const h of rows) {
      near(h, 2, "row pitch");
    }
    near(m.stack.getBoundingClientRect().height, 500, "stack height");
  });

  it("bins 1000 turns into rows of at least 2px and keeps the last at the foot", () => {
    const m = buildMap({ turns: 1000 });
    const stack = m.stack.getBoundingClientRect();
    const rows = pills(m).map((p) => p.getBoundingClientRect());
    expect(rows.length).toBeLessThan(1000);
    expect(rows[0]?.height ?? 0).toBeGreaterThanOrEqual(2 - EPS);
    near(rows[rows.length - 1]?.bottom ?? 0, stack.bottom, "last row's bottom");
    near(rows[0]?.top ?? 0, stack.top, "first row's top");
  });
});

describe("the map's foot clears the docked resume control", () => {
  it("leaves --sp-2 between the map and the control's target", () => {
    const m = buildMap({ turns: 5 });
    const gap = m.resume.getBoundingClientRect().top - m.nav.getBoundingClientRect().bottom;
    near(gap, lengthOf(m.outer, "--sp-2"), "clearance");
  });

  it("sizes the docked control to the hit floor", () => {
    const m = buildMap({ turns: 5 });
    near(
      m.resume.getBoundingClientRect().height,
      Math.max(lengthOf(m.outer, "--ctl-h-sm"), lengthOf(m.outer, "--hit-floor")),
      "control height",
    );
  });

  it("leaves the control floating at the column's centre on a coarse pointer, with no map", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    const m = buildMap({ turns: 5 });
    const box = m.resume.getBoundingClientRect();
    const outer = m.outer.getBoundingClientRect();
    near(box.left + box.width / 2, outer.left + outer.width / 2, "control centre");
  });
});

describe("the map draws only where the gutter holds it and a pointer can aim at it", () => {
  it("shows the map in a wide chat on a fine pointer", () => {
    const m = buildMap({ turns: 5 });
    expect(getComputedStyle(m.nav).display).toBe("flex");
  });

  it("hides the map under 57.5rem of chat area", () => {
    const m = buildMap({ turns: 5, width: NARROW_PX });
    expect(getComputedStyle(m.nav).display).toBe("none");
  });

  it("hides the map on a coarse pointer at any width", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    const m = buildMap({ turns: 5 });
    expect(getComputedStyle(m.nav).display).toBe("none");
  });

  it("hides a map the renderer has not marked shown, its track still measured", () => {
    const m = buildMap({ turns: 5 });
    const shownTrack = m.track.getBoundingClientRect().height;
    m.nav.removeAttribute("data-shown");
    expect(getComputedStyle(m.nav).visibility).toBe("hidden");
    for (const b of m.buttons) {
      expect(getComputedStyle(b).visibility).toBe("hidden");
    }
    expect(m.track.getBoundingClientRect().height).toBe(shownTrack);
    expect(shownTrack).toBeGreaterThan(0);
  });
});

describe("the step buttons meet the hit floor", () => {
  // Fine tier only: on a coarse pointer the map does not draw. They are the equivalent control
  // that lets the rows sit under the floor (WCAG 2.5.8).
  it("on a fine pointer", () => {
    const m = buildMap({ turns: 5 });
    const floor = lengthOf(m.outer, "--hit-floor");
    for (const b of m.buttons) {
      expect(b.getBoundingClientRect().height).toBeGreaterThanOrEqual(floor - EPS);
      expect(b.getBoundingClientRect().width).toBeGreaterThanOrEqual(floor - EPS);
    }
  });
});
