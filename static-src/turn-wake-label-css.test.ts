// A woken turn's label stays one line of text under a finger: the run's name is a target inside that
// sentence, so the coarse hit floor does not make the line 44px tall, and the name ellipsizes before
// the step and the verb do. A re-delivery's run names, inline in its text, keep their lines too.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { wakeLine } from "./turn-wake.js";
import { namedWake } from "./turns.js";
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

const LONG_NAME = "publish-docs-release-notes-and-changelog";

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
    wake: wakeLine(
      namedWake(
        w,
        new Map(["wf_5e1f0c2a9b7d4e36", "wf_7c3a9e1f0b2d4a58"].map((id) => [id, LONG_NAME])),
      ),
    ),
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
      id: "wfmsg-3f2a",
      run: "wf_5e1f0c2a9b7d4e36",
      step: "check-links",
      severity: "info",
      text: "Link check done: 412 links, none broken.",
    },
  ],
};

describe("a woken turn's label under a finger", () => {
  it("is one line of text tall with the run's name a button on it", () => {
    const line = mountLabel(300, STEP);
    const run = line.querySelector<HTMLElement>(".turn-wake-run[role=button]")!;
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
        { id: "a", run: "wf_5e1f0c2a9b7d4e36", step: "check-links", text: "Link check done." },
        { id: "b", run: "wf_7c3a9e1f0b2d4a58", step: "collect", text: "Please confirm." },
      ],
    });
    const rest = line.querySelector<HTMLElement>(".turn-wake-rest")!;

    expect(rest.scrollWidth).toBeGreaterThan(rest.clientWidth);
    expect(rest.getBoundingClientRect().right).toBe(line.getBoundingClientRect().right);
  });
});

describe("a re-delivery's run names under a finger", () => {
  it("keep each message to its own text lines, the coarse floor not raising them", () => {
    const line = mountLabel(600, {
      reason: "rewake",
      steers: [
        { id: "a", run: "wf_5e1f0c2a9b7d4e36", step: "draft", text: "Which branch?" },
        { id: "b", run: "wf_7c3a9e1f0b2d4a58", step: "collect", text: "Done." },
      ],
    });
    const text = line.parentElement!.querySelector<HTMLElement>(".turn-req-text")!;
    const lh = parseFloat(getComputedStyle(text).lineHeight);

    expect(text.querySelectorAll(".turn-wake-row-run[role=button]")).toHaveLength(2);
    expect(text.getBoundingClientRect().height).toBe(2 * lh);
  });
});
