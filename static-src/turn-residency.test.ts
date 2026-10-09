// ---------------------------------------------------------------------------
// Turn residency: which turns carry a real `.turn-body`. The fold policy owns OPEN/CLOSED
// (fold-state.test.ts); `block-window.ts` decides MOUNTEDNESS on an entry and tool-card budget
// (block-window.node.test.ts). Here: a non-resident turn is a header/footer stub whose body is built
// on interaction or a fold-pass transition, and a stub always offers the toggle. Fixtures are BIG
// because residency is priced in paint cost. Heavy turns use `thinking` entries: `sliceTurn` merges
// a run of `text` entries into ONE element.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { userEvent } from "vitest/browser";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

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

// The canonical scroll.ts mock; its deferral runs its mutation, so the fold pass applies
// immediately unless a case holds the queue open.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The clipboard action, so what a turn's Copy hands over is observable. A REPLACING
// factory naming both exports: real-ESM linking fails on a missing one, and the second
// is the error explainer this graph also imports.
const { copied } = vi.hoisted(() => ({ copied: [] as string[] }));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: {
    dispatch: (text: string) => {
      copied.push(text);
      return Promise.resolve();
    },
  },
  explainError: { dispatch: () => Promise.resolve(null) },
}));

// The graph's network edge, real except for the two GETs these cases drive: a
// run's state (the live-run fold input) and the rail's session-wide turn index.
const runStatus = new Map<string, string>();
const railTurns: { turns: unknown[] } = { turns: [] };
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn((path: string) => {
    const run = /^\/api\/runs\/([^/?]+)$/.exec(path);
    if (run !== null) {
      const id = decodeURIComponent(run[1] ?? "");
      const status = runStatus.get(id);
      return Promise.resolve(
        status === undefined ? null : { workflowId: id, state: { workflowId: id, status } },
      );
    }
    if (/^\/api\/chats\/[^/]+\/turns$/.test(path)) {
      return Promise.resolve(railTurns);
    }
    return Promise.resolve(null);
  }),
}));

const { mountChatView, mountTurnBody, mountTurnBodyForWalk, endWalkReveal, activeTranscriptView } =
  await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { setTurnOpen, openForSearch, clearSearchOpened, resetFoldState } =
  await import("./fold-state.js");
const { OVERSCAN_ENTRIES, RESIDENT_ENTRIES, RESIDENT_TOOL_CALLS } =
  await import("./block-window.js");
const { entryTextSigs, ensureEntryTextSig, entryKey } = await import("./store-signals.js");
const { mountedWindow } = await import("./messages-blocks.js");
const { invalidateRun } = await import("./run-store.js");
const { loadTurnRail, resetTurnRail } = await import("./turn-rail.js");
const { scrollMock } = await import("./__test-helpers__/scroll-mock.js");
const { KEY_ATTR } = await import("./reconcile.js");

const messagesEl = document.getElementById("messages") as HTMLElement;

/** The whole grant a demand build gets: one overscan each side of the ordinal asked
 *  for. Restated as one name because half these cases are about its bound. */
const GRANT = 2 * OVERSCAN_ENTRIES;

// --- Fixtures ---------------------------------------------------------------

function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  lane = "",
): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    lane,
    payload,
  } as Entry;
}

function turnOpen(turnID: string, n = 1, text = `prompt ${turnID}`): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

function turnClose(turnID: string, at: number, outcome = "completed", extra = {}): Entry {
  return sealed(turnID, at, "turn_close", { outcome, ...extra });
}

/** One PROSE turn: a single sealed text entry, so `turnFoldHides` reads false and the
 *  turn's face equals its body. One entry of budget. */
function proseTurn(turnID: string, n = 1): Entry[] {
  return [
    turnOpen(turnID, n),
    sealed(turnID, 1, "text", { text: `reply ${turnID}` }),
    turnClose(turnID, 2),
  ];
}

/** `n` prose turns, turn i's id `u{i}` (1-based). Nowhere near the budget. */
function plainTurns(n: number): Entry[][] {
  return Array.from({ length: n }, (_, i) => proseTurn(`u${String(i + 1)}`, i + 1));
}

/** One turn that ran a TOOL, so its fold HIDES something and it offers the toggle.
 *  A prose-only turn does not: its face equals its body, so it carries `data-no-fold`
 *  and stays open. */
function toolTurn(turnID: string, n = 1): Entry[] {
  return [
    turnOpen(turnID, n),
    sealed(turnID, 1, "tool_call", {
      id: `${turnID}-tc`,
      title: "Read File",
      kind: "read",
      status: "completed",
      ts: 1,
    }),
    sealed(turnID, 2, "text", { text: `reply ${turnID}` }),
    turnClose(turnID, 3),
  ];
}

function toolTurns(n: number): Entry[][] {
  return Array.from({ length: n }, (_, i) => toolTurn(`u${String(i + 1)}`, i + 1));
}

/** ONE tool-bearing turn whose `turn_close` carries `extra` (the outcome fields). The tool card puts
 *  it on the ordinary residency ladder; a prose-only turn is `data-no-fold`, open and mounted at
 *  any distance, and was measured to keep the outcome cases green with the exemption deleted. */
function outcomeTurn(outcome: string, extra = {}): Entry[] {
  const entries = toolTurn("u1");
  entries[entries.length - 1] = turnClose("u1", 3, outcome, extra);
  return entries;
}

/** One turn costing `count` ordinals: `count` sealed `thinking` entries, each its own
 *  mounted element. */
function heavyTurn(turnID: string, count: number, n = 1): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `chunk ${String(at)}` }));
  }
  out.push(turnClose(turnID, count + 1));
  return out;
}

/** `heavyTurn` whose LAST ordinal is an emphasised TEXT entry. The store keeps the
 *  markers and the markdown renderer eats them, so `**` in a clipboard names the
 *  store's answer and its absence names the mounted body's. */
