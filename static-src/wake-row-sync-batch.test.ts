// A re-delivery row's run button is inert while the header's clamp hides it. Measuring that inside a paint costs a
// forced layout per header, so the measurement waits for one frame and covers every header updated before it.
import { describe, it, expect, afterEach, vi } from "vitest";

import {
  buildTurnHeader,
  updateTurnHeader,
  type TurnHeaderData,
} from "./fundamentals/turn-header.js";
import { wakeLine } from "./turn-wake.js";
import { namedWake } from "./turns.js";
import type { EntryTurnWake } from "./wire/types.gen.js";

const RUN = "wf_5e1f0c2a9b7d4e36";
const OTHER = "wf_7c3a9e1f0b2d4a58";
const names = new Map([
  [RUN, "publish-docs"],
  [OTHER, "nightly-report"],
]);

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
  vi.restoreAllMocks();
});

function wakeData(w: EntryTurnWake): TurnHeaderData {
  return {
    n: 1,
    outcome: "completed",
    ts: 0,
    request: undefined,
    attachments: [],
    wake: wakeLine(namedWake(w, names)),
    onOpenRun: () => undefined,
  };
}

/** Two re-delivered messages; `tag` changes the words so an update repaints the rows. */
function redelivery(tag: string): EntryTurnWake {
  return {
    reason: "rewake",
    steers: [
      { id: "q", run: RUN, step: "draft", text: `Which branch? ${tag}` },
      { id: "r", run: OTHER, step: "collect", text: `Leg 1 is done. ${tag}` },
    ],
  };
}

function stepMessage(text: string): EntryTurnWake {
  return {
    reason: "step_message",
    steers: [{ id: "wfmsg-1", run: RUN, step: "check-links", severity: "warning", text }],
  };
}

/** A header in the document whose request text is clamped to its first line, so the second row is hidden. */
function mountHeader(d: TurnHeaderData): HTMLElement {
  const header = buildTurnHeader(d);
  const text = requestText(header);
  text.style.cssText = "white-space:pre-wrap;line-height:20px;max-height:20px;overflow:hidden";
  const host = document.createElement("div");
  host.style.inlineSize = "600px";
  host.append(header);
  document.body.append(host);
  hosts.push(host);
  return header;
}

function requestText(header: HTMLElement): HTMLElement {
  const t = header.querySelector<HTMLElement>(".turn-req-text");
  if (t === null) {
    throw new Error("no .turn-req-text");
  }
  return t;
}

async function nextFrame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

describe("re-delivery run buttons", () => {
  it("are measured after the paint, not inside it, and end with the clamped row inert", async () => {
    const headers = Array.from({ length: 10 }, (_, i) =>
      mountHeader(wakeData(redelivery(`v${String(i)}`))),
    );
    await nextFrame();
    await nextFrame();
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    headers.forEach((h, i) => {
      updateTurnHeader(h, wakeData(redelivery(`w${String(i)}`)));
    });
    expect(rect.mock.calls.length, "rect reads inside the paint that updated ten headers").toBe(0);
    await nextFrame();
    for (const h of headers) {
      const buttons = [
        ...requestText(h).querySelectorAll<HTMLButtonElement>("button.turn-wake-row-run"),
      ];
      expect(
        buttons.map((b) => b.inert),
        "the shown row live, the clamped row inert",
      ).toEqual([false, true]);
    }
  });

  it("never measures a wake header with no run buttons", async () => {
    const header = mountHeader(wakeData(stepMessage("The link check failed.")));
    const text = requestText(header);
    expect(text.querySelector("button.turn-wake-row-run")).toBeNull();
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    updateTurnHeader(header, wakeData(stepMessage("The link check failed twice.")));
    await nextFrame();
    await nextFrame();
    expect(
      rect.mock.contexts.filter((c) => c === text).length,
      "rect reads on its request text",
    ).toBe(0);
  });
});
