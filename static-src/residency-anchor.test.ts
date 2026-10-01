// ---------------------------------------------------------------------------
// The residency ANCHOR under a reader's own scroll: one gesture, one window move.
//
// The window is grown around where the reader is, and where the reader is comes off
// the scroll position — so a pass that WRITES the scroll position is feeding its own
// input. `block-virtualization.test.ts` covers what one drag mounts on a single
// long turn; what is here is the feedback path, which needs turns whose bodies hold
// SEVERAL mounted elements and several turns the window has to choose between.
//
// Real `scroll.ts`, the shipped stylesheet and a sized scrollport, because every
// assertion is a measurement: the shared mock's scroller has no geometry and its
// `onViewportChange` never fires, so no window pass runs under it at all.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";

// NESTED as the shipped page nests them: `#messages-wrap` is `position: absolute`
// inside the outer wrapper, so it is the `offsetParent` of the whole transcript AND
// the scroller. Flat siblings measure every card against the body instead, which is
// the anchor ladder's coordinates in the wrong space.
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

/** Long enough to WRAP, so a mounted element measures several times the per-entry
 *  estimate the spacer above it priced. One line of prose per entry and the head's
 *  growth is too small for a compensation to be observable at all. */
const LONG = "the quick brown fox jumps over the lazy dog and keeps going ".repeat(6);

/** Prose RUNS per turn. A run is ONE mounted element however many entries it holds
 *  (`sliceTurn` snaps a window past a run's ends), so the runs are what give a body
 *  the several mounted elements the anchor ladder chooses between. */
const RUNS = 10;

/** Entries per run in the two fixtures, which is what decides where the window's EDGE
 *  lands. SMALL makes a turn the window can hold whole, so its edges land on turn
 *  BOUNDARIES and a crossing is a handful of gestures away. BIG makes one turn twice
 *  the whole budget, so both edges sit inside the card the reader is in — the only
 *  state in which a move can extend that card's tail underneath them. */
const SMALL_PER_RUN = 8;
const BIG_PER_RUN = Math.ceil((2 * RESIDENT_ENTRIES) / RUNS);

/** One reader gesture. Bounded ABOVE by the shortest card the walk has to land in:
 *  `.msg-row` carries `content-visibility: auto`, so an off-screen turn stands at its
 *  intrinsic estimate (about a kilopixel here) rather than at its rendered height, and
 *  a gesture wider than that steps over whole turns without ever being inside one. */
const STEP_PX = 800;

let seq = 0;
function chatID(): string {
  seq++;
  return `c-anchor-${String(seq)}`;
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

/** One turn whose body is `RUNS` stretches of `perRun` wrapping `text` entries, each
 *  stretch separated by a `thinking` entry.
 *
 *  The separators are the whole point: consecutive `text` entries are ONE element, so
 *  a single stretch of any length gives the ladder one box to answer with and a turn
 *  ordinal it can never tell from a run's own start. */
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

/** One comparable reading of what the whole transcript holds: which entry ordinals are
 *  mounted per card, and what each spacer reserves. This is the pass's own output, so
 *  two readings that differ mean another pass ran between them. */
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
    const spacers = [...c.querySelectorAll<HTMLElement>(":scope > .turn-space")]
      .map((e) => `${e.dataset["space"] ?? "?"}=${e.style.blockSize}`)
      .join("/");
    parts.push(`${id}{${seqs}}[${spacers}]`);
  }
  return parts.join(" ");
}

/** The card the viewport top currently sits in: the last one starting at or above it,
 *  which on a tiled column is the one containing it.
 *
 *  The CARD rather than the mounted entry inside it, because `.msg-row` carries
 *  `content-visibility: auto`: a row the engine is skipping reports a zero rect for
 *  every descendant, so an entry-level walk answers the same box for every position
 *  and reads as a reader who never moves. A card always has a box. */
