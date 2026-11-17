// The holding area above the composer (the run bar, then the steer and queued rows) under one cap: however
// many runs and messages wait, the composer stays inside the pane and a few transcript lines stay visible.

import { describe, it, expect, afterEach, vi } from "vitest";
import { page as browser } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  CHROME_PX,
  SHARE,
  holds,
  inside,
  mountPane,
  mountView,
  openStack,
  settle,
  unmountView,
  type Chat,
} from "./__test-helpers__/holding-area.js";
import { rowEntry, runAt, turnEntry } from "./__test-helpers__/run-order.js";
import { mountHoldingArea } from "./holding-area.js";
import { mountSteerStack, type SteerStackSource } from "./pending-steers.js";
import type { PendingSteer, QueuedPrompt, RunEntryView } from "./types.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

const ATTACHED = "pasted-2026-11-16T10-00-41.png";

const VIEWPORTS = [
  [1440, 900],
  [390, 844],
] as const;

let style: HTMLStyleElement | null = null;

afterEach(async () => {
  unmountView();
  style?.remove();
  style = null;
  vi.unstubAllGlobals();
  localStorage.removeItem(LS_UI_STATE_KEY);
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.style.removeProperty("--dock-viewport-h");
  await browser.viewport(1280, 720);
});

/** `queued` follow-ups behind a running turn (the last one attachment-only) and `steers` unread steers. */
function stackSource(queued: number, steers: number): SteerStackSource {
  const prompts = Array.from({ length: queued }, (_, i): QueuedPrompt =>
    i === queued - 1 && queued > 1
      ? {
          id: `m-q${String(i)}`,
          text: "",
          attachments: [{ path: `/uploads/${ATTACHED}`, name: ATTACHED }],
        }
      : { id: `m-q${String(i)}`, text: `follow-up number ${String(i + 1)}, check the logs again` },
  );
  return sourceOf(prompts, steers);
}

/** `prompts` queued behind a running turn and `steers` unread steers. */
function sourceOf(prompts: readonly QueuedPrompt[], steers: number): SteerStackSource {
  const entries: RunEntryView[] = prompts.map((row, i) =>
    rowEntry(
      row.id,
      i === 0 ? { after: "turns", turns: 1, this: true } : { after: "turns", turns: i + 1 },
      { row, send_now: "stop" },
    ),
  );
  const run = runAt(1, [turnEntry("t1", { origin: "prompt", kind: "row" }), ...entries], {
    cancel: "t1",
    send: "steer",
  });
  const pending = Array.from({ length: steers }, (_, i): PendingSteer => ({
    id: `steer-${String(i)}`,
    text: i === 0 ? "fsd" : "and check the logs",
    origin: "user",
  }));
  return {
    read: () => ({ id: "c1", steers: pending, run }),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  };
}

async function mountChat(
  w: number,
  h: number,
  runs: number,
  runsOpen: boolean,
  queued: number,
  steers: number,
): Promise<Chat> {
  await browser.viewport(w, h);
  style = mountAppCSS();
  return mountPane(h, runs, runsOpen, stackSource(queued, steers));
}

