// `collectWindowMove` must pick a folded card's head/tail side from the CARD: its body is `block-size: 0`, so the
// body's `offsetTop` answers "head" and reading it forces a render of a `content-visibility: hidden` subtree.
// A head change runs inside `preserveReadingPosition`, a tail change after it, so the filing is observable.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import type { ShiftKind } from "./scroll.js";

// The graph reads the DOM registry at module scope and `byId` throws on a missing element.
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
// A full factory rather than a spy, which would call through to a real request.
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

/** Several overscan windows, so a demand range around one ordinal is a strict subset of the body. */
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

/**
 * A turn of `count` reasoning entries plus its close. Not `text`: `sliceTurn` snaps a window to a prose run's
 * bounds, so a text body is one run and every window over it is the same.
 */
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

/** Two turns so the older folds, plus the reveal grant that gives the folded card a body without unfolding it. */
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

  // The body is inside the skipped subtree, or the cases below would pass for an unguarded read.
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
  // The no-measure half is not asserted here: the move is collected during the demand grant's settle, so no
  // counting window separates the guarded read from the builder's own unguarded one.
  it("files the move under the card's own side, not the folded body's", async () => {
    const { chat, first } = await arrangeFoldedBodiedCard();

    // A scroll position the two sides disagree about: at 0 both answer "tail", which passes with the guard deleted.
    const card = turnCard("t-fold");
    const body = bodyOf("t-fold");
    expect(body.offsetTop).toBeGreaterThan(card.offsetTop);
    // A getter, because assigning `scrollTop` on a detached div is clamped to 0.
    const scroller = document.createElement("div");
    Object.defineProperty(scroller, "scrollTop", { get: () => body.offsetTop });
    vi.mocked(scroll.getScrollEl).mockReturnValue(scroller);

    // The head drop is what `bodySide` files; `mountedWindow` is also written by the tail extension, filed "tail"
    // unconditionally, so it cannot say which batch ran the drop.
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
    // The batch ran, so either filing would have been observable.
    expect(batches).toBeGreaterThan(0);
    // The card is above the reader, so its side is "head"; reading the body would answer "tail".
    expect(droppedInside).toBe(true);
  });
});