function markedTail(turnID: string, count: number, n = 1): Entry[] {
  const out = heavyTurn(turnID, count, n);
  out[count] = sealed(turnID, count, "text", { text: `**chunk ${String(count)}**` });
  return out;
}

/** One turn whose body carries an inline EVENT beside its prose. A `compaction` entry
 *  is built by messages-events.ts rather than by the block dispatcher, so it is the
 *  body row a mountedness question has to answer from the DOM. */
function eventTurn(turnID: string): Entry[] {
  return [
    turnOpen(turnID),
    sealed(turnID, 1, "text", { text: `**reply ${turnID}**` }),
    sealed(turnID, 2, "compaction", { summary: "" }),
    turnClose(turnID, 3),
  ];
}

/** One turn holding `tools` settled tool cards — cheap in ENTRIES relative to the
 *  entry budget, expensive in cards. */
function toolHeavyTurn(turnID: string, tools: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID)];
  for (let at = 1; at <= tools; at++) {
    out.push(
      sealed(turnID, at, "tool_call", {
        id: `${turnID}-tc${String(at)}`,
        title: "Read File",
        kind: "read",
        status: "completed",
        ts: at,
      }),
    );
  }
  out.push(turnClose(turnID, tools + 1));
  return out;
}

/** A chat holding whole turns, the shape a page GET lands. `live` makes the newest
 *  turn RUNNING, which the live-turn case needs and no other case may have. */
function activate(chatID: string, turnEntries: readonly (readonly Entry[])[], live = false): void {
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
      ...makeSession({
        id: chatID,
        name: "c",
        model: "",
        thinking: live,
        working_label: "Thinking",
      }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chatID);
  // A same-id re-activation writes no signal the paint effect tracks; the bump
  // is what store-load.ts issues after every page mutation.
  bumpMessages(chatID);
}

