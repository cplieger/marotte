// ---------------------------------------------------------------------------
// A CARD'S ORDINAL AND ITS ANCHOR ID BOTH COME FROM `turn_open.n`.
//
// The turn-base mechanism this file was written for is DELETED. The store held a
// newest-first window of MESSAGES, `projectTurns`' scan could not know what preceded
// it, and told nothing it numbered turn 1 of the PAGE as turn 1 of the session — so
// the window response carried its left edge (`turn_offset` / `turn_segment_closed`)
// and `store.ts turnBaseOf` fed that base to the paint pass. Measured: `turn_offset`,
// `turn_segment_closed`, `turnBaseOf`, `TurnWindowBase` and `WHOLE_SESSION` appear in
// NO production module — two comments mentioning the old names are the only hits. The
// appender assigns `n` at the turn's open, so an ordinal is session-absolute in every
// window and nothing carries a base across a page edge.
//
// WHAT SURVIVES IS THE JOIN, and it is pinned nowhere else: `messages.ts` reads that
// ordinal into the header AND into `turnAnchorID`, so a `#turn-N` fragment names the
// turn the rail's own session-wide index calls by that number. The two ends are each
// covered already — `turns.node.test.ts` pins `projectTurns` taking the ordinal from
// `turn_open.n` rather than the window's position, and `fundamentals/turn-header.test.ts`
// pins the header rendering `#N` from the number it is handed — and neither can see a
// paint that hands one of them a different number from the other.
//
// TWO ORACLES DROPPED OUT LOUD:
//
//   (1) "keeps every visible turn's number when an older page is prepended", which was
//   this file's centrepiece. Its mechanism is the base being REPLACED per page, and
//   there is no base: a prepend adds older turns carrying their own `n` and recomputes
//   nothing, so the case is true by construction. Its one residual killer — a renderer
//   deriving the number positionally, which a prepend would renumber — is killed by
//   case 1 below (a window starting at 12 reads `#1` under that mutant) and, at the
//   projection, by `turns.node.test.ts`.
//
//   (2) "resolves a folded row's search-hit count against the server's absolute turn".
//   The defect it pinned is unreachable AND its fixture is unrepresentable: there is no
//   window-local `n` to look an absolute key up with, and `Hit` is reshaped to
//   `{turn_id, entry_id, segment_kind, offset, segment_len}` with no `turn` member to
//   stage. HAND-OFF, by file and field: `chat-search.ts` still keys `countsByTurn` by
//   `h.turn` (its own diagnostic, and that module is not this fence's), and
//   `messages.ts:1467` still asks `searchHitCount(t.n)`. When that keying moves to
//   `turn_id` the call site moves to `t.id`, and the badge — that the count lands on the
//   turn the server said matched and on no other — wants a case then.
//
// REAL PAINT over the real renderer, because what is under test is what `messages.ts`
// WRITES; no geometry is staged, since neither case reads a box.
// ---------------------------------------------------------------------------
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// The DOM the renderer's import graph resolves at load, nested the way the page nests
// it: `#messages-wrap` is the scroller inside the positioned wrapper.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}

// The rail's session-wide index is its own fetch and is not what these cases are
// about. Spy-wrapped rather than replaced: this module has a dozen other exports the
// graph links.
vi.mock("./api-client.js", { spy: true });

const store = await import("./store.js");
const messages = await import("./messages.js");
const { apiGet } = await import("./api-client.js");

messages.mountChatView();

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

/** A reader-opened turn at session ordinal `n`: the `turn_open` the appender stamped,
 *  one text entry so the body has something, and its close. */
