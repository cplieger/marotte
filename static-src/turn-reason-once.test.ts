// A FAILED TURN STATES ITS REASON EXACTLY ONCE, IN ONE CARD, IN BOTH FOLD STATES.
//
// This is the composition half of the ownership rule, and it is the half no
// existing suite reaches. Three surfaces of one card could each carry the server's
// prose, and each is pinned somewhere on its own:
//
//   - the card-level `.turn-notice`   -> `turnFailureText`, pinned in turns.node.test.ts
//   - the folded card's `.turn-face`  -> `syncTurnFace` deliberately mounts none,
//     pinned by a COMMENT at messages.ts and by nothing else
//   - the footer's outcome lead       -> `OUTCOME_LEAD`, a fixed word per outcome
//
// THERE IS NO FOURTH SURFACE: a broken turn's body carries no `.boundary` divider. A
// turn's own end is its `turn_close.outcome`, and `messages-events.ts` builds a
// boundary for FOUR entry kinds only — `compaction`, `compaction_failed`,
// `safety_blocked` and `model_switched` — so an interrupted or cancelled turn renders
// no divider at all and no labelFn can carry a reason into one. The FOOTER is what
// case 1 asserts against instead: present, marking the boundary, WITHOUT the prose,
// which is also the control the two cancelled cases below read.
//
// Three fixtures, three suites, and nothing asserting the SUM. So a reason on the
// face puts the sentence on screen twice with every existing test still green —
// which is exactly the defect `messages-events.ts` records as measured on a live
// chat, about 50px apart.
//
// The FOLDED case is the one with no other guard at all: the face is the surface a
// broken turn's reader sees after collapsing it, and `syncTurnFace`'s "No failure
// text here" is a comment rather than a test.
//
// TWO TURNS IN EVERY FIXTURE, and that is a requirement rather than realism:
// `isTurnOpen` returns true for the newest turn UNCONDITIONALLY, above the
// overrides, so a one-turn chat cannot be folded at all and a folded case built on
// one would assert against an open card while reading as though it had folded.
//
// Counted over the rendered card's own text, through the REAL paint, because the
// property is about what one card shows rather than about what any one builder
// returns.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";

// The render graph reaches the shared DOM registry, which throws on a missing app
// root. Every id has to exist before the imports below are evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every other suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The graph's network edge. The rail's session-wide turn index is the one GET a
// paint issues, and it must not reach a real fetch.
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");

mountChatView();

/** The server's own sentence. Deliberately unlike anything else the card renders:
 *  the header carries the prompt, the footer carries a fixed outcome word, so a
 *  count over the card's text can only be counting this. */
const REASON = "ACP bridge exited";

/** The broken turn's opening message id, which IS its turn id — the key
 *  `setTurnOpen` records an override under. */
const BROKEN_TURN = "u1";

/** One sealed entry of `turnID`, at `seq`. */
function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

/** A prompt-opened turn: the `turn_open` the appender stamped, TWO text entries, and
 *  the `turn_close` carrying the outcome — and, for a turn that did not end cleanly,
 *  its own account. `turnFailureText`'s specific source is that close's
 *  `failure_reason`.
 *
 *  TWO text entries rather than one, and that is a requirement rather than realism:
 *  `turnFoldHides` counts a body of one text entry as hiding NOTHING, and
 *  `planResidency` keeps such a turn open whatever the override says (its face would
 *  be identical to its body). A folded case built on a one-entry turn would assert
 *  against an open card while reading as though it had folded. */
function turnEntries(
  id: string,
  n: number,
  request: string,
  reply: string,
  outcome: string,
  reason?: string,
): Entry[] {
  return [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: request }, source: "prompt", n }),
    sealed(id, 1, "text", { text: `Working on it.` }),
    sealed(id, 2, "text", { text: reply }),
    sealed(
      id,
      3,
      "turn_close",
      reason === undefined ? { outcome } : { outcome, failure_reason: reason },
    ),
  ];
}

/** A turn that broke, followed by one that did not. The first turn is the subject;
 *  the second exists so the first is not the newest and can therefore fold. */
function brokenThenClean(): Entry[][] {
  return [
    turnEntries(BROKEN_TURN, 1, "run the build", "Building.", "interrupted", REASON),
    turnEntries("u2", 2, "try again", "Built.", "completed"),
  ];
}

/** The same pair with the first turn ended cleanly. The NEGATIVE CONTROL: without
 *  it every count assertion below passes just as well for a card that renders no
 *  reason at all. */
function twoCleanTurns(): Entry[][] {
  return [
    turnEntries(BROKEN_TURN, 1, "run the build", "Building.", "completed"),
    turnEntries("u2", 2, "try again", "Built.", "completed"),
  ];
}

/** The sentence a cancelled turn used to carry in its own record, and still carries
 *  on every one persisted before `defaultFailureReason` stopped supplying it. */
const CANCELLED_REASON = "The turn was cancelled.";

