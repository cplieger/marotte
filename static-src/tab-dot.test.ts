// The tab activity dot: the state mapping, the accessible name and reduced motion.
//  1. MAPPING: states derive from independent signals and an ask COEXISTS with a running turn, so
//     precedence is load-bearing: working first would mask every background ask.
//  2. NAME: a 9px disc says nothing to a screen reader, and the word must follow the tab name.
//  3. REDUCED MOTION: 40-a11y.css RUNS animations to completion; with no fill-mode a completed
//     `vk-dot-wave` leaves an opaque band on the disc, which `content: none` prevents.
// The CSS half reads SOURCE: the test page loads no app stylesheet.

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
  setActive,
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

/** A chat whose newest turn FINISHED (a launching chat once its run is created): no open turn,
 *  and a header outcome grading `done`, the verdict's one source. */
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

// LIVENESS IS THE LOG: `working` is a resident turn with no `turn_close`, `done` the header's
// statement. `store.test.ts` owns the mapping; this drives the rule through the real store
// operations (a silent delegate's tool calls must never paint the tab green mid-turn).
describe("the dot derives working from an OPEN TURN rather than from a latch", () => {
  beforeEach(() => {
    setSessions([session({ id: "c1" })]);
  });

  it("returns nothing at all for a chat it has never heard of", () => {
    expect(tabStatusFor(undefined)).toBe("");
  });

  it("reads WORKING for a turn holding only delegate-lane entries", () => {
    // A delegate's entries are the PARENT's turn in its lane, so the parent's turn stays open whatever
    // the delegate emits.
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
    // Two open turns (a pending prompt beside an agent turn): each `turn_closed` settles only its own,
    // so the header outcome may not be read while the other is open.
    openTurnIn("c1", "t1", 1);
    openTurnIn("c1", "t2", 2);
    closeTurnIn("c1", "t1", 1, "completed");
    upsertHeader(headerWithOutcome("c1", "completed"));
    expect(tabStatusFor(get("c1"))).toBe("working");

    closeTurnIn("c1", "t2", 1, "completed");
    expect(tabStatusFor(get("c1"))).toBe("done");
  });

  it("never lets `thinking` outrank the log, in either direction", () => {
    // `thinking` is NOT an input: a stale latch must not claim busy, a missing one must not claim done.
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
    // `failed` and `done` read a field rewritten only at `turn_close`, so ungated `failed` would paint a
    // running turn red.
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
    // and `completed` is not an input at all — the verdict has one source.
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
    // pointer-tier.ts's imports, reached through the run-input card. Inert here.
    cachePointerTier: undefined,
    cachedPointerTier: undefined,
    coarseEverSeen: undefined,
    markCoarseSeen: undefined,
    pointerModeChoice: undefined,
    setPointerModeChoice: undefined,
  };
});
vi.mock("./run-store.js", () => ({
  // Inert here; a Browser-Mode mock is linked as real ESM, so a name any module in the graph
  // reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));

// Two `apiGetTyped` readers: store-load's chat read (stubbed) and tabs-sync's `GET /api/tabs`
// (answered by the harness).
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
// `./actions/index.js` is NOT mocked: `actions/tabs.ts` needs its whole surface; the harness resets
// the framework.

// The dock's two leaves that reach for DOM it does not own, plus the toast,
// mocked exactly as decision-dock.test.ts mocks them.
vi.mock("./editor-openers.js", () => ({
  // Undefined: present only so real-ESM linking succeeds.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: vi.fn(),
}));
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
    // Every name the import graph reaches must exist (real ESM linking): `decision-dock.ts` imports the
    // first, `model-switcher.ts` (via the shared turn teardown) the second.
    forceReflow: vi.fn(() => 0),
    setBusy: vi.fn(),
  };
});

async function paint(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(null)));
}

/** A screen reader's name-from-contents for the `role="tab"` row, in DOM order. Excludes the
 *  `aria-hidden` dot, `.tab-pin` on an unpinned row (12-tabs.css `display: none`; every row carries
 *  the node), and the close BUTTON (its own AT node). The property is ORDER. */
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