function card(turnID: string): HTMLElement {
  // The card walk roots at the ACTIVE transcript view: the multiplexer holds
  // one view per resident chat, and this suite mints a fresh chat per case.
  const root = activeTranscriptView() ?? messagesEl;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

function hasBody(turnID: string): boolean {
  return card(turnID).querySelector(":scope > .turn-body") !== null;
}

function isFolded(turnID: string): boolean {
  return card(turnID).hasAttribute("data-folded");
}

/** The entry ordinals this turn's body holds, ascending. A prose RUN is stamped for
 *  its first member alone, which is why the heavy fixture is `thinking` entries. */
function mountedSeqs(turnID: string): number[] {
  return [...card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")]
    .map((e) => Number(e.dataset["entrySeq"]))
    .sort((a, b) => a - b);
}

function ordinalsIn(turnID: string): number {
  return mountedSeqs(turnID).length;
}

/** Press the turn footer's "Copy as text", the reader's own gesture. */
function copyAsText(turnID: string): void {
  const btn = card(turnID).querySelector<HTMLButtonElement>(
    '.turn-action-btn[aria-label="Copy as text"]',
  );
  if (btn === null) {
    throw new Error(`no copy button on turn ${turnID}`);
  }
  btn.click();
}

/** The spacer sides this card carries, in document order. The head is the body's first
 *  child; the tail is the body's next SIBLING, since the renderer seats a streamed entry
 *  with `appendChild`. */
function spacersIn(turnID: string): string[] {
  return [
    ...card(turnID).querySelectorAll<HTMLElement>(
      ":scope > .turn-body > .turn-space, :scope > .turn-space",
    ),
  ].map((e) => e.dataset["space"] ?? "");
}

function spacerPx(turnID: string, side: "head" | "tail"): number {
  const host = side === "head" ? ":scope > .turn-body" : ":scope";
  const space = card(turnID).querySelector<HTMLElement>(
    `${host} > .turn-space[data-space="${side}"]`,
  );
  return space === null ? 0 : Number.parseFloat(space.style.blockSize);
}

/** Every turn holding a SPACER and no mounted ordinal: pure reserved height, which
 *  is what the reader sees as a blank card. Named so a failure lists them. */
function rowlessBodies(): string[] {
  const root = activeTranscriptView() ?? messagesEl;
  const out: string[] = [];
  for (const child of root.children) {
    const turnID = child.getAttribute(KEY_ATTR);
    if (turnID === null || !child.classList.contains("turn")) {
      continue;
    }
    if (spacersIn(turnID).length > 0 && ordinalsIn(turnID) === 0) {
      out.push(turnID);
    }
  }
  return out;
}

let seq = 0;
/** A fresh chat id per case, so fold overrides and search reveals cannot bleed. */
function chatID(): string {
  seq++;
  return `c-res-${String(seq)}`;
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  resetFoldState();
  runStatus.clear();
  resetTurnRail();
  // Tear the previous case's transcript down through the renderer's own door
  // (an empty active id runs teardownAll), so per-turn state cannot leak
  // between cases.
  setSessions([] as never);
  setActive("");
});

// --- What residency is measured in --------------------------------------------

describe("the mounted derivation", () => {
  it("mounts every turn of a cheap chat, however many there are", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    // 8 prose turns cost 8 entries against a 320-entry budget. Distance is not a cost.
    for (let n = 1; n <= 8; n++) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} body`).toBe(true);
    }
  });

  it("stubs a FOLDED turn the window is not grown over, however cheap it is", () => {
    const id = chatID();
    activate(id, toolTurns(8));
    // A folded body holds no height, so the window is not grown over it; only the open newest turn gets
    // a body.
    for (let n = 1; n <= 7; n++) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} folded`).toBe(true);
    }
    expect(hasBody("u8")).toBe(true);
  });

  it("windows a SINGLE over-budget turn instead of stubbing it", async () => {
    const id = chatID();
    const span = RESIDENT_ENTRIES + 64;
    activate(id, [heavyTurn("big", span)]);
    // The newest turn is what the reader is looking at, so it keeps a body — and
    // the body holds the window rather than the turn, with a head spacer standing
    // in for the ordinals above it.
    expect(hasBody("big")).toBe(true);
    expect(isFolded("big")).toBe(false);
    // The paint takes ONE slice and owes the rest, so assert the settled shape: the head spacer alone,
    // tail reached.
    await vi.waitFor(() => {
      expect(spacersIn("big")).toEqual(["head"]);
    });
    expect(ordinalsIn("big")).toBe(RESIDENT_ENTRIES);
    expect(ordinalsIn("big")).toBeLessThan(span);
    // That spacer prices the ordinals it stands in for, floored at 1px so no
    // ordinal is ever worth nothing at all.
    expect(spacerPx("big", "head")).toBeGreaterThan(0);
  });

  it("stubs the older turns behind an over-budget newest one, contiguously", () => {
    const id = chatID();
    // REVEALED, all three: a folded turn is outside the window whatever the budget, so only revealed
    // turns let the spent budget be the cause.
    for (const n of [1, 2, 3]) {
      setTurnOpen(id, `u${String(n)}`, true);
    }
    activate(id, [...toolTurns(3), heavyTurn("big", RESIDENT_ENTRIES + 64, 4)]);
    expect(hasBody("big")).toBe(true);
    for (const n of [1, 2, 3]) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} born folded`).toBe(true);
    }
  });

  it("STUBS a turn the fold policy wants open where the window cannot reach it", () => {
    const id = chatID();
    // An open card whose only child is a whole-turn spacer is a blank box (23,988px on a 400-entry
    // turn), so a stub folds to its face and offers the toggle instead.
    setTurnOpen(id, "u1", true);
    activate(id, [toolTurn("u1"), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u1")).toBe(false);
    expect(isFolded("u1")).toBe(true);
    expect(card("u1").hasAttribute("data-no-fold")).toBe(false);
  });

  it("bodies a turn holding NO ordinal even behind the budget, because .is-bodyless needs a body", async () => {
    const id = chatID();
    // A prompt-only turn costs nothing to mount — its body holds no row — and the
    // bodyless marker is stamped on that body, so stubbing it buys no budget and
    // loses the mark. messages-paint-causes.test.ts owns what the mark then does.
    activate(id, [
      [turnOpen("empty"), turnClose("empty", 1)],
      heavyTurn("big", RESIDENT_ENTRIES + 64, 2),
    ]);
    await vi.waitFor(() => {
      expect(spacersIn("big"), "the fixture spends the budget").toEqual(["head"]);
    });
    expect(hasBody("empty")).toBe(true);
    expect(ordinalsIn("empty")).toBe(0);
  });

  it("cuts the window at the TOOL budget, which the entry budget would not reach", async () => {
    const id = chatID();
    // Its ordinals are one per tool card, well inside the entry budget; the cards
    // are what a paint pays for, so the window stops short of the turn's head.
    expect(RESIDENT_TOOL_CALLS + 1).toBeLessThan(RESIDENT_ENTRIES);
    activate(id, [toolHeavyTurn("tools", RESIDENT_TOOL_CALLS + 1)]);
    expect(hasBody("tools")).toBe(true);
    await vi.waitFor(() => {
      expect(spacersIn("tools")).toEqual(["head"]);
    });
  });

  it("keeps the RUNNING turn resident whatever it costs", () => {
    const id = chatID();
    // A live turn's body arrives one entry at a time, so it never costs a cold
    // build — and the reader is watching it.
    activate(id, [heavyTurn("live", RESIDENT_ENTRIES * 2)], true);
    expect(hasBody("live")).toBe(true);
    expect(isFolded("live")).toBe(false);
  });

  it("keeps a hand-opened turn resident whatever it costs, across a repaint", () => {
    const id = chatID();
    setTurnOpen(id, "big", true);
    activate(id, [heavyTurn("big", RESIDENT_ENTRIES + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(true);
    expect(isFolded("big")).toBe(false);
    bumpMessages(id, "shape");
    expect(hasBody("big")).toBe(true);
    expect(isFolded("big")).toBe(false);
  });

  it("keeps a search-opened turn resident whatever it costs", () => {
    const id = chatID();
    openForSearch(id, "big");
    activate(id, [heavyTurn("big", RESIDENT_ENTRIES + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(true);
  });

  it("does not pin a turn the reader FOLDED — a recorded fold is not a request for its body", () => {
    const id = chatID();
    setTurnOpen(id, "big", false);
    activate(id, [heavyTurn("big", RESIDENT_ENTRIES + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(false);
  });

  it("a stub renders header and footer only, with no mounted ordinal and no body element", () => {
    const id = chatID();
    activate(id, [heavyTurn("big", RESIDENT_ENTRIES + 64), ...toolTurns(1)]);
    const stub = card("big");
    expect(stub.querySelector(":scope > .turn-header")).not.toBeNull();
    expect(stub.querySelector(":scope > .turn-body")).toBeNull();
    expect(stub.querySelector("[data-entry-seq]")).toBeNull();
  });
});

// --- The invariant the two shapes above share ----------------------------------
//
// A spacer prices the ordinals it stands in for, so an OPEN card holding a spacer and no row is pure
// reserved height and must be unrepresentable.

describe("the open-turn invariant", () => {
  it("never renders a turn body as a spacer with no ordinal beside it", async () => {
    const id = chatID();
    // Three revealed turns against one over-budget newest one: the window is
    // contiguous and seeded at the live edge, so it reaches none of them, and the
    // single demand pin can speak for at most one.
    for (const turnID of ["u1", "old", "mid"]) {
      setTurnOpen(id, turnID, true);
    }
    activate(id, [
      toolTurn("u1"),
      heavyTurn("old", 400, 2),
      heavyTurn("mid", 120, 3),
      heavyTurn("big", RESIDENT_ENTRIES + 64, 4),
    ]);
    expect(rowlessBodies()).toEqual([]);
    // The fixture reaches the state it is guarding: those three turns really are
    // outside the window, and the newest one really is windowed.
    for (const turnID of ["u1", "old", "mid"]) {
      expect(hasBody(turnID), `${turnID} stub`).toBe(false);
    }
    await vi.waitFor(() => {
      expect(spacersIn("big")).toEqual(["head"]);
    });
  });
});

// --- Disclosure, which residency does not decide -------------------------------

describe("the fold policy over residency", () => {
  it("folds every resident turn but the newest when its fold hides something", () => {
    const id = chatID();
    activate(id, toolTurns(8));
    for (let n = 1; n <= 7; n++) {
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} folded`).toBe(true);
    }
    expect(isFolded("u8")).toBe(false);
  });

  it("leaves a prose-only turn OPEN while resident — its fold would hide nothing", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    // One prose answer per turn, so a face would equal its body: an auto-fold
    // there animates and changes nothing.
    for (let n = 1; n <= 8; n++) {
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} open`).toBe(false);
      expect(card(`u${String(n)}`).hasAttribute("data-no-fold")).toBe(true);
    }
  });

  it("stamps data-no-fold on the newest turn and on hides-nothing turns only", () => {
    const id = chatID();
    activate(id, toolTurns(2));
    // Both turns ran tools. u2 is newest, so it offers no fold; u1 does.
    expect(card("u1").hasAttribute("data-no-fold")).toBe(false);
    expect(card("u2").hasAttribute("data-no-fold")).toBe(true);
    // A third (prose-only) turn arrives: u2 gains its toggle, u3 offers none —
    // newest AND nothing to hide.
    activate(id, [...toolTurns(2), proseTurn("u3", 3)]);
    expect(card("u2").hasAttribute("data-no-fold")).toBe(false);
    expect(card("u3").hasAttribute("data-no-fold")).toBe(true);
  });

  it("offers the toggle on every stub, whatever the fold rules say", () => {
    const id = chatID();
    // A PROSE-ONLY turn's fold hides nothing (no toggle), but on a stub the toggle is the only route to
    // its body.
    activate(id, [...plainTurns(2), heavyTurn("big", RESIDENT_ENTRIES + 64, 3)]);
    for (const n of [1, 2]) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} stub`).toBe(false);
      expect(card(`u${String(n)}`).hasAttribute("data-no-fold")).toBe(false);
    }
  });

  it("never stubs the NEWEST turn, so its missing toggle strands nobody", () => {
    const id = chatID();
    // Presence follows the WINDOW, not the fold policy; the newest turn is never a stub at the live
    // edge. `canFold` reads true for every stub.
    activate(id, [heavyTurn("big", RESIDENT_ENTRIES + 64)]);
    expect(hasBody("big")).toBe(true);
    expect(card("big").hasAttribute("data-no-fold")).toBe(true);
  });

  it("keeps a failed turn OPEN and mounted at any distance", () => {
    // A broken turn does not auto-fold, and residency follows openness: a turn failed on its wire
    // turn_close has no event row, so its face would be empty. It ran a TOOL, so the exemption alone
    // holds it open (see `outcomeTurn`).
    const id = chatID();
    activate(id, [
      outcomeTurn("refused", { refusal: { category: "safety" } }),
      ...plainTurns(10).slice(1),
    ]);
    expect(isFolded("u1")).toBe(false);
    expect(hasBody("u1")).toBe(true);
  });

  it("still folds and unmounts a CANCELLED turn, which is not a failure", () => {
    // The control: `cancelled` is `stopped`, not `broken`, so its fold IS the unmount; a widened
    // exemption fails here.
    const id = chatID();
    activate(id, [outcomeTurn("cancelled"), ...plainTurns(10).slice(1)]);
    expect(isFolded("u1")).toBe(true);
    expect(hasBody("u1")).toBe(false);
  });

  it("folds a turn holding a live workflow run, and mounts no duplicate card", async () => {
    const id = chatID();
    runStatus.set("wf-live", "running");
    invalidateRun("wf-live");
    // One macrotask for the mocked fetch to land in the run cell.
    await new Promise((r) => setTimeout(r, 0));
    const launcher: Entry[] = [
      turnOpen("u1"),
      sealed("u1", 1, "tool_call", {
        id: "tc-run",
        title: "Run Workflow",
        kind: "other",
        status: "completed",
        ts: 1,
        workflow_id: "wf-live",
      }),
      turnClose("u1", 2),
    ];
    activate(id, [launcher, ...plainTurns(4).slice(1)]);
    // A live run does not hold a turn open or earn a second rendering: the composer
    // band's run bar is the persistent surface for one, so
    // the fold takes away nothing a reader is watching.
    expect(isFolded("u1")).toBe(true);
    // The launcher turn carries no top-level prose, so `turnFaceProse` is "" and
    // `syncTurnFace` appends no face at all — stated explicitly, because the run
    // card was the only other thing that could have put one here.
    expect(card("u1").querySelector(":scope > .turn-face")).toBeNull();
    // And nowhere else on the card either: the in-body card lives in `.turn-body`,
    // which a turn this far back does not have.
    expect(card("u1").querySelector(".run-card")).toBeNull();
  });

  it("the NEWEST turn ignores a recorded collapse — it cannot be folded", () => {
    // Its toggle is hidden (data-no-fold), so a recorded fold is a leftover from an older build or a
    // rewind; honouring it would strand the tail closed with no way to reopen it.
    const id = chatID();
    setTurnOpen(id, "u8", false);
    activate(id, toolTurns(8));
    expect(isFolded("u8")).toBe(false);
    expect(hasBody("u8")).toBe(true);
    expect(card("u8").hasAttribute("data-no-fold")).toBe(true);
    // The same override applies once the turn stops being newest — and a turn the
    // reader folded is a stub, because the window is not grown over folded turns.
    activate(id, [...toolTurns(8), proseTurn("u9", 9)]);
    expect(isFolded("u8")).toBe(true);
    expect(hasBody("u8")).toBe(false);
  });
});