/** A cancelled turn followed by a clean one, in the HISTORICAL shape: the close
 *  stamped `cancelled` AND still holding the dead sentence. The second turn exists so
 *  the first can fold, exactly as in `brokenThenClean`. */
function cancelledThenClean(): Entry[][] {
  return [
    turnEntries(BROKEN_TURN, 1, "run the build", "Building.", "cancelled", CANCELLED_REASON),
    turnEntries("u2", 2, "try again", "Built.", "completed"),
  ];
}

/** The cards of the active view, in document order. */
function cards(): HTMLElement[] {
  const root = activeTranscriptView();
  return root === null ? [] : [...root.querySelectorAll<HTMLElement>(":scope > .turn")];
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

/** The fold pass applies its transitions through a compensated batch rather than in the
 *  paint that planned them, so a card's `data-folded` lands a frame or two after `mount`
 *  resolves and after a toggle click. Waiting on the card count alone reads the pre-fold
 *  state, which is the premise every folded case here opens with. */
async function untilFolded(card: HTMLElement, folded: boolean): Promise<void> {
  await until(
    () => card.hasAttribute("data-folded") === folded,
    `data-folded to read ${String(folded)}`,
  );
}

/** Paint `turns` as `chatID`'s window and return that chat's turn cards, in order. */
async function mount(chatID: string, turnRows: Entry[][]): Promise<HTMLElement[]> {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnRows) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chatID, name: chatID }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chatID);
  bumpMessages(chatID, "load");
  await until(() => cards().length === order.length, `${String(order.length)} cards for ${chatID}`);
  return cards();
}

/** How many times `needle` occurs in `haystack`. `indexOf` rather than a regex,
 *  because the needle is upstream prose and may carry regex metacharacters. */
function occurrences(haystack: string, needle: string): number {
  let n = 0;
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + 1)) {
    n += 1;
  }
  return n;
}

describe("a failed turn's reason", () => {
  beforeEach(() => {
    resetFoldState();
  });

  it("appears exactly once on an OPEN card, and not in the body", async () => {
    const chat = "open-broken";
    setTurnOpen(chat, BROKEN_TURN, true);
    const [card] = await mount(chat, brokenThenClean());
    if (card === undefined) {
      throw new Error("no card");
    }

    // The premise: this case is about an OPEN card, so it must be one.
    expect(card.hasAttribute("data-folded"), "the subject card is open").toBe(false);
    expect(occurrences(card.textContent ?? "", REASON), "one card, one sentence").toBe(1);

    // WHERE it is, so the count cannot be satisfied by the wrong surface.
    const notice = card.querySelector<HTMLElement>(":scope > .turn-notice");
    expect(notice, "the notice is the surface that carries it").not.toBeNull();
    expect(notice?.textContent).toBe(REASON);

    // The BODY carries the turn's own work and no account of its end: a turn's end is
    // its `turn_close`, which renders no row of its own, so the reason may not reach
    // any body element.
    const body = card.querySelector<HTMLElement>(":scope > .turn-body");
    expect(body, "the open card has a body").not.toBeNull();
    expect(body?.textContent ?? "", "the body renders the work, never the reason").not.toContain(
      REASON,
    );

    // THE NEGATIVE CONTROL, at the one surface a broken turn's body has: the footer
    // names HOW the turn ended, without the prose. Its presence is what
    // makes the two absences above meaningful — a card rendering nothing at all about
    // its end would pass them trivially. `Interrupted` is `OUTCOME_LEAD`'s word.
    const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
    expect(footer, "the footer is rendered").not.toBeNull();
    expect(footer?.textContent ?? "").toContain("Interrupted");
    expect(footer?.textContent ?? "", "the footer names the kind, not the reason").not.toContain(
      REASON,
    );
  });

  it("appears exactly once on a FOLDED card, where the face could repeat it", async () => {
    const chat = "folded-broken";
    setTurnOpen(chat, BROKEN_TURN, false);
    const [card] = await mount(chat, brokenThenClean());
    if (card === undefined) {
      throw new Error("no card");
    }

    // The premise, and the reason this fixture carries a second turn: an override
    // is the only thing that folds a BROKEN turn (the policy never auto-folds one),
    // and no override can fold the newest.
    await untilFolded(card, true);
    expect(card.hasAttribute("data-folded"), "the subject card is folded").toBe(true);
    expect(occurrences(card.textContent ?? "", REASON), "one card, one sentence").toBe(1);
    expect(
      card.querySelector<HTMLElement>(":scope > .turn-notice"),
      "the notice survives the fold — that is why it owns the prose",
    ).not.toBeNull();

    const face = card.querySelector<HTMLElement>(":scope > .turn-face");
    if (face !== null) {
      expect(face.textContent ?? "", "the face renders the answer, never the reason").not.toContain(
        REASON,
      );
    }
  });

  it("mounts the FOLDED card's notice after its face, not between body and face", async () => {
    // The order is load-bearing twice over, and neither half had a guard.
    // READING: the notice says why the turn stopped, so it belongs after what the
    // turn produced — before it, the error sat above the answer. CASCADE: the
    // hidden body is still in the DOM at `block-size: 0`, so a notice adjacent to
    // it matches 29-turns.css's divider-is-the-frame rules, which suppress the
    // notice's own top rule — and the face's fill IS the body's, so with that rule
    // suppressed nothing at all would mark the edge between the answer and the
    // reason. (It used to read "the header has already dropped its bottom border on
    // the promise that the element below draws one"; that seam is gone outright now
    // — 29-turns.css's file header — and the consequence of the wrong order is the
    // same either way.)
    //
    // What decides it is `syncTurnFace`'s ANCHOR, not the call order: the face has
    // five call sites against the notice's two, and the fold toggle plus the fold
    // pass mount a face without touching the notice — so a face anchored on the
    // footer lands wherever the notice is not. Which is why this drives the real
    // paint AND the reader's own toggle rather than any one sync function.
    // Painted OPEN first and then folded, so the body is still mounted when the
    // face arrives: that is the shape the cascade half is about, and a card born
    // folded builds no body at all (`mountTurn` skips it), which would pass the
    // ordering assertion without ever putting a body beside a notice.
    const chat = "folded-order";
    const REGIONS = ["turn-body", "turn-face", "turn-notice", "turn-footer"];
    setTurnOpen(chat, BROKEN_TURN, true);
    const [card] = await mount(chat, brokenThenClean());
    if (card === undefined) {
      throw new Error("no card");
    }
    expect(
      card.querySelector(":scope > .turn-body"),
      "premise: the open card has a body for the face to be inserted after",
    ).not.toBeNull();

    // The reader's own gesture, not another paint: `setTurnOpen` is consulted by
    // the fold pass, and a second `mount` of the same chat does not re-run it.
    card.querySelector<HTMLButtonElement>(":scope > .turn-header .turn-fold-toggle")?.click();
    await untilFolded(card, true);
    expect(card.hasAttribute("data-folded"), "the subject card is folded").toBe(true);

    const kinds = [...card.children]
      .map((c) => REGIONS.find((k) => c.classList.contains(k)))
      .filter((k) => k !== undefined);
    expect(kinds, "the notice is the last region before the ledger").toEqual(
      REGIONS.filter((k) => kinds.includes(k)),
    );
    expect(kinds, "and the face is on screen for it to follow").toContain("turn-face");
  });

  it("renders no notice at all for a turn that ended cleanly", async () => {
    // The negative control for both cases above.
    const chat = "clean";
    setTurnOpen(chat, BROKEN_TURN, true);
    const [card] = await mount(chat, twoCleanTurns());
    if (card === undefined) {
      throw new Error("no card");
    }
    expect(card.querySelector(":scope > .turn-notice")).toBeNull();
    expect(occurrences(card.textContent ?? "", REASON)).toBe(0);
  });
});

