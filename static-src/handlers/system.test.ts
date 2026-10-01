// Tests for handlers/system.ts: the `connected` handshake and its two-directional thinking
// reconcile, the BUS_RECONCILE body, the two connect-hook snapshots, the page resume and
// mode_changed.
//
// Drives the REAL store. LIVENESS IS THE LOG, so a case needing a running turn opens one
// and a case needing a settled chat carries the header's own `last_turn_outcome`; there are
// no `turn_done` / `turn_failed` latches and no live-turn marker. store-load.ts and tabs.ts
// stay mocked, so the reload and dispatcher assertions verify the command at that boundary.

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
// The gap door's answer to a failed list load. A spy, because the LADDER is
// store-load.test.ts's subject (it owns the reach gate, the delays and the bound);
// what this file owns is that the door consults it and only on a failure.
const mockScheduleListRetry = vi.fn();

const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => true);
// The dispatcher door. The gate itself is tabs.test.ts's subject; what this file
// owns is that the gap and the resume reach it, once, and after the epoch bump.
const mockRefreshActiveView = vi.fn();
vi.mock("../tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activateTab: undefined,
  // navigate.js's `openSpec` (the spec tab's door) imports these three beside
  // `activateTab` and the `tabIdFor` below, and Browser Mode links for real, so
  // one missing name fails this whole file's import.
  openTab: undefined,
  parentChatRef: undefined,
  setTabParent: undefined,
  getActiveTabId: undefined,
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
  // Reached through turn-teardown.ts, which the gap handler shares with the
  // turn_closed door. No-ops rather than present-but-undefined: the gap handler
  // CALLS both once per session, so undefined would throw rather than link.
  // tabIdFor answers "" (the no-tab answer), the mock rule's empty value.
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
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
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

// The mode/model catalog re-read. Mocked because the real one is a network fetch
// that seeds four controls, and because a call count is the whole assertion: the
// catalog is announced on no frame, so a gap is the only signal this client gets
// that a `config_option_update` or a server restart happened during the outage.
const mockFetchCatalog = vi.fn(() => Promise.resolve());
vi.mock("../session-catalog.js", () => ({ fetchCatalog: mockFetchCatalog }));

// The live-runs inventory rebuild. A PARTIAL mock, because the two doors want opposite
// things: the gap door's two run readers stay spies (what this file owns there is the token
// they share, and both are network reads), while `adoptConnectRuns` is the REAL function,
// because what the connect door owns IS the state it seeds — a spy could only say the
// handler called something, which is the shape that let this door ship with no test at all.
// Both spies go through `vi.hoisted` for the api-client pair's reason below: run-store is
// imported STATICALLY here, so the mocker resolves this factory during linking.
const { mockInvalidateCachedRuns, mockRebuildLiveRuns } = vi.hoisted(() => ({
  mockInvalidateCachedRuns: vi.fn(),
  mockRebuildLiveRuns: vi.fn(),
}));
vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  rebuildLiveRuns: mockRebuildLiveRuns,
  invalidateCachedRuns: mockInvalidateCachedRuns,
}));

// The two fetchers run-store reaches, replaced so a per-run read is OBSERVABLE: the
// connect adoption's promise is that it issues none, and the withheld-inventory fallback's
// is that it issues exactly one. Spread the real surface, so every other consumer keeps
// the module it had.
//
// Through `vi.hoisted` because `../run-store.js` is statically imported above and imports
// api-client, so the mocker resolves this factory during linking — above this file's own
// top-level initializers, where a plain `const` is still in its temporal dead zone.
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