// The projection harness: every row arrives through a real `open_tab` round trip, so ids are
// OPAQUE and addressed through `tabIdFor`. Activation, teardown and the seeded dot use the
// FACTORY's registration, as the composition root does.

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
    // `waiting` and `input` share one VISUAL (css/12-tabs.css), so the phrases alone keep them apart.
    expect(new Set(spoken.values()).size).toBe(6);
    expect(spoken.get("waiting")).toBe("Fix the parser, waiting for you");
    expect(spoken.get("input")).toBe("Fix the parser, needs a decision");
  });

  it("claims exactly what the failed latch's one producer supports", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    setTabStatus(chatTabID, "failed");
    // `setTurnFailed`'s one live producer is `turn_closed` with outcome `failed` or `refused`, so the
    // phrase claims exactly a failed turn.
    expect(nameFromContents(row)).toBe("Fix the parser, turn failed");
  });

  it("keeps the state word between the name and the pinned marker", async () => {
    const { setTabStatus, setTabPinned } = await import("./tabs.js");
    const row = await openChat();
    setTabPinned(chatTabID, true);
    await paint();
    setTabStatus(chatTabID, "input");
    // What it IS, what it needs, how it is filed: DOM position orders it, hence the word is a sibling
    // after `.tab-name`.
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
    // A singleton's ref is EMPTY: its identity is its kind, so there is no sentinel id.
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

// The phrase names the tab's own SUBJECT: three producers (`tabStatusFor` a turn, `runStatusFor` a
// run, `subagentStatusFor` a delegate) write outcome states, so the two outcome phrases are
// per-subject while the neutral ones are shared.

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

    // `run-dots.ts` writes this; the noun is the one the app already shows for a run.
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

    // `tabStatusFor`'s subject genuinely is a turn (its latch has one live producer, `turn_closed`
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

    // Neutral states say nothing about a subject, so one shared wording.
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
        // `endsWith` rules out the bare verb; the `undefined` read is the runtime totality check a cast hides.
        expect(phrase.endsWith(` ${verb}`)).toBe(true);
        expect(phrase).not.toContain("undefined");
      }
      // The `done` write above is the last one, so this is each kind's FINISHED
      // phrase.
      finishedBy.set(kind, phraseOn(row));
    }

    // Three live producers, three distinct phrases (both outcomes compose from one `DOT_SUBJECT` entry).
    const producers = ["chat", "run", "subagent"] as const;
    expect(new Set(producers.map((k) => finishedBy.get(k))).size).toBe(3);
  });

  it("paints the tooltip and the announced word from one string", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { TAB_ICONS } = await import("./tab-view.js");

    // Tooltip and screen-reader word come from ONE resolver call, so they cannot drift per kind.
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
    // The disc animates its OWN opacity only while working (a shared `:root` clock invalidated style
    // every frame); phase agrees via `beat-phase.ts`.
    const disc = ruleContaining(tabs, dot, "top");
    expect(disc.body).toContain("vk-dot-beat");
    expect(disc.body).toContain("var(--beat-phase, 0ms)");
    // Anchored at line start, so the run row's `::before` (whose selector contains this) cannot match.
    expect(tabs).not.toMatch(/^\.tab-status-dot\[data-status="working"\]::before/mu);
  });

  it("needs no reduced-motion arm for the disc, because the beat rests visible", () => {
    // 40-a11y.css runs `vk-dot-beat` once with implicit keyframes, so the disc rests at opacity 1; the
    // run row's overlay is removed with the other square marks.
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
    // A TRANSPARENT stop, not an inset shadow: the dot sits on five row fills in two themes.
    expect(/inset .*var\(--c-bg/.test(reduced.body)).toBe(false);
  });

  it("does not borrow the wants-you ring to make up for the motion", () => {
    // The ring means "this chat wants you"; `working` wants nothing, so no ring.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    expect(/box-shadow/.test(reduced.body)).toBe(false);
  });

  it("keeps the donut's band tellable apart from a hollow dot's hairline", () => {
    // Hollow states are a 1.5px edge, so the donut differs by weight: a 45% hole leaves a 2.5px band;
    // wider closes the hole into a solid disc. Bounded both ways.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    const stop = /transparent 0 (\d+)%, var\(--dot-color\) \1% 100%/.exec(reduced.body);
    expect(stop, "the donut must have one hole radius, used by both stops").not.toBeNull();
    const hole = Number(stop?.[1]);
    expect(hole).toBeGreaterThanOrEqual(35);
    expect(hole).toBeLessThanOrEqual(50);
  });

  it("keeps the global reduced-motion sweep that backs it up", () => {
    // The component rule above owns this; this is the belt. If the global sweep
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
    // Selection belongs to the row and a status to the chat, so selecting must not darken the ink.
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

// The ink vocabulary is web-terminal-kiro's own (marotte hosts a web-terminal panel), hue-exact in
// both themes, so a state never carries two colours between the apps.

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
    // `dirty` and `working` read different tokens; their values are close, so MOTION separates them
    // (a tab is never both a chat and a file).
    const working = ruleContaining(tabs, '.tab-status-dot[data-status="working"]', "top");
    const dirty = ruleContaining(tabs, '.tab-status-dot[data-status="dirty"]', "top");
    expect(working.body).not.toContain("--c-accent");
    expect(dirty.body).toContain("--dot-color: var(--c-accent)");
  });

  it("un-merges waiting from input on fill, which is the only channel it has", () => {
    // One ink for "action required", so fill carries the pair (hue alone fails WCAG 1.4.1): `input`
    // solid, `waiting` hollow. On a RUN row fill means ended, so band carries them (4e).
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
    // Exactly FOUR rules at any scope carry a ring, all meaning "blocked on a person". Read via
    // `allRules` so each is named by its whole selector list, and a new consumer shows up here.
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
    // scripts/css-contrast.py's DOT_ALIASES exempts pairs from its hue check; the wants-you pair
    // separated only by fill must never re-enter it.
    expect(cssContrastScript).toContain("DOT_ALIASES: list[tuple[str, str]] = []");
  });
});