describe("a cancelled turn's ONE status channel", () => {
  // The reader caused the stop, so the footer's own outcome word is the whole
  // record and a notice beside it would render one fact twice. Asserted through the
  // real paint because that is the only harness here that can answer what a reader
  // actually sees, and in BOTH fold states because the notice survives a fold —
  // which is exactly why it had to be the surface that goes.
  //
  // The fixture carries the HISTORICAL shape (`turn_failure_reason` still on the
  // carrier), so these two cases fail if `turnFailureText`'s outcome gate is
  // reverted, not merely if the default sentence comes back.
  beforeEach(() => {
    resetFoldState();
  });

  for (const open of [true, false]) {
    it(`renders no notice and keeps the footer word on an ${open ? "OPEN" : "FOLDED"} card`, async () => {
      const chat = `cancelled-${open ? "open" : "folded"}`;
      setTurnOpen(chat, BROKEN_TURN, open);
      const [card] = await mount(chat, cancelledThenClean());
      if (card === undefined) {
        throw new Error("no card");
      }

      // The premise, so neither case silently tests the other's fold state.
      await untilFolded(card, !open);
      expect(card.hasAttribute("data-folded"), "the subject card's fold state").toBe(!open);

      expect(
        card.querySelector(":scope > .turn-notice"),
        "a cancel mounts no notice at all",
      ).toBeNull();
      expect(
        card.textContent ?? "",
        "and the dead sentence reaches no surface of the card",
      ).not.toContain(CANCELLED_REASON);

      // THE NEGATIVE CONTROL, and it is what stops both assertions above passing for
      // a card that renders nothing: the footer still says which way the turn ended.
      // `Cancelled` is `OUTCOME_LEAD`'s word (fundamentals/turn-footer.ts).
      const footer = card.querySelector<HTMLElement>(":scope > .turn-footer");
      expect(footer, "the footer is rendered").not.toBeNull();
      expect(footer?.textContent ?? "").toContain("Cancelled");
    });
  }
});