function cardAtViewportTop(): HTMLElement | null {
  const frameTop = scroller().getBoundingClientRect().top;
  let found: HTMLElement | null = null;
  for (const c of root().children) {
    if (c.getAttribute(KEY_ATTR) === null) {
      continue;
    }
    if (c.getBoundingClientRect().top - frameTop <= 0) {
      found = c as HTMLElement;
    }
  }
  return found;
}

/** The turn whose card the viewport top sits in. */
function turnAtViewportTop(): string {
  return cardAtViewportTop()?.getAttribute(KEY_ATTR) ?? "";
}

describe("the residency anchor under a reader's own scroll", () => {
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    // Left mounted for the file's lifetime: every case in it measures real boxes, and
    // the stylesheet is what gives them any.
    mountAppCSS();
    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build, so
    // without a height the absolutely-positioned scroller inside it is 0 tall and the
    // ladder answers the live edge for every position.
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

  /** `count` heavy turns of `perRun` entries per run, all but the newest explicitly OPEN
   *  so every one is `openable` and the window has to choose between them. Left to the
   *  fold policy only the newest would be, and then the window never moves across a
   *  turn boundary at all.
   *
   *  `perRun` is what decides where the window's EDGE lands, and the two cases below
   *  need different answers: turns the window can hold whole put its edge on turn
   *  boundaries, and turns it cannot put the edge mid-turn. */
  async function openTurns(count: number, perRun: number): Promise<void> {
    const turnEntries: Entry[][] = [];
    for (let i = 1; i <= count; i++) {
      turnEntries.push(heavyTurn(`t${String(i)}`, i, perRun));
    }
    const chat = chatID();
    activate(chat, turnEntries);
    for (let i = 1; i < count; i++) {
      setTurnOpen(chat, `t${String(i)}`, true);
    }
    bumpMessages(chat, "shape");
    // The cold build yields per slice, and the window pass refuses to move a body that
    // is still filling — so every case has to let the build finish first. Two
    // consecutive agreeing readings, seeded with one no transcript can produce.
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
    // Back to the live edge, then out past the bottom pin's own settle window: a
    // gesture inside it is undone before any pass sees it.
    scrollToBottom();
    await new Promise((resolve) => {
      setTimeout(resolve, 900);
    });
  }

  /** Scroll UP by `px` as a reader does, and let the frame settle.
   *
   *  The wheel's DIRECTION is load-bearing rather than decoration: the controller
   *  enters Reading from the aim of the reader's input, and a bare positional write is
   *  the shape of the platform's own clamp, which stays Following on purpose.
   *  `behavior: "instant"`, not `scrollTop =`, because the scroller declares
   *  `scroll-behavior: smooth` and an assignment only starts an animation. */
  async function readerScrollsUp(px: number, frames = 6): Promise<number> {
    const el = scroller();
    const asked = Math.max(0, el.scrollTop - px);
    el.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    el.scrollTo({ top: asked, behavior: "instant" });
    for (let f = 0; f < frames; f++) {
      await frame();
    }
    return asked;
  }

  /** Walk up in `STEP_PX` gestures until the viewport top has CROSSED from one turn's
   *  card into the previous one `crossings` times, and report the last such gesture.
   *
   *  The crossing is the state every case here needs: inside one turn the anchor sits
   *  in a run whose own start is the ordinal it reports, and a run's start and the
   *  turn's ordinal space coincide there. Walking a fixed number of steps instead
   *  would make the state a case reaches depend on the fixture's rendered height,
   *  which the font decides.
   *
   *  TWO by default, because the FIRST crossing still has the window's tail LATCHED at
   *  the transcript's end: a backward anchor error cannot retract a latched tail, so
   *  that one absorbs it and reports nothing. */
  async function scrollUpAcrossTurnBoundaries(
    crossings = 2,
  ): Promise<{ asked: number; landed: number }> {
    let from = turnAtViewportTop();
    expect(from, "the viewport top must start inside a turn's card").not.toBe("");
    let seen = 0;
    for (let s = 0; s < 40; s++) {
      const asked = await readerScrollsUp(STEP_PX);
      const now = turnAtViewportTop();
      if (now !== from && now !== "") {
        from = now;
        seen++;
        if (seen === crossings) {
          return { asked, landed: scroller().scrollTop };
        }
      }
      if (scroller().scrollTop === 0) {
        break;
      }
    }
    throw new Error(
      `the walk crossed ${String(seen)} turn boundaries, wanted ${String(crossings)}`,
    );
  }

  /** Walk up until the reader is INSIDE a card whose own window is partial at the tail.
   *
   *  That is the one state in which a window move extends a card's tail while the reader
   *  sits in it, which is the work the compensation attributes to the content above them.
   *  A card the window holds whole cannot produce it, and neither can one the reader is
   *  not in — so a step count cannot name the state and the condition has to. */
  async function readerReachesRetractingTail(): Promise<void> {
    for (let s = 0; s < 40; s++) {
      const card = cardAtViewportTop();
      if (card?.querySelector(':scope > .turn-space[data-space="tail"]') != null) {
        return;
      }
      if (scroller().scrollTop === 0) {
        break;
      }
      await readerScrollsUp(STEP_PX / 2, 3);
    }
    throw new Error("the walk never put the reader in a card with a tail spacer");
  }

  it("moves the reader by what they asked when their scroll crosses into an earlier turn", async () => {
    // Turns the window can hold WHOLE, so its edges land on turn boundaries and a
    // crossing is a handful of gestures away.
    await openTurns(6, SMALL_PER_RUN);

    const { asked, landed } = await scrollUpAcrossTurnBoundaries();

    // The crossing is where the anchor's own box is a run the window's edge is about to
    // move past. Read in the wrong space the anchor lands ordinals early, the window
    // recentres itself a turn's worth backwards, and the head it mounts above the reader
    // is paid for out of their scroll position. The bound is the gesture itself, so a
    // correction can never exceed the travel it is correcting.
    expect(Math.abs(landed - asked)).toBeLessThan(STEP_PX);
  }, 90000);

  it("does not schedule a window pass from the scroll the last pass wrote", async () => {
    // Turns LARGER than the whole budget, so the window sits inside one card and both
    // its edges are in the card the reader is in — which is where a move extends that
    // card's tail underneath them and the compensation carries a correction for content
    // below the reader.
    await openTurns(3, BIG_PER_RUN);
    await readerReachesRetractingTail();

    // SCROLL EVENTS, not residency readings: the loop's own edge is a write to
    // `scrollTop`, so counting the writes measures the edge directly rather than what
    // it did to the window. One reader gesture may produce two — the reader's own, and
    // the pass's one compensation.
    const el = scroller();
    const writes: number[] = [];
    const onScroll = (): void => {
      writes.push(Math.round(el.scrollTop));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    const gestures = 4;
    for (let g = 0; g < gestures; g++) {
      await readerScrollsUp(600);
    }
    // Then twenty-four frames with NO input at all. A loop that feeds itself writes about
    // once a frame and does not settle when the reader stops, so the idle frames are what
    // separate a pass correcting the reader once from a pass chasing its own correction.
    // Twenty-four rather than ninety because every frame here is a real one: the longer
    // wait made this case the slowest in the file and timed it out under a loaded suite.
    for (let f = 0; f < 24; f++) {
      await frame();
    }
    el.removeEventListener("scroll", onScroll);

    // With the pass hanging off every scroll frame rather than off a reader gesture, this
    // is what the writes look like: sixteen for four gestures, alternating over the same
    // few hundred pixels, because each compensation re-planned the window from the
    // position it had just corrected.
    expect(writes.length, `scroll writes: ${writes.join(",")}`).toBeLessThanOrEqual(2 * gestures);
  }, 90000);
});
