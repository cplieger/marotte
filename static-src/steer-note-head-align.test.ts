// A workflow message's first row shares one vertical centre: at desktop width the glyph, run name,
// dot, step label and times; on a phone the glyph and run name, with the step and times on the row
// under them sharing their own. Each case runs in an IFRAME so the `width <= 48rem` query answers
// for the frame's width.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";

const SENT = Date.UTC(2026, 9, 9, 12, 0, 5);
const READ = SENT + 14_000;
// Step names only: the note renders the step name as label text and has no per-severity style.
const STEPS = ["notify", "success", "warning", "error", "info"] as const;
const STAMPS = [
  ["sent and delivered", true],
  ["sent only", false],
] as const;
const RUN = "grelot-watch-grelot-ops";
const LONG_RUN = "grelot-watch-grelot-ops-and-every-sibling-repository-in-the-fleet";

type Part = "icon" | "name" | "dot" | "label" | "times";

let style: HTMLStyleElement;
const frames: HTMLIFrameElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
  style.remove();
});

function frameOf(width: number): Document {
  const frame = document.createElement("iframe");
  frame.width = String(width);
  frame.height = "400";
  document.body.appendChild(frame);
  frames.push(frame);
  const doc = frame.contentDocument;
  if (doc === null) {
    throw new Error("iframe has no contentDocument");
  }
  const sheet = doc.createElement("style");
  sheet.textContent = style.textContent;
  doc.head.appendChild(sheet);
  return doc;
}

function mount(width: number, step: string, runLabel: string, delivered: boolean): HTMLElement {
  const note = buildSteerNote({
    text: "Watch tick: nothing new.",
    origin: "step",
    dropped: false,
    step,
    run: "wf_1",
    runLabel,
    onOpenRun: () => undefined,
    sentAt: SENT,
    tracked: delivered,
    ...(delivered ? { deliveredAt: READ } : {}),
  });
  const doc = frameOf(width);
  doc.body.appendChild(doc.adoptNode(note));
  return note;
}

/** The centre of an element's first line of text: the em box a reader lines up, not the element's
 *  box, which a taller box around the same line moves off it. */
function firstLineCentre(e: Element): number {
  const range = e.ownerDocument.createRange();
  range.selectNodeContents(e);
  const r = [...range.getClientRects()].find((x) => x.width > 0 && x.height > 0);
  if (r === undefined) {
    throw new Error(`no rendered text in .${e.className}`);
  }
  return r.top + r.height / 2;
}

function centres(note: HTMLElement, parts: readonly Part[]): Record<string, number> {
  const pick = (sel: string): Element => {
    const hit = note.querySelector(sel);
    if (hit === null) {
      throw new Error(`no ${sel}`);
    }
    return hit;
  };
  const out: Record<string, number> = {};
  for (const p of parts) {
    if (p === "icon") {
      const r = pick(".steer-note-head > .tool-icon svg").getBoundingClientRect();
      out[p] = r.top + r.height / 2;
    } else {
      const sel = {
        name: ".steer-note-run",
        dot: ".steer-note-run-sep",
        label: ".steer-note-label",
        times: ".steer-note-times",
      }[p];
      out[p] = firstLineCentre(pick(sel));
    }
  }
  return out;
}

function spread(c: Record<string, number>): number {
  const v = Object.values(c);
  return Math.max(...v) - Math.min(...v);
}

describe.each(STEPS)("the head of a workflow message from step %s", (step) => {
  describe.each(STAMPS)("stamped %s", (_stamp, delivered) => {
    it("puts the glyph, run name, dot, label and times on one centre at desktop width", () => {
      const c = centres(mount(900, step, RUN, delivered), [
        "icon",
        "name",
        "dot",
        "label",
        "times",
      ]);
      expect(spread(c), JSON.stringify(c)).toBeLessThanOrEqual(1);
    });

    it("puts the glyph and run name on one centre, and the label and times on another, on a phone", () => {
      const note = mount(375, step, RUN, delivered);
      const top = centres(note, ["icon", "name"]);
      const under = centres(note, ["label", "times"]);
      expect(spread(top), JSON.stringify(top)).toBeLessThanOrEqual(1);
      expect(spread(under), JSON.stringify(under)).toBeLessThanOrEqual(1);
    });
  });
});

describe("a workflow message's head with a run name too long for a phone", () => {
  it("keeps the glyph and the ellipsized name on one centre and the label and times on the next", () => {
    const note = mount(320, "notify", LONG_RUN, true);
    const name = note.querySelector<HTMLElement>(".steer-note-run");
    const top = centres(note, ["icon", "name"]);
    const under = centres(note, ["label", "times"]);
    expect(name?.scrollWidth).toBeGreaterThan(name?.clientWidth ?? Infinity);
    expect(spread(top), JSON.stringify(top)).toBeLessThanOrEqual(1);
    expect(spread(under), JSON.stringify(under)).toBeLessThanOrEqual(1);
  });
});
