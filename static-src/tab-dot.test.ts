//
// The tab activity dot: the state mapping, the accessible name, and the
// reduced-motion degradation.
//
// Ported from @cplieger/web-terminal-ui's `.wt-status-dot`, and the three things
// pinned here are the three that a port gets wrong silently:
//
//  1. THE MAPPING. Six chat states derive from four independent signals
//     (`thinking`, `agent_status`, the failure latch, the dock's queue), and two
//     of them COEXIST — a permission ask arrives mid-turn with `thinking` still
//     true. Precedence is therefore load-bearing rather than cosmetic: with
//     working first, every ask on a background chat is masked by the state that
//     needs nothing from anyone. That masking is a cost the app already pays for
//     elsewhere (the permission push notice cannot be silenced precisely because
//     "a background chat waiting on an approval renders identically to one that
//     is working"), so getting the order wrong would silently keep it.
//
//  2. THE ACCESSIBLE NAME. A 9px disc gives a screen-reader user nothing, and
//     this feature exists FOR tabs nobody is looking at. The announced word also
//     has to follow the tab name rather than precede it, which is a function of
//     where in the row the element sits — invisible to any type check.
//
//  3. REDUCED MOTION. 40-a11y.css zeroes every animation's duration and
//     iteration count globally, which RUNS each animation to completion rather
//     than suppressing it. Neither dot keyframe declares a fill-mode, so a
//     completed `vk-dot-wave` reverts its ::after to that rule's own
//     declarations — `opacity: 1`, no transform — leaving a solid opaque band
//     welded to the disc forever. `content: none` is what prevents that, and
//     nothing about the global rule makes it obvious.
//
// The CSS half asserts SOURCE facts because the test page loads no app
// stylesheet: nothing links `css/MANIFEST`, so `getComputedStyle` has no cascade
// to report on and cannot answer "which rule applies".

import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";
import cssContrastScript from "../scripts/css-contrast.py?raw";
import chatSrc from "./chat.ts?raw";
import { allRules, ruleContaining, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  tabStatusFor,
  setSessions,
  setThinking,
  setAgentStatus,
  openTurn,
  appendEntry,
  upsertHeader,
  outcomeLatch,
  runStatusFor,
  defaultUsage,
  get,
} from "./store.js";
import type {
  ChatHeader,
  PermissionNeededPayload,
  RunInputNeededPayload,
  Session,
  TabKind,
} from "./types.js";
import type { Entry, EntryToolCall, TurnOutcome } from "./wire/types.gen.js";
import type { TabDotStatus } from "./tab-view.js";
import type { ClassifiedRunStatus } from "./run-status.js";

function session(over: Partial<Session> = {}): Session {
  return {
    id: "c1",
    name: "Fix the parser",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...over,
  } as Session;
}

/** A chat whose newest turn FINISHED, which is the state a launching chat comes back to
 *  the moment its run was created: no resident turn without a `turn_close`, and a header
 *  outcome that grades `done`. The verdict has ONE source, so this is spelled as the
 *  header field rather than as a latch a caller sets. */
function settledChat(id: string, outcome: TurnOutcome = "completed"): Session {
  return session({ id, last_turn_outcome: outcome, turn_open: false });
}

/** A server header for `id` carrying `outcome` as the newest FINISHED turn's verdict. The
 *  closer stamps that field in the same header rewrite that appends the `turn_close`, so a
 *  case that closes a turn states both halves. */
function headerWithOutcome(id: string, outcome?: TurnOutcome): ChatHeader {
  return {
    id,
    name: id,
    usage: defaultUsage(),
    turn_count: 1,
    created_at: 0,
    updated_at: 0,
    ...(outcome !== undefined && { last_turn_outcome: outcome }),
  };
}

/** The `turn_open` that opens `turnID` at session-absolute ordinal `n`. `wire_turn_start`
 *  is the agent-initiated source, which is the shape every case here needs: none of them
 *  addresses a prompt by its id. */
function turnOpenEntry(turnID: string, n = 1): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "wire_turn_start", n },
  };
}

/** A sealed entry of any kind at `seq`, in lane `lane` (`""`, the transcript's own, unless
 *  named). */
function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  lane?: string,
): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    ...(lane !== undefined && { lane }),
    payload,
  };
}

/** Open a turn on `chatID` through the real store operation. */
function openTurnIn(chatID: string, turnID: string, n = 1): void {
  openTurn(chatID, turnOpenEntry(turnID, n));
}

/** Close `turnID` with `outcome`, at the seq the caller says the body reached. */
function closeTurnIn(chatID: string, turnID: string, seq: number, outcome: TurnOutcome): void {
  appendEntry(chatID, sealed(turnID, seq, "turn_close", { outcome }));
}

/** A delegate's invocation `tool_call`, which is the one entry a subagent surface reads for
 *  that delegate's state. It sits in the ISSUER's lane (`""`), which is why no lane is
 *  named here. */
function invocationCall(over: Partial<EntryToolCall> = {}): EntryToolCall {
  const base: EntryToolCall = {
    id: "tc1",
    title: "Sub-agent: introspect",
    status: "in_progress",
    kind: "other",
    ts: 1,
    agent_subtask_id: "task-1",
  };
  return Object.assign(base, over);
}

// ---------------------------------------------------------------------------
// 1. Signal -> state.
// ---------------------------------------------------------------------------

