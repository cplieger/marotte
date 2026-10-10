// The `connected` handshake and its thinking reconcile, the BUS_RECONCILE body, the connect-hook
// snapshots, page resume and mode_changed. The REAL store: liveness is the LOG, so a running turn
// is opened and a settled chat carries `last_turn_outcome`. store-load.ts and tabs.ts stay mocked.

import { vi, describe, it, expect, beforeEach } from "vitest";
import {
  setSessions,
  setActive,
  get,
  appendEntry,
  defaultUsage,
  isThinking,
  openTurn,
  recordSteerQueued,
  steerCount,
  tabStatusFor,
  transcriptStale,
} from "../store.js";
import { observeStamp, _resetForTest as resetVersions } from "../subject-versions.js";
import { workspaceRoot, _resetForTest as resetWorkspace, setWorkspaceRoot } from "../workspace.js";
import { liveRunsForChat, registerLiveRunObserver, runChatID } from "../run-store.js";
import type { Session } from "../types.js";
import type { Entry } from "../wire/types.gen.js";
// The two modules the factories below spread rather than replace. Type-only, so neither
// adds a runtime edge the `vi.mock` would have to reach around.
import type * as RunStore from "../run-store.js";
import type * as ApiClient from "../api-client.js";

vi.mock("../store-load.js", () => ({
  loadList: (signal?: AbortSignal) => mockLoadList(signal),
  loadMessages: mockLoadMessages,
  scheduleListRetry: () => mockScheduleListRetry(),
}));
const mockLoadList = vi.fn((_signal?: AbortSignal) => Promise.resolve(true));
const mockLoadMessages = vi.fn(() => Promise.resolve(true));
// A spy, because the LADDER is store-load.test.ts's subject (it owns the reach gate, the delays and
// the bound); what this file owns is that the door consults it and only on a failure.
const mockScheduleListRetry = vi.fn();

const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => true);
// The gate itself is tabs.test.ts's subject; what this file owns is that the gap and the resume
// reach it, once, and after the epoch bump.
const mockRefreshActiveView = vi.fn();
vi.mock("../tabs.js", () => ({
  // Undefined: present only so real-ESM linking succeeds.
  activateTab: undefined,
  // navigate.js's `openSpec` (the spec tab's door) imports these three beside
  // `activateTab` and the `tabIdFor` below, and Browser Mode links for real, so
  // one missing name fails this whole file's import.
  openTab: undefined,
  parentChatRef: undefined,
  setTabParent: undefined,
  getActiveTabId: undefined,
  getActiveTabKind: undefined,
  getActiveTabRoute: undefined,
  openEditorView: undefined,
  setGitTab: undefined,
  setSettingsTab: undefined,
  setTabDirty: undefined,
  openGitView: undefined,
  openSettingsView: undefined,
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
  refreshActiveView: mockRefreshActiveView,
  // No-ops, not undefined: the gap handler (via turn-teardown.ts) CALLS both once per session.
  tabIdFor: vi.fn(() => ""),
  setTabStatus: vi.fn(),
}));

vi.mock("../settings.js", () => ({
  syncSettings: vi.fn(() => Promise.resolve({})),
  // The settings_updated handler adopts the payload's theme, which is what
  // makes a theme chosen on another device land here live.
  adoptThemeFromSettings: vi.fn(),
  // …and re-seeds the General panel's own controls, which is the same thing for
  // retention, the agent capabilities and the debug level.
  applyGeneralPanel: vi.fn(),
}));
vi.mock("../session-context.js", () => ({
  // Undefined: present only so real-ESM linking succeeds.
  setLastModel: undefined,
  restoreLastModel: vi.fn(),
  restoreLastEffort: vi.fn(),
  setCurrentModel: undefined,
}));
vi.mock("../status.js", () => ({
  // Present-but-undefined, for the reason above.
  updateContextBar: undefined,
}));
vi.mock("../retention.js", () => ({ refreshRetention: vi.fn() }));

// A call count is the assertion: the catalog rides no frame, so a gap is the only signal that it
// may have changed during the outage.
const mockFetchCatalog = vi.fn(() => Promise.resolve());
vi.mock("../session-catalog.js", () => ({ fetchCatalog: mockFetchCatalog }));