// 4c. The WORKFLOW mark reuses the dot's `--c-dot-*` inks and `--dot-color`, but is a rounded
// SQUARE in every state (the dot is a circle except `failed`), separating them at identical hue.
// `css-contrast.py dot` transcribes these channels; this reads them off `12-tabs.css` to compare.

describe("the workflow mark is a ring, and never the dot's disc", () => {
  const tabs = loadCSS("12-tabs.css");
  const RUN_STATES = ["working", "waiting", "input"] as const;
  /** The exec view's own ratio, which is what the band channel is measured against
   *  wherever two states differ on it alone. */
  const BAND_RATIO_FLOOR = 2;

  /** The mark's shared LOOK rule, keyed on the bar's class: `.tab-run-dot` also belongs to the tab
   *  row's layout rule. */
  function lookRule(): string {
    return ruleContaining(tabs, ".run-bar-glyph", "top").body;
  }

  /** The band a state's rule paints in px, from `--run-band`, `border-width` or the `border`
   *  shorthand, so both marks are measurable with one reader. */
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

  /** The DOT donut's band from its hole stop: radius 4px (half of --dot-size), so `4 * (1 - H/100)`. */
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
    // A state arm that forgets its ink paints the base: idle, the quieter failure, like the dot's.
    expect(lookRule()).toContain("--dot-color: var(--c-dot-idle)");
  });

  it("declares every band in one unit, so the relation survives a root font size", () => {
    // px throughout: a rem band beside px literals inverts at 20px. File-wide over all three band
    // spellings.
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
    // No ink fill: shape alone separates the marks at identical hue. The one fill is the closing beat's
    // seal, invisible at rest.
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
    // One ink, halo and silhouette, and still: BAND WIDTH is the whole separator, so the RATIO is
    // asserted (`css-contrast.py dot` gates the same number).
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
    // Only the working overlay animates: an animation outranks a declaration, so a settled state could
    // not reset a base-rule beat.
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
    // Chromium paints `1.5px` and `1px` borders identically at dpr 1-3, so a computed-style check cannot
    // tell; taking the mark's numbers makes the DECLARED ladder the painted one.
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
    // The silhouette answers the cross-mark question: against the dot's reduced-motion `working` donut
    // a circular mark has no 2:1 band that neither collides with `waiting` nor closes the hole.
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
    // No band here equals the dot's idle hairline, so that likely neighbour is separated twice.
    const idle = ruleContaining(tabs, '.tab-status-dot[data-status="idle"]', "top").body;
    const hairline = Number(/border:\s*([\d.]+)px/.exec(idle)?.[1]);
    expect(mark).toBeGreaterThan(hairline);
    for (const state of RUN_STATES) {
      expect(bandOf(`.tab-run-dot[data-status="${state}"]`)).not.toBe(hairline);
    }
  });

  it("is carried by the mechanical 1.4.1 gate, not only by this file", () => {
    // `css-contrast.py dot` (the WCAG 1.4.1 check) must enumerate the mark's three states and the axis
    // separating them.
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
    // `RUN_ROW_MEMBERS` (the run row's 1.4.1 population, in another language) can be wrong about
    // MEMBERSHIP, so `runStatusFor` is driven over its whole input space; `ALL_STATUSES` is a Record
    // over `ClassifiedRunStatus`. `""` paints no mark.
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

// 4d. `box-sizing: border-box` makes --dot-size the painted DIAMETER, so the ring matches the disc
// beside it (else a 3px band paints 14px). Measured with the shipped stylesheet mounted.

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
    // Measured through the cascade: the radius is a calc over --dot-size. Half the diameter is a circle.
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

// 4e. A run row carries NO `.tab-run-dot` (chat kind only), so its activity dot takes the mark's
// square: a CASCADE question (the kind rule outranks `50%` and the `failed` diamond's corner),
// measured off real boxes. Its three live states take the mark's treatment; the two OUTCOMES stay
// the dot's.

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
    // Measured rather than transcribed: one token, two elements, so a retune moves both or fails here.
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
    // The convergence state by state: fill, band, halo, ink and corner must all match the mark, or the
    // strip contradicts itself about ONE run. `transition` is not compared: the mark spawns, the dot
    // does not.
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
    // `input` and `waiting` are both hollow here, so BAND separates them and the RATIO (2:1, the mark's
    // 1px/2px) is asserted.
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
    // Channels are RE-SPENT: `done` and `failed` keep the dot's disc, so fill separates live from
    // settled on this row; each row is internally consistent (WCAG 1.4.1).
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
    // The radius rule is state-independent apart from `failed`, and the slot plus the diamond's margin
    // hold under it. `runStatusFor` never answers `idle`; `""` keeps its reserved box.
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
    // Keyed on the KIND, not the nesting: a top-level run row reports the same subject, so it takes the
    // same silhouette in its trailing slot.
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
    // Here the name is the flex grower, so the smaller diamond would move the MARK. `done` is the
    // departure state; `failed` is the only state with a different box.
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

// 5b. EVERY wire outcome reaches the dot and none as "nothing is happening": `interrupted` once fell
// to `idle`, a TRANSPARENT ring beside an inline error. The table is the taxonomy, with the SHAPE
// painted for the two that matter.

describe("every turn outcome reaches the tab dot", () => {
  const tabs = loadCSS("12-tabs.css");

  /** outcome -> the dot state once that turn has ended, hardcoded: re-deriving through `severityOf`
   *  would pass for any mapping. */
  const cases: [TurnOutcome, TabDotStatus][] = [
    // BROKEN. All three are failures and all three must say so.
    ["failed", "failed"],
    ["refused", "failed"],
    ["interrupted", "failed"],
    // CLEAN.
    ["completed", "done"],
    // STOPPED: not a failure, but not `idle` either (the hollow ring means the chat has NOT INITIATED).
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
      // Through the HEADER, the one door: an unfetched window is the ordinary case.
      upsertHeader(headerWithOutcome("c1", outcome));
      expect(tabStatusFor(get("c1"))).toBe(want);
    });
  }

  it("grades every outcome the same way the dot above painted it", () => {
    // Pins the shared grader under the header path; `store.test.ts` owns its full table.
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
    // `cancelled` (a model switch discarding a turn) must not paint red or hollow; `unknown` (a turn
    // nothing closed) must not invent a failure.
    expect(outcomeLatch("cancelled")).toBe("done");
    expect(outcomeLatch("unknown")).toBe("done");
  });

  it("reads an ABSENT outcome on a later header as a CLEAR, back to the floor", () => {
    // The header is the AUTHORITY both ways: an absent outcome is a real statement, so the hollow ring
    // is reachable again.
    upsertHeader(headerWithOutcome("c1", "failed"));
    expect(tabStatusFor(get("c1"))).toBe("failed");
    upsertHeader(headerWithOutcome("c1"));
    expect(tabStatusFor(get("c1"))).toBe("idle");
  });

  it("paints a failure as the red lozenge and never as the idle ring", () => {
    // `failed` is a filled rotated square, `idle` a transparent hairline disc; a source fact, since no
    // app stylesheet is linked.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(failed.body).toMatch(/transform:\s*rotate\(45deg\)/u);
    expect(failed.body).toMatch(/background:\s*var\(--dot-color\)/u);
    expect(failed.body).not.toMatch(/background:\s*transparent/u);

    const idle = ruleContaining(tabs, '.tab-status-dot[data-status="idle"]', "top");
    expect(idle.body).toMatch(/background:\s*transparent/u);
    expect(idle.body).toMatch(/border:/u);
    // The two must not be one shape, or the case above would be unobservable.
    expect(failed.body.trim()).not.toBe(idle.body.trim());
  });
});

