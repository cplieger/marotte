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
  run: HTMLElement;
  label: HTMLElement;
  times: HTMLElement;
}

function mountNote(doc: Document): Header {
  const note = buildSteerNote({
    text: "P4 gate: APPROVED.",
    origin: "step",
    dropped: false,
    step: "P4-REVIEW-GPT",
    run: "wf_run1",
    runLabel: "forge-f2-build",
    onOpenRun: () => undefined,
    sentAt: Date.UTC(2026, 9, 9, 12, 0, 5),
    tracked: true,
  });
  // Only the header is measured; the body's clamp observer would report a resize loop in the frame.
  note.querySelector(".steer-note-body")?.remove();
  doc.body.appendChild(doc.adoptNode(note));
  const pick = (sel: string): HTMLElement => {
    const hit = note.querySelector<HTMLElement>(sel);
    if (hit === null) {
      throw new Error(`no ${sel}`);
    }
    return hit;
  };
  return {
    run: pick(".steer-note-run"),
    label: pick(".steer-note-label"),
    times: pick(".steer-note-times"),
  };
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
    const r = run.getBoundingClientRect();
    const l = label.getBoundingClientRect();
    const t = times.getBoundingClientRect();

    expect(run.scrollWidth).toBeLessThanOrEqual(run.clientWidth);
    expect(l.top).toBeGreaterThanOrEqual(r.bottom);
    expect(t.top).toBeGreaterThanOrEqual(l.bottom);
    // Each of the three is one line: a wrapped step id or time is two lines tall.
    expect(l.height).toBeLessThan(r.height * 1.5);
    expect(t.height).toBeLessThan(r.height * 1.5);
  });

  it("keeps the run's name beside the step on a wide row", () => {
    const { run, label } = mountNote(frameOf(900));

    expect(
      Math.abs(run.getBoundingClientRect().top - label.getBoundingClientRect().top),
    ).toBeLessThan(run.getBoundingClientRect().height);
    expect(run.getBoundingClientRect().right).toBeLessThanOrEqual(
      label.getBoundingClientRect().left,
    );
  });
});