// PARTIAL: the gap door's two run readers are spies (they share a token and both fetch), while
// `adoptConnectRuns` is REAL, since the state it seeds is what the connect door owns. `vi.hoisted`
// for the api-client pair's reason below.
const { mockInvalidateCachedRuns, mockRebuildLiveRuns } = vi.hoisted(() => ({
  mockInvalidateCachedRuns: vi.fn(),
  mockRebuildLiveRuns: vi.fn(),
}));
vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  rebuildLiveRuns: mockRebuildLiveRuns,
  invalidateCachedRuns: mockInvalidateCachedRuns,
}));

// Replaced so a per-run read is OBSERVABLE (the connect adoption issues none, the fallback one).
// `vi.hoisted`: the static run-store import resolves this factory during linking, before plain consts.
const { mockApiGet, mockApiGetTyped } = vi.hoisted(() => ({
  mockApiGet: vi.fn(),
  mockApiGetTyped: vi.fn(),
}));
vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: mockApiGet,
  apiGetTyped: mockApiGetTyped,
}));

// The rail is a boundary this test has no business driving: it FETCHES the session-wide
// turn index, and turn-rail.ts also pulls in scroll.ts, whose module-level initialisation
// demands a real #messages scroller. `invalidateTurnRails` is what the gap door calls.
const mockRefreshTurnRail = vi.fn(() => Promise.resolve());
const mockInvalidateTurnRails = vi.fn();
vi.mock("../turn-rail.js", () => ({
  refreshTurnRail: mockRefreshTurnRail,
  invalidateTurnRails: mockInvalidateTurnRails,
  pointTurnRail: vi.fn(),
  mountTurnRail: undefined,
  resetTurnRail: undefined,
}));

// `decodeEnvelope` and `dispatch` are REAL: pending_snapshot's job is pushing each item through
// that door.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
import type * as Bus from "../bus.js";
const busHandlers = new Map<string, (...args: unknown[]) => void>();
const dispatched: unknown[] = [];
vi.mock("../bus.js", async (importOriginal) => {
  const real = await importOriginal<typeof Bus>();
  return createBusMock({
    // Undefined: present only so real-ESM linking succeeds.
    emitBus: undefined,
    lookupSSEDecoder: real.lookupSSEDecoder,
    registerSSEDecoder: real.registerSSEDecoder,
    decodeEnvelope: real.decodeEnvelope,
    dispatch: vi.fn((evt: unknown) => {
      dispatched.push(evt);
    }),
    onBus: vi.fn((event: string, handler: (...args: unknown[]) => void) => {
      busHandlers.set(event, handler);
    }),
    BUS_RECONCILE: "transport:reconcile",
    BUS_PAGE_RESUMED: "page:resumed",
  });
});

// Import after mocks so system.ts registers its handlers against the bus mock.
await import("./system.js");

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
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
  };
}

/** Open a turn in `chatID`'s resident log. LIVENESS IS THE LOG, so this is how a case
 *  says "this chat has a turn of its own running" — there is no marker to set. */
function openTurnOn(chatID: string, turnID = "t1"): void {
  const open: Entry = {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1 },
  };
  openTurn(chatID, open);
}

function steerEntryOn(chatID: string, steerID: string, text: string, turnID = "t1"): void {
  appendEntry(chatID, {
    id: steerID,
    turn: turnID,
    kind: "steer",
    seq: 1,
    ts: 2,
    payload: { text, origin: "user", state: "read" },
  });
}

function entryIDs(chatID: string, turnID = "t1"): string[] {
  return (get(chatID)?.turns.get(turnID)?.entries ?? []).map((e) => e.id);
}

function fireReconcile(cause = "full:hello"): AbortSignal {
  const signal = new AbortController().signal;
  busHandlers.get("transport:reconcile")?.({ cause, signal });
  return signal;
}

function fireResume(): void {
  busHandlers.get("page:resumed")?.(undefined);
}

beforeEach(() => {
  vi.clearAllMocks();
  mockLoadList.mockReturnValue(Promise.resolve(true));
  mockLoadMessages.mockReturnValue(Promise.resolve(true));
  // `mockReset` is on, so an implementation set at construction is gone by now. A null
  // answer is what a 404 gives, and what the real decoders' callers already handle.
  mockApiGet.mockResolvedValue(null);
  mockApiGetTyped.mockResolvedValue(null);
  dispatched.length = 0;
  setSessions([]);
  resetWorkspace();
  resetVersions();
});