function promptTurn(n: number, request: string): Entry[] {
  const id = `t${String(n)}`;
  return [
    sealed(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: request },
      source: "prompt",
      n,
    }),
    sealed(id, 1, "text", { text: `reply ${String(n)}` }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

/** An AGENT-INITIATED turn at ordinal `n`: no `prompt` on its `turn_open`, so the header
 *  has no request to render and `turnIsDrawn` admits it on its body's text entry. The
 *  population case 1 cannot reach. */
function agentTurn(n: number): Entry[] {
  const id = `t${String(n)}`;
  return [
    sealed(id, 0, "turn_open", { source: "agent", n }),
    sealed(id, 1, "text", { text: `unprompted ${String(n)}` }),
    sealed(id, 2, "turn_close", { outcome: "completed" }),
  ];
}

let seq = 0;
/** A chat id no earlier case has used: `setActive` is a no-op for the id already
 *  active, so a reused id paints nothing and the case runs against an empty view. */
function nextChat(): string {
  seq += 1;
  return `n${String(seq)}`;
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${String(FRAME_BUDGET_MS)}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

function cards(): HTMLElement[] {
  const root = messages.activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
}

/** What the reader SEES: the `#N` each card's header renders, in document order. */
function renderedNumbers(): string[] {
  return cards().map((c) => c.querySelector(".turn-n")?.textContent ?? "");
}

/** The anchor id each card carries (`turnAnchorID`), in document order. */
function anchorIDs(): string[] {
  return cards().map((c) => c.id);
}

/** Paint `turnEntries` as chat `id`'s window and wait for the cards to mount.
 *
 *  `turn_count` is the WHOLE chat's, so a window of three turns out of fourteen is
 *  honest about its left edge — which is the shape the retired base mechanism existed
 *  for and the one an absolute ordinal has to be right about without it. */
async function paint(
  id: string,
  turnEntries: readonly (readonly Entry[])[],
  total: number,
): Promise<void> {
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
  store.setSessions([
    {
      ...makeSession({ id, name: id, has_more: order.length < total }),
      turns,
      turn_order: order,
      turn_count: total,
    },
  ]);
  store.setActive(id);
  store.bumpMessages(id, "load");
  await until(
    () => cards().length === order.length,
    `${String(order.length)} cards to mount for ${id}`,
  );
}

beforeEach(() => {
  vi.mocked(apiGet).mockResolvedValue({ turns: [] });
});

describe("a paged transcript's rendered turn numbers", () => {
  it("numbers a window from `turn_open.n`, and the anchor id agrees", async () => {
    // Turns 12-14 of a 14-turn chat: eleven precede the window, so the oldest card the
    // reader can see is #12. A renderer counting the window's own positions reads
    // #1..#3, which is the defect the deleted base mechanism existed to prevent and
    // which the appender's own `n` now makes unreachable.
    const id = nextChat();
    await paint(
      id,
      [promptTurn(12, "twelfth"), promptTurn(13, "thirteenth"), promptTurn(14, "last")],
      14,
    );

    expect(renderedNumbers()).toEqual(["#12", "#13", "#14"]);
    // The anchor moves with the number, so a `#turn-{n}` fragment names the turn the
    // rail's own session-wide index calls by that number. Asserted beside the header's
    // text because ONE paint writes both and nothing else compares them.
    expect(anchorIDs()).toEqual(["turn-12", "turn-13", "turn-14"]);
  });

  it("takes an agent-initiated turn's ordinal from `n` as well", async () => {
    // The header's other branch: no `prompt` on the `turn_open`, so there is no request
    // to read a number out of and `data-trigger` says the trigger was the system's.
    // A join that derived the ordinal from the trigger has nothing to derive it from
    // here, so this is the case that says it is read off `n` rather than off the prompt.
    const id = nextChat();
    await paint(id, [promptTurn(7, "seventh"), agentTurn(8)], 8);

    expect(renderedNumbers()).toEqual(["#7", "#8"]);
    expect(anchorIDs()).toEqual(["turn-7", "turn-8"]);
    const trigger = cards().map(
      (c) => c.querySelector<HTMLElement>(":scope > .turn-header")?.dataset["trigger"] ?? "",
    );
    expect(trigger).toEqual(["user", "system"]);
  });
});
