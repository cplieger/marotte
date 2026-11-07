import { describe, it, expect, afterEach, vi } from "vitest";

import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

// The import graph reaches the DOM registry, which throws on a missing app root.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { isSliding, slideBox } = await import("./disclosure-row.js");
const { buildReasoning } = await import("./fundamentals/reasoning.js");
const { buildAssistantBody, initBlockRenderer } = await import("./messages-blocks.js");

initBlockRenderer({
  pushEntryEffect: (): void => undefined,
  disposeEntryEffects: (): void => undefined,
  makeRow: () => document.createElement("div"),
  makeEvent: () => document.createElement("div"),
  liveAnchorChanged: (): void => undefined,
});

// A reasoning trace sealed while it is mounted into a parked or detached view has no slide to
// show, so it must flip `open` without measuring: every rect read there is a forced layout of
// the transcript being re-shown.

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
  vi.restoreAllMocks();
});

function countRects(): { n: () => number } {
  const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
  const rects = vi.spyOn(Element.prototype, "getClientRects");
  return { n: () => rect.mock.calls.length + rects.mock.calls.length };
}

describe("a motionless flip reads no geometry", () => {
  it("slideBox with motion off applies the state and measures nothing", () => {
    const box = document.createElement("details");
    box.textContent = "body";
    const host = document.createElement("div");
    host.appendChild(box);
    document.body.appendChild(host);
    hosts.push(host);
    const reads = countRects();
    slideBox(
      box,
      true,
      (o) => {
        box.open = o;
      },
      { motion: false },
    );
    expect(box.open).toBe(true);
    expect(reads.n(), "geometry reads for a motionless flip").toBe(0);
  });
});

describe("sealing a trace outside the active view", () => {
  it("collapses a detached trace with no geometry read", () => {
    const view = buildReasoning("thought", false, true);
    expect(view.root.open).toBe(true);
    const reads = countRects();
    view.seal();
    expect(view.root.open).toBe(false);
    expect(reads.n(), "geometry reads sealing a detached trace").toBe(0);
  });

  it("collapses a trace inside a parked transcript view with no geometry read", () => {
    const parked = document.createElement("div");
    parked.className = "transcript-view";
    document.body.appendChild(parked);
    hosts.push(parked);
    const view = buildReasoning("thought", false, true);
    parked.appendChild(view.root);
    const reads = countRects();
    view.seal();
    expect(view.root.open).toBe(false);
    expect(reads.n(), "geometry reads sealing a parked trace").toBe(0);
  });
});

describe("mounting a settled turn", () => {
  it("builds a followed trace closed and never measures it", () => {
    const turnID = "t-seal-mount";
    const entry = (seq: number, kind: Entry["kind"], text: string, id: string): Entry => ({
      id,
      turn: turnID,
      kind,
      seq,
      ts: seq + 1,
      payload: { text },
    });
    const turn: Turn = {
      id: turnID,
      n: 1,
      trigger: { id: `${turnID}-p`, text: "go" },
      branchFrom: undefined,
      body: [
        entry(1, "thinking", "weighing it up", `${turnID}-e1`),
        entry(2, "text", "answer", `${turnID}-say#2`),
      ],
      openEntries: new Map(),
      ts: 1,
      outcome: "completed",
      rewindTo: undefined,
    };
    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    const body = document.createElement("div");
    body.className = "turn-body";
    view.appendChild(body);
    document.body.appendChild(view);
    hosts.push(view);
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    buildAssistantBody(body, turn, "c-seal-mount", false);
    const trace = body.querySelector<HTMLDetailsElement>(".reasoning-block");
    expect(trace).not.toBeNull();
    expect(trace?.open).toBe(false);
    const onTrace = rect.mock.contexts.filter((c) => c === trace).length;
    expect(onTrace, "getBoundingClientRect calls on the mounted trace").toBe(0);
  });
});

describe("a live seal in the active view", () => {
  it("still slides shut", () => {
    const view = document.createElement("div");
    view.className = "transcript-view is-active";
    document.body.appendChild(view);
    hosts.push(view);
    const trace = buildReasoning("a long enough thought to have a body height", true, true);
    view.appendChild(trace.root);
    trace.seal();
    expect(isSliding(trace.root), "a slide runs on the sealed trace").toBe(true);
  });
});
