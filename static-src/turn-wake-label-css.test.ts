// A woken turn's label stays one line of text under a finger: the run's name is a target inside that
// sentence, so the coarse hit floor does not make the line 44px tall, and the name ellipsizes before
// the step and the verb do.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { wakeLine } from "./turn-wake.js";
import type { EntryTurnWake } from "./wire/types.gen.js";

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
  document.documentElement.dataset["pointer"] = "coarse";
});

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

afterAll(() => {
  style.remove();
  delete document.documentElement.dataset["pointer"];
});

/** The label of a header `width` px wide, the width of a phone's transcript column. */
function mountLabel(width: number, w: EntryTurnWake): HTMLElement {
  const host = document.createElement("div");
  host.style.width = `${String(width)}px`;
  document.body.appendChild(host);
  hosts.push(host);
  const header = buildTurnHeader({
    n: 2,
    outcome: "completed",
    ts: 0,
    request: undefined,
    attachments: [],
    wake: wakeLine(w),
    onOpenRun: () => undefined,
  });
  host.appendChild(header);
  const line = header.querySelector<HTMLElement>(".turn-wake");
  if (line === null) {
    throw new Error("no .turn-wake");
  }
  return line;
}

const STEP: EntryTurnWake = {
  reason: "step_message",
  steers: [
    {
      id: "wfmsg-01a12d6b",
      run: "wf_d73e51b5c94bdb82",
      run_label: "leftovers-resume-review-census",
      step: "job1-aggregate",
      severity: "info",
      text: "CHANGES_REQUESTED (gate: skills-dispatch-gate.json).",
    },
  ],
};

describe("a woken turn's label under a finger", () => {
  it("is one line of text tall with the run's name a button on it", () => {
    const line = mountLabel(300, STEP);
    const run = line.querySelector<HTMLElement>("button.turn-wake-run")!;
    const rest = line.querySelector<HTMLElement>(".turn-wake-rest")!;
    const lh = parseFloat(getComputedStyle(line).lineHeight);

    expect(line.getBoundingClientRect().height).toBe(lh);
    expect(run.getBoundingClientRect().top).toBe(rest.getBoundingClientRect().top);
  });

  it("ellipsizes the run's name and keeps the step and the verb whole", () => {
    const line = mountLabel(300, STEP);
    const run = line.querySelector<HTMLElement>(".turn-wake-run")!;
    const rest = line.querySelector<HTMLElement>(".turn-wake-rest")!;

    expect(rest.scrollWidth).toBeLessThanOrEqual(rest.clientWidth);
    expect(run.scrollWidth).toBeGreaterThan(run.clientWidth);
  });

  it("gives a label with no run the whole line before it ellipsizes", () => {
    const line = mountLabel(300, {
      reason: "rewake",
      steers: [
        { id: "a", run: "wf_d73e51b5c94bdb82", step: "job1-aggregate", text: "Gate APPROVED." },
        { id: "b", run: "wf_1729f9b5688ec7bf", step: "ratify", text: "Please confirm." },
      ],
    });
    const rest = line.querySelector<HTMLElement>(".turn-wake-rest")!;

    expect(rest.scrollWidth).toBeGreaterThan(rest.clientWidth);
    expect(rest.getBoundingClientRect().right).toBe(line.getBoundingClientRect().right);
  });
});