// 7. A list refetch carries the verdict on the HEADER, so the rebuild is the mechanism. Real store
// and real loadList with no prior session (a reload, a new browser, a long reconnect).
// `store-load.test.ts` owns the rebuild rule; this checks the value reaches the DOT.

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
    // A cancelled turn RAN, so not hollow; a record with no outcome has nothing to read.
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
    // A live turn: the rebuild keeps the resident window, so `working` outranks the header's
    // `completed` (which describes the previous turn).
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1" })]);
    openTurnIn("c1", "t1");
    mockApiGetTyped.mockResolvedValue({ chats: [headerWithOutcome("c1", "completed")] });

    await loadList();
    expect(get("c1")?.turns.size, "the window was dropped by the rebuild").toBe(1);
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  it("lets a waiting_on_user chat keep saying so over a header's done", async () => {
    // `waiting` outranks `done`, and the rebuild carries the status over, so the header must not bury it.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", agent_status: "waiting_on_user" })]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("waiting");
  });
});

// 8. An abandoned ask stops claiming the chat needs a decision (`input` outranks everything).

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

    // handlers/turn.ts runs this on turn_closed: an ended turn waits on no ask (cmdCancel cleared the
    // server's set).
    dropTurnDecisions("c1");
    expect(tabStatusFor(get("c1"), hasPendingDecision("c1"))).toBe("idle");
  });

  it("leaves a workflow run's ask alone when the launching turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions, dropDecisions } =
      await import("./decision-dock.js");
    // An agent-launched run OUTLIVES the launching turn, so dropping its asks would strand the run.
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

// 9. A chat-parented run's ask is filed under the LAUNCHING chat, so `input` lands on the parent's
// dot, while the run sub-tab reads `runPendingAsks`; every case asserts BOTH surfaces. The state
// to return to is `done` (the launching turn ended long before the run).

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

  /** The parent tab's composer dock, showing the active chat's queue. */
  async function mountChatHost(): Promise<HTMLElement> {
    const { mountDecisionDock } = await import("./decision-dock.js");
    const el = document.createElement("div");
    el.id = "decision-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    setActive(PARENT);
    mountDecisionDock(el);
    return el;
  }

  /** Answer the card on screen in a dock. `:scope >` skips an answered card still on screen for the
   *  length of its phase. */
  function answerIn(hostEl: HTMLElement, text: string): void {
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

  // The sub-tab answers through its composer (run-composer.ts), so its dock draws no card; the
  // parent tab's card is the one a click settles.
  it("clears the parent when the parent tab answers the step's question", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks } = await import("./decision-dock.js");
    const runHost = await mountRunHost();
    const chatHost = await mountChatHost();
    pushDecision(runAsk());

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
    expect(runPendingAsks(RUN).count).toBe(1);
    expect(runHost.querySelector(":scope > .dock-card")).toBeNull();

    answerIn(chatHost, "yes, ship it");

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

    // A terminal run waits on no one, and `dropTurnDecisions` exempts run-scoped asks, so only this
    // drop reaches it.
    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears a step's user-input question when the run ends too", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // REQUEST-shaped with a `runID`: neither `collapseSettledRunInput` nor `dropTurnDecisions` takes it,
    // so removal asks "this run's ask, wherever filed".
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
    // Two runs share one chat's queue key, so the sweep separates by RUN; a sibling survivor pins that.
    pushDecision(runAsk());
    pushDecision(runAsk({ workflow_id: "wf-2", ask_id: "ask-2" }));

    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(runPendingAsks("wf-2").count).toBe(1);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });

  // A step question whose `run_id` arrived EMPTY holds the parent amber. Not a red check: it guards
  // against widening the run sweep to take orphans indiscriminately. The real trigger is red-checked
  // in `handlers/run.test.ts`.
  it("holds the parent on input for a step question that carries NO run id", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropTurnDecisions } =
      await import("./decision-dock.js");
    // The orphan, and the run's own answerable ask, both under the launching chat.
    pushDecision({ ...stepQuestion(1), runID: "" });
    pushDecision(runAsk());
    expect(runPendingAsks(RUN).count).toBe(1);

    // The run's own ask settles where the run tab answers it; the orphan stays queued.
    const { collapseSettledRunInput } = await import("./decision-dock.js");
    collapseSettledRunInput(RUN, "ask-1", "user");

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

