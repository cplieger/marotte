// ---------------------------------------------------------------------------
// Which BATCH a folded card's window move is collected into, and the read that
// used to decide it.
//
// `applyFoldPass` splits its queued changes in two: a "head" change runs inside
// `preserveReadingPosition` (its height delta is compensated out of the reader's
// scroll position), a "tail" change runs outside it. `collectWindowMove` reads the
// BODY's own `offsetTop` to pick a side — and a folded card's body is
// `block-size: 0`, so it reports 0 and answers "head" whatever the card's real
// position is. Reading it also forces the browser to render the subtree
// `content-visibility: hidden` told it to skip.
//
// Two observables, because the defect had two halves. The READ: a move over a folded
// card's body must measure nothing inside the skipped subtree. The ANSWER: the change
// must be filed under the CARD's side, so a mutant hardcoding "head" for a skipped
// body is caught rather than waved through. The compensation itself needs geometry the
// scroll mock has none of; the SIDE is observable, because a "head" change runs inside
// `preserveReadingPosition` and a "tail" change after it.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import type { ShiftKind } from "./scroll.js";

// messages.ts's graph reads the shared DOM registry at module scope, and `byId`
// throws on a missing element, so the hosts exist before any import resolves.
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

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// The network edge, stubbed as a full factory rather than a spy: this graph reaches the
// run store's fetch, and a spy would call through to a real request. Inert — no run is
// in play here.
vi.mock("./api-client.js", () => ({
  apiPost: vi.fn(),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiGetOrError: vi.fn(),
}));

const store = await import("./store.js");
const messages = await import("./messages.js");
const scroll = await import("./scroll.js");
const { mountedWindow, geometrySkipped } = await import("./messages-blocks.js");
const { OVERSCAN_ENTRIES } = await import("./block-window.js");
const { KEY_ATTR } = await import("./reconcile.js");

messages.mountChatView();

/** Entries in the folded turn: several overscan windows, so a demand range around one
 *  ordinal is a strict subset of the body and moving the pin genuinely moves the window. */
const ENTRIES = OVERSCAN_ENTRIES * 6;

let seq = 0;

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  };
}

function turnOpen(turnID: string, n: number, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

/** A turn of `count` REASONING entries plus its close.
 *
 *  Reasoning rather than prose, and that is the fixture the entry model forces: `sliceTurn`
 *  snaps a window down to a prose run's first entry and up past its last, so a body of N
 *  `text` entries is ONE run and every window over it is the same window — the moves below
 *  would be unobservable. A `thinking` entry renders at its own position and joins no run,
 *  so the body has `count` ordinals a window can sit inside. */
function reasoningTurn(turnID: string, n: number, count: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `step ${String(at)}` }));
  }
  out.push(sealed(turnID, count + 1, "turn_close", { outcome: "completed" }));
  return out;
}

/** A chat holding whole turns, the shape a page GET lands. */
function seed(turnEntries: readonly (readonly Entry[])[]): string {
  const chat = `c-side-${String(++seq)}`;
  const s = makeSession({ id: chat, name: chat });
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  store.setSessions([s]);
  store.setActive(chat);
  store.bumpMessages(chat, "load");
  return chat;
}

function turnCard(turnID: string): HTMLElement {
  const card = (messages.activeTranscriptView() ?? document.body).querySelector<HTMLElement>(
    `:scope > [${KEY_ATTR}="${turnID}"]`,
  );
  if (card === null) {
    throw new Error(`no card for turn ${turnID}`);
  }
  return card;
}

function bodyOf(turnID: string): HTMLElement {
  const body = turnCard(turnID).querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null) {
    throw new Error(`no body for turn ${turnID}`);
  }
  return body;
}

/** TWO turns so the policy folds the older one, a body big enough that a demand range is
 *  a strict subset of it, and the reveal grant that gives that folded card a body WITHOUT
 *  unfolding it — the "hidden build" the boot fold pass performs. */