// The handshake alone states the workspace root (workspace.ts needs it for relative paths);
// recorded here because transport.ts's hook skips a page's first connection.
describe("connected handshake", () => {
  it("records the workspace root", () => {
    expect.assertions(1);
    fireSSE("connected", "", { workspace: "/workspace", floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("/workspace");
  });

  it("ignores a handshake that carries no workspace", () => {
    // An older server, or a frame that lost the field: leaving the root unknown
    // makes the file request fail as it did before rather than being rewritten.
    expect.assertions(1);
    fireSSE("connected", "", { floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("");
  });

  it("ignores an empty workspace rather than recording it as the root", () => {
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    fireSSE("connected", "", { workspace: "", floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("/workspace");
  });

  it("ignores a non-string workspace", () => {
    expect.assertions(1);
    fireSSE("connected", "", { workspace: 42, floor: 1, head: 9 });
    expect(workspaceRoot()).toBe("");
  });
});

// The handshake's two NEGATIVE statements: `busy_chats` alone retracts the `thinking` of a turn
// that died with the previous process. Each is guarded by its STATED flag, since a scoped, capped
// or withheld list looks complete.

/** The two flags default TRUE here, which is the opposite of the wire default, so each case names
 *  the withholding it is about. */
function fireConnected(over: Record<string, unknown> = {}): void {
  fireSSE("connected", "", {
    floor: 1,
    head: 9,
    busy_stated: true,
    live_runs_stated: true,
    live_runs: [],
    ...over,
  });
}

describe("the connect handshake retracts a thinking the server does not confirm", () => {
  it("retracts a thinking chat the busy set does not name", () => {
    setSessions([makeSession("a", { thinking: true }), makeSession("b", { thinking: true })]);
    fireConnected({ busy_chats: ["b"] });
    expect(get("a")?.thinking, "unconfirmed").toBe(false);
    expect(get("b")?.thinking, "the server says this one is busy").toBe(true);
  });

  it("retracts nothing when the set is not STATED, and still adopts the runs", () => {
    // A filtered or over-cap connect says nothing about omitted chats. The runs are OUTSIDE that gate:
    // the inventory is workspace-global with its own flag.
    setSessions([makeSession("a", { thinking: true })]);
    fireConnected({
      busy_stated: false,
      busy_chats: [],
      live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }],
    });
    expect(get("a")?.thinking, "no statement, no retraction").toBe(true);
    expect(
      liveRunsForChat("a").some((r) => r.executing),
      "the inventory landed anyway",
    ).toBe(true);
  });

  it("touches the retracted chat's own log not at all", () => {
    // NARROW: `busy_chats` omits a chat whose only open turn is a workflow STEP, so this clears
    // `thinking` only: no entry, no closed turn, no dropped window.
    setSessions([makeSession("a", { thinking: true })]);
    openTurnOn("a");
    fireConnected({ busy_chats: [] });
    expect(get("a")?.thinking).toBe(false);
    expect(entryIDs("a"), "still the only copy of the reply").toEqual(["t1-open"]);
    expect(get("a")?.turns.get("t1")?.closeAt, "and the turn is still open").toBeUndefined();
  });
});

describe("the connect handshake adopts a thinking the server DOES confirm", () => {
  it("adopts a busy chat this client has no latch for", () => {
    // `thinking` is latched from streamed frames alone and a page load replays none, so
    // without this the composer offers Send over a live turn until the agent's next delta.
    setSessions([makeSession("a"), makeSession("b")]);
    fireConnected({ busy_chats: ["a"] });
    expect(get("a")?.thinking, "the server says this turn is in flight").toBe(true);
    expect(get("b")?.thinking, "not named, so nothing to adopt").toBe(false);
  });

  it("drops the stale liveness statement of the chat it adopts", () => {
    // The dot's verdict is the header's `last_turn_outcome`, never written here, so the adopt arm
    // clears the server's stale `turn_open: false`, which `turnLive` would read as settled.
    setSessions([makeSession("a", { turn_open: false, last_turn_outcome: "completed" })]);
    fireConnected({ busy_chats: ["a"] });
    expect(get("a")?.thinking).toBe(true);
    expect(get("a")?.turn_open, "the previous turn's liveness, not this one's").toBeUndefined();
  });

  it("reaches the composer, and leaves the dot to the log", () => {
    // `thinking` is not an input to `turnLive`, so this arm cannot make the dot read `working`; it
    // reaches `isThinking`, which offers Cancel over Send.
    setSessions([makeSession("a", { last_turn_outcome: "completed" })]);
    fireConnected({ busy_chats: ["a"] });
    expect(isThinking("a"), "the composer offers Cancel").toBe(true);
    expect(tabStatusFor(get("a")), "the dot still grades the header's outcome").toBe("done");
  });

  it("adopts nothing when the set is not STATED", () => {
    // A withheld list is withheld WHOLE, so it names no chat: adopting from one would
    // latch nothing while reading as a complete answer.
    setSessions([makeSession("a")]);
    fireConnected({ busy_stated: false, busy_chats: [] });
    expect(get("a")?.thinking).toBe(false);
  });

  it("leaves an already-thinking chat's retained status alone", () => {
    // On the TRANSITION only, like `markTurnLive`: `setThinking(true)` clears carriers, and a live
    // turn's retained `waiting_on_user` must survive a reconnect.
    setSessions([makeSession("a", { thinking: true, agent_status: "waiting_on_user" })]);
    fireConnected({ busy_chats: ["a"] });
    expect(get("a")?.thinking).toBe(true);
    expect(get("a")?.agent_status, "somebody still owes this agent an answer").toBe(
      "waiting_on_user",
    );
  });
});

describe("the connect handshake adopts the live-run inventory off the frame", () => {
  it("seeds the inventory, the chat pairing and the observer", () => {
    // Three seeds per row: without the inventory a live-run chat is evicted, without the pairing the
    // run's tab opens at the strip's end, without the observer the tab keeps its placeholder label.
    const seen: string[] = [];
    registerLiveRunObserver((id) => seen.push(id));
    setSessions([makeSession("a")]);

    fireConnected({ live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }] });

    expect(liveRunsForChat("a")).toEqual([{ id: "wf-1", chat: "a", executing: true }]);
    expect(runChatID("wf-1")).toBe("a");
    expect(seen).toEqual(["wf-1"]);
  });

  it("issues no per-run read of its own", () => {
    // No cause: the mark's floor paints a live run's square from THIS frame, so a per-run `inspect` on
    // every reconnect would be pure cost.
    setSessions([makeSession("a")]);
    fireConnected({ live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }] });
    expect(mockApiGet).not.toHaveBeenCalled();
    expect(mockApiGetTyped).not.toHaveBeenCalled();
  });

  it("falls back to the endpoint when the inventory was WITHHELD", () => {
    // `live_runs_stated: false` means the lease store held more rows than the frame will
    // carry, so the list is withheld rather than truncated — and the adoption CLEARS before
    // it repopulates, so reading a withheld list as empty would drop every live run.
    setSessions([makeSession("a")]);
    fireConnected({ live_runs_stated: false });
    expect(mockApiGetTyped).toHaveBeenCalledWith("/api/runs/live", expect.any(Function), undefined);
  });
});