// 8b. The real dock repaints a real row's dot: the link neither section 8 nor
// `chat-tab-strip.test.ts` covers is `hasPendingDecision` reading `queueVersion.value` (not
// `.peek()`). The effect replicates chat.ts's `chatRowEffect` minus its name and tooltip writers
// (importing chat.ts needs mocks that break this suite); the last case pins the replica to the source.

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
    // The replica stands in for `chatRowEffect`: both reads must be unconditional and ahead of every
    // early return in chat.ts, which subscribes a BACKGROUND chat's row.
    expect(chatSrc).toContain("hasPendingDecision(chatID)");
    expect(chatSrc).toContain("tabStatusFor(s, pendingAsk)");
    expect(chatSrc).toContain("setTabStatus(tabID,");
  });
});

// 10. A row that is CREATED knows its state: the dot is recorded, not DOM-only, so a boot restore
// (sessions before tabs) or a rebuilt row paints its chat's state without a later change.

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
    // Dropping the node and making the store emit is the rebuild any lost row takes.
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

// 11. The dot is live state and never persisted: a restored dot would describe a turn that ended
// before the page loaded.

// Structural: a `TabSubject` has no dot field, and a dot write bumps `dotVersion` without `emit()`.
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
    // Token-derived, never a number: the SLOT reads --icon-ui (tier-dependent), the MARK the state's dot
    // token, so the smaller diamond reserves the same width and a name never moves on a status flip.
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
    // A rotated square's DIAGONAL is its footprint, so the diamond takes the smaller token; the margin
    // override above depends on that, so both are asserted.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(/inline-size:\s*var\(--dot-size-sm\)/.test(failed.body)).toBe(true);
  });

  it("re-derives the SUB-TAB diamond's slot from both dot tokens too", () => {
    // On a sub-tab the nesting arrow holds the glyph slot, so the dot matches only its siblings. Both
    // tokens, so --dot-size changes cannot desynchronise diamond and disc.
    const diamond = ruleContaining(
      tabs,
      '.tab.tab-child .tab-status-dot[data-status="failed"]',
      "top",
    );
    expect(diamond.body).toContain("calc((var(--dot-size) - var(--dot-size-sm)) / 2)");
  });
});

// 13. In REAL LAYOUT for a subagent sub-tab: `subagent-dots.ts` flips it to `failed`, and
// `.tab-nest + .tab-status-dot` (0,2,0) contests the diamond's correction (0,4,0), so the cascade is
// verified numerically.

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
    // With no state the slot is `display: block` plus `visibility: hidden`, occupying its 8px.
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

// 14. The join, end to end: `subagent-dots.test.ts` mocks tabs.js and section 13 writes states by
// hand, so this drives the real subscriber, projection and store, and asserts a VISIBLE dot in a row
// whose name did not move.

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

  /** A chat whose window holds one delegate's invocation at `status`: a `tool_call` entry in the
   *  issuer's lane (`""`) with `payload.agent_subtask_id` naming the delegate. */
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
    // The screen-reader word rides its own element after the name, and names the DELEGATE (a chat row
    // says "turn failed").
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

    // A `tool_result` FOLDS into its `tool_call` by id (`<tool_call entry id>:result`); the call's own
    // entry is never rewritten.
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
