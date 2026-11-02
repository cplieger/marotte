// A workflow update's header keeps the run's name legible: on a phone it takes a line of its own above the
// step and the times, and on a wide row it stays beside the step. Each case runs in an IFRAME so the
// stylesheet's `width <= 48rem` query answers for the frame's width, not the test page's.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";

let style: HTMLStyleElement;
const frames: HTMLIFrameElement[] = [];

function frameOf(width: number): Document {
  const frame = document.createElement("iframe");
  frame.width = String(width);
  frame.height = "600";
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

interface Header {
  run: HTMLElement | null;
  head: HTMLElement;
  icon: HTMLElement;
  label: HTMLElement;
  times: HTMLElement;
  text: HTMLElement;
}

const SENT = new Date(2026, 9, 18, 9, 58, 47).getTime();

function mountNote(doc: Document, withRun = true): Header {
  const run = withRun
    ? { run: "wf_run1", runLabel: "actions-reactive-owner-loading", onOpenRun: () => undefined }
    : {};
  const note = buildSteerNote({
    text: "CloudyLake ruling: apply your shared fix as builder items.\n- (1) the one-root shape;\n- (2) the test that pins it.",
    origin: "parent",
    dropped: false,
    step: "gate",
    ...run,
    sentAt: SENT,
    tracked: true,
    deliveredAt: SENT,
  });
  doc.body.appendChild(doc.adoptNode(note));
  const pick = (sel: string): HTMLElement => {
    const hit = note.querySelector<HTMLElement>(sel);
    if (hit === null) {
      throw new Error(`no ${sel}`);
    }
    return hit;
  };
  return {
    run: note.querySelector<HTMLElement>(".steer-note-run"),
    head: pick(".steer-note-head"),
    icon: pick(".steer-note-head > .tool-icon"),
    label: pick(".steer-note-label"),
    times: pick(".steer-note-times"),
    text: pick(".steer-note-text"),
  };
}

/** The left edge of each rendered line of `nodes`' text, keyed by the line's top. */
function lineStarts(...nodes: HTMLElement[]): Map<number, number> {
  const starts = new Map<number, number>();
  for (const n of nodes) {
    const range = n.ownerDocument.createRange();
    range.selectNodeContents(n);
    for (const r of range.getClientRects()) {
      if (r.width === 0) {
        continue;
      }
      const top = Math.round(r.top);
      starts.set(top, Math.min(starts.get(top) ?? Infinity, r.left));
    }
  }
  return starts;
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  for (const f of frames) {
    f.remove();
  }
  style.remove();
});

describe("a workflow update's header", () => {
  it("gives the run's name a line above the step and the times on a phone", () => {
    const { run, label, times } = mountNote(frameOf(360));
    if (run === null) {
      throw new Error("no run label");
    }
    const r = run.getBoundingClientRect();

    expect(run.scrollWidth).toBeLessThanOrEqual(run.clientWidth);
    expect(label.getBoundingClientRect().top).toBeGreaterThanOrEqual(r.bottom);
    expect(times.getBoundingClientRect().top).toBeGreaterThanOrEqual(r.bottom);
  });

  it("starts the step and times on the body's edge and keeps them to two lines on a phone", () => {
    const { label, times, text } = mountNote(frameOf(390));
    const bodyEdge = text.getBoundingClientRect().left;
    const starts = lineStarts(label, times);

    expect(starts.size).toBeGreaterThan(0);
    expect(starts.size).toBeLessThanOrEqual(2);
    for (const left of starts.values()) {
      expect(left).toBeCloseTo(bodyEdge, 0);
    }
  });

  it("wraps the times between the sent and delivered clauses, never inside a time", () => {
    // A 320px phone's chat column leaves the card about 262px, as this frame does.
    const { label, times, text } = mountNote(frameOf(278));
    const bodyEdge = text.getBoundingClientRect().left;

    expect(
      lineStarts(label, times).size,
      "the 278px frame must force the wrap this case checks",
    ).toBe(2);
    for (const left of lineStarts(label, times).values()) {
      expect(left).toBeCloseTo(bodyEdge, 0);
    }
    for (const stamp of times.querySelectorAll("time")) {
      expect(stamp.getClientRects()).toHaveLength(1);
    }
    for (const clause of times.querySelectorAll<HTMLElement>(".steer-note-clause")) {
      expect(lineStarts(clause).size).toBe(1);
    }
  });

  it("keeps the run's name, the step and the times on one row when it is wide", () => {
    const { run, head, label, times } = mountNote(frameOf(900));
    if (run === null) {
      throw new Error("no run label");
    }
    const r = run.getBoundingClientRect();
    const l = label.getBoundingClientRect();
    const contentEnd =
      head.getBoundingClientRect().right - parseFloat(getComputedStyle(head).paddingInlineEnd);

    expect(Math.abs(r.top - l.top)).toBeLessThan(r.height);
    expect(r.right).toBeLessThanOrEqual(l.left);
    expect(lineStarts(label, times).size).toBe(1);
    expect(times.getBoundingClientRect().right).toBeCloseTo(contentEnd, 0);
  });

  it("keeps the step and the times on one line when there is no run's name and they fit", () => {
    const { head, label, times } = mountNote(frameOf(600), false);
    const contentEnd =
      head.getBoundingClientRect().right - parseFloat(getComputedStyle(head).paddingInlineEnd);

    expect(lineStarts(label, times).size).toBe(1);
    expect(times.getBoundingClientRect().right).toBeCloseTo(contentEnd, 0);
  });

  it("keeps the step beside the glyph under the run's name when there are no times on a phone", () => {
    const doc = frameOf(390);
    const note = buildSteerNote({
      text: "Step finished.",
      origin: "agent",
      dropped: false,
      run: "wf_run1",
      runLabel: "actions-reactive-owner-loading",
      onOpenRun: () => undefined,
    });
    doc.body.appendChild(doc.adoptNode(note));
    const run = note.querySelector<HTMLElement>(".steer-note-run");
    const label = note.querySelector<HTMLElement>(".steer-note-label");
    if (run === null || label === null) {
      throw new Error("no run label or step label");
    }

    expect(label.getBoundingClientRect().top).toBeGreaterThanOrEqual(
      run.getBoundingClientRect().bottom,
    );
    expect(label.getBoundingClientRect().left).toBeCloseTo(run.getBoundingClientRect().left, 0);
  });

  it("puts the times under the glyph, on the body's edge, when there is no run's name on a phone", () => {
    const { icon, label, times, text } = mountNote(frameOf(278), false);
    const i = icon.getBoundingClientRect();

    expect(lineStarts(label).size).toBe(1);
    expect(label.getBoundingClientRect().left).toBeGreaterThanOrEqual(i.right);
    expect(times.getBoundingClientRect().top).toBeGreaterThanOrEqual(
      label.getBoundingClientRect().bottom,
    );
    for (const left of lineStarts(times).values()) {
      expect(left).toBeCloseTo(text.getBoundingClientRect().left, 0);
    }
  });
});