describe("BUS_RECONCILE handler", () => {
  it("clears the thinking flag on every session", () => {
    setSessions([
      makeSession("a", { thinking: true }),
      makeSession("b", { thinking: false }),
      makeSession("c", { thinking: true }),
    ]);
    fireReconcile();
    expect(get("a")?.thinking).toBe(false);
    expect(get("b")?.thinking).toBe(false);
    expect(get("c")?.thinking).toBe(false);
  });

  it("reloads the header list", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockLoadList).toHaveBeenCalled();
  });

  it("arms the bounded retry when that reload failed", async () => {
    // The gap has already dropped every claim this client held, so a failed reload
    // leaves the sidebar on rows it was licensed to drop — and on a stream that stayed
    // up there is no later `connected` to re-read it.
    setSessions([makeSession("a")]);
    mockLoadList.mockReturnValue(Promise.resolve(false));
    fireReconcile();
    await mockLoadList();
    expect(mockScheduleListRetry).toHaveBeenCalledTimes(1);
  });

  it("arms nothing when the reload landed", async () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    await mockLoadList();
    expect(mockScheduleListRetry).not.toHaveBeenCalled();
  });

  it("rebuilds the live-runs inventory from the endpoint", () => {
    // Events were lost both ways during the gap, so the presence projection is re-read (the degrade
    // rule is run-store.test.ts's).
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockRebuildLiveRuns).toHaveBeenCalledTimes(1);
  });

  // Run node state is APPLIED from `run_progress`, so frames lost in a gap leave a stale tree.
  it("re-reads every cached run, because the progress frames it missed were applied ones", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockInvalidateCachedRuns).toHaveBeenCalledTimes(1);
  });

  // ONE token for both run readers (one request per live run, not two); its meaning is
  // run-store.test.ts's.
  it("threads ONE cause and the run's signal through both of its run readers", () => {
    setSessions([makeSession("a")]);
    const signal = fireReconcile();

    const cause = mockInvalidateCachedRuns.mock.calls[0]?.[0];
    expect(typeof cause).toBe("string");
    expect(cause).not.toBe("");
    expect(mockRebuildLiveRuns).toHaveBeenCalledWith(cause, signal);
  });

  it("derives the token from the cause, so two reconciles are two tokens", () => {
    setSessions([makeSession("a")]);
    fireReconcile("full:hello");
    fireReconcile("must_refetch");

    const first = mockInvalidateCachedRuns.mock.calls[0]?.[0];
    const second = mockInvalidateCachedRuns.mock.calls[1]?.[0];
    expect(second).not.toBe(first);
  });

  it("passes the run's signal to the list read and the catalog read", () => {
    setSessions([makeSession("a")]);
    const signal = fireReconcile();
    expect(mockLoadList).toHaveBeenCalledWith(signal);
    expect(mockFetchCatalog).toHaveBeenCalledWith({ signal });
  });

  it("drops every held turn-rail index, which no stamp certifies", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockInvalidateTurnRails).toHaveBeenCalledTimes(1);
  });

  // The catalog rides no frame and is fetched at boot and login only, so a gap must re-read it.
  it("re-reads the mode/model catalog, which no frame announces", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockFetchCatalog).toHaveBeenCalledTimes(1);
  });

  // A reconcile closes NO tab: membership by set difference over two separate fetches races. It
  // re-reads the tab collection (app.ts → `listTabs`); the coordinator closes deleted chats' tabs.
  it("closes NO tab, whatever the chat list came back holding", async () => {
    expect.assertions(2);
    setSessions([makeSession("s1")]);
    mockHasTab.mockReturnValue(true);

    fireReconcile();
    // Flush the loadList continuation, the one place a close could be dispatched from.
    await mockLoadList();

    expect(mockLoadList).toHaveBeenCalled();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("does not ask the tab store what is open either", async () => {
    // The other half: with no set to difference against, the handler has no reason
    // to enumerate the strip at all. `getOpenTabIDs` went with the reconcile, so
    // there is nothing left here to reach it with.
    expect.assertions(2);
    setSessions([makeSession("s1")]);
    fireReconcile();
    await mockLoadList();
    expect(mockHasTab).not.toHaveBeenCalled();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  // ONE refresh, whatever kind of tab is on screen: the handler asks the projection
  // for the active VIEW instead of the store for the active CHAT, so the git, docs,
  // files, run and editor kinds are healed by the same line the chat kind is.
  it("refreshes the active view exactly once", () => {
    setSessions([makeSession("active-chat")]);
    setActive("active-chat");
    fireReconcile();
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
  });

  // `getActiveId()` (the last chat) and the active TAB diverge, so the store's "active" chat is a
  // BACKGROUND view that heals at its next activation.
  it("refetches no chat window of its own, even one the store still calls active", () => {
    setSessions([makeSession("bg-1"), makeSession("last-viewed"), makeSession("bg-2")]);
    setActive("last-viewed");
    fireReconcile();
    expect(mockLoadMessages).not.toHaveBeenCalled();
    expect(mockRefreshTurnRail).not.toHaveBeenCalledWith("last-viewed");
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
  });

  // Nothing else in this handler reaches the dispatcher, so a resume's whole effect on it
  // is this one call. It forgets NOTHING: the chat kinds are answered by the adapter's
  // digest, and the views whose kind has no subject read stale on their own.
  it("refreshes the active view on a page resume, and forgets no held version", () => {
    const fresh = makeSession("a", { residency: "loaded" });
    setSessions([fresh]);
    observeStamp({ kind: "chat", ref: "a", version: "1" });
    fireResume();
    expect(mockRefreshActiveView).toHaveBeenCalledTimes(1);
    expect(transcriptStale(get("a")!)).toBe(false);
  });

  // The stream clears held versions before this body runs, and the hello's snapshot frames replace
  // the dock sets, so clearing them here would race those frames.
  it("leaves the decision dock and the steers to the snapshot frames", async () => {
    const { pushDecision, hasPendingDecision, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("a")]);
    pushDecision({
      kind: "permission",
      chatID: "a",
      runID: "",
      requestID: 1,
      payload: { request_id: 1, title: "run a command", options: [] } as never,
      submit: vi.fn(),
    });
    recordSteerQueued("a", { id: "steer-a", text: "one", origin: "user" });

    fireReconcile();

    expect(hasPendingDecision("a")).toBe(true);
    expect(steerCount("a")).toBe(1);
  });

  it("clears this client's own memory and leaves the server's statements standing", () => {
    // A gap asserts nothing about outcomes (the server's `last_turn_outcome` and `turn_open`); it
    // clears only `thinking`. A stale `turn_open: true` therefore outlives the gap until a window GET.
    setSessions([
      makeSession("a", { thinking: true, last_turn_outcome: "completed" }),
      makeSession("b", { thinking: true, turn_open: true }),
    ]);

    fireReconcile();

    expect(get("a")?.thinking, "this client's memory of a stream, gone").toBe(false);
    expect(get("a")?.last_turn_outcome, "the server's verdict, untouched").toBe("completed");
    expect(get("b")?.turn_open, "and the server's own liveness with it").toBe(true);
    expect(tabStatusFor(get("b")), "so the dot still reads live").toBe("working");
  });
});

// The connect hook's pending set: WHOLE, possibly empty, replaced atomically; each item
// re-dispatches through the real envelope door and handler.

describe("pending_snapshot handler", () => {
  it("drops every unanswered ask, because the snapshot carries every live one", async () => {
    // An ask whose answering frame this client never saw (answered on another device
    // while this one was away) leaves no frame behind; only the whole current set takes
    // it off the screen.
    const { pushDecision, hasPendingDecision, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("a")]);
    pushDecision({
      kind: "permission",
      chatID: "a",
      runID: "",
      requestID: 1,
      payload: { request_id: 1, title: "run a command", options: [] } as never,
      submit: vi.fn(),
    });
    expect(hasPendingDecision("a")).toBe(true);

    fireSSE("pending_snapshot", "", { items: [] });
    expect(hasPendingDecision("a")).toBe(false);
  });

  it("drops a RUN-keyed ask too, which the per-session sweep cannot reach", async () => {
    // The sweep walks `getSessions()`, and `run:<workflowId>` is no chat, so a
    // parentless run's ask — and any ask the server reconstructed for a run nothing
    // hosts — would escape it.
    const { pushDecision, runPendingAsks, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("a")]);
    pushDecision({
      kind: "run_input",
      chatID: "run:wf_1",
      runID: "wf_1",
      askID: "reconciled:root/review",
      payload: {
        workflow_id: "wf_1",
        ask_id: "reconciled:root/review",
        node_id: "review",
        step_session_id: "sess_step",
        agent_name: "reviewer",
        question: "",
        asked_at: "2026-09-03T10:00:00Z",
      },
      submit: vi.fn(),
    });
    expect(runPendingAsks("wf_1").count).toBe(1);

    fireSSE("pending_snapshot", "", { items: [] });
    expect(runPendingAsks("wf_1").count).toBe(0);
  });

  it("clears every session's steers, because the snapshot re-offers the waiting ones", () => {
    // A chip saying "the agent hasn't read this" is an assertion about the server; the
    // snapshot is the server saying which are still waiting, under their own ids.
    setSessions([makeSession("a"), makeSession("b", { thinking: true })]);
    recordSteerQueued("a", { id: "steer-a", text: "one", origin: "user" });
    recordSteerQueued("b", { id: "steer-b", text: "two", origin: "user" });

    fireSSE("pending_snapshot", "", { items: [] });
    expect(steerCount("a")).toBe(0);
    expect(steerCount("b")).toBe(0);
  });

  it("clears every run step's dock too, which no session row reaches", async () => {
    const { recordStepSteerQueued, stepSteers } = await import("../run-step-steers.js");
    recordStepSteerQueued("wf_1", "root/review", { id: "steer-s", text: "one", origin: "user" });
    expect(stepSteers("wf_1", "root/review")).toHaveLength(1);

    fireSSE("pending_snapshot", "", { items: [] });
    expect(stepSteers("wf_1", "root/review")).toEqual([]);
  });

  it("clears the dock and touches no steer entry, because an entry is a fact", () => {
    // A steer's transcript fact is its own `steer` ENTRY, appended where it landed and
    // carrying `state`. The dock holds CLAIMS about the server, which the snapshot
    // replaces; the log holds what happened, which it may not rewrite.
    setSessions([makeSession("a")]);
    setActive("a");
    openTurnOn("a");
    steerEntryOn("a", "steer-read", "read one");
    recordSteerQueued("a", { id: "steer-waiting", text: "unresolved", origin: "user" });

    fireSSE("pending_snapshot", "", { items: [] });

    expect(steerCount("a"), "the dock is forgotten").toBe(0);
    expect(entryIDs("a"), "the record survives").toEqual(["t1-open", "steer-read"]);
  });

  it("re-dispatches every item through the envelope door, in order, after the clears", () => {
    setSessions([makeSession("a")]);
    fireSSE("pending_snapshot", "", {
      items: [
        { type: "permission_needed", chat_id: "a", payload: { request_id: 1 } },
        { type: "steer_queued", chat_id: "a", payload: { id: "s1", text: "t" } },
      ],
    });
    expect(dispatched.map((e) => (e as { type: string }).type)).toEqual([
      "permission_needed",
      "steer_queued",
    ]);
    expect((dispatched[0] as { chat_id?: string }).chat_id).toBe("a");
  });

  it("drops one malformed item and keeps dispatching the rest", () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    fireSSE("pending_snapshot", "", {
      items: [{ chat_id: "a" }, { type: "steer_queued", chat_id: "a", payload: { id: "s1" } }],
    });
    expect(dispatched.map((e) => (e as { type: string }).type)).toEqual(["steer_queued"]);
    expect(errors).toHaveBeenCalledTimes(1);
  });

  it("retracts every chat and run banner the snapshot does not name, whether or not this tab held the ask", async () => {
    // a's and wf_1's asks were answered elsewhere, so their banners come down (a never rendered here);
    // b's is re-offered and stays. The registration is faked to observe the closed tags.
    const { pushDecision, _resetForTest } = await import("../decision-dock.js");
    const { _setRegistrationForTest } = await import("../notify.js");
    _resetForTest();
    const closed: string[] = [];
    _setRegistrationForTest(() =>
      Promise.resolve({
        getNotifications: () =>
          Promise.resolve(
            [
              "marotte:a",
              "marotte:b",
              "marotte:run:wf_1",
              "marotte:run:wf_2",
              "marotte:pr:x#1",
            ].map((tag) => ({ tag, close: () => closed.push(tag) }) as unknown as Notification),
          ),
      }),
    );
    try {
      setSessions([makeSession("a"), makeSession("b")]);
      pushDecision({
        kind: "permission",
        chatID: "b",
        runID: "",
        requestID: 1,
        payload: { request_id: 1, title: "run a command", options: [] } as never,
        submit: vi.fn(),
      });
      fireSSE("pending_snapshot", "", {
        items: [
          { type: "permission_needed", chat_id: "b", payload: { request_id: 1 } },
          // Chat-parented, so the envelope's chat id is the launching chat and the
          // run's tag has to be derived from the payload rather than read off it.
          {
            type: "run_input_needed",
            chat_id: "c",
            payload: {
              workflow_id: "wf_2",
              ask_id: "reconciled:root/review",
              node_id: "review",
              step_session_id: "sess_step",
              agent_name: "reviewer",
              question: "",
              asked_at: "2026-09-03T10:00:00Z",
            },
          },
        ],
      });
      await vi.waitFor(() => {
        expect(closed).toEqual(["marotte:a", "marotte:run:wf_1"]);
      });
    } finally {
      _setRegistrationForTest(null);
    }
  });

  it("keeps a step's permission banner under the RUN's tag, which is the tag it was shown under", async () => {
    // A step's ask is tagged `askTarget(chatID, run_id)` (the run's slot), so the sweep must compute
    // the same tag.
    const { _setRegistrationForTest } = await import("../notify.js");
    const closed: string[] = [];
    _setRegistrationForTest(() =>
      Promise.resolve({
        getNotifications: () =>
          Promise.resolve(
            ["marotte:a", "marotte:run:wf_3"].map(
              (tag) => ({ tag, close: () => closed.push(tag) }) as unknown as Notification,
            ),
          ),
      }),
    );
    try {
      setSessions([makeSession("a")]);
      fireSSE("pending_snapshot", "", {
        items: [
          {
            type: "permission_needed",
            chat_id: "a",
            payload: { request_id: 1, run_id: "wf_3", node_id: "review" },
          },
        ],
      });
      await vi.waitFor(() => {
        expect(closed).toEqual(["marotte:a"]);
      });
    } finally {
      _setRegistrationForTest(null);
    }
  });
});