// Capture SSE handlers (shared helper) + bus handlers (onBus) so we can fire
// both transport:reconcile and mode_changed. `decodeEnvelope` and `dispatch` are the
// REAL ones: the pending_snapshot handler's whole job is to push each item through
// that door, and a stub there would assert only that the handler called something.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
import type * as Bus from "../bus.js";
const busHandlers = new Map<string, (...args: unknown[]) => void>();
const dispatched: unknown[] = [];
vi.mock("../bus.js", async (importOriginal) => {
  const real = await importOriginal<typeof Bus>();
  return createBusMock({
    // Present-but-undefined so real-ESM linking succeeds: another module in this
    // graph imports the name, and Browser Mode links for real rather than reading
    // properties off a namespace object. `undefined` is what the node runner gave
    // these, so no path under test changes behavior.
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

/** Append the `steer` ENTRY that records a steer's transcript fact. */
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

/** Every entry the chat's resident log holds for `turnID`, by id. */
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

// The handshake is the only channel that states where the workspace is, and every
// relative agent path needs it to become openable (workspace.ts). It is recorded
// HERE rather than in transport.ts, whose own handshake hook returns early on the
// first connection of a page load — the connection that matters.
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

// The handshake's two NEGATIVE statements, neither of which any other frame carries.
// `busy_chats` is the only thing that ever says a chat this client believes is working is
// not: a turn that died with the previous process emits no terminal frame, so nothing else
// retracts that `thinking`. Both are
// guarded by their own STATED flag rather than by the list being non-empty, because a
// scoped, capped or withheld list is indistinguishable from a complete one — and read as
// complete, the first clears a live turn and the second drops live runs.

/** A handshake stating both halves. The two flags default TRUE here, which is the
 *  opposite of the wire default, so each case names the withholding it is about. */
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
    // A topic-filtered or over-cap connect states nothing about the chats it omits, so the
    // flag bounds the blast radius rather than making a clear over a live turn merely
    // unlikely. The runs are OUTSIDE that gate on purpose: the inventory is
    // workspace-global, carries its own flag, and an early return must not swallow it.
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
    // The retraction is NARROW. `busy_chats` deliberately
    // omits a chat whose only open turn is a workflow STEP, so a chat reached here may
    // still be streaming — and the reply is entries in the log now rather than a marker,
    // so the retraction has to leave the window alone. It clears this client's `thinking`
    // and writes no entry, closes no turn and drops no window.
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
    // The dot's verdict has ONE source —
    // the header's `last_turn_outcome`, which this handler never writes — so what the adopt
    // arm has to clear is the SERVER's previous liveness statement: a `turn_open: false`
    // from a GET taken before this turn started, which `turnLive` reads as settled and
    // which would therefore outlive the handshake that contradicts it.
    setSessions([makeSession("a", { turn_open: false, last_turn_outcome: "completed" })]);
    fireConnected({ busy_chats: ["a"] });
    expect(get("a")?.thinking).toBe(true);
    expect(get("a")?.turn_open, "the previous turn's liveness, not this one's").toBeUndefined();
  });

  it("reaches the composer, and leaves the dot to the log", () => {
    // NEW, and MEASURED rather than assumed — recorded as an observation for the 4c run.
    // `thinking` is not an input to `turnLive`, so an adopted chat whose window is not
    // resident keeps whatever the header's own outcome grades to; the dot cannot read
    // `working` from this arm alone. What the arm does reach is `isThinking`, which is what
    // the composer reads to offer Cancel over Send.
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
    // On the TRANSITION only, like `markTurnLive`: `setThinking(true)` clears the previous
    // turn's carriers, and `agent_status` is the one a live turn can still be holding —
    // the agent declares `waiting_on_user` mid-turn and keeps working, and that claim is
    // deliberately retained because a person still owes an answer. Re-clearing it on every
    // reconnect is what the gate prevents.
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
    // Three seeds per row, and a reload loses each one differently: without the inventory
    // a chat with a live run is evicted mid-run, without the pairing that run's tab opens
    // at the end of the strip instead of under its conversation, and without the observer
    // the row is seeded and nothing repaints — so the tab keeps the factory's placeholder
    // label for as long as the run takes.
    const seen: string[] = [];
    registerLiveRunObserver((id) => seen.push(id));
    setSessions([makeSession("a")]);

    fireConnected({ live_runs: [{ workflow_id: "wf-1", chat_id: "a", executing: true }] });

    expect(liveRunsForChat("a")).toEqual([{ id: "wf-1", chat: "a", executing: true }]);
    expect(runChatID("wf-1")).toBe("a");
    expect(seen).toEqual(["wf-1"]);
  });

  it("issues no per-run read of its own", () => {
    // The one thing the connect adoption deliberately does NOT do, and the reason it can
    // be free: the mark's floor paints a live run's square from THIS frame, so the per-run
    // `inspect` is a refinement rather than a precondition. Passing a cause here would put
    // one GET per live run on every reconnect.
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
    // The inventory is event-fed, and the gap means events were lost in both
    // directions: a run that started or settled inside the outage leaves the
    // inventory blind. Re-reading the presence projection is the heal; the degrade
    // rule (a failed rebuild keeps event-fed state) is run-store.test.ts's subject.
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockRebuildLiveRuns).toHaveBeenCalledTimes(1);
  });

  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost in the outage leave a stale tree with nothing to notice it — a
  // node that completed during the gap keeps reading `running` and its clock keeps
  // ticking. A gap is the one moment the client knows it missed frames.
  it("re-reads every cached run, because the progress frames it missed were applied ones", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockInvalidateCachedRuns).toHaveBeenCalledTimes(1);
  });

  // ONE token for both run readers, which is what takes a gap's run cost from two
  // requests per live run to one. They act on the same event a network round trip
  // apart — `rebuildLiveRuns` invalidates each live run only once
  // `/api/runs/live` has answered — so the token is the only thing that can say
  // they are the same event. What the token MEANS is run-store.test.ts's subject.
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

  // The catalog sits in one workspace-global holder that no frame announces a change to,
  // and `fetchCatalog` runs at boot and after login otherwise — so without this read a
  // model list that moved during the outage, or a server restart that empties the holder
  // until a bridge respawns, leaves the picker on whatever boot answered until a reload.
  it("re-reads the mode/model catalog, which no frame announces", () => {
    setSessions([makeSession("a")]);
    fireReconcile();
    expect(mockFetchCatalog).toHaveBeenCalledTimes(1);
  });

  // A reconcile closes NO tab, and adding one in any form is membership by SET DIFFERENCE
  // over two separately fetched collections: the two answers race, and a chat absent from
  // one is not evidence its tab was closed. It re-reads the TAB collection instead (app.ts
  // wires `transport:reconcile` to `listTabs`), and a chat the server deleted has already
  // had its tabs closed by the membership coordinator, under the record's own lock.
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

  // THE RULED BEHAVIOUR CHANGE (design rev 5). `getActiveId()` is the active CHAT and
  // the projection's active row is the active TAB, and the two diverge because no
  // production `setActive` runs on a non-chat activation — so with a git tab on screen
  // the store still names the last-viewed chat. That chat is a BACKGROUND view now and
  // heals at its next activation, like every other background view.
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

  // The held versions are the stream's to clear: the reconcile is emitted AFTER the bind
  // that emptied the map, so a background chat already reads stale by the time this body
  // runs, and the body must not empty the docks either — the fresh hello's own snapshot
  // frames replace those sets, and clearing them here would race the frames.
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
    // A gap can assert nothing about how anything finished: the verdict is the SERVER's
    // (the header's `last_turn_outcome`, graded on every read), and so is `turn_open`; what
    // it clears is `thinking`, this client's own memory of a stream.
    // HAND-OFF for the 4c run, measured here: a stale `turn_open: true` therefore outlives
    // the gap, so a chat the server last called live keeps reading `working` until a window
    // GET or a handshake re-states it — and the next `connected` frame's `busy_chats` does
    // not write that field either, so nothing retracts it.
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