// LIVENESS IS THE LOG, which is what this section is for: the four independent
// latches are gone (`thinking`, `turn_done`, `turn_failed`, an `agent_status` of
// `completed`), so `working` is DERIVED from a resident turn carrying no
// `turn_close` and `done` reads the header's own statement about the newest turn
// that finished. `store.test.ts` owns the unit MAPPING of that rule; what only
// this file can drive is the rule against the real store operations, which is
// where the reported defect lived — a delegate spends minutes in tool calls
// emitting no text, so nothing latched, and any completed-shaped signal landing in
// that window painted the tab green while the turn was mid-flight.
describe("the dot derives working from an OPEN TURN rather than from a latch", () => {
  beforeEach(() => {
    setSessions([session({ id: "c1" })]);
  });

  it("returns nothing at all for a chat it has never heard of", () => {
    expect(tabStatusFor(undefined)).toBe("");
  });

  it("reads WORKING for a turn holding only delegate-lane entries", () => {
    // ADDENDUM 6's green bar, first case, and the reported bug. The delegate's
    // entries are entries of the PARENT's own turn in the delegate's lane, so the
    // parent's turn carries no `turn_close` and the dot cannot read anything else
    // for the duration — whatever the delegate is or is not emitting at the top
    // level.
    openTurnIn("c1", "t1");
    appendEntry("c1", sealed("t1", 1, "text", { text: "reading" }, "delegate-uuid"));
    appendEntry("c1", sealed("t1", 2, "tool_call", invocationCall(), "delegate-uuid"));
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  it("keeps WORKING when a waiting_on_user status arrives DURING that turn", () => {
    // Green bar, second case, and the reason `waiting` moved BEHIND `working`: a
    // status declared mid-turn by a sub-execution is the same false verdict in
    // yellow. The status is not discarded — it paints the moment the turn ends.
    openTurnIn("c1", "t1");
    setAgentStatus("c1", "waiting_on_user");
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  it("flips to DONE only on the turn_closed that leaves no open turn", () => {
    // Green bar, third case. The close is what makes the verdict readable, and the
    // verdict's one source is the header — so the close plus the header the closer
    // stamped is the whole of it.
    openTurnIn("c1", "t1");
    expect(tabStatusFor(get("c1"))).toBe("working");
    closeTurnIn("c1", "t1", 1, "completed");
    upsertHeader(headerWithOutcome("c1", "completed"));
    expect(tabStatusFor(get("c1"))).toBe("done");
  });

  it("does NOT flip on a turn_closed for a DIFFERENT turn of the same chat", () => {
    // Green bar, fourth case: the two-open-turns state of design 4.1, a prompt in
    // `pending` beside an agent-initiated turn. Every `turn_closed` settles exactly
    // the turn it names, so the chat stays live while the OTHER turn has no close —
    // and the header's outcome, already written by the first closer, may not be read
    // as this chat's state while that is true.
    openTurnIn("c1", "t1", 1);
    openTurnIn("c1", "t2", 2);
    closeTurnIn("c1", "t1", 1, "completed");
    upsertHeader(headerWithOutcome("c1", "completed"));
    expect(tabStatusFor(get("c1"))).toBe("working");

    closeTurnIn("c1", "t2", 1, "completed");
    expect(tabStatusFor(get("c1"))).toBe("done");
  });

  it("never lets `thinking` outrank the log, in either direction", () => {
    // `thinking` survives only as a render-time convenience and is NOT an input
    // here. Both directions are asserted, because the defect is symmetric: a stale
    // latch must not claim a chat is busy, and a missing one must not claim it is
    // finished.
    openTurnIn("c1", "t1");
    setThinking("c1", false);
    expect(tabStatusFor(get("c1"))).toBe("working");

    setSessions([settledChat("c2")]);
    setThinking("c2", true);
    expect(tabStatusFor(get("c2"))).toBe("done");
  });

  it("puts a pending ask AHEAD of the open turn that raised it", () => {
    // The one state that outranks liveness, and the two genuinely coexist: KAS
    // raises the ask mid-turn, so reporting `working` here would hide every
    // approval the app is blocked on.
    openTurnIn("c1", "t1");
    expect(tabStatusFor(get("c1"), true)).toBe("input");
  });

  it("puts a pending ask ahead of every settled verdict too", () => {
    // An ask outlives the turn that raised it, so a close landing first must not
    // bury it.
    expect(tabStatusFor(settledChat("c1", "failed"), true)).toBe("input");
    expect(tabStatusFor(settledChat("c1", "completed"), true)).toBe("input");
  });

  it("gates FAILED on liveness, so a running turn is never painted red", () => {
    // `failed` and `done` read the SAME field, and it is rewritten only at a
    // `turn_close` — so it still describes the previous turn for the whole of the
    // next one, and an ungated `failed` paints a chat red for the duration of a turn
    // that is running fine.
    upsertHeader(headerWithOutcome("c1", "failed"));
    openTurnIn("c1", "t1");
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  it("keeps a chat that has NOT initiated on the idle floor", () => {
    // The hollow ring means the chat has not initiated, so it is reachable only for
    // a chat with no turn and a record with no outcome. Every other row shows a
    // state.
    expect(tabStatusFor(settledChat("c1", "completed"))).not.toBe("idle");
    expect(tabStatusFor(session({ id: "c9", turn_open: false }))).toBe("idle");
  });

  it("ignores the agent statuses that are not their own dot state", () => {
    // `in_progress` and `idle` arrive on the same channel as `waiting_on_user`. A
    // chat is not made busy by the agent SAYING so while the log reports otherwise,
    // and `completed` is no longer an input at all — the verdict has one source.
    setSessions([session({ id: "c1", turn_open: false })]);
    for (const status of ["in_progress", "idle", "completed"]) {
      setAgentStatus("c1", status);
      expect(tabStatusFor(get("c1")), status).toBe("idle");
    }
  });
});

// ---------------------------------------------------------------------------
// 2. The accessible name.
// ---------------------------------------------------------------------------

vi.mock("./router.js", () => ({
  pushRoute: vi.fn(),
  buildPath: vi.fn(() => "/"),
  parseRoute: vi.fn(),
}));
// Type-only, for the `importOriginal` below.
import type * as TabsDrag from "./tabs-drag.js";
// `exceedsSlop` stays real: `tabs.ts` reads it, so a partial factory would fail
// this whole file at link time.
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// The tab set is SERVER-owned, so a row lands on the strip through a real round
// trip against the fake collection: `send` answers the four tab commands off it
// and emits the frames, `newOpID` mints the correlation id.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./run-store.js", () => ({
  // The tab factory's name read. Inert here; a Browser-Mode mock is linked as
  // real ESM, so a name any module in the graph reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));

// TWO readers of `apiGetTyped` in this graph and they answer different routes:
// store-load's chat read (stubbed at the boundary so the reconcile logic under
// test is the real one) and tabs-sync's `GET /api/tabs`, which the harness answers
// off the fake collection.
const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => {
    const listTabs = m.tabListRead();
    return {
      apiGetTyped: vi.fn((path: string, decode: unknown) =>
        path === "/api/tabs" ? listTabs(path) : mockApiGetTyped(path, decode),
      ),
      // Present-but-inert so real-ESM linking succeeds: store-load's deep-link
      // confirmation reads it, and that module is in this graph. No case here
      // resolves an unknown chat id.
      apiGetTypedOrError: vi.fn(),
      // Reached through tabs.ts -> files-shared.ts; no case here lists a directory.
      apiGet: vi.fn(),
    };
  }),
);
// `./actions/index.js` is deliberately NOT mocked. It is the actions framework's
// re-export, and `actions/tabs.ts` — the four tab mutations the projection
// dispatches — needs its whole surface, so a one-symbol stub no longer links. The
// harness resets the framework instead, which is what the action suites do.

// The dock's two leaves that reach for DOM it does not own, plus the toast,
// mocked exactly as decision-dock.test.ts mocks them.
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: vi.fn(),
}));
vi.mock("./actions/permissions.js", () => ({ editNativeRule: { dispatch: vi.fn() } }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

/** The tab strip's real render target, so renderDOM runs for real. */
vi.mock("./dom.js", () => {
  const cache = new Map<string, HTMLElement>();
  return {
    $: new Proxy(
      {},
      {
        get(_t, prop: string) {
          if (prop === "tabList") {
            let list = cache.get("tabList");
            if (list === undefined || !list.isConnected) {
              list = document.getElementById("tab-list") ?? document.createElement("div");
              cache.set("tabList", list);
            }
            return list;
          }
          return document.createElement("div");
        },
      },
    ),
    byId: vi.fn(() => document.createElement("div")),
    // Present because the mock must carry every name anything in this test's
    // import graph reaches — Browser Mode links ESM for real, so a missing
    // export fails the whole file at link time rather than at the call.
    // `decision-dock.ts` (reached via the ask readers) imports the first;
    // `model-switcher.ts`, now in this graph through the shared turn teardown,
    // imports the second.
    forceReflow: vi.fn(() => 0),
    setBusy: vi.fn(),
  };
});

async function paint(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(null)));
}

/** What a screen reader computes for the `role="tab"` row: its name from
 *  contents, in DOM order.
 *
 *  no accessible-name algorithm is consulted here and none of the
 *  app's CSS, so this is the traversal, and its two exclusions are the model
 *  rather than convenience:
 *
 *   - `aria-hidden="true"` — the dot itself. Excluded by the spec.
 *   - `.tab-pin` on a row without `.tab-pinned` — 12-tabs.css gives it
 *     `display: none`, which removes it from the accessibility tree. Every row
 *     carries the node so renderDOM toggles a class instead of adding and
 *     removing one, so a traversal that ignored the class would announce
 *     "Pinned" on every tab in the strip.
 *   - the close BUTTON — a focusable child with its own role and its own name,
 *     which AT presents as a separate node rather than folding into its
 *     container's.
 *
 *  The property under test is ORDER, and none of the three exclusions can
 *  affect it: the state word sits between the name and the pin. */
function nameFromContents(row: HTMLElement): string {
  const parts: string[] = [];
  for (const node of row.childNodes) {
    if (node.nodeType === Node.TEXT_NODE) {
      parts.push(node.textContent ?? "");
      continue;
    }
    const el = node as HTMLElement;
    if (el.getAttribute("aria-hidden") === "true") {
      continue;
    }
    if (el.tagName === "BUTTON") {
      continue;
    }
    if (el.classList.contains("tab-pin") && !row.classList.contains("tab-pinned")) {
      continue;
    }
    parts.push(el.getAttribute("aria-label") ?? el.textContent ?? "");
  }
  return parts
    .join(" ")
    .replace(/\s+([,.])/g, "$1")
    .replace(/\s+/g, " ")
    .trim();
}

// ---------------------------------------------------------------------------
// The projection harness the DOM sections below share.
//
// Every row on the strip arrives through a real `open_tab` round trip against the
// fake collection, so the ids are OPAQUE and server-minted: nothing here composes
// `c1` or `editor:a.ts`, and a row is addressed through `tabIdFor` after it
// exists. The tab's activation and teardown hooks are the FACTORY's, registered
// the way the composition root registers them, and the seeded dot rides that same
// registration rather than a field a caller sets.
// ---------------------------------------------------------------------------

const seededDots = new Map<string, TabDotStatus>();

async function resetProjection(): Promise<void> {
  const { _resetForTest } = await import("./tabs.js");
  const { registerTabOpeners, _resetTabOpenersForTest } = await import("./tab-materialize.js");
  const { _resetTabsSyncForTest, ingestTabsChanged, listTabs } = await import("./tabs-sync.js");
  const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
  const { bindTabsSync, tabServer } = await import("./__test-helpers__/tabs-server.js");
  bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  seededDots.clear();
  registerTabOpeners({
    chat: {
      show: vi.fn(),
      refresh: vi.fn(),
      close: vi.fn(),
      dot: (chatID: string) => seededDots.get(chatID) ?? "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    // `TabOpeners` is total over `TabKind`, which is a REGISTERED WIRE ENUM, so a
    // kind another session adds server-side reaches this fixture through the
    // codegen. Inert: no case here opens one.
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
}

/** Open a tab of any kind and answer with its minted id. */
async function openSubject(
  kind: TabKind,
  ref = "",
  opts: { activate?: boolean } = {},
): Promise<string> {
  const { openTab, tabIdFor } = await import("./tabs.js");
  await openTab({
    kind,
    ref,
    ...(opts.activate === undefined ? {} : { activate: opts.activate }),
  });
  return tabIdFor(kind, ref);
}

describe("the tab's accessible name announces its state", () => {
  /** The chat tab's opaque id, for the cases that write to it after opening. */
  let chatTabID = "";

  beforeEach(async () => {
    await resetProjection();
    chatTabID = "";
  });

  async function openChat(): Promise<HTMLElement> {
    const { renameTab } = await import("./tabs.js");
    chatTabID = await openSubject("chat", "c1");
    // The store holds no row for this chat, so the factory's derived default is a
    // placeholder. The label a caller knows is the one field of a spec it may
    // override, which is exactly how the run and chat openers supply theirs.
    renameTab(chatTabID, "Fix the parser");
    await paint();
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${chatTabID}"]`);
    if (row === null) {
      throw new Error("tab did not render");
    }
    return row;
  }

  it("names the chat first and its state second", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();

    // Seeded at creation, so the row is never a frame narrower than its
    // neighbours and never announces a chat with no state.
    expect(nameFromContents(row)).toBe("Fix the parser, idle");

    setTabStatus(chatTabID, "working");
    expect(nameFromContents(row)).toBe("Fix the parser, working");
  });

  it("gives every state a distinct spoken phrase", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    const spoken = new Map<string, string>();
    for (const s of ["idle", "working", "waiting", "input", "failed", "done"] as const) {
      setTabStatus(chatTabID, s);
      spoken.set(s, nameFromContents(row));
    }
    // `waiting` and `input` share one VISUAL (a 9px disc has no channel left to
    // separate them — see css/12-tabs.css), so these phrases are the only place
    // the distinction survives. If they ever collapse to near-synonyms the
    // information is simply gone.
    expect(new Set(spoken.values()).size).toBe(6);
    expect(spoken.get("waiting")).toBe("Fix the parser, waiting for you");
    expect(spoken.get("input")).toBe("Fix the parser, needs a decision");
  });

  it("claims exactly what the failed latch's one producer supports", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    setTabStatus(chatTabID, "failed");
    // REWRITTEN, and it used to pin the OPPOSITE. This case asserted
    // "last operation failed" and that the name did NOT contain "turn", on the
    // grounds that the latch was set for every `error` frame naming the chat,
    // `switch_failed` and `bridge_start_failed` among them. That breadth is gone:
    // the error handler stopped touching turn state when `endsTurn` was removed
    // (handlers/turn.ts), so `setTurnFailed` has one live producer, `turn_closed`
    // with outcome `failed` or `refused`, and its two other callers re-derive the
    // same turn verdict. The phrase is the only channel a screen-reader user has
    // here, so it must claim neither more NOR less than that.
    expect(nameFromContents(row)).toBe("Fix the parser, turn failed");
  });

  it("keeps the state word between the name and the pinned marker", async () => {
    const { setTabStatus, setTabPinned } = await import("./tabs.js");
    const row = await openChat();
    setTabPinned(chatTabID, true);
    await paint();
    setTabStatus(chatTabID, "input");
    // Both extra words compose onto the name, in the order they are read: what
    // this chat IS, then what it needs, then how it is filed. That ordering falls
    // out of DOM position, which is the only reason the announced word is a
    // sibling after `.tab-name` rather than a child of the leading dot.
    expect(nameFromContents(row)).toBe("Fix the parser, needs a decision Pinned");
  });

  it("hides the dot itself from the accessibility tree", async () => {
    const row = await openChat();
    // Colour and shape are one channel; the word beside it is the other. A dot
    // that is not aria-hidden would announce as an unlabelled element AND put
    // its state ahead of the name, since it leads the row.
    expect(row.querySelector(".tab-status-dot")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("leads a chat row with the dot and gives every other kind its glyph", async () => {
    const chat = await openChat();
    // A singleton's ref is EMPTY: its identity is its kind, so the `__files__`
    // sentinel id is gone with every other composed one.
    const filesID = await openSubject("files");
    await paint();
    const files = document.querySelector<HTMLElement>(`[data-tab-id="${filesID}"]`);

    // The replacement: a chat tab has no role glyph at all, and its leading
    // element is the dot. That position is what the CSS's `:first-child` slot
    // rule keys on, so it is a real contract rather than an ordering detail.
    expect(chat.querySelector(".tab-icon")).toBeNull();
    expect(chat.firstElementChild?.classList.contains("tab-status-dot")).toBe(true);

    // Every other kind is untouched: it keeps its glyph and the dot stays in the
    // trailing slot, which is where the editor's unsaved mark already lived.
    expect(files?.querySelector(".tab-icon")).not.toBeNull();
    expect(files?.firstElementChild?.classList.contains("tab-icon")).toBe(true);
    expect(files?.querySelector(".tab-status-dot")?.hasAttribute("data-status")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 3. The phrase names the tab's own SUBJECT.
//
// The disc is `aria-hidden`, so the phrase is the only channel a screen-reader
// user has for the state — and THREE producers write these states about three
// different subjects: `tabStatusFor` about a turn, `runStatusFor` about a
// workflow run (run-dots.ts), `subagentStatusFor` about a delegate
// (subagent-dots.ts). So the two OUTCOME states have to name the subject of the
// row they are painted on, while the five that say nothing about a subject stay
// one wording for every kind. Widening a phrase back toward "last operation
// failed" to cover all three would spend the narrowness the chat case's own
// producer measurement bought, so the outcome phrases are pinned to a subject
// rather than to a state alone.
// ---------------------------------------------------------------------------

describe("the announced phrase names the subject of the tab it is on", () => {
  /** Every kind's ref except the composite one. A singleton's identity IS its
   *  kind, so its ref is empty. */
  const PLAIN_REF: Readonly<Record<Exclude<TabKind, "subagent">, string>> = {
    chat: "c1",
    editor: "/a.ts",
    run: "wf-1",
    // A spec's ref is its DIRECTORY, so the row's name is that path's last segment.
    spec: ".kiro/specs/parser",
    settings: "",
    git: "",
    files: "",
    history: "",
    docs: "",
    web: "/workspace/demo/index.html",
  };

  const NEUTRAL: readonly (readonly [TabDotStatus, string])[] = [
    ["idle", "idle"],
    ["working", "working"],
    ["waiting", "waiting for you"],
    ["input", "needs a decision"],
    ["dirty", "unsaved changes"],
  ];

  const EVERY_STATE: readonly TabDotStatus[] = [
    "idle",
    "working",
    "waiting",
    "input",
    "failed",
    "done",
    "dirty",
  ];

  beforeEach(async () => {
    await resetProjection();
  });

  /** Open a tab of `kind` and answer with its id and its rendered row. */
  async function openKind(kind: TabKind): Promise<{ id: string; row: HTMLElement }> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    let id: string;
    if (kind === "subagent") {
      // A delegate's tab exists only as a SUB-TAB under the chat that dispatched
      // it, and the composite ref is spelled by the module that owns the format.
      const parent = await openSubject("chat", "c1");
      const ref = subagentRef("c1", "task-1");
      await openTab({ kind, ref, parent, owns: false });
      id = tabIdFor(kind, ref);
    } else {
      id = await openSubject(kind, PLAIN_REF[kind]);
    }
    await paint();
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    if (row === null) {
      throw new Error(`the ${kind} row did not render`);
    }
    return { id, row };
  }

  function srOf(row: HTMLElement): HTMLElement {
    const sr = row.querySelector<HTMLElement>(".tab-status-sr");
    if (sr === null) {
      throw new Error("no announced-word element");
    }
    return sr;
  }

  function dotOf(row: HTMLElement): HTMLElement {
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    return dot;
  }

  /** The announced word without the `, ` that composes it onto the tab's name. */
  function phraseOn(row: HTMLElement): string {
    return (srOf(row).textContent ?? "").replace(/^, /u, "");
  }

  it("names the workflow run on a run tab", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("run");

    // The writer is `run-dots.ts`, and this row announced "turn failed" about a
    // workflow run for as long as that module has existed. The noun is the one
    // the rest of the app already puts in front of a reader (run-bar.ts's
    // fallback name, run-exec-source.ts's label, the run card's aria-label).
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("workflow run failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("workflow run finished");
  });

  it("names the subagent on a subagent sub-tab", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("subagent");

    // `subagent`, not `delegate`: the phrase is announced immediately after the
    // tab's own label, and the LABEL vocabulary is Subagent (roles.ts's fallback
    // name, the pipeline container's title, the route segment).
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("subagent failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("subagent finished");
  });

  it("still names the turn on a chat row", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("chat");

    // The regression guard for the kind that was already right: `tabStatusFor`'s
    // subject genuinely is a turn (its latch has one live producer, `turn_closed`
    // with a broken outcome), so kind-awareness must not move this wording.
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("turn failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("turn finished");
  });

  it("gives the five non-outcome states one wording on every kind", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const chat = await openKind("chat");
    const run = await openKind("run");
    const sub = await openKind("subagent");

    // These five say nothing about WHAT is idle or waiting, so a per-kind
    // wording would be five duplicated tables for no information — and a kind
    // that drifted in one of them would announce a state the CSS paints
    // identically on every other row.
    for (const [state, phrase] of NEUTRAL) {
      for (const t of [chat, run, sub]) {
        setTabStatus(t.id, state);
        expect(phraseOn(t.row)).toBe(phrase);
      }
    }
  });

  it("gives every kind a subject-bearing outcome phrase", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { TAB_ICONS } = await import("./tab-view.js");
    // Enumerated from the exhaustive table rather than listed here, so a tenth
    // kind added to the Go const block is covered the day it is generated.
    const kinds = Object.keys(TAB_ICONS) as TabKind[];
    const finishedBy = new Map<TabKind, string>();

    for (const kind of kinds) {
      const { id, row } = await openKind(kind);
      for (const [state, verb] of [
        ["failed", "failed"],
        ["done", "finished"],
      ] as const) {
        setTabStatus(id, state);
        const phrase = phraseOn(row);
        // A subject, then the verb. `endsWith` is what rules out the bare verb,
        // which is the subject-less phrase this change exists to remove; the
        // `undefined` read is the runtime half of the lookup's totality, because
        // a table missing a kind type-checks under a cast and only shows up here.
        expect(phrase.endsWith(` ${verb}`)).toBe(true);
        expect(phrase).not.toContain("undefined");
      }
      // The `done` write above is the last one, so this is each kind's FINISHED
      // phrase.
      finishedBy.set(kind, phraseOn(row));
    }

    // The three kinds with a live producer say three different things, which is
    // the whole property one state-keyed table could not have. Both outcomes are
    // composed from one `DOT_SUBJECT` entry, so three distinct finished phrases
    // is three distinct failed ones.
    const producers = ["chat", "run", "subagent"] as const;
    expect(new Set(producers.map((k) => finishedBy.get(k))).size).toBe(3);
  });

  it("paints the tooltip and the announced word from one string", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { TAB_ICONS } = await import("./tab-view.js");

    // The invariant kind-awareness had to survive: a sighted reader's tooltip and
    // a screen-reader user's word come from ONE resolver call, so they cannot
    // drift per kind. Asserted over every kind and every state, because a second
    // lookup is exactly the shape a per-kind phrase invites.
    for (const kind of Object.keys(TAB_ICONS) as TabKind[]) {
      const { id, row } = await openKind(kind);
      for (const state of EVERY_STATE) {
        setTabStatus(id, state);
        const announced = srOf(row).textContent ?? "";
        expect(announced.startsWith(", ")).toBe(true);
        expect(dotOf(row).dataset["tooltip"]).toBe(announced.slice(2));
      }
    }
  });

  it("clears the tooltip with the state, so no phrase outlives its dot", async () => {
    // The clear has to reach the attribute the paint writes. Miss it and a dot
    // with nothing to show still answers a hover with the last state it held,
    // which is the one reading a reader cannot check against anything on screen.
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("chat");
    setTabStatus(id, "working");
    expect(dotOf(row).hasAttribute("data-tooltip")).toBe(true);
    setTabStatus(id, "");
    expect(dotOf(row).hasAttribute("data-tooltip")).toBe(false);
    expect(dotOf(row).hasAttribute("title")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 4. Reduced motion.
// ---------------------------------------------------------------------------

describe("prefers-reduced-motion stops the dot's animation", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("carries the beat on the disc itself, with no overlay to carry it", () => {
    // The disc animates its OWN opacity, created only while `data-status` is working,
    // so an idle strip runs none — the shared `:root` clock this replaced cost a
    // whole-document style invalidation every frame forever. Phase still agrees
    // across dots because the delay comes off one origin (`beat-phase.ts`).
    const disc = ruleContaining(tabs, dot, "top");
    expect(disc.body).toContain("vk-dot-beat");
    expect(disc.body).toContain("var(--beat-phase, 0ms)");
    // And the overlay is GONE rather than merely unanimated. Anchored at the start of
    // a line so it cannot match the run row's own `::before`, whose selector carries
    // this one as a descendant — the substring is present there and means the
    // opposite thing.
    expect(tabs).not.toMatch(/^\.tab-status-dot\[data-status="working"\]::before/mu);
  });

  it("needs no reduced-motion arm for the disc, because the beat rests visible", () => {
    // 40-a11y.css runs the animation once for 0.01ms and `vk-dot-beat` leaves
    // `from`/`to` implicit, so the disc rests at its own opacity — 1 — and the donut
    // below is the whole degradation. What DOES need removing there is the run row's
    // overlay, and it is removed with the other two square marks rather than by a
    // disc-scoped arm, because it is one of them.
    expect(tabs).not.toMatch(/^ {2}\.tab-status-dot\[data-status="working"\]::before/mu);
    const reduced = ruleContaining(
      tabs,
      '.tab[data-kind="run"] .tab-status-dot[data-status="working"]::before',
      "prefers-reduced-motion",
    );
    expect(/content:\s*none/.test(reduced.body)).toBe(true);
  });

  it("replaces the lost motion with a shape, not just a colour", () => {
    // Motion is what separates `working` from every settled state, so with it
    // gone the disc has to differ by SHAPE or the state is carried by hue alone
    // (WCAG 1.4.1). It becomes a donut.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    expect(/radial-gradient\(closest-side, transparent/.test(reduced.body)).toBe(true);
    // The hole is a TRANSPARENT gradient stop, not a background-coloured inset
    // shadow: the same dot sits on five different row fills (resting, hovered,
    // selected, selected-hover, selected-press) in two themes, and an opaque
    // hole would be wrong on four of them.
    expect(/inset .*var\(--c-bg/.test(reduced.body)).toBe(false);
  });

  it("does not borrow the wants-you ring to make up for the motion", () => {
    // It used to. The ring means "this chat wants you", `working` wants nothing,
    // and with the waiting/input pair un-merged three of six states would have
    // carried one. Dropping it also leaves the donut one channel from `idle`
    // alone rather than from both ringed states.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    expect(/box-shadow/.test(reduced.body)).toBe(false);
  });

  it("keeps the donut's band tellable apart from a hollow dot's hairline", () => {
    // `idle` and `waiting` are hollow — a 1.5px edge — so at 9px the donut is the
    // OTHER ring of ink in the vocabulary and the two have to differ by weight,
    // not just by hue. A 45% hole leaves a 2.5px band around a 4px hole; the 55%
    // it started at left 2.0px, close enough to a hairline to read as one.
    // Widening past 45% closes the hole until the donut reads as a solid disc,
    // which is the collision on the other side, so the stop is bounded twice.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    const stop = /transparent 0 (\d+)%, var\(--dot-color\) \1% 100%/.exec(reduced.body);
    expect(stop, "the donut must have one hole radius, used by both stops").not.toBeNull();
    const hole = Number(stop?.[1]);
    expect(hole).toBeGreaterThanOrEqual(35);
    expect(hole).toBeLessThanOrEqual(50);
  });

  it("keeps the global reduced-motion sweep that backs it up", () => {
    // The component rule above is the fix; this is the belt. If the global sweep
    // ever narrows to a selector list, an animation added to the dot later would
    // silently keep running.
    const a11y = loadCSS("40-a11y.css");
    const global = ruleContaining(a11y, "*", "prefers-reduced-motion");
    expect(global.selector).toContain("*::before");
    expect(/animation-iteration-count:\s*1/.test(global.body)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 5. Selection does not change status ink.
// ---------------------------------------------------------------------------

describe("the active row keeps the same dot color", () => {
  it("never re-points --dot-color from an active-tab selector", () => {
    // Selection belongs to the row. A status belongs to the chat, so selecting
    // the row must not turn normal green into a darker green. Contrast
    // adjustments belong to the row fill, not the state indicator's identity.
    const sel = loadCSS("70-selection.css");
    expect(sel).not.toMatch(/\.tab\.active\s+\.tab-status-dot/u);
  });

  it("keeps every state resolving through the one custom property", () => {
    // The disc, both hollow borders, the hard ring's 30% mix and the
    // reduced-motion donut all read --dot-color. The selected row inherits the
    // exact same value because it has no override.
    const tabs = loadCSS("12-tabs.css");
    for (const state of ["working", "input", "failed", "done", "dirty"]) {
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      expect(
        /background: var\(--dot-color\)/.test(rule.body),
        `${state} must paint from --dot-color; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
    // The two hollow states paint their ink into a border instead of a
    // background, so they read the same property from the other side.
    for (const state of ["idle", "waiting"]) {
      expect(
        /border: 1\.5px solid var\(--dot-color\)/.test(
          ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top").body,
        ),
        `${state} must draw its hollow edge from --dot-color`,
      ).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// 4b. The ink vocabulary, shared with web-terminal-kiro.
//
// The two apps sit in the same window (marotte hosts a web-terminal panel), so a
// state that means one thing must not carry two colours between them. It did
// twice over. First `working` was marotte's violet accent while the terminal's was
// blue. Then the fix aligned these tokens to @cplieger/web-terminal-ui's LIBRARY
// DEFAULTS — which web-terminal-kiro overrides on every member — so the two apps
// agreed with the package and still disagreed with each other. The values are now
// web-terminal-kiro's own, hue-exact in both themes with L and C sized per theme.
// ---------------------------------------------------------------------------

describe("the dot inks are web-terminal-kiro's status vocabulary", () => {
  const tabs = loadCSS("12-tabs.css");

  it("gives each state the token carrying the source system's ink", () => {
    // Tokens, not values: the source has one theme and marotte has two, so the
    // token is where the per-theme sizing lives. See the --c-dot-* block in
    // 01-tokens.css for each value's provenance.
    const inks: [string, string][] = [
      ["working", "--c-dot-working"], // --status-working #c6a0ff
      ["waiting", "--c-dot-input"],
      ["input", "--c-dot-input"], // --status-input  oklch(78% 0.15 95deg)
      ["failed", "--c-dot-failed"], // --status-failed #dc2626, lightened to be visible
      ["done", "--c-dot-done"], // --status-done   oklch(78% 0.15 150deg)
    ];
    for (const [state, token] of inks) {
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      expect(
        rule.body.includes(`--dot-color: var(${token})`),
        `${state} must take ${token}; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
  });

  it("keeps the editor's mark on the accent token, and working off it", () => {
    // `dirty` and `working` were BOTH literally --c-accent, which is how an editor
    // tab with unsaved changes and a chat mid-turn came to look identical. They
    // read different tokens now. The VALUES are close again — the source's working
    // violet is nearly this app's accent, deliberately — and what separates them is
    // MOTION, plus the structural fact that a tab is never both a chat and a file.
    const working = ruleContaining(tabs, '.tab-status-dot[data-status="working"]', "top");
    const dirty = ruleContaining(tabs, '.tab-status-dot[data-status="dirty"]', "top");
    expect(working.body).not.toContain("--c-accent");
    expect(dirty.body).toContain("--dot-color: var(--c-accent)");
  });

  it("un-merges waiting from input on fill, which is the only channel it has", () => {
    // The pair shares ONE ink on purpose: both mean "action required", which is the
    // single thing the source's --status-input says. So fill is what carries them
    // apart, and it is load-bearing rather than decorative — the alias that used to
    // exempt this pair from the non-colour-channel check is gone, so hue alone
    // would be a WCAG 1.4.1 failure. `input` is solid (a turn frozen mid-flight),
    // `waiting` is hollow (its turn is over).
    //
    // These rules serve a CHAT row, which is the population that reasoning is about.
    // On a RUN row the same two states are both rings and BAND carries them instead,
    // because fill is spent there on whether the run has ended (4e).
    const waiting = ruleContaining(tabs, '.tab-status-dot[data-status="waiting"]', "top");
    const input = ruleContaining(tabs, '.tab-status-dot[data-status="input"]', "top");
    expect(waiting.body).toContain("background: transparent");
    expect(input.body).toContain("background: var(--dot-color)");
    // The ring is the wants-you marker and BOTH keep it: that is the half of the
    // treatment they still share, and it is why the phrases still have to differ.
    for (const rule of [waiting, input]) {
      expect(
        /box-shadow: 0 0 0 2px color-mix\(in srgb, var\(--dot-color\) 30%/.test(rule.body),
      ).toBe(true);
    }
  });

  it("gives the ring to the wants-you states and to nothing else", () => {
    // Every rule in the block, at every scope: exactly FOUR carry a ring, and all
    // four mean "blocked on a person". The reduced-motion `working` donut used to be
    // among them, which put the wants-you marker on the one state that wants nothing
    // from the reader; the workflow mark's own `waiting`/`input` pair joined instead,
    // which is agreement with the grammar rather than a loan against it — the marker
    // still means exactly one thing, now across two marks.
    // Read through `allRules` rather than a prelude regex, so each rule is named by
    // its WHOLE selector list: the mark's two wants-you rules are shared with the
    // composer band's glyph and a History run row's lead, and a rule that gains a
    // consumer has to show it here rather than hiding behind whichever member
    // happens to be written last.
    const ringed = allRules(tabs)
      .filter((r) => /box-shadow: 0 0 0 2px/.test(r.body))
      .map((r) => r.selector.replace(/\s+/gu, " "))
      .sort();
    expect(ringed).toEqual([
      '.tab-run-dot[data-status="input"], .run-bar-glyph[data-status="input"], .entry-mark[data-status="input"]',
      '.tab-run-dot[data-status="waiting"], .run-bar-glyph[data-status="waiting"], .entry-mark[data-status="waiting"]',
      '.tab-status-dot[data-status="input"]',
      '.tab-status-dot[data-status="waiting"]',
    ]);
  });

  it("keeps the tool's alias list empty, which is where the un-merge is proven", () => {
    // scripts/css-contrast.py fails when two chat states differ by hue alone, and
    // DOT_ALIASES is the list of pairs it has been told not to look at. The
    // wants-you pair was in it, and it now shares one INK, so an entry re-appearing
    // here would exempt from the check the exact pair whose only separator is fill —
    // letting a merge come back with the check still reporting PASS, which is the
    // one failure mode a mechanical gate has that a human reviewer does not.
    expect(cssContrastScript).toContain("DOT_ALIASES: list[tuple[str, str]] = []");
  });
});

// ---------------------------------------------------------------------------
// 4c. The WORKFLOW mark, the second mark in a chat row's leading cluster.
//
// It reuses this vocabulary rather than opening a second one — the same four
// `--c-dot-*` inks and the same `--dot-color` indirection — so everything section
// 4b pins about the dot's ink applies to it unchanged. What is NOT inherited is
// the SILHOUETTE: the dot is a circle in every state but `failed`, and this mark
// is a rounded SQUARE in all of them, which is what separates the two at identical
// hue without spending a channel per state (WCAG 1.4.1).
//
// `css-contrast.py dot` now carries both marks in one pairwise population — its
// matrix used to enumerate the dot's seven states alone, which is how a mark whose
// `waiting` resolved to the dot's `waiting` tuple exactly shipped with that script
// reporting PASS. So this section is the STYLESHEET side of the same claim: the
// script TRANSCRIBES the channels and cannot see the CSS, so every number it
// transcribes is read off `12-tabs.css` here and compared. A transcription that
// drifts from the rule it describes turns a mechanical gate into a green light.
// ---------------------------------------------------------------------------

describe("the workflow mark is a ring, and never the dot's disc", () => {
  const tabs = loadCSS("12-tabs.css");
  const RUN_STATES = ["working", "waiting", "input"] as const;
  /** The exec view's own ratio, which is what the band channel is measured against
   *  wherever two states differ on it alone. */
  const BAND_RATIO_FLOOR = 2;

  /** The mark's shared LOOK rule: the box, the silhouette, the band's style and the
   *  default ink. Keyed on the BAR's class rather than the tab's, because
   *  `.tab-run-dot` is a member of two top-level rules now — this one, which the
   *  composer band's live-run glyph shares, and the tab row's own tuck-and-spawn,
   *  which is layout the bar has no use for. */
  function lookRule(): string {
    return ruleContaining(tabs, ".run-bar-glyph", "top").body;
  }

  /** The band a state's own rule paints, in px, in whichever of the three spellings
   *  that rule uses: --run-band when the closing beat has to read the same number,
   *  `border-width` when a base rule already supplies the style, and the `border`
   *  shorthand when it does not. Both marks answer, so a run row's own ring is
   *  measurable against the mark's with one reader. */
  function bandOf(selector: string, scope = "top"): number {
    const body = ruleContaining(tabs, selector, scope).body;
    const hit =
      /--run-band:\s*([\d.]+)(px|rem)/.exec(body) ??
      /border-width:\s*([\d.]+)(px|rem)/.exec(body) ??
      /border:\s*([\d.]+)(px|rem)/.exec(body);
    expect(hit, `${selector} must declare a band width; got: ${body.trim()}`).not.toBeNull();
    const n = Number(hit?.[1]);
    return hit?.[2] === "rem" ? n * 16 : n;
  }

  /** The band the DOT's `radial-gradient` donut paints, derived from its hole stop.
   *  The gradient's radius is half of --dot-size (8px), so a hole of H% leaves a
   *  band of `4 * (1 - H/100)` — the arithmetic that rule is written against, and
   *  the only way to compare it against the mark's own band numerically. */
  function donutBandOf(selector: string): number {
    const body = ruleContaining(tabs, selector, "prefers-reduced-motion").body;
    const stop = /transparent 0 (\d+)%, var\(--dot-color\) \1% 100%/.exec(body);
    expect(stop, `${selector} must paint one donut hole; got: ${body.trim()}`).not.toBeNull();
    return 4 * (1 - Number(stop?.[1]) / 100);
  }

  it("takes the same inks as the dot, through the same custom property", () => {
    // Tokens, not values, for the dot's reason: the source has one theme and
    // marotte has two, so the token is where the per-theme sizing lives.
    const inks: [string, string][] = [
      ["working", "--c-dot-working"],
      ["waiting", "--c-dot-input"],
      ["input", "--c-dot-input"],
    ];
    for (const [state, token] of inks) {
      const rule = ruleContaining(tabs, `.tab-run-dot[data-status="${state}"]`, "top");
      expect(
        rule.body.includes(`--dot-color: var(${token})`),
        `${state} must take ${token}; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
    // Every state's edge reads the one property, which is what lets
    // 70-selection.css keep saying nothing about either mark.
    expect(lookRule()).toContain("border: 0 solid var(--dot-color)");
  });

  it("defaults its ink to the quiet one, not to the state that means work", () => {
    // The base body's `--dot-color` is what a state arm added later paints with if
    // it forgets its own ink. Defaulting to --c-dot-working makes that omission
    // claim a run is executing; the dot's own base defaults to idle, which is the
    // quieter failure and the one to match.
    expect(lookRule()).toContain("--dot-color: var(--c-dot-idle)");
  });

  it("declares every band in one unit, so the relation survives a root font size", () => {
    // A --run-band in rem beside 1.5px/2px literals reads as the same ladder at
    // 16px and inverts at 20px, where 0.125rem is 2.5px. px throughout, like the
    // activity dot's own bands above.
    // File-wide and over every SPELLING of a band, because there are three: the
    // `border` shorthand (a state whose base rule has no border-style to inherit),
    // `border-width` (one that does), and --run-band (one whose beat mask has to read
    // the same number). The mark's own four are the three states plus its
    // reduced-motion arm; the run row's rings are in here too, and a rem among any of
    // them is what this rejects.
    const bands = [...tabs.matchAll(/(?:border|border-width|--run-band):\s*([\d.]+)(px|rem|em)/g)];
    expect(bands.length).toBeGreaterThanOrEqual(RUN_STATES.length + 1);
    expect([...new Set(bands.map(([, , unit]) => unit))]).toEqual(["px"]);
  });

  it("declares exactly four states, so no outcome can reach it", () => {
    // `done` and `failed` are unreachable from a live inventory row, so a rule for
    // one would be a treatment for a state the producer cannot supply — and its
    // presence is what would invite a producer change later.
    const states = [...tabs.matchAll(/\.tab-run-dot\[data-status="(\w+)"\]/g)].map(([, s]) => s);
    expect([...new Set(states)].sort()).toEqual(["input", "waiting", "working"]);
  });

  it("is never a filled disc, in any state or at any scope", () => {
    // The disc is the activity dot's. Shape is the only thing keeping the two
    // marks apart at identical hue, so a background of the ink here would collapse
    // that separation at the one moment both marks are violet. The one fill is the
    // closing beat's seal, which rests invisible and shows only at the beat's peak.
    const marks = allRules(tabs).filter((r) => r.selector.includes(".tab-run-dot"));
    for (const rule of marks.filter((r) => !r.selector.includes("::after"))) {
      expect(
        /background:\s*var\(--dot-color\)/.test(rule.body),
        `${rule.selector} must not fill the mark; got: ${rule.body.trim()}`,
      ).toBe(false);
    }
    const seal = ruleContaining(tabs, '.tab-run-dot[data-status="working"]::after', "top");
    expect(seal.body).toContain("opacity: 0;");
    expect(seal.body).toContain("vk-mark-seal");
  });

  it("separates its own wants-you pair by band at the exec column's ratio", () => {
    // The pair shares one ink AND one halo AND one silhouette, and it is still,
    // so BAND WIDTH is the whole separator — which makes the RATIO the assertion
    // rather than the inequality. The exec column separates its own rings at 2px
    // against a snapped 1px; a 1.5px hairline here would have given 1.33:1, a third
    // of a pixel of ink at 8px, which the stylesheet can express and a reader
    // cannot. `css-contrast.py dot` gates the same number over its transcription.
    const waiting = bandOf('.tab-run-dot[data-status="waiting"]');
    const input = bandOf('.tab-run-dot[data-status="input"]');
    expect(input / waiting).toBeGreaterThanOrEqual(BAND_RATIO_FLOOR);
    expect(cssContrastScript).toContain(`BAND_RATIO_FLOOR = ${BAND_RATIO_FLOOR}.0`);
  });

  it("separates working from both by motion, with no halo to lean on", () => {
    // working needs nothing from the reader, so it may not take the wants-you halo
    // — the same correction the dot's own reduced-motion donut carries. What is left
    // is motion, read off the document clock.
    const working = ruleContaining(tabs, '.tab-run-dot[data-status="working"]', "top").body;
    expect(/box-shadow/.test(working)).toBe(false);
    expect(
      ruleContaining(tabs, '.tab-run-dot[data-status="working"]::before', "top").body,
    ).toContain("vk-mark-close");
  });

  it("beats on the overlay and not on the ring", () => {
    // The ring is the shape; only the overlay moves. Its animation is scoped to the
    // working state, so a settled run-dot carries none at all — which matters more
    // than it reads: an animation OUTRANKS a normal declaration, so a settled state
    // could not answer a base-rule beat by resetting it.
    const ring = ruleContaining(tabs, '.tab-run-dot[data-status="working"]', "top");
    const glow = ruleContaining(tabs, '.tab-run-dot[data-status="working"]::before', "top");
    expect(/animation:/.test(ring.body)).toBe(false);
    expect(glow.body).toContain("vk-mark-close var(--dot-beat-dur)");
  });

  it("closes the hole by scaling it, clipped to the ring", () => {
    // The beat is a transform on a pre-painted hole, so it repaints nothing; the
    // parent's overflow is what keeps the overlay's ink inside the ring.
    const ring = ruleContaining(tabs, '.tab-run-dot[data-status="working"]', "top");
    const glow = ruleContaining(tabs, '.tab-run-dot[data-status="working"]::before', "top");
    expect(ring.body).toContain("overflow: hidden");
    // Derived from the mark's own size and band, so moving either cannot leave the
    // hole behind; the open hole reaches the square hole's CORNER.
    expect(glow.body).toContain("--mark-hole: calc(var(--dot-size) / 2 - var(--run-band))");
    expect(glow.body).toContain("transparent 0 calc(1.4142 * var(--mark-hole))");
    const beat = /@keyframes\s+vk-mark-close\s*\{([\s\S]*?)\n\}/u.exec(loadCSS("03-base.css"));
    expect(beat, "03-base.css defines vk-mark-close").not.toBeNull();
    expect(beat![1]!).toContain("transform: scale(var(--mark-close))");
    expect(beat![1]!, "never a paint property").not.toMatch(/background|border|opacity/u);
    // The seal covers the pinhole the scale leaves, on the same clock, in the ink.
    const seal = ruleContaining(tabs, '.tab-run-dot[data-status="working"]::after', "top");
    expect(seal.body).toContain("vk-mark-seal var(--dot-beat-dur)");
    expect(seal.body).toContain("background: var(--dot-color)");
  });

  it("removes the beat overlay entirely under reduced motion", () => {
    // `content: none` rather than a reset of its opacity, for the dot's reason: the
    // clock rests at its registered 0 today, and that is a property of the initial
    // VALUE rather than of this rule.
    const reduced = ruleContaining(
      tabs,
      '.tab-run-dot[data-status="working"]::before',
      "prefers-reduced-motion",
    );
    expect(/content:\s*none/.test(reduced.body)).toBe(true);
  });

  it("replaces the lost motion with the heaviest band in the cluster", () => {
    const reduced = ruleContaining(
      tabs,
      '.tab-run-dot[data-status="working"]',
      "prefers-reduced-motion",
    );
    // The same channel the mark already reads, rather than the dot's gradient donut:
    // this mark is a ring at rest, so a hole is not a state change it can make.
    const heavy = bandOf('.tab-run-dot[data-status="working"]', "prefers-reduced-motion");
    // Heavier than EVERY band the mark paints with motion available, or stillness
    // would leave it separated from `input` by the halo alone.
    for (const state of RUN_STATES) {
      expect(heavy).toBeGreaterThan(bandOf(`.tab-run-dot[data-status="${state}"]`));
    }
    // No fill, and no wants-you halo bought to make up for the motion — the
    // correction the dot's own donut already carries.
    expect(/background:/.test(reduced.body)).toBe(false);
    expect(/box-shadow/.test(reduced.body)).toBe(false);
    // And the hole stays open: closing it is the solid disc this mark may never be.
    const probe = document.createElement("div");
    probe.style.inlineSize = "var(--dot-size)";
    document.body.append(probe);
    const size = probe.getBoundingClientRect().width;
    probe.remove();
    expect(heavy * 2).toBeLessThan(size);
  });

  it("hands a run's own row the whole band ladder, number for number", () => {
    // The parity claim on the one axis a render cannot check, and that is why it is
    // here rather than in 4e: MEASURED in this suite's own browser, Chromium reports
    // `border-width: 1.5px` as "1px" at dpr 1, 2 and 3 alike, so the dot's declared
    // hairline and the mark's 1px paint identically and a computed-style comparison
    // passes whichever number the source holds. What taking the mark's numbers buys
    // for certain is that the DECLARED ladder is the painted one, so the 2:1 floor
    // below holds by construction instead of by an engine's snapping.
    for (const state of RUN_STATES) {
      expect(
        bandOf(`.tab[data-kind="run"] .tab-status-dot[data-status="${state}"]`),
        `${state} band`,
      ).toBe(bandOf(`.tab-run-dot[data-status="${state}"]`));
    }
  });

  it("hands a run's own row the same still band, not a lighter one", () => {
    // The two rings report ONE run, so a reader on this preference may not get a 2px
    // ring under a 3px one. Source rather than render, because the test page cannot
    // emulate the query; 4e measures the pair with motion available.
    const heavy = bandOf('.tab-run-dot[data-status="working"]', "prefers-reduced-motion");
    const onRunRow = bandOf(
      '.tab[data-kind="run"] .tab-status-dot[data-status="working"]',
      "prefers-reduced-motion",
    );
    expect(onRunRow).toBe(heavy);
    // Its two siblings need no arm at all, and that is the claim worth pinning here:
    // both are STILL with motion available, so stopping the clock takes nothing from
    // them, and the 1px/2px ladder they separate on is the one below.
    for (const state of ["waiting", "input"] as const) {
      expect(() =>
        ruleContaining(
          tabs,
          `.tab[data-kind="run"] .tab-status-dot[data-status="${state}"]`,
          "prefers-reduced-motion",
        ),
      ).toThrow();
    }
  });

  it("keeps the two marks separable by SILHOUETTE, in every state", () => {
    // THE CROSS-MARK QUESTION, and the silhouette is what answers it once rather
    // than per state. The pair that forces it is the reduced-motion one: the dot's
    // `working` becomes a 2.2px band around a 3.6px hole in the SAME violet 8px to
    // the left, and a circular mark would have had to answer it with a band tellable
    // apart at 2:1 — under 1.1px, which collides with `waiting`, or over 4.4px,
    // which closes the hole at this diameter. Neither exists, so the shape does the
    // work and the bands are free to separate the mark's own three states.
    expect(lookRule()).toContain("border-radius: var(--dot-radius-square)");
    // The dot is the circle, in every state it paints one — which is what the
    // square is being told apart FROM.
    expect(ruleContaining(tabs, ".tab-status-dot", "top").body).toContain("border-radius: 50%");
    // ONE owner for the proportion, and a run SUB-TAB's dot reads that same token:
    // two marks report one workflow run, so two spellings of the divisor would be
    // two things that can drift — at render time (4e measures that) and in source.
    expect(
      ruleContaining(tabs, '.tab[data-kind="run"] .tab-status-dot:not([data-status="failed"])')
        .body,
    ).toContain("border-radius: var(--dot-radius-square)");
    // A quarter of the diameter, so the flat sides are half of it: enough silhouette
    // to read at 8px, and bounded well under the half that would make it a circle
    // again.
    const radius = /--dot-radius-square:\s*calc\(var\(--dot-size\) \/ (\d+)\)/.exec(
      loadCSS("01-tokens.css"),
    );
    expect(Number(radius?.[1])).toBeGreaterThan(2);
    // The dot's own donut band is still what the mark's is measured against, and the
    // two remain different numbers as well as different shapes — belt and braces,
    // since a reader at DPR 1 gets whichever channel survives snapping.
    const dot = donutBandOf('.tab-status-dot[data-status="working"]');
    const mark = bandOf('.tab-run-dot[data-status="working"]', "prefers-reduced-motion");
    expect(mark).toBeGreaterThan(dot);
    // And apart from the dot's hollow hairline, which is the state the mark is most
    // likely to sit beside: a chat that has not initiated with a run already going.
    // No band in this block equals it, so that pair is separated twice rather than
    // by silhouette alone.
    const idle = ruleContaining(tabs, '.tab-status-dot[data-status="idle"]', "top").body;
    const hairline = Number(/border:\s*([\d.]+)px/.exec(idle)?.[1]);
    expect(mark).toBeGreaterThan(hairline);
    for (const state of RUN_STATES) {
      expect(bandOf(`.tab-run-dot[data-status="${state}"]`)).not.toBe(hairline);
    }
  });

  it("is carried by the mechanical 1.4.1 gate, not only by this file", () => {
    // `css-contrast.py dot` is the app's declared WCAG 1.4.1 check, and its matrix
    // enumerated the activity dot's seven states alone while the strip had ten
    // marks — which is exactly how a mark whose `waiting` was byte-identical to the
    // dot's shipped with that script reporting PASS. The three states and the axis
    // that can express their separation both have to be in it.
    expect(cssContrastScript).toContain(
      "RUN_MARK_STATES: list[tuple[str, str, dict[str, str]]] = [",
    );
    // Declared is not checked: the pairwise pass has to READ the table, or the
    // matrix prints three more rows and gates none of them.
    expect(cssContrastScript).toContain("for s, _ink, ch in RUN_MARK_STATES");
    for (const state of RUN_STATES) {
      expect(cssContrastScript).toMatch(new RegExp(`"${state}",\\s*\\n\\s*"--c-dot-`));
    }
    // The band axis, without which the mark's `waiting` transcribes to the dot's
    // `waiting` tuple exactly and the pairwise check cannot see the collision.
    expect(cssContrastScript).toContain('"band"');
    // And the mark's own reduced-motion substitution, which is NOT the dot's: it is
    // hollow already, so it has no fill left to trade for the lost motion.
    expect(cssContrastScript).toContain("RUN_REDUCED_MOTION_SUBSTITUTION = {");
  });

  it("gates a run row's WHOLE vocabulary, transcribed from the producer", () => {
    // `RUN_ROW_MEMBERS` is the run row's 1.4.1 population, and it is a hand-written
    // list in another language — so the one thing it can be wrong about is MEMBERSHIP.
    // A state the producer can paint and this list omits is a state no pair is ever
    // checked against, and the sweep reports PASS over a population that is not the
    // row. This is the transcription check for it.
    //
    // The producer is `store.ts runStatusFor`, driven over its whole input space
    // rather than trusted to a second list here: `ALL_STATUSES` is a Record keyed by
    // `ClassifiedRunStatus`, so a member added to the wire enum fails THIS type check
    // rather than quietly shrinking the sweep. `""` is excluded because it means "no
    // state to show" — a reserved empty slot paints no mark, so it has nothing to be
    // confusable with.
    const ALL_STATUSES: Record<ClassifiedRunStatus, true> = {
      running: true,
      paused: true,
      completed: true,
      failed: true,
      aborted: true,
      cancelled: true,
      unknown: true,
    };
    const painted = new Set<string>();
    for (const status of [undefined, ...(Object.keys(ALL_STATUSES) as ClassifiedRunStatus[])]) {
      for (const ask of [false, true]) {
        for (const pause of ["", "need_input"] as const) {
          const s = runStatusFor(status, ask, pause);
          if (s !== "") {
            painted.add(s);
          }
        }
      }
    }

    const members = /RUN_ROW_MEMBERS = \[([^\]]*)\]/.exec(cssContrastScript)?.[1];
    expect(members, "RUN_ROW_MEMBERS is not declared").toBeDefined();
    // Each entry is `<element>:<state>` — the element half says which table the
    // channel tuple comes from and is deliberately not part of this comparison, which
    // is about the STATE vocabulary.
    const gated = new Set(
      [...(members ?? "").matchAll(/"(?:dot|run):(\w+)"/g)].map((m) => m[1] ?? ""),
    );
    expect([...gated].sort()).toEqual([...painted].sort());
  });
});

// ---------------------------------------------------------------------------
// 4d. The mark's painted geometry, against real layout.
//
// `box-sizing: border-box` is what makes --dot-size the painted DIAMETER rather
// than the diameter plus two bands, and it is the whole reason the ring can sit at
// the size of the disc it shares a cluster with. A 3px band would otherwise paint
// a 14px mark beside an 8px dot. Nothing about the markup implies it, so it is
// measured with the shipped stylesheet mounted — the same thing
// state-column-size.test.ts does for the run bar's own ring.
// ---------------------------------------------------------------------------

describe("the ring is painted at the size of the disc beside it", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  beforeEach(async () => {
    await resetProjection();
  });

  it("paints every state at --dot-size, band width notwithstanding", async () => {
    const { setTabRunStatus, tabIdFor } = await import("./tabs.js");
    const id = await openSubject("chat", "c1");
    await paint();
    expect(tabIdFor("chat", "c1")).toBe(id);
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    const mark = row?.querySelector<HTMLElement>(".tab-run-dot");
    const dot = row?.querySelector<HTMLElement>(".tab-status-dot");
    expect(mark, "a chat row carries the workflow mark").not.toBeNull();

    // --dot-size RESOLVED, through a probe rather than by parsing the declaration:
    // the token is authored in rem, so reading it off `:root` answers `0.5rem` and
    // converting it here would put a second unit rule in the test.
    const probe = document.createElement("div");
    probe.style.inlineSize = "var(--dot-size)";
    document.body.append(probe);
    const size = probe.getBoundingClientRect().width;
    probe.remove();
    expect(size).toBeGreaterThan(0);

    const tally = { total: 1, working: 1, waiting: 0, input: 0 };
    for (const state of ["working", "waiting", "input"] as const) {
      setTabRunStatus(id, state, tally);
      const box = mark?.getBoundingClientRect();
      expect(box?.width, `${state} must paint at --dot-size`).toBeCloseTo(size, 2);
      expect(box?.height, `${state} must be square, not a rectangle`).toBeCloseTo(size, 2);
    }

    // And the same diameter as the dot it sits beside, which is what makes a glance
    // down the cluster compare states rather than sizes.
    expect(mark?.getBoundingClientRect().width).toBeCloseTo(
      dot?.getBoundingClientRect().width ?? 0,
      2,
    );
  });

  it("paints a square where the dot paints a circle, resolved not transcribed", async () => {
    // The cross-mark separator, measured through the cascade rather than read off
    // the rule: the mark's radius is a calc over --dot-size, so the number a reader
    // sees is the resolved one and a token retune moves it. Half the diameter IS a
    // circle, so the assertion is the gap between the two.
    const { setTabRunStatus } = await import("./tabs.js");
    const id = await openSubject("chat", "c1");
    await paint();
    // A STATE first, because the mark takes no box without one (12-tabs.css): it is
    // revealed by `[data-status]`, so an unpainted mark measures 0 and every ratio
    // below would divide by it.
    setTabRunStatus(id, "working", { total: 1, working: 1, waiting: 0, input: 0 });
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    const mark = row?.querySelector<HTMLElement>(".tab-run-dot");
    const dot = row?.querySelector<HTMLElement>(".tab-status-dot");
    expect(mark, "a chat row carries the workflow mark").not.toBeNull();
    const size = mark?.getBoundingClientRect().width ?? 0;
    // Chromium reports a PERCENTAGE radius as the percentage and a calc as a used
    // length, so the two spellings have to be normalised before they can be
    // compared: the dot's is 50%, the mark's is px.
    const cornerPx = (node: HTMLElement): number => {
      const v = getComputedStyle(node).borderTopLeftRadius;
      return v.endsWith("%") ? (size * Number.parseFloat(v)) / 100 : Number.parseFloat(v);
    };
    const radius = cornerPx(mark as HTMLElement);
    expect(radius).toBeGreaterThan(0);
    expect(radius).toBeLessThan(size / 2);
    // Half the box IS the circle, and that is what the dot resolves to — the state
    // the square is being told apart from.
    expect(cornerPx(dot as HTMLElement)).toBeCloseTo(size / 2, 1);
    // Flat sides at least a third of the mark's width on all four edges, which is
    // the ink a reader compares against a curve at 8px.
    expect(size - 2 * radius).toBeGreaterThanOrEqual(size / 3);
  });
});

// ---------------------------------------------------------------------------
// 4e. The same MARK on a RUN SUB-TAB's own activity dot.
//
// A run's row carries NO `.tab-run-dot` — `createTabEl` appends one for the chat
// kind only — so the square that means "a workflow run" had to reach the one mark
// such a row does carry, its activity dot. That makes the geometry a CASCADE
// question rather than a rule read, twice over: the kind-scoped rule (0,4,0)
// contests the base `border-radius: 50%` AND outranks the `failed` diamond's own
// corner, and both silhouettes resolve one shared token. So it is measured off
// real boxes with the bundle mounted, the way 4d measures the mark's.
//
// The SILHOUETTE was the first half and the TREATMENT is the second: all three
// states the mark has are the mark's here, fill and band and beat included, because
// the two elements report one run and the strip may not answer with two looks. The
// two OUTCOMES stay the dot's, which is what re-spends the channels on this row
// rather than losing one — see the two cases at the end of this section.
// ---------------------------------------------------------------------------

describe("a run sub-tab's dot takes the workflow mark's square", () => {
  let sheet: HTMLStyleElement;

  beforeAll(() => {
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(async () => {
    await resetProjection();
  });

  /** Chromium reports a PERCENTAGE radius as the percentage and a `calc` as a used
   *  length, so the two spellings have to be normalised against the element's own
   *  box before they can be compared: the dot's circle is 50%, the square is px. */
  function cornerPx(node: HTMLElement): number {
    const v = getComputedStyle(node).borderTopLeftRadius;
    const box = node.getBoundingClientRect().width;
    return v.endsWith("%") ? (box * Number.parseFloat(v)) / 100 : Number.parseFloat(v);
  }

  function dotOf(row: HTMLElement): HTMLElement {
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    return dot;
  }

  function rowOf(id: string): HTMLElement {
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    if (row === null) {
      throw new Error("the row did not render");
    }
    return row;
  }

  /** A run sub-tab of the chat that launched it, opened exactly as `openRunView`
   *  opens one, with its parent's id beside it. */
  async function runSubTab(ref = "wf_1"): Promise<{ id: string; parent: string }> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const parent = await openSubject("chat", "c1");
    await openTab({ kind: "run", ref, parent, owns: false });
    await paint();
    return { id: tabIdFor("run", ref), parent };
  }

  it("paints a rounded square where a chat's dot paints a circle", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, parent } = await runSubTab();
    // A STATE first: the dot is revealed by `[data-status]`, and a run row's
    // reserved slot is `visibility: hidden` until one lands.
    setTabStatus(id, "working");
    setTabStatus(parent, "working");
    const dot = dotOf(rowOf(id));
    const size = dot.getBoundingClientRect().width;
    expect(size).toBeGreaterThan(0);

    // Still square, so the radius is the only thing that moved.
    expect(dot.getBoundingClientRect().height).toBeCloseTo(size, 2);
    const radius = cornerPx(dot);
    expect(radius).toBeGreaterThan(0);
    expect(radius).toBeLessThan(size / 2);
    // Flat sides at least a third of the width on all four edges, the bound the
    // mark's own geometry case uses: the ink a reader compares against a curve.
    expect(size - 2 * radius).toBeGreaterThanOrEqual(size / 3);

    // The launching CHAT's dot is untouched, and half the box IS the circle — the
    // state the square is being told apart from.
    const chatDot = dotOf(rowOf(parent));
    expect(cornerPx(chatDot)).toBeCloseTo(chatDot.getBoundingClientRect().width / 2, 1);
  });

  it("resolves the same radius as the workflow mark on its parent's row", async () => {
    // The consistency claim the change is about, measured rather than transcribed:
    // one token, two elements, so a retune moves both or fails here.
    const { setTabStatus, setTabRunStatus } = await import("./tabs.js");
    const { id, parent } = await runSubTab();
    setTabStatus(id, "working");
    // The mark takes no box without a state of its own.
    setTabRunStatus(parent, "working", { total: 1, working: 1, waiting: 0, input: 0 });
    const mark = rowOf(parent).querySelector<HTMLElement>(".tab-run-dot");
    expect(mark, "a chat row carries the workflow mark").not.toBeNull();

    expect(Math.abs(cornerPx(dotOf(rowOf(id))) - cornerPx(mark as HTMLElement))).toBeLessThan(0.5);
  });

  it("paints every live state exactly as the mark on its parent's row does", async () => {
    // THE WHOLE CONVERGENCE, state by state, and the reported defect is one of its
    // three rows: the square was there and `working` was FILLED, so a solid violet
    // square meaning "a conversation is mid-turn" sat one row under a hollow violet
    // ring meaning the run was going. `input` was the same defect one state over,
    // and `waiting` differed on band (the dot's 1.5px hairline against the mark's
    // 1px). Fill, band, halo and ink are all channels in this file, so a mark that
    // answers any of them differently is the strip contradicting itself about ONE run.
    //
    // Property by property rather than "is it hollow", because hollow alone passes on
    // a ring of the wrong weight, the wrong hue or the wrong corner. `transition` is
    // deliberately not compared: the mark SPAWNS into its row when a run starts and
    // the dot does not, which is a lifecycle difference rather than a look.
    const { setTabStatus, setTabRunStatus } = await import("./tabs.js");
    const { id, parent } = await runSubTab();

    for (const state of ["working", "waiting", "input"] as const) {
      setTabStatus(id, state);
      // The mark takes no box without a state of its own, and its tally is what the
      // announced phrase reads — one run in the state under test.
      setTabRunStatus(parent, state, {
        total: 1,
        working: state === "working" ? 1 : 0,
        waiting: state === "waiting" ? 1 : 0,
        input: state === "input" ? 1 : 0,
      });
      const dot = dotOf(rowOf(id));
      const mark = rowOf(parent).querySelector<HTMLElement>(".tab-run-dot");
      expect(mark, "a chat row carries the workflow mark").not.toBeNull();

      const ring = getComputedStyle(dot);
      const twin = getComputedStyle(mark as HTMLElement);
      // A RING in all three: the ink is in the band, and no fill is left to read as
      // the dot's disc.
      expect(ring.backgroundColor, `${state} fill`).toBe("rgba(0, 0, 0, 0)");
      expect(ring.backgroundImage, `${state} fill`).toBe("none");
      expect(Number.parseFloat(ring.borderTopWidth), `${state} band`).toBeGreaterThan(0);

      for (const prop of [
        "backgroundColor",
        "backgroundImage",
        "borderTopWidth",
        "borderTopStyle",
        "borderTopColor",
        "borderTopLeftRadius",
        "boxShadow",
      ] as const) {
        expect(ring[prop], `${state} ${prop}`).toBe(twin[prop]);
      }
      // The BOX too, so the band paints inside the disc's own footprint.
      expect(dot.getBoundingClientRect().width, `${state} size`).toBeCloseTo(
        (mark as HTMLElement).getBoundingClientRect().width,
        2,
      );
    }
  });

  it("keeps the two wants-you states apart on band, at the exec column's ratio", async () => {
    // The cost of converging `input`: it was the one state fill separated from
    // `waiting` on this row, and both are hollow now, so BAND is the whole separator
    // and the RATIO is the assertion rather than the inequality. Taking the mark's
    // 1px/2px is what buys 2:1; keeping the dot's 1.5px hairline under a 2px ring
    // would have been 1.33:1, a third of a pixel of ink at 8px.
    const { setTabStatus } = await import("./tabs.js");
    const { id } = await runSubTab();
    const dot = dotOf(rowOf(id));

    setTabStatus(id, "waiting");
    const hairline = Number.parseFloat(getComputedStyle(dot).borderTopWidth);
    setTabStatus(id, "input");
    const heavy = Number.parseFloat(getComputedStyle(dot).borderTopWidth);
    expect(hairline).toBeGreaterThan(0);
    expect(heavy / hairline).toBeGreaterThanOrEqual(2);
  });

  it("keeps the beat, and closes the ring's hole with it", async () => {
    // The motion is the SQUARE MARKS' shared overlay rule, which this row's selector
    // joins — a chat row's disc animates itself and carries no pseudo at all.
    const { setTabStatus } = await import("./tabs.js");
    const { id, parent } = await runSubTab();
    setTabStatus(id, "working");
    setTabStatus(parent, "working");

    const dot = dotOf(rowOf(id));
    const glow = getComputedStyle(dot, "::before");
    expect(glow.content).toBe('""');
    expect(glow.animationName).toBe("vk-mark-close");
    expect(getComputedStyle(dot).overflow).toBe("hidden");

    // The launching chat's own dot is the control, in the same state: a solid disc
    // that beats on ITSELF and builds no overlay, because it has no hole to close.
    const chatDot = dotOf(rowOf(parent));
    expect(getComputedStyle(chatDot).backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
    expect(getComputedStyle(chatDot).animationName).toBe("vk-dot-beat");
    expect(getComputedStyle(chatDot, "::before").content).toBe("none");
  });

  it("spends fill on finished-or-not, which is where the convergence stops", async () => {
    // The channels are RE-SPENT on this row rather than reduced, and this is the
    // trade: `done` and `failed` are states the mark has no vocabulary for at all
    // (it withdraws when a run ends), so they keep the dot's disc and fill separates
    // the three live states from the two settled ones. A chat row spends fill on the
    // wants-you pair instead, because it has no outcome to tell them from; each row
    // is internally consistent, which is the population WCAG 1.4.1 is judged over.
    const { setTabStatus } = await import("./tabs.js");
    const { id } = await runSubTab();
    const dot = dotOf(rowOf(id));

    for (const state of ["done", "failed"] as const) {
      setTabStatus(id, state);
      expect(getComputedStyle(dot).backgroundColor, `${state} lost its fill`).not.toBe(
        "rgba(0, 0, 0, 0)",
      );
    }

    for (const state of ["working", "waiting", "input"] as const) {
      setTabStatus(id, state);
      expect(getComputedStyle(dot).backgroundColor, `${state} is not a ring`).toBe(
        "rgba(0, 0, 0, 0)",
      );
    }

    // And the halo goes on meaning exactly one thing across both rows: a person is
    // owed something. `working` is the live state that needs nothing, so it carries
    // none, which is what separates it from the two that do.
    for (const state of ["waiting", "input"] as const) {
      setTabStatus(id, state);
      expect(getComputedStyle(dot).boxShadow, `${state} lost its halo`).not.toBe("none");
    }
    setTabStatus(id, "working");
    expect(getComputedStyle(dot).boxShadow).toBe("none");
  });

  it("leaves a subagent sub-tab's dot a full disc", async () => {
    // The untouched-kind control. Both kinds nest under a chat and both write
    // through the same reserved slot, so the KIND is the whole of what separates
    // them — a rule keyed on anything else would take this row with it.
    const { openTab, tabIdFor, setTabStatus } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const parent = await openSubject("chat", "c1");
    const ref = subagentRef("c1", "task-1");
    await openTab({ kind: "subagent", ref, parent, owns: false });
    await paint();
    setTabStatus(tabIdFor("subagent", ref), "working");

    const dot = dotOf(rowOf(tabIdFor("subagent", ref)));
    const size = dot.getBoundingClientRect().width;
    expect(size).toBeGreaterThan(0);
    expect(cornerPx(dot)).toBeCloseTo(size / 2, 1);
  });

  it("keeps failed's own rotated square rather than re-rounding it", async () => {
    // The exclusion, and it is what keeps `done` and `failed` separable by SHAPE on
    // a run row (WCAG 1.4.1): the diamond is a smaller box with its own corner and
    // a rotation, and the square rule outranks it on specificity.
    const { setTabStatus } = await import("./tabs.js");
    const { id } = await runSubTab();
    setTabStatus(id, "working");
    const dot = dotOf(rowOf(id));
    const size = dot.getBoundingClientRect().width;
    const square = cornerPx(dot);

    setTabStatus(id, "failed");
    // The LAYOUT box, not the client rect: a rotated square's rect is its DIAGONAL
    // (8.49px for a 6px mark), which is the whole reason the diamond is sized off
    // --dot-size-sm and would read here as the mark having grown.
    expect(dot.offsetWidth).toBeLessThan(size);
    expect(getComputedStyle(dot).transform).not.toBe("none");
    expect(cornerPx(dot)).toBeLessThan(square);
  });

  it("keeps the row's geometry across every state a run tab can take", async () => {
    // ONE run of the state vocabulary, TWO measurements, because one fact governs
    // both: the radius rule is state-INDEPENDENT apart from the `failed` exclusion,
    // and the reserved slot plus the diamond's margin correction hold under it — a
    // row whose label stepped sideways when its run failed would jump at the one
    // moment the reader is watching it. `runStatusFor` (store.ts) answers
    // "" | working | waiting | input | done | failed and never `idle`, so this is the
    // whole vocabulary; `""` is measurable because a sub-tab's dot RESERVES its slot
    // with no state written, so it keeps its box.
    const { setTabStatus } = await import("./tabs.js");
    const { id } = await runSubTab();
    const row = rowOf(id);
    const dot = dotOf(row);
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    const offset = (): number =>
      name.getBoundingClientRect().left - row.getBoundingClientRect().left;

    setTabStatus(id, "");
    const blank = offset();
    let square = 0;

    for (const state of ["", "working", "waiting", "input", "done"] as const) {
      setTabStatus(id, state);
      const size = dot.getBoundingClientRect().width;
      square = cornerPx(dot);
      expect(Math.abs(offset() - blank), `${state} moved the name`).toBeLessThan(0.5);
      expect(square, `${state} lost the square`).toBeLessThan(size / 2);
      expect(size - 2 * square, `${state} has no flat side`).toBeGreaterThanOrEqual(size / 3);
    }

    // The other side of the boundary, from the same run of states: the diamond keeps
    // its own smaller corner rather than the token this rule resolves, and the margin
    // correction keeps the name where the disc left it.
    setTabStatus(id, "failed");
    expect(Math.abs(offset() - blank), "failed moved the name").toBeLessThan(0.5);
    expect(cornerPx(dot)).toBeLessThan(square);
  });

  it("gives a PARENTLESS run's row the same mark", async () => {
    // The scope, and it is the KIND rather than the nesting: a manual or scheduled
    // run opens a top-level row, and it reports the same subject through the same
    // element, so the silhouette may not depend on whether the run happens to have a
    // launching chat. Its SLOT differs (that row leads with its kind glyph and
    // carries the dot in the trailing one) and its treatment does not.
    //
    // This reverses what the rule used to do — it was keyed on `.tab-child`, so such
    // a row kept the dot's circle — and the earlier note said the square was right by
    // subject and raised it rather than taking it.
    const { setTabStatus } = await import("./tabs.js");
    const id = await openSubject("run", "wf_2");
    await paint();
    setTabStatus(id, "working");

    const dot = dotOf(rowOf(id));
    const size = dot.getBoundingClientRect().width;
    expect(size).toBeGreaterThan(0);
    // A square, not the disc's half-box circle.
    expect(cornerPx(dot)).toBeGreaterThan(0);
    expect(cornerPx(dot)).toBeLessThan(size / 2);
    // And a RING, so the whole treatment travels rather than just the corner.
    expect(getComputedStyle(dot).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(Number.parseFloat(getComputedStyle(dot).borderTopWidth)).toBeGreaterThan(0);
  });

  it("holds a PARENTLESS row's trailing slot when the run fails", async () => {
    // The sub-tab case's twin, one position over, and the neighbours are SWAPPED:
    // there the dot leads and the name is what would move, here the dot is trailing
    // and `.tab-name` is the flex grower — so the name absorbs the 2px the smaller
    // diamond frees and the MARK is what steps sideways. Measured before the fix:
    // the mark's centre moved 1px right while the × stayed put, at the one moment a
    // reader is watching the row.
    //
    // `done` is the departure state rather than `working`, because both are settled
    // outcomes at --dot-size and the flip between them is the one this row makes on
    // its own; `failed` is the only state in the whole vocabulary with a different
    // box.
    const { setTabStatus } = await import("./tabs.js");
    const id = await openSubject("run", "wf_slot");
    await paint();
    const row = rowOf(id);
    const dot = dotOf(row);
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    const centre = (): number => {
      const r = dot.getBoundingClientRect();
      return r.left + r.width / 2 - row.getBoundingClientRect().left;
    };

    setTabStatus(id, "done");
    const disc = centre();
    const nameWidth = name.getBoundingClientRect().width;
    // The LAYOUT box is what the correction is derived from and what changes: a
    // rotated square's client rect is its diagonal, so the rect would read as the
    // mark having GROWN when its box shrank.
    const discBox = dot.offsetWidth;

    setTabStatus(id, "failed");
    expect(
      dot.offsetWidth,
      "failed must be the smaller box, or there is nothing to correct",
    ).toBeLessThan(discBox);
    expect(Math.abs(centre() - disc), "failed moved the mark").toBeLessThan(0.5);
    expect(name.getBoundingClientRect().width, "failed resized the name").toBeCloseTo(nameWidth, 1);
  });
});

// ---------------------------------------------------------------------------
// 6. The finished-turn latch is DELETED, subject and all.
//
// It existed because `agent_status === "completed"` is not a guaranteed signal, so
// a turn that ended without one fell to `idle` and "this chat finished" held only
// for the turns where the agent happened to say so. Under the entry log the
// verdict has ONE source — `last_turn_outcome`, written by the closer in the same
// header rewrite that appends the `turn_close` — so there is no client memory to
// latch, to clear, or to keep from being cleared by seeing it. `setTurnDone`,
// `clearTurnDone`, `applyLatch`, `relatchTurnVerdict` and the `turn_done` /
// `turn_failed` fields are gone with the four-latch precedence, and the section
// above pins what replaced them.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 5b. EVERY outcome the wire can send reaches the dot, and none reaches it as
// "nothing is happening here".
//
// This is the section symptom 2 was reported against. The latch's mapping was
// hand-written over the outcome values and `interrupted` fell into its default
// arm, so a turn stopped by a network error — which the transcript divider, the
// collapsed face, the footer glyph and the fold rule all treat as a failure —
// latched nothing, `tabStatusFor` fell through every rung to `idle`, and
// 12-tabs.css painted `idle` as a TRANSPARENT disc with a 1.5px ring. The user
// saw a chat with a clear inline error message and an empty circle beside it.
//
// So the table below is the taxonomy, not a sample: every outcome, the latch it
// sets, the dot state that follows, and — for the two that matter — the SHAPE the
// stylesheet paints, which is the observable that was actually wrong.
// ---------------------------------------------------------------------------

describe("every turn outcome reaches the tab dot", () => {
  const tabs = loadCSS("12-tabs.css");

  /** outcome -> the dot state a chat shows once that turn has ended.
   *
   *  Derived from `severityOf` in production; spelled out here on purpose, because
   *  a test that re-derived it through the same function it is checking would pass
   *  for any mapping at all. */
  const cases: [TurnOutcome, TabDotStatus][] = [
    // BROKEN. All three are failures and all three must say so.
    ["failed", "failed"],
    ["refused", "failed"],
    ["interrupted", "failed"],
    // CLEAN.
    ["completed", "done"],
    // STOPPED. Neither is a failure — a cancel is what the user asked for, and an
    // unmeasured stop reason says nothing about whether the work succeeded — but
    // neither may be `idle` either, because the hollow ring means the chat has NOT
    // INITIATED and both of these ran a turn. `done` is
    // the transport's "a turn finished here", which is what both of them are.
    ["cancelled", "done"],
    ["unknown", "done"],
  ];

  // The whole point of the table above, stated as its own assertion so it cannot
  // be weakened one row at a time: no ENDED turn paints the hollow ring.
  it("never paints the hollow ring for a turn that ended, whatever became of it", () => {
    for (const [outcome, want] of cases) {
      expect(want, `${outcome} must not fall to idle`).not.toBe("idle");
    }
  });

  beforeEach(() => {
    setSessions([]);
  });

  for (const [outcome, want] of cases) {
    it(`shows ${want} for a turn that ended ${outcome}`, () => {
      // Through the HEADER, which is the one door left: the verdict is the server's
      // statement about the newest finished turn, so a chat whose window was never
      // fetched is the ordinary case rather than a fallback and every row of this
      // table is that case. There is nothing client-side to re-derive.
      upsertHeader(headerWithOutcome("c1", outcome));
      expect(tabStatusFor(get("c1"))).toBe(want);
    });
  }

  it("grades every outcome the same way the dot above painted it", () => {
    // The table drove the header; this pins the shared grader underneath it, so a
    // change to `outcomeLatch` that the header path happens to survive still fails
    // here. `store.test.ts` owns the grader's own table including the two values
    // this one has no dot row for (`running`, and an absent outcome); what is
    // asserted here is that the two agree.
    for (const [outcome, want] of cases) {
      expect(outcomeLatch(outcome), `outcomeLatch(${outcome})`).toBe(
        want === "failed" ? "failed" : want === "done" ? "done" : "",
      );
    }
  });

  it("latches an interrupted turn as a failure, which is the reported defect", () => {
    // Kept as its own case rather than left to the table: this single mapping is
    // the whole of symptom 2, and a table row is easy to edit without noticing.
    expect(outcomeLatch("interrupted")).toBe("failed");
  });

  it("latches a discarded and an unreadable turn as DONE, which two later fixes rest on", () => {
    // Named for the same reason as the row above, because two changes now depend on
    // these exact two mappings and a table row is easy to edit without noticing.
    //
    // `cancelled` is what a model switch that threw a live turn away now concludes:
    // it must not paint red for a switch the reader asked for, and must not paint the
    // hollow ring, which means the chat has not initiated. `unknown` is what a turn
    // NOTHING closed now reads as, so it reaches ordinary turns rather than only
    // displaced fragments — a red dot there would invent a failure the wire never
    // reported.
    expect(outcomeLatch("cancelled")).toBe("done");
    expect(outcomeLatch("unknown")).toBe("done");
  });

  it("reads an ABSENT outcome on a later header as a CLEAR, back to the floor", () => {
    // The header is the AUTHORITY in both directions, so the field going away is a
    // real statement and not "no news" — which is what makes the hollow ring
    // reachable again for a record whose outcome the server no longer reports. The
    // old client-memory rule was the opposite: the latch was sticky, so nothing a
    // later read carried could return the row to the floor.
    upsertHeader(headerWithOutcome("c1", "failed"));
    expect(tabStatusFor(get("c1"))).toBe("failed");
    upsertHeader(headerWithOutcome("c1"));
    expect(tabStatusFor(get("c1"))).toBe("idle");
  });

  it("paints a failure as the red lozenge and never as the idle ring", () => {
    // The observable the user described. `failed` is a filled, rotated square with
    // a small radius; `idle` is a transparent disc with a hairline ring. A source
    // fact rather than a computed one, for the reason this file's header gives: the
    // test page links no app stylesheet, so there is no cascade to measure.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(failed.body).toMatch(/transform:\s*rotate\(45deg\)/u);
    expect(failed.body).toMatch(/background:\s*var\(--dot-color\)/u);
    expect(failed.body).not.toMatch(/background:\s*transparent/u);

    const idle = ruleContaining(tabs, '.tab-status-dot[data-status="idle"]', "top");
    expect(idle.body).toMatch(/background:\s*transparent/u);
    expect(idle.body).toMatch(/border:/u);
    // The two must not be one shape, or the fix above would be unobservable.
    expect(failed.body.trim()).not.toBe(idle.body.trim());
  });
});

// ---------------------------------------------------------------------------
// 7. A LIST REFETCH IS THE VERDICT'S OWN DOOR, and the client keeps nothing of
// its own to be erased.
//
// This section used to defend two client-only latches across `loadList`, because
// the server sent none of the client's projections and every field the rebuild did
// not carry over was silently reset. Under the entry log the verdict rides the
// HEADER, so the rebuild taking it is the mechanism rather than the hazard, and
// there is no carry-over rule, no rule-1 arbitration between a local latch and a
// header read, and no convergence order between the seed and the connect replay to
// reconcile. Every case below goes through the REAL store and the REAL loadList
// with no pre-existing session, which is what a full page reload, a brand-new
// browser session and a reconnect after hours all look like from here.
//
// `store-load.test.ts` owns the rebuild's own rule (the header replaces the
// outcome, and an absent one is a CLEAR); what this file adds is that the value
// reaches the DOT, which is the surface the bug was reported at: "if a turn is done
// after i close the window and come back, it will be an empty circle".
// ---------------------------------------------------------------------------

describe("a list refetch paints the verdict the header carries", () => {
  const header = (id: string): ChatHeader => headerWithOutcome(id);

  beforeEach(() => {
    setSessions([]);
    mockApiGetTyped.mockReset();
  });

  const outcomeHeader = (id: string, outcome: TurnOutcome): ChatHeader =>
    headerWithOutcome(id, outcome);

  it("paints DONE for a finished turn on a chat it has never seen live", async () => {
    // The reported bug, asserted at the surface the user sees: "if a turn is done
    // (green dot in tab) after i close the window and come back, when i come back
    // it will be an empty circle". The empty circle is `idle`'s hollow ring.
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c-done", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c-done"))).toBe("done");
  });

  it("paints FAILED for a turn that failed or was refused", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({
      chats: [outcomeHeader("c-fail", "failed"), outcomeHeader("c-refuse", "refused")],
    });

    await loadList();
    expect(tabStatusFor(get("c-fail"))).toBe("failed");
    expect(tabStatusFor(get("c-refuse"))).toBe("failed");
  });

  it("stays IDLE only for the LEGACY record, never for a turn that was stopped", async () => {
    // The two halves of the ruling, side by side on one fetch. A cancelled turn
    // RAN, so the hollow ring — which means the chat has not initiated — would be
    // wrong for it. A record written before the outcome existed carries nothing to
    // read, and that is the case the ruling exempts: no state to pull.
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({
      chats: [outcomeHeader("c-cancel", "cancelled"), header("c-legacy")],
    });

    await loadList();
    expect(tabStatusFor(get("c-cancel"))).toBe("done");
    expect(tabStatusFor(get("c-legacy"))).toBe("idle");
  });

  it("keeps a mid-turn reload WORKING, because the WINDOW travels and the verdict does not", async () => {
    // The inverse of the reported bug, and the one thing the header cannot settle: a
    // live turn invalidates every prior verdict, because the header's outcome
    // describes the turn BEFORE this one. What defends it is that the rebuild carries
    // the resident window over — a turn with no `turn_close` is still there after the
    // refetch — and the dot derives liveness from that window, so `working` outranks
    // the `completed` the same header carries.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1" })]);
    openTurnIn("c1", "t1");
    mockApiGetTyped.mockResolvedValue({ chats: [headerWithOutcome("c1", "completed")] });

    await loadList();
    expect(get("c1")?.turns.size, "the window was dropped by the rebuild").toBe(1);
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  it("lets a waiting_on_user chat keep saying so over a header's done", async () => {
    // `waiting` outranks `done` in tabStatusFor, and the status is one of the few
    // client-held projections the rebuild still carries over — so a header's own
    // verdict must not bury the one state whose whole meaning is that a person still
    // owes an answer.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", agent_status: "waiting_on_user" })]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("waiting");
  });
});

// ---------------------------------------------------------------------------
// 8. An abandoned ask stops claiming the chat needs a decision.
//
// `input` outranks every other state, so a queue entry left behind after its
// request died marked the chat as blocked indefinitely. The queue lives in
// decision-dock.ts and had no production caller for its own drop function at all.
// ---------------------------------------------------------------------------

describe("an abandoned ask does not keep a chat in input", () => {
  const ask = (chatID: string, requestID: number, runID: string) => ({
    kind: "permission" as const,
    chatID,
    runID,
    requestID,
    payload: { request_id: requestID, title: "run a command", options: [] } as unknown as
      PermissionNeededPayload | PermissionNeededPayload,
    submit: vi.fn(),
  });

  beforeEach(async () => {
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    setSessions([session({ id: "c1" })]);
  });

  it("drops a turn's ask when the turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions } =
      await import("./decision-dock.js");
    pushDecision(ask("c1", 1, ""));
    expect(tabStatusFor(get("c1"), hasPendingDecision("c1"))).toBe("input");

    // What handlers/turn.ts now runs on turn_closed. Every ask BLOCKS its turn, so
    // a turn that has ended is not waiting on one: it was answered (already
    // spliced) or abandoned when the turn was cancelled, and cmdCancel clears the
    // server's own pending set — the card left here could never be answered.
    dropTurnDecisions("c1");
    expect(tabStatusFor(get("c1"), hasPendingDecision("c1"))).toBe("idle");
  });

  it("leaves a workflow run's ask alone when the launching turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions, dropDecisions } =
      await import("./decision-dock.js");
    // An agent-launched run is parented on the calling chat's session and its asks
    // are keyed under that chat's id, but it OUTLIVES the launching turn (a goal
    // run ends its turn immediately and then runs). Dropping these would strand
    // the run waiting for an answer no surface offers — the exact failure the
    // dock's queue was built to end.
    pushDecision(ask("c1", 2, "run-7"));
    dropTurnDecisions("c1");
    expect(hasPendingDecision("c1")).toBe(true);

    // The chat going away is different: close_chat and delete_chat cancel the
    // chat's runs server-side, and there is no surface left to answer on.
    dropDecisions("c1");
    expect(hasPendingDecision("c1")).toBe(false);
  });

  it("keeps a turn's ask on one chat when another chat's turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions } =
      await import("./decision-dock.js");
    pushDecision(ask("c1", 3, ""));
    dropTurnDecisions("c2");
    expect(hasPendingDecision("c1")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 9. A run's wait ending releases the LAUNCHING chat's dot.
//
// A chat-parented run's ask is filed under the LAUNCHING CHAT's queue key:
// handlers/run.ts takes the chat id off the SSE envelope, and for such a run that
// is the conversation that started it. So `input` lands on the PARENT tab's dot
// while a step is parked, which is the correct half of the reported behaviour.
//
// The run's own sub-tab reads a DIFFERENT predicate over the same map —
// `runPendingAsks` scans every queue for the run's own id — so the two surfaces
// can disagree about whether the wait is over, and that seam is where the reported
// failure lives: the sub-tab recovers and the parent stays amber for the life of
// the page. Every case here therefore asserts BOTH surfaces, because a parent
// stuck beside a recovered sub-tab is a different defect from nothing clearing at
// all.
//
// The state to come back to is `done`, not `idle`: the launching turn ended long
// before the run did (`run_workflow` returns as soon as the run is created), so a
// dot stuck on `input` cannot be mistaken for the hollow-ring floor.
// ---------------------------------------------------------------------------

describe("a run's wait ending releases the launching chat's dot", () => {
  const PARENT = "c-parent";
  const RUN = "wf-1";

  function runInput(over: Partial<RunInputNeededPayload> = {}): RunInputNeededPayload {
    return {
      workflow_id: RUN,
      ask_id: "ask-1",
      node_id: "review",
      step_session_id: "sess-1",
      agent_name: "reviewer",
      question: "Ship it?",
      asked_at: "2026-09-04T10:00:00Z",
      ...over,
    };
  }

  /** The step's question, filed exactly as `handlers/run.ts` files it for a
   *  chat-parented run: the LAUNCHING chat's id as the queue key, the run stamped
   *  on the decision. */
  function runAsk(over: Partial<RunInputNeededPayload> = {}) {
    const payload = runInput(over);
    return {
      kind: "run_input" as const,
      chatID: PARENT,
      runID: payload.workflow_id,
      askID: payload.ask_id,
      payload,
      submit: vi.fn(),
    };
  }

  /** A step's `_kiro/userInput` question, which reaches the client through the
   *  OTHER door (`handlers/turn.ts`) with the run stamped on it by the
   *  step-session registry. Same queue key, request-shaped identity. */
  function stepQuestion(requestID: number) {
    return {
      kind: "user_input" as const,
      chatID: PARENT,
      runID: RUN,
      requestID,
      payload: { question: "Which branch?", request_id: requestID, run_id: RUN, node_id: "review" },
      submit: vi.fn(),
    };
  }

  /** The run tab's own dock. Copied from `decision-dock.test.ts` rather than
   *  exporting `settle`: answering through a real click is what proves the entry
   *  the sub-tab's card is built from is the one sitting in the PARENT's queue. */
  async function mountRunHost(): Promise<HTMLElement> {
    const { mountRunDecisionDock } = await import("./decision-dock.js");
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, () => RUN);
    return el;
  }

  /** Answer the card on screen in the run's dock. `:scope >` skips an answered
   *  card still on screen for the length of its phase. */
  function answerInRunHost(hostEl: HTMLElement, text: string): void {
    const card = hostEl.querySelector<HTMLElement>(":scope > .dock-card");
    const box = card?.querySelector<HTMLTextAreaElement>(".dock-ask-text") ?? null;
    if (box !== null) {
      box.value = text;
    }
    [...(card?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
      .find((b) => b.textContent === "Send answer")
      ?.click();
  }

  beforeEach(async () => {
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    document.body.replaceChildren();
    // The launching turn ended when the run was created — `run_workflow` returns as
    // soon as the run exists — so this is the state the parent has to come back to,
    // and it is the header's own verdict rather than anything this client latched.
    setSessions([settledChat(PARENT)]);
  });

  it("clears the parent when the sub-tab answers the step's question", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks } = await import("./decision-dock.js");
    const runHost = await mountRunHost();
    pushDecision(runAsk());

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
    expect(runPendingAsks(RUN).count).toBe(1);

    answerInRunHost(runHost, "yes, ship it");

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears the parent when another surface settled the ask", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, collapseSettledRunInput } =
      await import("./decision-dock.js");
    pushDecision(runAsk());
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // `run_input_settled`: a second device answered, so every surface still
    // showing the card has to retire it.
    collapseSettledRunInput(RUN, "ask-1", "user");

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears the parent when the run ENDS still parked on the ask", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropRunAsks } =
      await import("./decision-dock.js");
    pushDecision(runAsk());
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // A terminal run cannot still be waiting on a person, and `dropTurnDecisions`
    // exempts a run-scoped ask on purpose, so the launching turn's own end cannot
    // reach it. Nothing dropped it, so the parent's dot sat on `input` for the life
    // of the page.
    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears a step's user-input question when the run ends too", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // The sharpest instance of the same hole: this ask is REQUEST-shaped, so
    // `collapseSettledRunInput` cannot name it, and it carries a `runID`, so
    // `dropTurnDecisions` deliberately leaves it. Removal has to ask the adder's
    // own question — "this run's ask, wherever it is filed".
    pushDecision(stepQuestion(1));
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    dropRunAsks(RUN);

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("leaves another chat's own ask alone when a run ends", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // The run-scoped sweep is keyed on the RUN, so a plain chat ask sharing the
    // launching chat's queue is not its business — that one still blocks a turn and
    // still has a card to answer it with.
    pushDecision(stepQuestion(1));
    pushDecision({ ...stepQuestion(2), runID: "" });

    dropRunAsks(RUN);

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });

  it("leaves a SIBLING run's ask alone when one of the two ends", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropRunAsks } =
      await import("./decision-dock.js");
    // Two runs launched from one chat share that chat's queue key, so the sweep
    // has to separate them by RUN rather than by queue. A survivor carrying a real
    // run id is what pins that: an over-broad match on a sibling run and a match
    // on every ask in the queue are different defects, and only this case can
    // fail on the first one.
    pushDecision(runAsk());
    pushDecision(runAsk({ workflow_id: "wf-2", ask_id: "ask-2" }));

    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(runPendingAsks("wf-2").count).toBe(1);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });

  // The reported symptom, in one assertion: the sub-tab reads as answered and the
  // PARENT stays amber. What holds it is a step's question whose `run_id` arrived
  // EMPTY — the registry had not seen its sub-session — which puts it outside every
  // run-scoped remover while still lighting the launching chat's dot.
  //
  // ITS WARRANT IS NOT A RED CHECK, and it must not be read as one: `dropRunAsks`
  // is keyed on `runID` and this ask has none, so no change to that predicate can
  // make the `"input"` assertion fail. What it guards is the OVER-BROAD fix —
  // widening the run sweep to take run-orphans indiscriminately, which is the same
  // change "leaves another chat's own ask alone when a run ends" below refuses from
  // the other side. The trigger that DOES clear it is red-checked in
  // `handlers/run.test.ts`, where the sweep's four gates live.
  it("holds the parent on input for a step question that carries NO run id", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropTurnDecisions } =
      await import("./decision-dock.js");
    const runHost = await mountRunHost();
    // The orphan, and the run's own answerable ask, both under the launching chat.
    pushDecision({ ...stepQuestion(1), runID: "" });
    pushDecision(runAsk());
    expect(runPendingAsks(RUN).count).toBe(1);

    answerInRunHost(runHost, "yes, ship it");

    // Every run surface goes quiet — the sub-tab's dock, the run card's `needs
    // input`, the exec page's alert all read this count — while the parent's dot
    // is still held by an ask no run surface can see.
    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // The turn-scoped sweep is the one predicate that CAN name it: it keeps only
    // asks carrying a non-empty runID.
    dropTurnDecisions(PARENT);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("refuses an empty run id rather than sweeping every chat's ask", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // `runID` is empty on every ordinary chat ask and the id arrives off the wire,
    // so an empty argument would match the whole store rather than one run.
    pushDecision({ ...stepQuestion(1), runID: "" });

    dropRunAsks("");

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });
});

// ---------------------------------------------------------------------------
// 8b. The REAL dock repaints a REAL row's dot.
//
// The gap this closes is a seam between two suites, each of which fakes the half
// the other runs for real. Section 8 above drives the real dock and asserts through
// `tabStatusFor` DIRECTLY, so it proves the QUEUE empties and never that a row
// repaints. `chat-tab-strip.test.ts` drives the real row effect and the real
// reactive graph, but against a signal-backed FAKE dock and a mocked
// `setTabStatus`, so it proves the subscription TOPOLOGY and never that the real
// predicate participates in it or that a `data-status` attribute moves.
//
// So the one link neither suite covers is the real `hasPendingDecision` reading
// `queueVersion.value` rather than `.peek()` — a one-character change that would
// leave both suites green and every background chat's dot frozen. This drives the
// real dock, a real `effect`, the real `setTabStatus` and a real row built through
// the real projection, and reads the attribute the browser paints from.
//
// The effect body is chat.ts's `chatRowEffect` minus its name and tooltip writers,
// which is what keeps this case honest without importing chat.ts: doing that needs
// 13 module mocks, three of which (`bus`, `composer-state`, `roles`) are reached by
// the real `tabs.ts` and `tab-materialize.ts` this suite depends on — measured, it
// breaks 30 of the 80 tests here. The last case pins the replica against chat.ts's
// own source instead, which is this suite's existing idiom for a chat.ts fact.
// ---------------------------------------------------------------------------

describe("the dock's own signal repaints the launching chat's row", () => {
  const PARENT = "c-parent";
  const RUN = "wf-1";

  function statusOf(tabID: string): string | null {
    return (
      document
        .querySelector(`[data-tab-id="${tabID}"] .tab-status-dot`)
        ?.getAttribute("data-status") ?? null
    );
  }

  /** The dot half of chat.ts's row effect: the dock read IS the subscription. */
  async function installRowEffect(chatID: string, tabID: string): Promise<() => void> {
    const { effect } = await import("@cplieger/reactive");
    const { hasPendingDecision } = await import("./decision-dock.js");
    const { setTabStatus } = await import("./tabs.js");
    return effect(() => {
      setTabStatus(tabID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));
    });
  }

  it("moves data-status when an ORPHANED ask arrives and when it is swept", async () => {
    await resetProjection();
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    // The launching turn ended when the run was created, so this is the state the
    // row starts in and the one it has to come back to.
    setSessions([settledChat(PARENT)]);

    const tabID = await openSubject("chat", PARENT);
    const dispose = await installRowEffect(PARENT, tabID);
    await paint();
    expect(statusOf(tabID)).toBe("done");

    // A step's question whose run_id arrived EMPTY: filed under the launching
    // chat, invisible to every run-scoped surface, and this is the row it holds.
    dock.pushDecision({
      kind: "user_input",
      chatID: PARENT,
      runID: "",
      requestID: 1,
      payload: { question: "Which branch?", request_id: 1 },
      submit: vi.fn(),
    });
    await paint();
    expect(dock.runPendingAsks(RUN).count).toBe(0);
    expect(statusOf(tabID)).toBe("input");

    dock.dropTurnDecisions(PARENT);
    await paint();
    expect(statusOf(tabID)).toBe("done");
    dispose();
  });

  it("reads the same two inputs chat.ts's own row effect reads", () => {
    // The replica above stands in for `chatRowEffect`, so it is only worth what
    // production still doing the same thing is worth. Both reads are unconditional
    // and ahead of every early return in chat.ts, which is what subscribes a
    // BACKGROUND chat's row to a decision arriving on it.
    expect(chatSrc).toContain("hasPendingDecision(chatID)");
    expect(chatSrc).toContain("tabStatusFor(s, pendingAsk)");
    expect(chatSrc).toContain("setTabStatus(tabID,");
  });
});

// ---------------------------------------------------------------------------
// 10. A row that is CREATED knows what it should show.
//
// The dot used to live only in the DOM and `setTabStatus` wrote only to the live
// node, so every path that built a row without a following state change showed
// the seeded `idle` whatever the chat was doing. Two such paths, both ordinary:
// the boot restore populates sessions BEFORE opening their tabs (so the store
// effect has already run by the time the rows exist, and nothing makes it run
// again), and `promoteTab` discards and rebuilds a row on purpose.
// ---------------------------------------------------------------------------

describe("a row built later paints the state its chat is in", () => {
  beforeEach(async () => {
    await resetProjection();
  });

  function statusOf(id: string): string | null {
    return (
      document
        .querySelector(`[data-tab-id="${id}"] .tab-status-dot`)
        ?.getAttribute("data-status") ?? null
    );
  }

  it("paints the seed the factory derived, not the idle floor", async () => {
    // What the factory derives from the chat this client already holds, through the
    // registered `dot` hook. This is the boot restore: the collection is adopted
    // before any dot write, so the row has to be BUILT with the right state.
    seededDots.set("c1", "working");
    const id = await openSubject("chat", "c1");
    await paint();
    expect(statusOf(id)).toBe("working");
  });

  it("paints a status written before its row existed", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const id = await openSubject("chat", "c1", { activate: false });
    // renderDOM is rAF-deferred, so this lands while the row does not exist —
    // which is the ordering every state change that arrives during boot has.
    setTabStatus(id, "failed");
    await paint();
    expect(statusOf(id)).toBe("failed");
  });

  it("keeps the dot through a rebuild, which a pin change forces", async () => {
    // `promoteTab` is gone with the reparent it performed: `TabSubject.Parent` is
    // set at open and never reassigned, which is what makes a parent cycle
    // unrepresentable. The rebuild it exercised is still reachable — dropping the
    // node and making the store emit is what any re-render after a lost row does —
    // so the property survives with the mechanism that remains.
    const { openTab, setTabStatus, setTabPinned, tabIdFor } = await import("./tabs.js");
    const parent = await openSubject("chat", "parent");
    await openTab({ kind: "chat", ref: "c2", parent });
    const child = tabIdFor("chat", "c2");
    await paint();
    setTabStatus(child, "input");
    expect(statusOf(child)).toBe("input");

    document.querySelector(`[data-tab-id="${child}"]`)?.remove();
    await setTabPinned(parent, true);
    await paint();
    expect(statusOf(child)).toBe("input");
  });

  it("still floors a chat row at idle and leaves other kinds blank", async () => {
    const chat = await openSubject("chat", "c3");
    const editor = await openSubject("editor", "a.ts");
    await paint();
    expect(statusOf(chat)).toBe("idle");
    // An editor tab with nothing unsaved has no state to show, and `[data-status]`
    // is the CSS reveal condition, so the attribute must be ABSENT rather than "".
    expect(statusOf(editor)).toBeNull();
  });

  it("carries the editor's dirty mark through a rebuild, in both directions", async () => {
    const { setTabDirty, setTabPinned } = await import("./tabs.js");
    const id = await openSubject("editor", "b.ts");
    await paint();

    /** Rebuild the row — drop the node, then make the store emit — so createTabEl
     *  runs again with no write behind it. A pin is a `changed` upsert, which is
     *  the cheapest real emit available. */
    async function rebuild(pinned: boolean): Promise<void> {
      document.querySelector(`[data-tab-id="${id}"]`)?.remove();
      await setTabPinned(id, pinned);
      await paint();
    }

    setTabDirty(id, true);
    expect(statusOf(id)).toBe("dirty");
    // An unsaved file must still read as unsaved after its row is rebuilt: the
    // editor's mark rides the same element and the same one attribute as a chat's
    // activity, so it needs the same spec record behind it.
    await rebuild(true);
    expect(statusOf(id)).toBe("dirty");

    setTabDirty(id, false);
    // And a SAVED file must not come back dirty, which is what recording "" as an
    // ABSENT field rather than an empty string buys.
    await rebuild(false);
    expect(statusOf(id)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 11. The dot is live state, so it must never be persisted.
//
// TabViewSpec feeds the persistence subscriber, and a dot restored from a previous
// process would be a claim about a turn that ended before the page loaded.
// ---------------------------------------------------------------------------

// The dot is LIVE state, and the projection is what makes that structural rather
// than a rule someone has to remember: a `TabSubject` has no dot field at all, so
// there is nothing for a dot write to travel on. `dotVersion` is the other half —
// a dot write does not `emit()`, so it queues no re-render.
describe("the dot is local and costs the projection nothing", () => {
  beforeEach(async () => {
    await resetProjection();
  });

  it("puts no dot on the wire, whatever the row is showing", async () => {
    const { setTabStatus, setTabPinned } = await import("./tabs.js");
    const { tabServer } = await import("./__test-helpers__/tabs-server.js");
    const id = await openSubject("chat", "c1");
    await setTabPinned(id, true);
    setTabStatus(id, "failed");
    await paint();

    // Every command this device sent, in full. A dot cannot reach the collection
    // because no payload has a field for it — which is stronger than a rule about
    // what a writer must not send.
    const payloads = JSON.stringify(tabServer.sent());
    expect(tabServer.sent().length).toBeGreaterThan(0);
    expect(payloads).not.toContain("failed");
    expect(payloads).not.toContain("dotStatus");
    // And the collection itself holds no trace of it.
    expect(JSON.stringify(tabServer.subjects())).not.toContain("failed");
  });

  it("sends nothing at all for a dot change", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { tabServer } = await import("./__test-helpers__/tabs-server.js");
    const id = await openSubject("chat", "c1");
    await paint();
    const before = tabServer.sent().length;
    setTabStatus(id, "working");
    setTabStatus(id, "done");
    await paint();
    // A status write is a direct DOM paint plus a row field, and it deliberately
    // does not emit: emitting would put a full re-render on every streaming state
    // change, and a mutation would put a round trip there.
    expect(tabServer.sent()).toHaveLength(before);
  });
});

// ---------------------------------------------------------------------------
// 12. Two source facts about the leading slot and the motion vocabulary.
// ---------------------------------------------------------------------------

describe("the leading dot slot is one width for every state", () => {
  const tabs = loadCSS("12-tabs.css");

  it("derives both slot margins from the tokens they measure, never a literal", () => {
    // Two derivations per rule and neither may be a number. The SLOT is the kind
    // glyph's, so it reads --icon-ui: that token is 1rem on a fine pointer and
    // 1.25rem on a coarse one, and the 0.875rem this used to spell was 2px short of
    // the glyph on every desktop and 6px on every touch device — a shared text
    // origin the rule claimed and did not have. The MARK is whichever dot token that
    // state paints, so the smaller diamond does not reserve less than every other
    // state, which would move a chat's name at the exact moment its status flipped
    // to or from failed.
    const generic = ruleContaining(tabs, ".tab-status-dot:first-child", "top");
    expect(generic.body).toContain("calc((var(--icon-ui) - var(--dot-size)) / 2)");

    const diamond = ruleContaining(
      tabs,
      '.tab-status-dot[data-status="failed"]:first-child',
      "top",
    );
    expect(diamond.body).toContain("calc((var(--icon-ui) - var(--dot-size-sm)) / 2)");
  });

  it("keeps the diamond on the small token, so the override stays paired", () => {
    // A rotated square's DIAGONAL is its footprint, so the diamond takes the
    // smaller token to sit level with the disc beside it (6px on the diagonal is
    // 8.49px against an 8px disc). If it ever returns to --dot-size the margin
    // override above becomes wrong rather than merely redundant, so the two are
    // asserted together.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(/inline-size:\s*var\(--dot-size-sm\)/.test(failed.body)).toBe(true);
  });

  it("re-derives the SUB-TAB diamond's slot from both dot tokens too", () => {
    // The same correction one position over, and the position is why it needs its
    // own rule: on a sub-tab the nesting arrow holds the 14px glyph slot, so the
    // dot only has to match its own siblings and the leading rule's arithmetic is
    // the wrong one. Both tokens, never a literal, so changing --dot-size cannot
    // leave the diamond and the disc reserving different widths — which is what
    // would move a delegate's name at the exact moment its status flipped to or
    // from failed.
    const diamond = ruleContaining(
      tabs,
      '.tab.tab-child .tab-status-dot[data-status="failed"]',
      "top",
    );
    expect(diamond.body).toContain("calc((var(--dot-size) - var(--dot-size-sm)) / 2)");
  });
});

// ---------------------------------------------------------------------------
// 13. The same claim in REAL LAYOUT, for a subagent sub-tab.
//
// The source facts above say the two margins are derived from the right tokens.
// They cannot say what the browser does with them, and the one thing that has to
// be true is that a delegate's name does not move when its dot changes state:
// `subagent-dots.ts` writes `working` while the delegate runs and `failed` if it
// fails, so a row whose label shifted on that transition would be a row jumping
// at the one moment the reader is watching it.
//
// It was never asserted for a sub-tab at all. `.tab-nest + .tab-status-dot` sets
// `margin-inline-start: 0` at (0,2,0) while the diamond's correction is (0,4,0),
// so the two rules genuinely contest one property and the cascade decides — which
// is exactly the class of thing this app's own rule says to verify numerically
// rather than reason about.
// ---------------------------------------------------------------------------

describe("a subagent sub-tab's name holds still while its dot changes state", () => {
  let sheet: HTMLStyleElement;

  beforeAll(async () => {
    // The assembled bundle, because the answer here is the CASCADE's rather than
    // any one rule's. Removed in afterAll so the source-fact sections around this
    // one keep the stylesheet-free page their own comments describe.
    const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(async () => {
    await resetProjection();
  });

  /** The delegate's row, opened as a sub-tab of its launching chat exactly as
   *  `openSubagentTab` opens one. */
  async function subagentRow(): Promise<HTMLElement> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const parent = await openSubject("chat", "c1");
    const ref = subagentRef("c1", "task-1");
    await openTab({ kind: "subagent", ref, parent, owns: false });
    await paint();
    const node = document.querySelector<HTMLElement>(
      `[data-tab-id="${tabIdFor("subagent", ref)}"]`,
    );
    if (node === null) {
      throw new Error("the subagent row did not render");
    }
    return node;
  }

  /** Where the label starts, measured from the row's own left edge so the strip's
   *  entry animation cancels out of the comparison. */
  function nameOffset(row: HTMLElement): number {
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    return name.getBoundingClientRect().left - row.getBoundingClientRect().left;
  }

  it("puts the name at the same x for working and for failed", async () => {
    const { setTabStatus, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const row = await subagentRow();
    const id = tabIdFor("subagent", subagentRef("c1", "task-1"));

    setTabStatus(id, "working");
    const working = nameOffset(row);
    setTabStatus(id, "failed");
    const failed = nameOffset(row);
    setTabStatus(id, "done");
    const done = nameOffset(row);

    // Sub-pixel, because the diamond's own correction is a `calc` that can land on
    // a fraction. What must not happen is the ~2px step an uncorrected 6px mark
    // would produce in an 8px slot.
    expect(Math.abs(failed - working)).toBeLessThan(0.5);
    expect(Math.abs(done - working)).toBeLessThan(0.5);
  });

  it("puts the name at that same x before any state has been written", async () => {
    // The reserved slot doing its job, which is the other half of the promise: the
    // effect paints on a later tick than the row is built, so a row that reserved
    // nothing would render one width and then shift.
    const { setTabStatus, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const row = await subagentRow();
    const blank = nameOffset(row);

    setTabStatus(tabIdFor("subagent", subagentRef("c1", "task-1")), "working");
    expect(Math.abs(nameOffset(row) - blank)).toBeLessThan(0.5);
  });

  it("reserves the slot rather than collapsing it, so the dot has somewhere to land", async () => {
    // The GAP this change closes, stated as a measurement so the fix is provable
    // rather than described: with no state written the slot is `display: block`
    // plus `visibility: hidden`, so it occupies its 8px and the name sits past it.
    // A dot that never gets a state is what left that 8px (plus the row's own 8px
    // gap) permanently empty.
    const row = await subagentRow();
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    expect(dot.getAttribute("data-status")).toBeNull();
    expect(getComputedStyle(dot).display).toBe("block");
    expect(getComputedStyle(dot).visibility).toBe("hidden");
    expect(dot.getBoundingClientRect().width).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// 14. The gap CLOSED, end to end.
//
// Everything above tests one half: `subagent-dots.test.ts` proves the effect
// resolves the right state (with `tabs.js` mocked, so no DOM), and section 13
// proves the row's layout holds for each state (written by hand, so no effect).
// Neither can fail if the two halves are not JOINED — and the join is the
// deliverable, because the reported defect is a slot that is reserved and never
// filled.
//
// So this drives the real subscriber against the real projection and the real
// store, and asserts what a reader would see: a dot that is VISIBLE with the
// delegate's own state on it, in a row whose name did not move to make room.
// ---------------------------------------------------------------------------

describe("the real subscriber fills the reserved slot", () => {
  let sheet: HTMLStyleElement;
  let installed = false;

  beforeAll(async () => {
    const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(async () => {
    await resetProjection();
    const { setSessions } = await import("./store.js");
    setSessions([]);
    if (!installed) {
      // Installed once, like the composition root does it: the subscriber
      // registers a module-level effect and exposes no disposer, exactly as
      // `installRunDotSubscriber` does not.
      const { installSubagentDotSubscriber } = await import("./subagent-dots.js");
      installSubagentDotSubscriber();
      installed = true;
    }
  });

  /** A chat whose resident window holds one delegate's invocation, at `status`.
   *
   *  The invocation is a `tool_call` ENTRY in the ISSUER's lane, which is what the
   *  subagent surfaces address a delegate by: `payload.agent_subtask_id` names the
   *  delegate, and the entry's own lane is `""` because the call was issued at the top
   *  level. */
  async function chatWithDelegate(status: EntryToolCall["status"]): Promise<void> {
    const { setSessions } = await import("./store.js");
    setSessions([session({ id: "c1" })]);
    openTurnIn("c1", "t1");
    appendEntry("c1", sealed("t1", 1, "tool_call", invocationCall({ status })));
  }

  async function subagentRow(): Promise<HTMLElement> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const parent = await openSubject("chat", "c1");
    const ref = subagentRef("c1", "task-1");
    await openTab({ kind: "subagent", ref, parent, owns: false });
    await paint();
    const node = document.querySelector<HTMLElement>(
      `[data-tab-id="${tabIdFor("subagent", ref)}"]`,
    );
    if (node === null) {
      throw new Error("the subagent row did not render");
    }
    return node;
  }

  function dotOf(row: HTMLElement): HTMLElement {
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    return dot;
  }

  it("paints a VISIBLE working dot on a running delegate's row", async () => {
    await chatWithDelegate("in_progress");
    const dot = dotOf(await subagentRow());

    // The whole deliverable in three reads: the attribute CSS keys off, the
    // computed visibility the reservation rule was hiding, and a real box.
    expect(dot.getAttribute("data-status")).toBe("working");
    expect(getComputedStyle(dot).visibility).toBe("visible");
    expect(dot.getBoundingClientRect().width).toBeGreaterThan(0);
  });

  it("paints a failed delegate's row red and announces it", async () => {
    await chatWithDelegate("failed");
    const row = await subagentRow();
    expect(dotOf(row).getAttribute("data-status")).toBe("failed");
    expect(getComputedStyle(dotOf(row)).visibility).toBe("visible");
    // The 9px mark is not the only channel: the screen-reader word rides its own
    // element after the name, which is what makes the state reach a reader who
    // cannot see the diamond.
    //
    // The phrase names the DELEGATE, and this case is where the wrong subject was
    // written down: it asserted ", turn failed" on a subagent's row, because the
    // phrase table was keyed on the state alone and a turn was the only producer
    // when it was written. A chat row still says "turn failed" (section 3).
    expect(row.querySelector(".tab-status-sr")?.textContent).toBe(", subagent failed");
    expect(nameFromContents(row)).toContain("subagent failed");
  });

  it("leaves the slot hidden when no invocation is resident", async () => {
    // The residual the design accepts: a boot-restored tab whose chat has not been
    // fetched. "" is honest, and the row keeps the invisible reserved slot rather
    // than claiming a state.
    const { setSessions } = await import("./store.js");
    setSessions([session({ id: "c1" })]);
    const dot = dotOf(await subagentRow());
    expect(dot.getAttribute("data-status")).toBeNull();
    expect(getComputedStyle(dot).visibility).toBe("hidden");
  });

  it("does not move the name when the state arrives", async () => {
    // The reservation earning its keep against the real writer rather than a hand
    // write: the effect paints on a later tick than the row is built, so this is
    // the ordering every real subagent tab has.
    await chatWithDelegate("in_progress");
    const row = await subagentRow();
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    const offsetOf = (): number =>
      name.getBoundingClientRect().left - row.getBoundingClientRect().left;
    const working = offsetOf();
    expect(dotOf(row).getAttribute("data-status")).toBe("working");

    // The real ingest path for a settled tool call, which is what turns a running
    // delegate into a failed one — and the diamond into the disc's own footprint. A
    // `tool_result` is an ordinary appended entry that FOLDS into its `tool_call` by
    // id (`<tool_call entry id>:result`), so the call's own entry is never rewritten.
    appendEntry("c1", {
      id: "t1-e1:result",
      turn: "t1",
      kind: "tool_result",
      seq: 2,
      ts: 3,
      payload: { status: "failed" },
    });
    await new Promise((r) => setTimeout(r, 0));

    expect(dotOf(row).getAttribute("data-status")).toBe("failed");
    expect(Math.abs(offsetOf() - working)).toBeLessThan(0.5);
  });
});

describe("the dot's motion uses the app's own easing vocabulary", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("beats on the standard curve, at the one shared period", () => {
    // Every dot names the same keyframes, duration and easing, so the SHAPE is
    // shared without a shared clock; only the peak differs per dot. There is
    // deliberately no `:root` animation left to read the curve off.
    const base = loadCSS("03-base.css");
    expect(base).toContain("@keyframes vk-dot-beat");
    expect(base).not.toMatch(/animation:\s*vk-beat/u);
    const disc = ruleContaining(tabs, dot, "top");
    expect(disc.body).toContain("var(--dot-beat-dur) var(--ease-standard)");
    expect(disc.body).not.toContain("ease-in-out");
  });

  it("scales the beat down rather than flashing to full opacity", () => {
    // Every dot beating in unison at full amplitude trades a noisy strip for a
    // throbbing one, which is not what "less visually present" asked for. 0.45 is
    // the sidebar transport dot's peak (10-shell-app.css) rather than a new number.
    const disc = ruleContaining(tabs, dot, "top");
    expect(disc.body).toContain("--beat-peak: 0.45");
  });

  it("resolves the token it names", () => {
    expect(loadCSS("01-tokens.css")).toContain("--ease-standard:");
  });
});