async function arrangeFoldedBodiedCard(): Promise<{
  chat: string;
  first: ReturnType<typeof mountedWindow>;
}> {
  const chat = seed([reasoningTurn("t-fold", 1, ENTRIES), reasoningTurn("t-new", 2, 2)]);
  expect(turnCard("t-fold").hasAttribute("data-folded")).toBe(true);

  await messages.mountTurnBody(chat, "t-fold", 0);
  expect(turnCard("t-fold").hasAttribute("data-folded")).toBe(true);
  const first = mountedWindow("t-fold");
  expect(first).toBeDefined();

  // The body IS inside the skipped subtree, or either case below would pass for a read
  // nothing guards.
  expect(geometrySkipped(bodyOf("t-fold"))).toBe(true);

  return { chat, first };
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  vi.mocked(scroll.preserveReadingPosition).mockImplementation((mutate: () => void) => {
    mutate();
  });
});

describe("a window move over a folded card's body", () => {
  // ONE ORACLE DROPPED OUT LOUD: "moves the window without measuring a row the page is not
  // rendering". It cannot fail from here. The pass collects its move during the demand
  // grant's own settle rather than at the store bump, so a counting window narrow enough to
  // exclude the builder's unguarded read of this same body excludes the guarded read too —
  // measured, with `bodySide` mutated to read the body: this file's read assertion stayed
  // green while the filing assertion below went red. Both halves come from `bodySide`, so
  // what is left uncovered is the forced render, and it is unobservable until the builder's
  // own read is guarded (the hand-off in the box report). That fix is what makes this case
  // writable again.
  it("files the move under the card's own side, not the folded body's", async () => {
    const { chat, first } = await arrangeFoldedBodiedCard();

    // A scroller position the two sides DISAGREE about, or the case is a null check: the
    // mock's own `getScrollEl` answers a fresh detached div, so `scrollTop` is 0, and at 0
    // the card and the body both answer "tail" — which is what the pre-migration case
    // asserted, and it passed with the guard deleted. Between the two offsets, the CARD is
    // "head" and the body "tail", so the filing says which one was read.
    const card = turnCard("t-fold");
    const body = bodyOf("t-fold");
    expect(body.offsetTop).toBeGreaterThan(card.offsetTop);
    // A GETTER, because assigning `scrollTop` on a detached div is clamped to 0 — which is
    // the zero the pre-migration case was reading without saying so. Production only reads
    // this, so a read-only stand-in is the honest stub.
    const scroller = document.createElement("div");
    Object.defineProperty(scroller, "scrollTop", { get: () => body.offsetTop });
    vi.mocked(scroll.getScrollEl).mockReturnValue(scroller);

    // The HEAD DROP is the change `bodySide` files, so the head ordinal leaving the DOM is
    // what says which batch it ran in. `mountedWindow` is the wrong observable for it: the
    // window is also written by the tail extension beside it, which is filed "tail"
    // unconditionally, so a move that dropped its head inside the batch still reports its
    // range moving outside it.
    const lowestSeq = (): number =>
      Math.min(
        ...[...body.querySelectorAll<HTMLElement>("[data-entry-seq]")].map((e) =>
          Number(e.dataset["entrySeq"]),
        ),
      );
    const wasLowest = lowestSeq();
    expect(Number.isFinite(wasLowest)).toBe(true);
    let batches = 0;
    let droppedInside = false;
    vi.mocked(scroll.preserveReadingPosition).mockImplementation(
      (mutate: () => void, kind: ShiftKind) => {
        if (kind !== "content-growth") {
          mutate();
          return;
        }
        batches++;
        const before = lowestSeq();
        mutate();
        if (lowestSeq() > before) {
          droppedInside = true;
        }
      },
    );

    await messages.mountTurnBody(chat, "t-fold", ENTRIES - 1);
    store.bumpMessages(chat, "shape");

    expect(mountedWindow("t-fold")).not.toEqual(first);
    expect(lowestSeq()).toBeGreaterThan(wasLowest);
    // The batch ran, so either filing would have been observable in it.
    expect(batches).toBeGreaterThan(0);
    // The card is above the reader, so its own side is "head" and the drop belongs INSIDE
    // the compensated batch. Reading the body instead answers "tail" and files it after.
    expect(droppedInside).toBe(true);
  });
});