// --- Lifecycle: resident → stub -------------------------------------------------

describe("the residency lifecycle", () => {
  it("keeps an entry's signals while resident, and drops them at the unmount", async () => {
    const id = chatID();
    // The target turn carries a delegate's entries, so the lane's own state has
    // something to lose, and a minted entry signal so the signal map does too.
    const target: Entry[] = [
      turnOpen("u1"),
      sealed("u1", 1, "text", { text: "parent prose" }),
      sealed("u1", 2, "tool_call", {
        id: "tc-inv",
        title: "Sub-agent: helper",
        kind: "other",
        status: "completed",
        ts: 2,
        agent_subtask_id: "sub-leak",
      }),
      sealed("u1", 3, "text", { text: "delegate prose" }, "sub-leak"),
      turnClose("u1", 4),
    ];
    activate(id, [target]);
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false); // the open tail

    // The signal a streamed entry would have minted, registered per turn so
    // the unmount's `clearTurnSigs` can find it.
    ensureEntryTextSig("u1", "u1-e1", "parent prose");
    expect(entryTextSigs.get(entryKey("u1", "u1-e1"))).toBeDefined();

    // Resident → stub: three more turns fold the target, and the window is not
    // grown over a folded turn, so the fold IS the unmount.
    activate(id, [target, ...plainTurns(4).slice(1)]);
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(false);
    });
    expect(isFolded("u1")).toBe(true);
    expect(card("u1").querySelector("[data-entry-seq]")).toBeNull();
    expect(entryTextSigs.get(entryKey("u1", "u1-e1"))).toBeUndefined();
  });

  it("defers the unmount while reading and applies it when the reader returns", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    openForSearch(id, "u1");
    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false);

    // The reader is mid-transcript: the fold pass must queue, not apply.
    const queue: (() => void)[] = [];
    scrollMock.deferWhileReading.mockImplementation((mutate: () => void) => {
      queue.push(mutate);
    });

    // The reveal goes away AND the budget is spent, so u1 loses both its pin and
    // its place: a search close alone leaves a cheap turn resident now.
    clearSearchOpened(id);
    setSessions([] as never);
    activate(id, [...plainTurns(8), heavyTurn("big", RESIDENT_ENTRIES + 64, 9)]);

    // Deferred: the body and its open state are both still there.
    expect(hasBody("u1")).toBe(true);
    expect(queue.length).toBeGreaterThan(0);

    // The reader returns: the queued flip runs.
    for (const fn of queue) {
      fn();
    }
    expect(hasBody("u1")).toBe(false);
    expect(isFolded("u1")).toBe(true);
  });
});