describe("the holding area", () => {
  for (const [w, h] of VIEWPORTS) {
    describe(`at ${String(w)}x${String(h)}`, () => {
      for (const [what, runs, queued, steers] of [
        ["5 runs and 17 queued messages", 5, 17, 0],
        ["5 runs", 5, 0, 0],
        ["15 steers and 2 queued messages", 0, 2, 15],
        ["17 queued messages", 0, 17, 0],
      ] as const) {
        for (const runsOpen of runs === 0 ? [false] : [true, false]) {
          const bar = runs === 0 ? "" : runsOpen ? ", the run bar open" : ", the run bar folded";
          for (const stackOpen of [true, false]) {
            const stack = stackOpen ? "every message shown" : "the stack folded";
            it(`keeps the composer on screen beside ${what}${bar}, ${stack}`, async () => {
              openStack(stackOpen);
              const chat = await mountChat(w, h, runs, runsOpen, queued, steers);
              holds(chat, h - CHROME_PX);
            });
          }
        }
      }
    });
  }

  for (const [w, h] of VIEWPORTS) {
    it(`lines a scrolling area's rows up with the composer at ${String(w)}x${String(h)}`, async () => {
      openStack(true);
      const chat = await mountChat(w, h, 5, true, 17, 0);
      expect(chat.area.scrollHeight, "the area scrolls").toBeGreaterThan(chat.area.clientHeight);
      const box = document.querySelector("#prompt-box")!.getBoundingClientRect();
      for (const sel of [".run-bar-host", ".steer-stack > li"]) {
        const r = chat.area.querySelector(sel)!.getBoundingClientRect();
        expect(r.left, `${sel} start`).toBeCloseTo(box.left, 0);
        expect(r.right, `${sel} end`).toBeCloseTo(box.right, 0);
      }
    });
  }

  it("ellipsises a long file name inside its row at phone width, the actions column in the row", async () => {
    await browser.viewport(390, 844);
    style = mountAppCSS();
    const root = mountView(844, '<div id="chat-view"', '<div id="settings-view"');
    const name = `${"a".repeat(116)}.png`;
    mountSteerStack(
      root.querySelector<HTMLUListElement>("#steer-stack")!,
      sourceOf([{ id: "m-q0", text: "", attachments: [{ path: `/uploads/${name}`, name }] }], 0),
    );
    mountHoldingArea(root.querySelector<HTMLElement>("#holding-area")!);
    await settle();
    const row = root.querySelector<HTMLElement>("#steer-stack > .steer-row")!;
    const shown = row.querySelector<HTMLElement>(".steer-attach-name")!;
    const box = row.getBoundingClientRect();

    expect(shown.textContent).toBe(name);
    expect(getComputedStyle(shown).textOverflow).toBe("ellipsis");
    expect(shown.scrollWidth, "the name is cut").toBeGreaterThan(shown.clientWidth);
    const text = row.querySelector(".steer-text")!.getBoundingClientRect();
    expect(
      shown.getBoundingClientRect().right,
      "the name ends inside its column",
    ).toBeLessThanOrEqual(text.right + 0.5);
    const actions = row.querySelector(".steer-actions")!.getBoundingClientRect();
    expect(actions.left, "the actions start inside the row").toBeGreaterThanOrEqual(box.left);
    expect(actions.right, "the actions end inside the row").toBeLessThanOrEqual(box.right + 0.5);
    expect(actions.left, "the actions follow the message").toBeGreaterThanOrEqual(text.right);
  });

  it("caps the area against the visual viewport when a keyboard is up", async () => {
    const vv = 464;
    vi.stubGlobal("visualViewport", {
      height: vv,
      width: 390,
      offsetTop: 0,
      offsetLeft: 0,
      scale: 1,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    });
    openStack(true);
    const chat = await mountChat(390, 844, 5, true, 17, 0);
    expect(chat.area.getBoundingClientRect().height).toBeLessThanOrEqual(
      SHARE * (vv - CHROME_PX) + 0.5,
    );
  });

  it("caps the area against the visual viewport iOS panned past the pane's top", async () => {
    const band = { height: 464, offsetTop: 380 };
    const listeners: Record<string, (() => void)[]> = {};
    vi.stubGlobal("visualViewport", {
      get height() {
        return band.height;
      },
      get offsetTop() {
        return band.offsetTop;
      },
      width: 390,
      offsetLeft: 0,
      scale: 1,
      addEventListener: (type: string, fn: () => void) => {
        (listeners[type] ??= []).push(fn);
      },
      removeEventListener: () => undefined,
    });
    openStack(true);
    const chat = await mountChat(390, 844, 5, true, 17, 0);
    const panned = chat.area.getBoundingClientRect().height;
    expect(panned, "0.4 of the pane's visible 464px").toBeLessThanOrEqual(SHARE * 464 + 0.5);
    expect(panned).toBeGreaterThanOrEqual(SHARE * 464 - 1);

    band.offsetTop = 0;
    for (const fn of listeners["scroll"] ?? []) {
      fn();
    }
    await settle();
    const top = chat.area.getBoundingClientRect().height;
    expect(top, "a pan with no resize repaints the cap").toBeLessThanOrEqual(
      SHARE * (464 - CHROME_PX) + 0.5,
    );
    expect(top).toBeGreaterThanOrEqual(SHARE * (464 - CHROME_PX) - 1);
  });

  it("keeps the run tab's composer on screen beside 17 messages to a step", async () => {
    await browser.viewport(390, 844);
    openStack(true);
    style = mountAppCSS();
    const root = mountView(844, '<div id="run-view"', "<!-- One Kiro spec");
    const runView = root.querySelector<HTMLElement>("#run-view")!;
    runView.classList.remove("hidden");
    const steers = Array.from({ length: 17 }, (_, i): PendingSteer => ({
      id: `steer-${String(i)}`,
      text: "check the logs",
      origin: "user",
    }));
    mountSteerStack(root.querySelector<HTMLUListElement>("#run-steer-stack")!, {
      read: () => ({ id: "run-1/step", steers }),
      edit: () => Promise.resolve(),
      editByClear: () => Promise.resolve(),
      remove: () => Promise.resolve(),
      discard: () => Promise.resolve(),
      waitingFor: "the step",
    });
    mountHoldingArea(root.querySelector<HTMLElement>("#run-holding-area")!);
    await settle();
    inside(
      root.querySelector("#run-composer")!,
      runView.getBoundingClientRect(),
      "the run composer",
    );
    inside(root.querySelector("#run-composer")!, new DOMRect(0, 0, 390, 844), "the run composer");
  });
});