// The retained waiting-status set REPLACES every chat's agent status.

describe("status_snapshot handler", () => {
  it("sets the status of every chat a row names", () => {
    setSessions([makeSession("a"), makeSession("b")]);
    fireSSE("status_snapshot", "", {
      rows: [{ chat_id: "a", status: "waiting_on_user", description: "needs a key" }],
    });
    expect(get("a")?.agent_status).toBe("waiting_on_user");
  });

  it("clears the status of every chat no row names", () => {
    setSessions([
      makeSession("a", { agent_status: "waiting_on_user" }),
      makeSession("b", { agent_status: "in_progress" }),
    ]);
    fireSSE("status_snapshot", "", { rows: [] });
    expect(get("a")?.agent_status ?? "").toBe("");
    expect(get("b")?.agent_status ?? "").toBe("");
  });

  it("ignores a row for a chat the store does not hold", () => {
    setSessions([makeSession("a")]);
    expect(() =>
      fireSSE("status_snapshot", "", { rows: [{ chat_id: "ghost", status: "waiting_on_user" }] }),
    ).not.toThrow();
    expect(get("a")?.agent_status ?? "").toBe("");
  });
});

describe("mode_changed handler", () => {
  it("reflects the new mode id on the chat", () => {
    setSessions([makeSession("chat-1", { current_mode_id: "" })]);
    fireSSE("mode_changed", "chat-1", { mode_id: "plan" });
    expect(get("chat-1")?.current_mode_id).toBe("plan");
  });

  it("ignores an empty mode id (current mode unchanged)", () => {
    setSessions([makeSession("chat-1", { current_mode_id: "build" })]);
    fireSSE("mode_changed", "chat-1", { mode_id: "" });
    expect(get("chat-1")?.current_mode_id).toBe("build");
  });

  // No empty-chat case: `setCurrentMode`'s missing-session guard no-ops it whatever this handler's
  // guard does, so a case could not fail.
});

