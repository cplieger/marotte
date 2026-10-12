// The live edge is windowed in before the reader lands on it: a return to the bottom from a reading window never
// shows the spacers that window left in the newest turn.

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";

// NESTED as the shipped page nests them: `#messages-wrap` is `position: absolute` inside the outer
// wrapper, so it is the `offsetParent` of the whole transcript AND the scroller.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { scrollToBottom } = await import("./scroll.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { RESIDENT_ENTRIES } = await import("./block-window.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

/** Long enough to WRAP, so a mounted element measures several times the per-entry estimate the
 *  spacer above it priced. One line of prose per entry and the head's growth is too small for a
 *  misplaced anchor to be observable at all. */
const LONG = "the quick brown fox jumps over the lazy dog and keeps going ".repeat(6);

/** Prose RUNS per turn. A run is ONE mounted element however many entries it holds (`sliceTurn`
 *  snaps a window past a run's ends), so the runs are what give a body the several mounted
 *  elements the anchor ladder chooses between. */
const RUNS = 10;

/** Twice the window, so a reading window cannot hold the newest turn whole. */
const BIG_PER_RUN = Math.ceil((2 * RESIDENT_ENTRIES) / RUNS);

let seq = 0;
function chatID(): string {
  seq++;
  return `c-live-edge-${String(seq)}`;
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    lane: "",
    payload,
  } as Entry;
}

function turnOpen(turnID: string, n: number): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text: `prompt ${turnID}` },
    source: "prompt",
    n,
  });
}

/** One turn whose body is `RUNS` stretches of `perRun` wrapping `text` entries, each stretch
 *  separated by a `thinking` entry. The separators are the whole point: consecutive `text`
 *  entries are ONE element, so a single stretch of any length gives the ladder one box to answer
 *  with and a turn ordinal it can never tell from a run's own start. */
function heavyTurn(turnID: string, n: number, perRun: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  let at = 1;
  for (let r = 0; r < RUNS; r++) {
    if (r > 0) {
      out.push(sealed(turnID, at++, "thinking", { text: `between ${String(r)}` }));
    }
    for (let i = 0; i < perRun; i++) {
      out.push(
        sealed(turnID, at++, "text", { text: `run ${String(r)} chunk ${String(i)}: ${LONG}` }),
      );
    }
  }
  out.push(sealed(turnID, at, "turn_close", { outcome: "completed" }));
  return out;
}

/** A chat holding whole turns, the shape a page GET lands. */
function activate(chat: string, turnEntries: readonly (readonly Entry[])[]): void {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chat, name: "c" }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
}

function root(): HTMLElement {
  return activeTranscriptView() ?? messagesEl;
}

function scroller(): HTMLElement {
  return document.getElementById("messages-wrap")!;
}

function frame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => resolve());
  });
}

/** One comparable reading of what the whole transcript holds: which entry ordinals are mounted
 *  per card, and what each spacer reserves. This is the pass's own output, so two readings that
 *  differ mean another pass ran between them. */
function residency(): string {
  const parts: string[] = [];
  for (const c of root().children) {
    const id = c.getAttribute(KEY_ATTR);
    if (id === null) {
      continue;
    }
    const seqs = [...c.querySelectorAll<HTMLElement>("[data-entry-seq]")]
      .map((e) => `${e.dataset["entryTurn"] ?? ""}#${e.dataset["entrySeq"] ?? ""}`)
      .join(",");
    const spacers = [
      ...c.querySelectorAll<HTMLElement>(":scope > .turn-body > .turn-space, :scope > .turn-space"),
    ]
      .map((e) => `${e.dataset["space"] ?? "?"}=${e.style.blockSize}`)
      .join("/");
    parts.push(`${id}{${seqs}}[${spacers}]`);
  }
  return parts.join(" ");
}

describe("asking for the live edge after reading far up", () => {
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    mountAppCSS();
    const outer = document.getElementById("messages-wrap-outer");
    if (outer !== null) {
      outer.style.height = `${String(VIEWPORT_PX)}px`;
    }
  });

  beforeEach(() => {
    mountChatView();
    localStorage.clear();
    resetFoldState();
    setSessions([]);
    setActive("");
  });

  async function settled(): Promise<void> {
    let last = "<none>";
    await vi.waitFor(
      () => {
        const now = residency();
        const was = last;
        last = now;
        expect(now).not.toBe("");
        expect(now).toBe(was);
      },
      { timeout: 10000, interval: 60 },
    );
  }

  /** The spacer pixels inside the scroller's viewport: what the reader sees as blank. */
  function visibleSpacerPx(): number {
    const view = scroller().getBoundingClientRect();
    let px = 0;
    for (const s of root().querySelectorAll<HTMLElement>(".turn-space")) {
      const r = s.getBoundingClientRect();
      px += Math.max(0, Math.min(r.bottom, view.bottom) - Math.max(r.top, view.top));
    }
    return px;
  }

  function newestTail(): HTMLElement | null {
    const cards = [...root().children].filter((c) => c.getAttribute(KEY_ATTR) !== null);
    return (
      cards.at(-1)?.querySelector<HTMLElement>(':scope > .turn-space[data-space="tail"]') ?? null
    );
  }

  it("lands on the newest turn's mounted end, never on its spacer", async () => {
    const turnEntries: Entry[][] = [];
    for (let i = 1; i <= 3; i++) {
      turnEntries.push(heavyTurn(`t${String(i)}`, i, BIG_PER_RUN));
    }
    const chat = chatID();
    activate(chat, turnEntries);
    for (let i = 1; i < 3; i++) {
      setTurnOpen(chat, `t${String(i)}`, true);
    }
    bumpMessages(chat, "shape");
    await settled();

    // The reader climbs to the top as a reader does, so the window leaves the newest turn's end.
    const el = scroller();
    for (let i = 0; i < 60 && newestTail() === null; i++) {
      el.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
      el.scrollTo({ top: Math.max(0, el.scrollTop - 2000), behavior: "instant" });
      for (let f = 0; f < 4; f++) {
        await frame();
      }
    }
    await settled();
    expect(
      newestTail(),
      "the reading window left a spacer at the newest turn's end",
    ).not.toBeNull();

    scrollToBottom();

    expect(el.scrollHeight - el.scrollTop - el.clientHeight, "landed at the bottom").toBeLessThan(
      2,
    );
    expect(visibleSpacerPx(), "blank in the landing frame").toBe(0);
    await frame();
    expect(visibleSpacerPx(), "blank a frame later").toBe(0);
  });
});
