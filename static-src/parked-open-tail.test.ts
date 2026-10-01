// ---------------------------------------------------------------------------
// A parked view's OPEN TAIL is frozen: the delta channel, which is the one channel
// `messages-parked-views.test.ts` measured and deliberately did not pin.
//
// `mountOpenProse` holds its `watchOpenText` subscription in `st.openTail.stop`,
// registered with neither `streamingEffects` nor `entryEffects` — so
// `pauseTurnBody`'s `disposeStreamingEffect` cannot reach it and only
// `pauseAssistantBody` can. This suite is that one claim, with the ACTIVE chat's own
// tail as the control that bounds the wait on the product's own output.
//
// REAL store, REAL renderer, REAL scroll controller: the park is what is under test,
// so nothing on its path is mocked. No stylesheet is loaded — every assertion here
// reads text, not geometry.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import type { Turn } from "./turns.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";
import { makeEntry, makeSession } from "./__test-helpers__/model.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId` throws
// on a missing element, so the hosts exist before any import resolves.
for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

const store = await import("./store.js");
const messages = await import("./messages.js");
const blocks = await import("./messages-blocks.js");
const turns = await import("./turns.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

function turnOpen(turnID: string, n: number): Entry {
  return makeEntry({
    id: `${turnID}-e0`,
    turn: turnID,
    kind: "turn_open",
    ts: 1,
    payload: { prompt: { id: `${turnID}-p`, text: "go" }, source: "prompt", n },
  });
}

function open(turnID: string, id: string, text: string): OpenEntry {
  return { turn: turnID, id, kind: "text", text, n: 1 };
}

/** The same open entry in a DELEGATE's lane: the shape a detached render mounts from. */
function laned(turnID: string, id: string, lane: string, text: string): OpenEntry {
  return { turn: turnID, id, lane, kind: "text", text, n: 1 };
}

/** A chat whose single turn is OPEN, which is the only shape that mounts an open tail. */
function streaming(id: string): { chat: Session; turn: string } {
  const turn = `${id}-t1`;
  const chat = session(id, { thinking: true });
  chat.turns.set(turn, { entries: [turnOpen(turn, 1)], openEntries: new Map() });
  chat.turn_order.push(turn);
  chat.turn_count = 1;
  return { chat, turn };
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

function liveText(chatID: string): string {
  return viewOf(chatID).querySelector(".message.assistant")?.textContent ?? "";
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

describe("a parked view's open tail", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("stops consuming deltas, while the active chat's own tail keeps growing", async () => {
    const a = freshID("c-tail");
    const b = freshID("c-tail");
    const parkedChat = streaming(a);
    const activeChat = streaming(b);
    store.setSessions([parkedChat.chat, activeChat.chat]);
    store.setActive(a);
    store.bumpMessages(a, "load");

    store.openEntry(a, open(parkedChat.turn, "say-a", "hello"));
    // THE PRECONDITION, asserted rather than assumed: A must hold a mounted live tail
    // for the park to have a subscription to stop. Without it the freeze below passes
    // for a chat that was never streaming.
    await vi.waitFor(() => {
      expect(viewOf(a).querySelector(".message.assistant.streaming")).not.toBeNull();
    });
    // The incremental markdown parser withholds a trailing character until the stream
    // moves, so what is on screen here is a PREFIX of the store's `hello`.
    expect(liveText(a)).toContain("hell");

    store.setActive(b);
    await flushed();
    expect(viewOf(a).classList.contains("is-active")).toBe(false);
    // The control's own precondition: B is ACTIVE and holds a live tail, so the delta
    // driven into it below is written by an armed reveal.
    store.openEntry(b, open(activeChat.turn, "say-b", "hello"));
    await vi.waitFor(() => {
      expect(viewOf(b).querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    // ONE delta into each chat, long enough that an armed reveal emits inside a frame:
    // the buffer withholds growth under `MIN_EMIT_CHARS` (`reveal.ts`), so a short
    // delta writes nothing for several frames whatever the park did.
    const delta = "world ".repeat(40);
    store.applyDelta(a, parkedChat.turn, "say-a", "", 2, delta);
    store.applyDelta(b, activeChat.turn, "say-b", "", 2, delta);
    await flushed();

    // The control LANDING is the wait, read off the rendered text rather than a frame
    // count or a record count: it is the product's own output through the same writer
    // the parked chat holds.
    await vi.waitFor(() => {
      expect(liveText(b)).toContain("world world");
    });

    // The park settles the text the store held BEFORE it — the release finishes the
    // reveal, which is what lands the withheld character — and takes nothing after it.
    expect(liveText(a)).toBe("hello");
    expect(liveText(a)).not.toContain("world");
  });
});

// ---------------------------------------------------------------------------
// A DETACHED render has no park door and needs none: its only teardown is
// `disposeDetachedBody`, which reaches `disposeAll` -> `releaseOpenTail`. An UNDISPOSED
// sibling lane is the control, or a frozen tail reads like a delta not yet emitted.
// ---------------------------------------------------------------------------

/** The projected turn a detached render takes: a REAL turn of the store, no lane stripped. */
function turnOf(chatID: string, turnID: string): Turn {
  const s = store.get(chatID);
  if (s === undefined) {
    throw new Error(`no session ${chatID}`);
  }
  const t = turns.projectTurn(s, turnID);
  if (t === undefined) {
    throw new Error(`turn ${turnID} is not drawn`);
  }
  return t;
}

function hostText(host: HTMLElement): string {
  return host.querySelector(".message.assistant")?.textContent ?? "";
}

describe("a detached render's open tail", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("stops consuming deltas once disposed, while an undisposed lane keeps growing", async () => {
    const c = freshID("c-detached");
    const { chat, turn } = streaming(c);
    store.setSessions([chat]);
    store.setActive(c);
    store.bumpMessages(c, "load");

    const laneGone = "lane-disposed";
    const laneKept = "lane-kept";
    store.openEntry(c, laned(turn, "say-gone", laneGone, "hello"));
    store.openEntry(c, laned(turn, "say-kept", laneKept, "hello"));

    const hostGone = document.createElement("div");
    const hostKept = document.createElement("div");
    document.body.append(hostGone, hostKept);
    blocks.buildDetachedBody(hostGone, turnOf(c, turn), c, laneGone, true);
    blocks.buildDetachedBody(hostKept, turnOf(c, turn), c, laneKept, true);

    // THE PRECONDITION, asserted rather than assumed: without a mounted tail on each,
    // the freeze below passes for a render that never had a subscription.
    await vi.waitFor(() => {
      expect(hostText(hostGone)).toContain("hell");
      expect(hostText(hostKept)).toContain("hell");
    });

    blocks.disposeDetachedBody(turn, laneGone);

    // Long enough that an armed reveal emits inside a frame (`reveal.ts` withholds
    // growth under `MIN_EMIT_CHARS`).
    const delta = "world ".repeat(40);
    store.applyDelta(c, turn, "say-gone", laneGone, 2, delta);
    store.applyDelta(c, turn, "say-kept", laneKept, 2, delta);

    // The control LANDING is the wait, through the same writer the disposed render held.
    await vi.waitFor(() => {
      expect(hostText(hostKept)).toContain("world world");
    });

    // `finishNow` lands the withheld character, so the disposed render settles at the
    // text the store held BEFORE the dispose and takes nothing after it.
    expect(hostText(hostGone)).toBe("hello");
    expect(hostText(hostGone)).not.toContain("world");

    blocks.disposeDetachedBody(turn, laneKept);
    hostGone.remove();
    hostKept.remove();
  });
});