// --- The on-demand build -------------------------------------------------------

describe("expanding a stub", () => {
  it("a fold-toggle click mounts the body and opens the turn in the same interaction", async () => {
    const id = chatID();
    activate(id, [toolTurn("u1"), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u1")).toBe(false);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
      expect(isFolded("u1")).toBe(false);
    });
    expect(ordinalsIn("u1")).toBeGreaterThan(0);
    // The click recorded the reader's own choice, which also PINS the turn
    // resident: it survives a repaint open, with the budget still spent.
    bumpMessages(id);
    expect(isFolded("u1")).toBe(false);
    expect(hasBody("u1")).toBe(true);
  });

  it("keeps the ordinals a reveal built after the pin's clock runs out", async () => {
    const id = chatID();
    activate(id, [heavyTurn("old", 400), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("old")).toBe(false);

    (card("old").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(ordinalsIn("old")).toBeGreaterThan(0);
    });

    // Past PIN_GOAL_MS with the reader unmoved: the reveal is a standing request, so the grant outlives
    // its clock instead of blanking the reader's open card.
    vi.spyOn(Date, "now").mockReturnValue(Date.now() + 10_000);
    bumpMessages(id, "shape");

    expect(ordinalsIn("old")).toBeGreaterThan(0);
    expect(isFolded("old")).toBe(false);
    expect(rowlessBodies()).toEqual([]);
  });

  it("keyboard activation on the stub header's toggle mounts it too", async () => {
    const id = chatID();
    activate(id, [toolTurn("u1"), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u1")).toBe(false);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
      expect(isFolded("u1")).toBe(false);
    });
  });

  it("a stub header keeps the disclosure contract while closed", () => {
    const id = chatID();
    activate(id, [toolTurn("u1"), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    const header = card("u1").querySelector<HTMLElement>(":scope > .turn-header");
    const toggle = header?.querySelector(":scope > .turn-head-row > .turn-fold-toggle");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    // The state lives on the BUTTON. `.turn-header` is a plain div, and
    // `aria-expanded` on one is an ARIA violation rather than a redundancy.
    expect(header?.hasAttribute("aria-expanded")).toBe(false);
  });

  it("expanding a stub renders exactly what a resident turn renders, one copy of every entry kind", async () => {
    // One turn carrying every body surface: reasoning trace, text bubble, plain
    // tool card, delegate box, run card.
    const everyKind = (turnID: string): Entry[] => [
      turnOpen(turnID, 1, "the prompt"),
      sealed(turnID, 1, "thinking", { text: "considering" }),
      sealed(turnID, 2, "text", { text: "the reply" }),
      sealed(turnID, 3, "tool_call", {
        id: "tc-read",
        title: "Read File",
        kind: "read",
        status: "completed",
        ts: 3,
        output: "ok",
      }),
      sealed(turnID, 4, "tool_call", {
        id: "tc-inv",
        title: "Sub-agent: helper",
        kind: "other",
        status: "completed",
        ts: 4,
        agent_subtask_id: "sub-par",
      }),
      sealed(turnID, 5, "text", { text: "delegate words" }, "sub-par"),
      sealed(turnID, 6, "tool_call", {
        id: "tc-run",
        title: "Run Workflow",
        kind: "other",
        status: "completed",
        ts: 6,
        workflow_id: "wf-par",
      }),
      turnClose(turnID, 7),
    ];

    // Reference: the same turn mounted RESIDENT and open by the ordinary paint. Disclosure ids (a
    // page-global counter) and the delegate link's chat id are normalized on both sides.
    const normalize = (html: string, chat: string): string =>
      html.replaceAll(/uip-disclosure-\d+/g, "uip-disclosure-N").replaceAll(chat, "CHAT");
    const warmChat = chatID();
    // Opened by the reader, because the window is only grown over turns that
    // render open: a folded turn behind the newest one is a stub, so "resident and
    // folded" is not a state to compare against any more.
    setTurnOpen(warmChat, "uv", true);
    activate(warmChat, [everyKind("uv"), ...plainTurns(4)]);
    expect(isFolded("uv")).toBe(false);
    const warmBody = card("uv").querySelector(":scope > .turn-body") as HTMLElement;
    for (const sel of [
      ".reasoning-block",
      ".message.assistant",
      ".tool-call",
      ".subagent-block",
      ".run-card",
    ]) {
      expect(warmBody.querySelector(sel), `resident ${sel}`).not.toBeNull();
    }
    const wantHTML = normalize(warmBody.innerHTML, warmChat);

    // Subject: the same content as a FOLD stub, expanded by the reader's gesture. Not a spent budget or
    // a bare `mountTurnBody`: a demand grant is clamped by an entry COUNT in SEQ space and stops one
    // ordinal short of the tail (a known gap).
    const stubChat = chatID();
    activate(stubChat, [everyKind("uv"), ...toolTurns(4)]);
    expect(hasBody("uv")).toBe(false);
    (card("uv").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(card("uv").querySelector(":scope > .turn-body > .run-card")).not.toBeNull();
    });
    const stubBody = card("uv").querySelector(":scope > .turn-body") as HTMLElement;
    expect(normalize(stubBody.innerHTML, stubChat)).toBe(wantHTML);
  });

  it("yields between entry batches on a heavy reveal and lands the GRANT complete", async () => {
    const id = chatID();
    // 96 ordinals: several BUILD_BATCH_ENTRIES slices, and more than the grant.
    // "Complete" is the range somebody asked for, not the turn: a reveal that
    // mounted a 700-entry turn whole is what this feature refuses.
    activate(id, [heavyTurn("u-heavy", 96), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u-heavy")).toBe(false);

    // An INTERIOR ordinal, which is what a search hit forwards: the grant is one
    // overscan each side of it, so 48 of the turn's 96 ordinals and a spacer at
    // both ends.
    const build = mountTurnBody(id, "u-heavy", GRANT);
    // The first batch lands synchronously in the interaction...
    const partial = ordinalsIn("u-heavy");
    expect(partial).toBeGreaterThan(0);
    expect(partial).toBeLessThan(GRANT);
    // ...and the rest follow across yields, up to the grant and no further.
    await build;
    expect(ordinalsIn("u-heavy")).toBe(GRANT);
    expect(spacersIn("u-heavy")).toEqual(["head", "tail"]);
  });
});

// --- One builder for the cold paint too ----------------------------------------

describe("the cold build", () => {
  it("takes one slice inside the paint and finishes the rest off the frame", async () => {
    const id = chatID();
    // Resident because the reader opened it, and far past one slice: the paint
    // that creates the card may not also build 320 ordinals into it.
    setTurnOpen(id, "u-heavy", true);
    activate(id, [heavyTurn("u-heavy", RESIDENT_ENTRIES)]);
    expect(hasBody("u-heavy")).toBe(true);
    const afterPaint = ordinalsIn("u-heavy");
    expect(afterPaint).toBeGreaterThan(0);
    expect(afterPaint).toBeLessThan(RESIDENT_ENTRIES);
    await vi.waitFor(() => {
      expect(ordinalsIn("u-heavy")).toBe(RESIDENT_ENTRIES);
    });
  });

  it("copies the whole turn while that body is still filling in", async () => {
    const id = chatID();
    setTurnOpen(id, "u-heavy", true);
    copied.length = 0;
    activate(id, [markedTail("u-heavy", RESIDENT_ENTRIES)]);
    // The state the case above measures: the plan WANTS all 320 ordinals from this
    // paint, and one batch of them is mounted. The plan alone reads "whole body" here,
    // so a copy that trusted it would hand over the ordinals the batch happened to reach.
    expect(ordinalsIn("u-heavy")).toBeLessThan(RESIDENT_ENTRIES);

    copyAsText("u-heavy");

    expect(copied).toHaveLength(1);
    // The emphasis markers are the STORE's own text, and no rendered bubble carries
    // them — so this names which side answered, not merely that the tail is present.
    expect(copied[0]).toContain(`**chunk ${String(RESIDENT_ENTRIES)}**`);

    await vi.waitFor(() => {
      expect(ordinalsIn("u-heavy")).toBe(RESIDENT_ENTRIES);
    });
    copied.length = 0;
    copyAsText("u-heavy");
    // And once the build lands the MOUNTED body answers: the same tail entry, rendered.
    expect(copied[0]).toContain(`chunk ${String(RESIDENT_ENTRIES)}`);
    expect(copied[0]).not.toContain("**");
  });

  it("copies the whole turn while a head-ward window move waits for the reader", async () => {
    const id = chatID();
    // A grant mounts a NARROW window in the middle of the turn, and the newest turn
    // holds the budget so nothing widens it. The turn is OPEN, so its body is the only
    // answer the DOM has: a folded one would offer the face and prove nothing.
    setTurnOpen(id, "u1", true);
    activate(id, [
      markedTail("u1", RESIDENT_ENTRIES + 20),
      heavyTurn("big", RESIDENT_ENTRIES + 64, 2),
    ]);
    await mountTurnBody(id, "u1", 100);
    bumpMessages(id, "shape");
    expect(spacersIn("u1")).toEqual(["head", "tail"]);

    // The reader parks, and the newest turn goes away: the freed budget grows u1's
    // range over its whole body, `updateTurn` refuses a head-ward growth, and the
    // reconcile that would mount it queues until the reader returns to Following.
    scrollMock.readingState.mockReturnValue("reading");
    const queue: (() => void)[] = [];
    scrollMock.deferWhileReading.mockImplementation((mutate: () => void) => {
      queue.push(mutate);
    });
    activate(id, [markedTail("u1", RESIDENT_ENTRIES + 20)]);
    expect(queue.length).toBeGreaterThan(0);
    // Held: the plan wants the whole body and the DOM still holds a hole at the HEAD, the edge
    // `updateTurn` refuses; no build is owed. Read off the MOUNTED ordinals, not the re-priced spacers.
    expect(mountedWindow("u1")?.from).toBe(76);
    expect(mountedSeqs("u1")[0]).toBe(76);

    copied.length = 0;
    copyAsText("u1");

    // The tail of the turn, which is not mounted: the store answered.
    expect(copied[0]).toContain(`**chunk ${String(RESIDENT_ENTRIES + 20)}**`);
  });

  it("copies the RENDERED text of a complete body whose rows are not all prose", async () => {
    const id = chatID();
    copied.length = 0;
    activate(id, [eventTurn("u-evt")]);

    // Premises: the body holds every ordinal and no spacer, one ordinal is a messages-events.ts EVENT
    // row, and the turn is open (no face bubble).
    expect(mountedWindow("u-evt")).toEqual({ from: 0, to: 4 });
    expect(spacersIn("u-evt")).toEqual([]);
    expect(card("u-evt").querySelector(":scope > .turn-body > .boundary")).not.toBeNull();
    expect(isFolded("u-evt")).toBe(false);
    expect(card("u-evt").querySelector(":scope > .turn-face > .message.assistant")).toBeNull();

    copyAsText("u-evt");

    expect(copied).toHaveLength(1);
    expect(copied[0]).toContain("reply u-evt");
    // The emphasis markers are the STORE's own text and no rendered bubble carries
    // them, so this names which side answered rather than merely that the prose is
    // there. The store's answer is complete too — it is markdown SOURCE.
    expect(copied[0]).not.toContain("**");
  });

  it("builds a within-budget turn whole in the paint — one slice covers it", () => {
    const id = chatID();
    activate(id, plainTurns(3));
    // Nothing to yield for: three prose turns are one ordinal each.
    expect(ordinalsIn("u1")).toBe(1);
    expect(ordinalsIn("u3")).toBe(1);
  });

  it("stops taking slices on the frame once the pass has spent its entry allowance", async () => {
    const id = chatID();
    // Twelve WALK-granted turns: grants sit outside the budget, so only they can sum past one window.
    // Granted before activation, so the creating paint bodies them.
    const turnEntries: Entry[][] = [];
    const grants: Promise<void>[] = [];
    for (let n = 1; n <= 12; n++) {
      const turnID = `h${String(n)}`;
      grants.push(mountTurnBodyForWalk(id, turnID));
      turnEntries.push(heavyTurn(turnID, 40, n));
    }
    await Promise.all(grants);
    activate(id, turnEntries);

    // A slice is 32 SEQ wide and the pass may mount 320, so exactly ten bodies get
    // one; the last two are born empty. 31 rather than 32 ordinals, because seq 0 is
    // the `turn_open` the HEADER draws rather than a body row.
    const started = [];
    for (let n = 1; n <= 12; n++) {
      const ordinals = ordinalsIn(`h${String(n)}`);
      expect(hasBody(`h${String(n)}`), `h${String(n)} resident`).toBe(true);
      expect([0, 31]).toContain(ordinals);
      if (ordinals > 0) {
        started.push(n);
      }
    }
    expect(started).toHaveLength(10);

    // Nothing is lost: the drain finishes all twelve off the frame.
    await vi.waitFor(() => {
      for (let n = 1; n <= 12; n++) {
        // 39 of 40 ordinals: the walk's grant is `2 * OVERSCAN_ENTRIES`, count-capped, in SEQ space (seq 0
        // is the header). The point is every body reaching its whole grant.
        expect(ordinalsIn(`h${String(n)}`), `h${String(n)} complete`).toBeGreaterThan(38);
      }
    });
    // h12 is the NEWEST turn, so the plan's own window covers it whole and outranks
    // the walk's narrower grant: 40 against the 39 every earlier turn lands.
    expect(ordinalsIn("h12")).toBe(40);
    endWalkReveal(id);
  });

  it("holds a walk-revealed turn across the repaint the reveal itself asks for", async () => {
    const id = chatID();
    activate(id, [toolTurn("u1"), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u1")).toBe(false);

    // The search-wide loop builds every hit turn and THEN repaints, so grants must survive that pass;
    // one pin slot cannot serve the loop, hence the walk's own scope.
    await mountTurnBodyForWalk(id, "u1");
    expect(hasBody("u1")).toBe(true);
    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(true);

    // And it ends with the reveal rather than with a clock.
    endWalkReveal(id);
    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(false);
  });

  it("clamps an ordinal past the turn's span into it, rather than granting nothing", async () => {
    const id = chatID();
    activate(id, [heavyTurn("u-heavy", 96), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u-heavy")).toBe(false);
    // A forwarded ordinal outlives the entry it named — a hit index after a rewind
    // trims the turn — so an `at` past the span is the caller's, not a defect here.
    await mountTurnBody(id, "u-heavy", 10_000);
    // The grant ends one overscan HEAD-ward of the clamped ordinal and one short of the tail, so a tail
    // spacer survives (the known gap above): CONTAINS, so a fix there must not redden this.
    expect(ordinalsIn("u-heavy")).toBeGreaterThan(0);
    expect(mountedSeqs("u-heavy")[0]).toBeGreaterThan(96 - GRANT);
    expect(mountedSeqs("u-heavy").at(-1)).toBeGreaterThanOrEqual(96 - OVERSCAN_ENTRIES);
    expect(spacersIn("u-heavy")).toContain("head");
  });

  it("gives the PIN's range to a turn the walk also named", async () => {
    const id = chatID();
    activate(id, [heavyTurn("u-heavy", 96), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    // Every hit turn is in the walk's set, so navigating onto one is the common path; the pin names the
    // ordinal the walk's head range would leave unmounted.
    await mountTurnBodyForWalk(id, "u-heavy");
    expect(spacersIn("u-heavy")).toEqual(["tail"]);

    await mountTurnBody(id, "u-heavy", GRANT);
    bumpMessages(id, "shape");
    expect(spacersIn("u-heavy")).toContain("head");
    endWalkReveal(id);
  });

  it("takes an evicted turn's owed slices with it instead of rebuilding the body", async () => {
    const id = chatID();
    // The newest turn, over budget, so the window covers its tail and the paint
    // owes the rest of that window as a cold build.
    activate(id, [heavyTurn("u-heavy", RESIDENT_ENTRIES + 64)]);
    const started = ordinalsIn("u-heavy");
    expect(started).toBeGreaterThan(0);
    expect(started).toBeLessThan(RESIDENT_ENTRIES);

    // A newer turn folds it in the task the drain is queued behind; the owed build must not rebuild a
    // body under a folded card.
    activate(id, [heavyTurn("u-heavy", RESIDENT_ENTRIES + 64), toolTurn("u1", 2)]);
    expect(hasBody("u-heavy")).toBe(false);

    await new Promise((r) => setTimeout(r, 60));
    expect(hasBody("u-heavy")).toBe(false);
  });
});

// --- Navigation surfaces --------------------------------------------------------

describe("navigation onto a stub", () => {
  it("a rail jump mounts the stub turn it lands on", async () => {
    const id = chatID();
    activate(id, [...plainTurns(2), heavyTurn("big", RESIDENT_ENTRIES + 64, 3)]);
    expect(hasBody("u1")).toBe(false);
    railTurns.turns = Array.from({ length: 2 }, (_, i) => ({
      id: `u${String(i + 1)}`,
      n: i + 1,
      outcome: "completed",
      ts: (i + 1) * 60_000,
    }));
    await loadTurnRail(id);
    const marker = document.querySelector<HTMLButtonElement>(".turn-rail .rail-marker");
    expect(marker?.textContent).toBe("1");
    marker?.click();
    // BOTH ends of the jump: the body is built BEFORE anything scrolls, so waiting
    // on the body alone would assert the scroll a frame before it happens.
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
    });
    // A jump navigates; it does not open. The build runs while the card is FOLDED,
    // so nothing visible moves and the turn lands exactly as a resident folded turn
    // does.
    expect(isFolded("u1")).toBe(true);
    // The scroll needs its own wait, because the body is built BEFORE anything
    // scrolls: that build's completion applies a scroller write, and taking it
    // mid-animation is what the ordering exists to prevent.
    await vi.waitFor(() => {
      expect(scrollMock.scrollToOffset).toHaveBeenCalled();
    });
  });

  it("keeps the jumped-to turn resident across the next paint", async () => {
    // A jump records no fold and no reveal, and the budget is spent, so without a pin the next paint
    // evicts the body the jump just built.
    const id = chatID();
    // Tool-bearing turns, so a fold HIDES something: this case reads the fold
    // state after a repaint, and a prose-only turn legitimately re-opens there.
    activate(id, [...toolTurns(2), heavyTurn("big", RESIDENT_ENTRIES + 64, 3)]);
    railTurns.turns = Array.from({ length: 2 }, (_, i) => ({
      id: `u${String(i + 1)}`,
      n: i + 1,
      outcome: "completed",
      ts: (i + 1) * 60_000,
    }));
    await loadTurnRail(id);
    document.querySelector<HTMLButtonElement>(".turn-rail .rail-marker")?.click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
    });

    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(true);
    // The pin is residency only: the jump still leaves the turn folded.
    expect(isFolded("u1")).toBe(true);
  });

  it("bounds the jumped-to body to the grant, never the whole turn", async () => {
    // The newest turn has already spent the budget, so the grant is the only thing
    // holding this one — and what it holds is one overscan each side of the ordinal
    // asked for, not the 200 ordinals a whole-turn exemption would have mounted.
    const id = chatID();
    activate(id, [heavyTurn("u1", 200), heavyTurn("big", RESIDENT_ENTRIES + 64, 2)]);
    expect(hasBody("u1")).toBe(false);

    await mountTurnBody(id, "u1", 100);
    bumpMessages(id, "shape");

    expect(hasBody("u1")).toBe(true);
    expect(ordinalsIn("u1")).toBe(GRANT);
    expect(ordinalsIn("u1")).toBeLessThan(200);
    // And the ordinals on BOTH sides of the grant stand behind their own spacer.
    expect(spacersIn("u1")).toEqual(["head", "tail"]);
  });
});

// --- Pagination -------------------------------------------------------------------

describe("pagination prepends", () => {
  it("land as stubs, folded at birth even while the reader is reading", () => {
    const id = chatID();
    const current = heavyTurn("big", RESIDENT_ENTRIES + 64, 7);
    activate(id, [current]);
    expect(hasBody("big")).toBe(true);

    // Reading: the fold pass would defer any state CHANGE — a born state is
    // not one, so the prepended stubs may not flash expanded first.
    scrollMock.readingState.mockReturnValue("reading");
    scrollMock.deferWhileReading.mockImplementation(() => {
      /* queue forever: nothing born may depend on this flushing */
    });
    activate(id, [...toolTurns(6), current]);

    // The newest turn already spent the budget, so every prepend is a stub.
    for (const n of [1, 2, 3, 4, 5, 6]) {
      expect(hasBody(`u${String(n)}`), `u${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `u${String(n)} born folded`).toBe(true);
    }
  });
});