// The gap door and the turn_closed door share one core: clear `thinking`, repaint the tab dot.

describe("the reconcile runs the shared turn teardown", () => {
  it("reaches every chat, and asks for no per-chat rail read", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("");

    fireReconcile();

    expect(get("chat-1")?.thinking).toBe(false);
    // And every chat, not only the active one: a gap describes the connection.
    expect(get("chat-2")?.thinking).toBe(false);
    // The rail left the shared teardown: a reconcile makes every chat's index equally
    // unsupportable, which `invalidateTurnRails` records, so no per-chat GET goes out
    // here — each rail heals on activation.
    expect(mockRefreshTurnRail).not.toHaveBeenCalled();
    expect(mockInvalidateTurnRails).toHaveBeenCalledTimes(1);
  });
});

// `refreshRetention` and the spawn-time capability reads carry the EFFECT; this re-seeds the
// controls on a second device (as `syncSettings` does for notifications).
describe("settings_updated re-seeds the General panel", () => {
  it("hands the refetched payload to applyGeneralPanel", async () => {
    const { syncSettings, applyGeneralPanel } = await import("../settings.js");
    const payload = { chat_retention_days: -1 };
    vi.mocked(syncSettings).mockResolvedValueOnce(payload as never);

    fireSSE("settings_updated", "", {});
    await vi.waitFor(() => {
      expect(applyGeneralPanel).toHaveBeenCalledWith(payload);
    });
  });

  it("seeds nothing when the refetch failed, and refuses rather than throwing", async () => {
    const { syncSettings, applyGeneralPanel } = await import("../settings.js");
    // Null is a failed fetch: seeding from it would clear every control. Every route past the guard
    // dereferences `s` first, so the case also watches for an unhandled rejection.
    vi.mocked(syncSettings).mockResolvedValueOnce(null);
    const rejected: unknown[] = [];
    const onReject = (e: PromiseRejectionEvent): void => {
      rejected.push(e.reason);
    };
    window.addEventListener("unhandledrejection", onReject);
    try {
      fireSSE("settings_updated", "", {});
      // Two task boundaries: the rejection is raised inside the refetch's own `.then`, so
      // the event is delivered a task after the microtask that produced it.
      await new Promise((r) => setTimeout(r, 0));
      await new Promise((r) => setTimeout(r, 0));

      expect(applyGeneralPanel).not.toHaveBeenCalled();
      expect(rejected, "the refusal is a return, not a throw").toEqual([]);
    } finally {
      window.removeEventListener("unhandledrejection", onReject);
    }
  });
});