// ---------------------------------------------------------------------------
// The connect hook's pending set: WHOLE, possibly empty, replaced atomically. Every
// item is an envelope the live path would have published, so it re-dispatches
// through the real envelope door and its real handler.
// ---------------------------------------------------------------------------

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
    // The fresh hello's snapshot still names b's ask and wf_2's; a's and wf_1's were
    // answered while this client was away and their banners come down. a's ask was never
    // rendered here — a tab that slept through the ask is the one holding the banner, so
    // its own decision state cannot be the gate. b's stays: the item re-offers it, and a
    // retraction there would close the banner for a live question. The registration is
    // faked so the tags closed are observable.
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
    // `handlers/turn.ts` tags a request-shaped ask by `askTarget(chatID, run_id)`: a
    // step's ask is ABOUT its run, so its banner sits in the run's slot, not the
    // launching chat's. The sweep has to compute the same tag or it closes the banner
    // for a live question and leaves the chat's slot, which holds nothing, alone.
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

// ---------------------------------------------------------------------------
// The connect hook's retained waiting-status set: every chat's agent status is
// REPLACED from it, so a `waiting_on_user` answered elsewhere clears and one still
// owed comes back on a second device.
// ---------------------------------------------------------------------------

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

  // THE EMPTY-CHAT CASE IS DELETED, and its absence is deliberate: an empty chat id names
  // no session, so `setCurrentMode`'s own missing-session guard makes the write a no-op
  // whatever this handler's guard does. The case was written, red-checked against the guard's
  // removal, found to pass anyway, and deleted rather than left as coverage. The guard stays
  // in production as a redundant producer that says the intent at the door.
});

// ---------------------------------------------------------------------------
// The gap door and the turn_closed door share ONE outcome-independent core, and it
// is two statements now: clear `thinking`, repaint the tab dot from what the log
// and the header say. What the rail does here is its own line in the gap body.
// ---------------------------------------------------------------------------

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

// The General panel's own CONTROLS. `refreshRetention` carries the retention
// EFFECT and the three capability keys are read per spawn, so before this arm the
// checkboxes on a second device kept showing the old value under an effect that
// had already changed. Notifications already worked this way through
// `syncSettings`; this is the same shape for the rest of the panel.
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
    // Null is a failed fetch, not "the settings are the defaults": seeding the
    // panel from nothing would clear every control the reader can see.
    //
    // THE REJECTION IS THE OBSERVABLE, and the panel assertion alone cannot be. Every route
    // past the null guard dereferences `s` on its very first statement, so a handler that
    // reached one would throw inside a `.then` with no catch: `applyGeneralPanel` goes
    // uncalled either way, and the case would pass because the code CRASHED rather than
    // because it refused. So the case watches for the unhandled rejection too.
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
