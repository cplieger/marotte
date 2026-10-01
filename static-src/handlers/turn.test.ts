// ---------------------------------------------------------------------------
// Tests for handlers/turn.ts: ERROR_ROUTES, the three asks, decision_settled, the
// error handler and turn_closed. Drives the REAL handlers and the REAL store.
//
// LIVENESS IS THE LOG, so a case needing a running turn opens one and the close is
// an ordinary append: no summary stamp, no live-turn marker, no verdict latch. A
// sibling subsystem stays mocked because a call into one is a command.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
// Capture SSE handlers via shared helper. FIRST, above every other import: the factory
// below closes over `createBusMock`, and ESM evaluates imported modules in the source
// order of their declarations, so with this import below `../store.js` the mocker
// resolves the factory while linking that graph and the binding is still uninitialized.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());
import {
  setSessions,
  setActive,
  get,
  appendEntry,
  defaultUsage,
  openTurn,
  recordSteerQueued,
  recordSteerSent,
  registerTurnRepair,
  steerCount,
  setAgentStatus,
  tabStatusFor,
  dropSteers,
  pendingSteerCarry,
} from "../store.js";
import { noteRunLive, noteRunSettled } from "../run-store.js";
import type { Session } from "../types.js";
import type { Entry, EntryTurnClose, TurnOutcome } from "../wire/types.gen.js";
import { severityOf } from "../turn-severity.js";
import type * as ApiClient from "../api-client.js";
import type * as ChatActions from "../actions/chat.js";

// The run store's fetcher. Replaced so the live-run cases below seed the inventory
// without a real request, and spread rather than swapped so every other consumer in
// this graph keeps the module it had.
//
// Through `vi.hoisted` because `run-store.js` is statically imported below and imports
// api-client, so the mocker resolves this factory during linking — above this file's own
// top-level initializers, where a plain `const` is still in its temporal dead zone.
const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockApiGetTyped,
}));

// scroll.ts touches DOM elements at import; use the shared mock.
vi.mock(
  "../scroll.js",
  async () => (await import("../__test-helpers__/scroll-mock.js")).scrollMock,
);

// Through `vi.hoisted` for the reason the notify trio below states: the mocker resolves
// these factories during LINKING, above this file's own top-level initializers, where a
// plain `const` is still in its temporal dead zone.
const { mockCollapseSettled, mockHasPendingDecision, mockDropTurnDecisions } = vi.hoisted(() => ({
  mockCollapseSettled: vi.fn(),
  mockHasPendingDecision: vi.fn(() => false),
  mockDropTurnDecisions: vi.fn(),
}));
vi.mock("../decision-dock.js", () => ({
  pushDecision: vi.fn(),
  collapseSettledDecision: mockCollapseSettled,
  hasPendingDecision: mockHasPendingDecision,
  dropTurnDecisions: mockDropTurnDecisions,
}));

vi.mock("../attachments.js", () => ({
  addAttachment: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: composer-state.ts is in
  // this graph now (the tab projection reaches it), and it imports the rest.
  addAttachmentTo: vi.fn(),
  attachmentGeneration: vi.fn(() => 0),
  takeAttachments: vi.fn(() => []),
  stashAttachments: vi.fn(),
  flushAttachments: vi.fn(),
  restoreAttachments: vi.fn(),
  dropAttachments: vi.fn(),
  seedAttachments: vi.fn(),
  adoptRemoteAttachments: vi.fn(),
  _resetAttachmentsForTest: vi.fn(),
}));

const { mockSetAgentDown, mockClearAgentDown } = vi.hoisted(() => ({
  mockSetAgentDown: vi.fn(),
  mockClearAgentDown: vi.fn(),
}));
vi.mock("../send-state.js", () => ({
  setAgentDown: mockSetAgentDown,
  clearAgentDown: mockClearAgentDown,
  setSSEStatus: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: steer-resend.js is in this graph
  // (the settled arm fires the boundary resend) and imports the name for its
  // give-up path, which no case here reaches.
  reportSendRefused: vi.fn(),
}));

// The boundary resend is driven for REAL in this file — the settled arm is its one
// firing point, so mocking it would leave the feature's whole trigger unpinned — and
// only its two outward calls are replaced. `sendPromptTo` is what a resent turn IS,
// and `clearSteers` would otherwise POST for every boundary in the file.
const { mockSendPromptTo } = vi.hoisted(() => ({
  mockSendPromptTo: vi.fn((_chatID: string, _text: string, _opts?: { messageID?: string }) =>
    Promise.resolve<"sent" | "failed">("sent"),
  ),
}));
vi.mock("../chat-commands.js", () => ({
  sendPromptTo: mockSendPromptTo,
  switchModel: vi.fn(),
}));
const { mockClearSteers } = vi.hoisted(() => ({
  mockClearSteers: vi.fn(() => Promise.resolve(true)),
}));
vi.mock("../actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  clearSteers: { dispatch: mockClearSteers },
}));

const { mockReportFailure } = vi.hoisted(() => ({ mockReportFailure: vi.fn() }));
vi.mock("../failure-notice.js", () => ({
  reportFailure: mockReportFailure,
  // Present-but-undefined so real-ESM linking succeeds: actions/chat.js is in this
  // graph and imports the name, and Browser Mode links for real rather than
  // reading properties off a namespace object. No path under test calls it.
  clearFailure: undefined,
}));

// There is no isPermissionNeededEnabled to mock: the permission ask has no per-kind
// switch, so the three ask handlers notify unconditionally and only the master gate
// inside notifyIfHidden applies.

// A HOLDER rather than a constant, so the agent-finished notification can be driven in
// its own block while staying off elsewhere — the permission-class block depends on
// that contrast to be non-vacuous. Through `vi.hoisted` because the factory CLOSES
// OVER it: the mocker resolves it above this file's own top-level initializers, where
// a plain `const` is in its temporal dead zone and the file dies in linking.
const { mockNotifyIfHidden, mockCloseNotificationsFor, notifyGate } = vi.hoisted(() => ({
  mockNotifyIfHidden: vi.fn(),
  mockCloseNotificationsFor: vi.fn(() => Promise.resolve()),
  notifyGate: { agentFinished: false },
}));
vi.mock("../notify.js", () => ({
  notifyIfHidden: mockNotifyIfHidden,
  closeNotificationsFor: mockCloseNotificationsFor,
  setBadge: vi.fn(),
  isAgentFinishedEnabled: () => notifyGate.agentFinished,
  NOTIFY_TITLE: "marotte",
}));

const { mockOpenSetting } = vi.hoisted(() => ({ mockOpenSetting: vi.fn() }));
vi.mock("../settings-highlight.js", () => ({ openSetting: mockOpenSetting }));

// The sign-in CTA's destination. Mocked because a call into it is a command at the
// handler's boundary, and the real module wires a whoami poll at import.
const { mockShowLoginModal } = vi.hoisted(() => ({ mockShowLoginModal: vi.fn() }));
vi.mock("../modals.js", () => ({ showLoginModal: mockShowLoginModal }));

vi.mock("../git.js", () => ({ refreshGitBadge: vi.fn() }));

// `refreshTurnRail` IS the assertion: turn.ts fires it fire-and-forget and three cases below
// read it as a spy, so removing this mock fails them with "is not a spy" rather than changing
// what the handler does. Every other export is replaced for a second reason of its own: the
// real fetchers issue GET /api/chats/{id}/turns at the page's own base URL and leave one
// request per frame for the window teardown to abort and print as an unhandled AbortError.
// Permanent, not scaffolding: a call into the rail is a command at the handler's boundary.
vi.mock("../turn-rail.js", () => ({
  invalidateTurnRails: vi.fn(),
  mountTurnRail: vi.fn(),
  pointTurnRail: vi.fn(),
  loadTurnRail: vi.fn(() => Promise.resolve()),
  refreshTurnRail: vi.fn(() => Promise.resolve()),
  resetTurnRail: vi.fn(),
  setResidentTurns: vi.fn(),
  initTurnRailCallbacks: vi.fn(),
}));

import { refreshTurnRail } from "../turn-rail.js";

// Import after mocks so turn.ts registers its handlers against the bus mock.
const { ERROR_ROUTES } = await import("./turn.js");
// After the mocks for the same reason turn.ts is: the cue module imports ../notify.js,
// so a STATIC import here links it against the real module before the mocker is ready
// and the whole file dies in module linking.
const { forgetDeferredCue, hasDeferredCue } = await import("../agent-finished-cue.js");
// After the mocks for the same reason: it reaches chat-commands and actions/chat, both
// replaced above. The REAL module, because the settled arm is the resend's one firing
// point and a mock would leave the trigger unpinned; `forgetSteerResend` is how each
// case resets the per-chat slot it holds.
const { forgetSteerResend, noteBoundaryDrop } = await import("../steer-resend.js");

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
function openTurnOn(chatID: string, turnID = "t1", n = 1): void {
  const open: Entry = {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n },
  };
  openTurn(chatID, open);
}

/** The `turn_close` entry the frame carries. `seq` defaults to 1, which is what fits a
 *  turn holding only its own `turn_open`; a case wanting the hole passes its own. */
function closeEntry(turnID: string, payload: Partial<EntryTurnClose> = {}, seq = 1): Entry {
  return {
    id: `${turnID}-close`,
    turn: turnID,
    kind: "turn_close",
    seq,
    ts: 2,
    payload: { outcome: "completed", ...payload },
  };
}

/** Fire the close frame. `workflow_id` is the RUN partition and is absent for a chat's
 *  own turn, so the default omits it rather than sending "". */
function fireClose(
  chatID: string,
  opts: {
    turnID?: string;
    payload?: Partial<EntryTurnClose>;
    seq?: number;
    workflowID?: string;
  } = {},
): void {
  const entry = closeEntry(opts.turnID ?? "t1", opts.payload ?? {}, opts.seq ?? 1);
  fireSSE(
    "turn_closed",
    chatID,
    opts.workflowID === undefined ? { entry } : { entry, workflow_id: opts.workflowID },
  );
}

/** A chat mid-turn: one open turn of its own, `thinking` latched by the frames that
 *  streamed into it, and the server's `live` recorded from the newest-page GET. */
function seedLive(chatID = "chat-1", over: Partial<Session> = {}): void {
  setSessions([makeSession(chatID, { thinking: true, turn_open: true, ...over })]);
  setActive(chatID);
  openTurnOn(chatID);
}

const mockRepairTurn = vi.fn();

beforeEach(() => {
  vi.clearAllMocks();
  // The armed slot is per chat and the module is cached, so the reset is its own
  // forget rather than a re-import.
  for (const id of ["chat-1", "chat-2"]) {
    forgetSteerResend(id);
  }
  mockSendPromptTo.mockResolvedValue("sent");
  // `mockReset` is on, so an implementation set at construction is gone by now. A null
  // answer is what a 404 gives, which is what every case that does not stub a run wants.
  mockApiGetTyped.mockResolvedValue(null);
  // The range read a hole asks for. Registered here rather than asserted through a real
  // fetch: the LADDER is store-load.test.ts's subject, and what this file owns is that
  // the append reaches the seam.
  registerTurnRepair(mockRepairTurn);
  setSessions([]);
  document.body.innerHTML = '<div id="messages"></div>';
});

describe("ERROR_ROUTES", () => {
  // A route carries a SURFACE and an optional in-app remedy, and nothing else. There
  // is deliberately no turn-scoped field: whether a failure finalized a turn is a
  // property of the emission, so the server states it per frame — see the two
  // no-turn cases in the "error handler" block below for what a per-code answer cost.
  const expectedRoutes: [
    string,
    {
      surface: string;
      action?:
        | { kind: "setting"; tab: string; control: string; label: string }
        | { kind: "sign-in"; label: string };
    },
  ][] = [
    ["agent_not_found", { surface: "toast" }],
    // A routed error that also names a Settings control: the payload carries a
    // .kiro/agents path, so the toast carries a jump to Custom instructions.
    [
      "agent_config_error",
      {
        surface: "toast",
        action: {
          kind: "setting",
          tab: "instructions",
          control: "steering-input",
          label: "Open custom instructions",
        },
      },
    ],
    // The runtime is running UNAUTHENTICATED, so the session opened and
    // everything behind it will fail. The only fix is signing in, and there is no
    // Settings control for that — which is why the action is a discriminated
    // union rather than a Settings jump with a stretched meaning.
    // The server marks this one turn-scoped AND it carries an action, which is the
    // pair that keeps the suppression honest: the turn it failed holds the reason
    // inline, and the toast is still raised because Sign in is reachable from
    // nowhere else on screen.
    [
      "auth_token_unavailable",
      {
        surface: "toast",
        action: { kind: "sign-in", label: "Sign in" },
      },
    ],
    ["rate_limit", { surface: "toast" }],
    ["compaction_failed", { surface: "toast" }],
    // The four failed-ATTEMPT codes. Each ends the turn and each leaves a
    // promptable chat behind, which is why none of them reaches the send button:
    // an alert icon on the control whose job is to send claims the chat is dead,
    // and it is not. The reason lands on a toast and on the turn's own divider.
    ["switch_failed", { surface: "toast" }],
    ["prompt_failed", { surface: "toast" }],
    // A pick refused before it reached the wire: same surface as switch_failed,
    // which is the other half of choosing a model.
    ["model_not_served", { surface: "toast" }],
    // Empty-turn recovery could not respawn or resend. Routed explicitly rather
    // than left to the unknown-code fallthrough, on the one error whose meaning is
    // "the automatic repair failed".
    ["recovery_failed", { surface: "toast" }],
    // The ONE code that earns the send button's alert face: kiro-cli could not be
    // spawned, so there is no ACP connection behind this chat to send to. Every
    // other code here happened to a live agent.
    ["bridge_start_failed", { surface: "agent-down" }],
    // The chat runs, just not in the requested mode, and one click on the mode
    // pill fixes it — so it reports without touching the send button.
    ["mode_not_applied", { surface: "toast" }],
    // The chat runs, it just will not ask before writing, and one click on the
    // supervised switch fixes it — the same shape as its mode sibling. Mapped
    // rather than left to the fallthrough: the generic failure surface would claim
    // the turn failed when the turn is fine.
    ["supervised_not_applied", { surface: "toast" }],
  ];

  it.each(expectedRoutes)("routes %s to the expected surface and action", (code, expected) => {
    expect(ERROR_ROUTES[code as keyof typeof ERROR_ROUTES]).toEqual(expected);
  });

  it("contains exactly the codes this table claims to route", () => {
    expect(Object.keys(ERROR_ROUTES).sort()).toEqual(expectedRoutes.map(([c]) => c).sort());
  });

  it("returns undefined for codes not in the table", () => {
    expect(ERROR_ROUTES["unknown_code" as keyof typeof ERROR_ROUTES]).toBeUndefined();
    expect(ERROR_ROUTES["" as keyof typeof ERROR_ROUTES]).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// THE CLOSE IS AN APPEND, and everything else the handler does follows from what
// the log then says. The summary is the entry's own payload, read by the footer, so
// this handler stamps nothing onto anything: `appendEntry` is its whole write.
// ---------------------------------------------------------------------------

describe("turn_closed appends the close", () => {
  it("lands the entry at the seq it claims", () => {
    seedLive();
    fireClose("chat-1", { payload: { outcome: "completed", credits: 1.5, elapsed_ms: 2000 } });
    const state = get("chat-1")?.turns.get("t1");
    expect(state?.entries.map((e) => e.kind)).toEqual(["turn_open", "turn_close"]);
    expect(state?.closeAt).toBe(1);
  });

  it("asks for the turn's range read when the seq does not fit", () => {
    // A frame arriving on a window that missed one of the turn's entries. The close is
    // not forced in at the wrong position: the hole is stated and one read repairs it,
    // which is why nothing here pads or re-indexes.
    seedLive();
    fireClose("chat-1", { seq: 4 });
    expect(mockRepairTurn).toHaveBeenCalledWith("chat-1", "t1", 0);
    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBeUndefined();
    // And the chat still SETTLES: the frame is proof the turn ended, so waiting for the
    // repair would leave `thinking` latched on a turn that is over. This is also what
    // makes the settle test's `id !== closedTurn` term load-bearing — with the close
    // absent from the log, the closing turn is the one that reads open.
    expect(get("chat-1")?.thinking).toBe(false);
  });

  it("asks for the whole turn when the window holds none of it", () => {
    // The lost-`turn_opened` case: the client has never seen this turn, so `afterSeq` is
    // omitted and the read returns all of it.
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    fireClose("chat-1", { turnID: "unseen" });
    expect(mockRepairTurn).toHaveBeenCalledWith("chat-1", "unseen", undefined);
  });
});

describe("turn_closed side effects", () => {
  it("clears the thinking flag on the chat", () => {
    seedLive();
    fireClose("chat-1");
    expect(get("chat-1")?.thinking).toBe(false);
  });

  // The server's own liveness statement, set FALSE because this close settled the chat.
  // Written at the CALL SITE rather than inside `clearTurnState`, deliberately — that
  // function also runs on `BUS_RECONCILE`, where dropping the server's last statement
  // while `thinking` is also cleared is the gap-path flash `turnLive` removes.
  it("marks the server's turn_open statement closed", () => {
    seedLive();
    fireClose("chat-1");
    expect(get("chat-1")?.turn_open).toBe(false);
  });

  // KAS clears its steering buffer at every turn boundary, and a steer it was HOLDING
  // gets no `steer{dropped}` entry — a bridge death writes none — so the row would sit
  // in the dock past the turn it belonged to. This is the leave that owes.
  it("drops the chat's waiting steers", () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(steerCount("chat-1")).toBe(1);

    fireClose("chat-1");
    expect(steerCount("chat-1")).toBe(0);
  });

  // NO active-chat gate here, deliberately, and this is the store half of the
  // reported symptom: the reader leaves the tab, the turn ends server-side, and
  // the dock they come back to is empty. It is RIGHT to be empty — a row still
  // waiting was never read and can never post.
  it("drops them for a background (non-active) chat too", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");
    openTurnOn("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });

    fireClose("chat-1");
    expect(steerCount("chat-1")).toBe(0);
  });

  // Every ask BLOCKS its turn, so a turn that has closed is not waiting on one. What is
  // left in the queue is an abandoned card (cmdCancel already cleared the server's own
  // pending set), and `input` outranks every other dot state — so the chat claimed it
  // needed a decision indefinitely.
  it("discards the turn's abandoned asks", () => {
    seedLive();
    fireClose("chat-1");
    expect(mockDropTurnDecisions).toHaveBeenCalledWith("chat-1");
  });

  // The set of turns changed, so the rail re-reads its session-wide index — including
  // for the FIRST turn of a chat that was empty when it was activated, whose marker
  // exists nowhere until this fires. The id has to be the frame's.
  it("re-reads the rail's index for the chat the frame names", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    openTurnOn("chat-1");

    fireClose("chat-1");
    expect(refreshTurnRail).toHaveBeenCalledWith("chat-1");
  });
});

// ---------------------------------------------------------------------------
// A CLOSE THAT LEAVES ANOTHER TURN OPEN SETTLES NOTHING, read from the LOG.
//
// The frame names its turn, so `superseded` and `workflow_step` — which existed only
// because the turn-end frame it replaced carried none — are gone. What decides the teardown
// is `anotherTurnOpen`: a prompt's turn is in the store from its admission, so an
// agent-initiated turn closing while that prompt waits must leave the chat live.
// ---------------------------------------------------------------------------

describe("turn_closed with another turn still open", () => {
  /** Two open turns, the shape 4.1's registry produces: an agent-initiated turn and a
   *  prompt admitted during it. `t1` is the one that closes. */
  function seedTwoOpen(): void {
    seedLive();
    openTurnOn("chat-1", "t2", 2);
  }

  it("keeps the chat reading working", () => {
    seedTwoOpen();
    fireClose("chat-1");
    expect(tabStatusFor(get("chat-1"))).toBe("working");
  });

  it("retracts neither liveness input", () => {
    seedTwoOpen();
    fireClose("chat-1");
    expect(get("chat-1")?.thinking).toBe(true);
    expect(get("chat-1")?.turn_open).toBe(true);
  });

  it("leaves an unread steer waiting", () => {
    // The agent can still read it: the other turn is running right now, so dropping the
    // row here would report a steer as undelivered while it is about to be delivered.
    seedTwoOpen();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireClose("chat-1");
    expect(steerCount("chat-1")).toBe(1);
  });

  it("sends no resend", async () => {
    seedTwoOpen();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireClose("chat-1");
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  it("retires no pending ask", () => {
    // The sweep keeps only RUN-scoped asks, so ungated it strands the live JSON-RPC
    // request of the turn that is still running.
    seedTwoOpen();
    fireClose("chat-1");
    expect(mockDropTurnDecisions).not.toHaveBeenCalled();
  });

  it("still appends the close and runs the ungated effects", () => {
    // The gate covers the teardown, not the handler: the close is a line of the log
    // whichever turn it names, this chat's turn index changed, and a frame arriving at
    // all proves an agent is behind the chat. Without this row every case above passes
    // just as well for a handler that returns early on a second open turn.
    seedTwoOpen();
    fireClose("chat-1");
    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBe(1);
    expect(refreshTurnRail).toHaveBeenCalledWith("chat-1");
    expect(mockClearAgentDown).toHaveBeenCalled();
  });

  // The NEGATIVE CONTROL for all of them: without it each passes just as well when the
  // handler stops applying these effects for every close.
  it("applies every one of them once the last open turn closes", () => {
    seedTwoOpen();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireClose("chat-1", { turnID: "t2" });
    fireClose("chat-1");

    expect(get("chat-1")?.thinking).toBe(false);
    expect(get("chat-1")?.turn_open).toBe(false);
    expect(steerCount("chat-1")).toBe(0);
    expect(mockDropTurnDecisions).toHaveBeenCalledWith("chat-1");
  });
});

// ---------------------------------------------------------------------------
// A RUN'S CLOSE IS `handlers/run.ts`'s, and the guard is what replaced the
// `workflow_step` marker: a run's frames carry an EMPTY chat id, so without it the
// settle below would run a chat teardown against "".
// ---------------------------------------------------------------------------

describe("turn_closed carrying a workflow id", () => {
  it("is left to the run's handler entirely", () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });

    fireClose("chat-1", { workflowID: "wf_1" });

    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBeUndefined();
    expect(get("chat-1")?.thinking).toBe(true);
    expect(get("chat-1")?.turn_open).toBe(true);
    expect(steerCount("chat-1")).toBe(1);
    expect(mockDropTurnDecisions).not.toHaveBeenCalled();
    expect(refreshTurnRail).not.toHaveBeenCalled();
  });

  it("reads an EMPTY workflow id as the chat's own close", () => {
    // The control for the guard's own condition: `omitempty` means a chat's close can
    // arrive with the field present and blank, and refusing that one would leave every
    // such turn open forever.
    seedLive();
    fireClose("chat-1", { workflowID: "" });
    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBe(1);
    expect(get("chat-1")?.thinking).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// THE UNREAD MESSAGE IS SENT AS THE NEXT TURN, and the settled branch is its one
// firing point. The join, precedence and retry ladder are steer-resend.test.ts's;
// these cases own that the trigger fires with the right payload, and not for a close
// that settles nothing. `handlers/steer.test.ts` handed this file all six.
// ---------------------------------------------------------------------------

describe("the settled close carries an unread steer forward", () => {
  it("sends it as a new turn when the turn ends with the message unread", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });

    fireClose("chat-1");

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[0]).toBe("chat-1");
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("actually target main");
  });

  // Several unread messages are ONE new turn, joined by a blank line in the order they
  // were typed — not N turns, which would make the agent answer each in isolation.
  it("concatenates several unread steers into one new turn", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });

    fireClose("chat-1");

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("first\n\nsecond\n\nthird");
  });

  // THE CAPTURE READS THE ROWS THE DROP REMOVES, so the order inside the branch is
  // load-bearing: `noteBoundaryDrop(pendingSteerCarry(...))`, then `dropSteers`, then
  // the fire. Reversed, every boundary carries nothing.
  it("captures the text before it empties the dock", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "carry me", origin: "user" });

    fireClose("chat-1");

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("carry me");
    expect(steerCount("chat-1")).toBe(0);
  });

  // A steer KAS DID read leaves the dock on its own `steer` entry, inside `appendEntry`,
  // so the boundary finds nothing to carry. That is what replaces the old capture at
  // `steer_cleared`: the entry is the leave, and the two cannot double up.
  it("carries nothing for a steer whose own entry already arrived", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "the agent read this", origin: "user" });
    appendEntry("chat-1", {
      id: "steer-1",
      turn: "t1",
      kind: "steer",
      seq: 1,
      ts: 2,
      payload: { text: "the agent read this", origin: "user", state: "read" },
    });
    expect(steerCount("chat-1")).toBe(0);

    fireClose("chat-1", { seq: 2 });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  // THE LOOP GUARD, and it is structural rather than a counter: a turn opened by a
  // resend ends with an empty dock, so the capture arms nothing and nothing fires.
  it("opens nothing further when the resent turn ends with nothing pending", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireClose("chat-1");
    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });

    // The resent turn's own end.
    openTurnOn("chat-1", "t2", 2);
    fireClose("chat-1", { turnID: "t2" });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  // A turn that ended with everything read has nothing to carry, and this is the common
  // case, so it must cost no POST at all.
  it("sends nothing when the agent read everything", async () => {
    seedLive();

    fireClose("chat-1");
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockClearSteers).not.toHaveBeenCalled();
  });

  // A row still SENDING is excluded: its own POST is still resolving, and submit.ts
  // already converts a `no_turn` refusal of it into a prompt — so resending it here
  // would send one message twice.
  it("leaves a still-sending steer to its own POST", async () => {
    seedLive();
    recordSteerSent("chat-1", "m-1", "still in flight");

    fireClose("chat-1");
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  // A slot armed by an earlier boundary is still owed a turn, and this is the door that
  // fires it — spelled here the way steer-resend.ts's own producer spells it.
  it("fires a slot armed before the frame arrived", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "armed earlier", origin: "user" });
    noteBoundaryDrop("chat-1", pendingSteerCarry("chat-1", ["steer-1"]));
    dropSteers("chat-1", ["steer-1"]);
    expect(mockSendPromptTo).not.toHaveBeenCalled();

    fireClose("chat-1", { payload: { outcome: "cancelled" } });

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("armed earlier");
  });
});

// ---------------------------------------------------------------------------
// THE DOT FOLLOWS THE LOG, and this handler's contribution to it is LIVENESS alone.
// The verdict has ONE source — the header's `last_turn_outcome`, written by the server
// at every `turn_close` — so there is no latch here to agree with anything, and
// store.test.ts owns the outcome-to-dot table. What is this file's is the gate:
// `failed` and `done` are both unreachable while a turn of the chat is open.
// ---------------------------------------------------------------------------

describe("the dot reads the header's outcome once the close lands", () => {
  it("takes a settled chat off working", () => {
    seedLive();
    expect(tabStatusFor(get("chat-1"))).toBe("working");
    fireClose("chat-1");
    expect(tabStatusFor(get("chat-1"))).toBe("idle");
  });

  it("lets a failed outcome the header already carries paint", () => {
    // The gate in `tabStatusFor`: that field describes the newest FINISHED turn, so it
    // still names the previous one for the whole of the next, and an ungated `failed`
    // paints a chat red for the duration of a turn that is running fine.
    seedLive("chat-1", { last_turn_outcome: "failed" });
    expect(tabStatusFor(get("chat-1"))).toBe("working");
    fireClose("chat-1");
    expect(tabStatusFor(get("chat-1"))).toBe("failed");
  });

  it("still prefers the agent's own claim where it lands", () => {
    // A finished turn that left a question behind is a chat that WANTS something, not a
    // chat that is done, and the agent is the only thing that knows which.
    seedLive("chat-1", { last_turn_outcome: "completed" });
    setAgentStatus("chat-1", "waiting_on_user");
    fireClose("chat-1");
    expect(tabStatusFor(get("chat-1"))).toBe("waiting");
  });
});

describe("error handler", () => {
  // The turn lifecycle and the error PROSE are two different questions: `thinking` is what
  // the renderer reads to decide whether an assistant bubble subscribes to its own deltas,
  // so clearing it for every code freezes the whole first turn at its first streamed chunk
  // on a `.kiro/agents` typo, which fires `agent_config_error` at session construction.
  it("leaves the turn running for a routed error and reports it", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "rate_limit", message: "slow down" });
    expect(get("chat-1")?.thinking).toBe(true);
    // `false` is the turn-scoped flag: a rate-limit notice ends no turn, so it has
    // no transcript row to duplicate and keeps its toast whatever is on screen.
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "slow down", undefined, false);
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  it.each([
    "agent_not_found",
    "agent_config_error",
    "rate_limit",
    "compaction_failed",
    "mode_not_applied",
    "auth_token_unavailable",
  ])("keeps thinking set for %s, which says nothing about this turn", (code) => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code, message: "something is wrong elsewhere" });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  // A routed error reaches a surface for EVERY chat, not only the one on screen: a
  // toast claims no shared control, unlike the send button below. failure-notice is
  // what names the chat, so the chat id arriving here is the whole contract.
  it("reports a BACKGROUND chat's config error, with its own remedy", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");
    fireSSE("error", "chat-1", { code: "agent_config_error", message: "bad agent front matter" });
    expect(get("chat-1")?.thinking).toBe(true);
    expect(mockReportFailure).toHaveBeenCalledWith(
      "chat-1",
      "bad agent front matter",
      expect.objectContaining({ label: "Open custom instructions" }),
      false,
    );
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  // A route that names a setting carries a working in-app jump, and one whose route
  // does not carries no affordance at all.
  it("gives agent_config_error a toast action that opens the named control", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "agent_config_error", message: "bad agent json" });

    const action = mockReportFailure.mock.calls[0]?.[2] as
      { label: string; onClick: () => void } | undefined;
    expect(action?.label).toBe("Open custom instructions");
    action?.onClick();
    expect(mockOpenSetting).toHaveBeenCalledWith("instructions", "steering-input");
  });

  it("passes no toast action for a routed error that names none", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "compaction_failed", message: "nope" });
    expect(mockReportFailure.mock.calls[0]?.[2]).toBeUndefined();
    expect(mockOpenSetting).not.toHaveBeenCalled();
  });

  // D106. Before this the auth failure existed only as one server log line and a
  // JSON-RPC error to KAS, and KAS's answer to that error is to run
  // unauthenticated — the chat opens and every turn fails with nothing on screen
  // saying the runtime is signed out.
  it("routes the auth failure to a toast carrying the sign-in CTA", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", {
      code: "auth_token_unavailable",
      message: "kiro-cli: refresh token expired",
      turn_scoped: true,
    });

    expect(mockReportFailure).toHaveBeenCalledWith(
      "chat-1",
      // kiro-cli's own reason travels through: it names which leg of the login
      // chain is dead, and no wording invented client-side is more specific.
      "kiro-cli: refresh token expired",
      expect.objectContaining({ label: "Sign in" }),
      // TURN-SCOPED, off the FRAME, and still raised: the remedy is the half an
      // inline row cannot offer, so failure-notice.ts never suppresses an
      // action-bearing notice. This is the code where both conjuncts are true at
      // once, which is what makes that clause real rather than defensive.
      true,
    );
    const action = mockReportFailure.mock.calls[0]?.[2] as
      { label: string; onClick: () => void } | undefined;
    action?.onClick();
    expect(mockShowLoginModal).toHaveBeenCalledTimes(1);
    // Not a Settings jump: the login modal is not in Settings at all.
    expect(mockOpenSetting).not.toHaveBeenCalled();
    // And not the send button: it is not one send that is broken.
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  // The 2026-08 routing change, and the assertion the user's complaint reduces
  // to: a throttle / 5xx / capacity failure goes to the toast, carrying the
  // server's prose VERBATIM (no `code: ` prefix — the code is machine vocabulary
  // in front of a human sentence), and it does NOT touch the send button.
  it.each(["prompt_failed", "recovery_failed", "switch_failed", "model_not_served"])(
    "routes %s to the toast and leaves the send button alone",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      fireSSE("error", "chat-1", { code, message: "boom", turn_scoped: true });
      expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "boom", undefined, true);
      expect(mockSetAgentDown).not.toHaveBeenCalled();
    },
  );

  // ---------------------------------------------------------------------------
  // TURN-SCOPEDNESS COMES OFF THE FRAME, NOT OFF THE CODE. The flag decides whether
  // failure-notice.ts drops the toast for the chat on screen, and three of the five
  // emitters behind `prompt_failed` and `recovery_failed` open no turn at all — for those
  // the toast is the ONLY surface, so a per-code flag reports them nowhere.
  // ---------------------------------------------------------------------------

  it.each(["prompt_failed", "recovery_failed"])(
    "reports a %s that opened NO turn, even on the chat in front of you",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      // No `turn_scoped` on the frame: the emitter finalized nothing, so there is
      // no inline row for the toast to be a duplicate of.
      fireSSE("error", "chat-1", { code, message: "The prompt could not start." });
      expect(mockReportFailure).toHaveBeenCalledWith(
        "chat-1",
        "The prompt could not start.",
        undefined,
        false,
      );
    },
  );

  it.each(["prompt_failed", "recovery_failed"])(
    "suppresses a %s that DID finalize a turn, because that turn says it",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      fireSSE("error", "chat-1", { code, message: "at capacity", turn_scoped: true });
      expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "at capacity", undefined, true);
    },
  );

  it("reads a frame carrying no turn_scoped field as NOT turn-scoped", () => {
    // The compatibility direction, and the one that decides which way this fails
    // safe. A server that predates the field, or an emitter that forgets it, must
    // leave the failure REPORTED rather than trusting an inline row that is not
    // there. `false` is therefore the answer for an absent field and for an
    // explicit `false` alike.
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "compaction_failed", message: "nope" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "nope", undefined, false);
  });

  // NO error code touches the turn lifecycle: the server closes every turn exactly
  // once with a `turn_close` entry, so an error is a report.
  it.each([
    "prompt_failed",
    "bridge_start_failed",
    "rate_limit",
    "auth_token_unavailable",
    "mystery_code",
  ])("leaves the turn lifecycle alone for %s, whatever its surface", (code) => {
    seedLive();
    fireSSE("error", "chat-1", { code, message: "something happened" });
    expect(get("chat-1")?.thinking).toBe(true);
    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBeUndefined();
    expect(tabStatusFor(get("chat-1"))).toBe("working");
  });

  // The one code that DOES earn the button's alert face: kiro-cli could not be
  // spawned, so this chat has no ACP connection behind it and the icon is a true
  // statement rather than a claim about one attempt.
  it("routes bridge_start_failed to the send button, not the toast", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "bridge_start_failed", message: "spawn failed" });
    expect(mockSetAgentDown).toHaveBeenCalledWith("spawn failed");
    expect(mockReportFailure).not.toHaveBeenCalled();
  });

  // A BACKGROUND chat's failure now reaches the user, which is the hole the old
  // routing left: the prose was dropped for every non-active chat, so a failed
  // background turn had nothing but a tab dot. A toast claims no shared control,
  // so it is safe to raise from any chat; the send button still is not.
  it("reports a background chat's failure and spares its send button", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");
    fireSSE("error", "chat-1", {
      code: "prompt_failed",
      message: "at capacity",
      turn_scoped: true,
    });
    // Turn-scoped, and raised anyway: chat-2 is on screen, so chat-1's own card is
    // not, and the toast is the only surface that can report at all.
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "at capacity", undefined, true);

    mockReportFailure.mockClear();
    fireSSE("error", "chat-1", { code: "bridge_start_failed", message: "spawn failed" });
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  it("falls through unknown codes to the toast", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "mystery_code", message: "huh" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "huh");
  });

  // An empty message is the only time machine vocabulary beats nothing.
  it("uses the code when an unknown error carries no message", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "mystery_code", message: "" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "mystery_code");
  });
});

// D103: the protected approval floor, at the client's notification site. There
// is no per-kind switch left, so all three turn-blocking asks reach
// notifyIfHidden — which is where the master switch is checked. The
// isAgentFinishedEnabled mock returns false, so a turn-close notification would
// NOT fire; that contrast is what makes these assertions non-vacuous.
describe("the permission-class asks always notify", () => {
  it.each([
    ["permission_needed", { request_id: 1, options: [] }, "Permission needed"],
    ["elicitation_needed", { request_id: 2 }, "Input requested by a tool"],
    ["user_input_needed", { request_id: 3, options: [] }, "The agent has a question"],
  ])("%s notifies with no per-kind gate", (event, payload, body) => {
    fireSSE(event, "chat-1", payload);
    // No `run_id` on any of these payloads, so `askTarget` falls to the envelope chat.
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", body, {
      kind: "chat",
      chatID: "chat-1",
    });
  });

  it("uses the turn-approval wording when the ask carries files", () => {
    fireSSE("permission_needed", "chat-1", {
      request_id: 4,
      options: [],
      files: [{ path: "a.go", action_id: "act-1" }],
    });
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", "Review this turn's changes", {
      kind: "chat",
      chatID: "chat-1",
    });
  });
});

// ---------------------------------------------------------------------------
// THE OFF-SCREEN NOTIFICATION, per outcome, read off the close entry's payload.
//
// It gated on `stop_reason !== "cancelled"` and then said `Agent finished` whatever had
// happened, so a failed, interrupted or refused turn pushed a claim of success to a
// reader who was not looking. It reads the SEVERITY now. A distinct chat id per case:
// the cue dedups per chat, so seven frames on one id would measure that window.
// ---------------------------------------------------------------------------

describe("the agent-finished notification reads the severity", () => {
  /** outcome -> the notification body, or "" for a turn that says nothing.
   *
   *  Hardcoded rather than derived through `severityOf`/`defaultFailureReason`: an
   *  expectation computed by the code under test passes for any mapping at all,
   *  including the one that shipped the defect. */
  const cases: [TurnOutcome, string][] = [
    ["completed", "seeded: Agent finished"],
    ["failed", "seeded: The agent reported an error and the turn stopped."],
    ["interrupted", "seeded: The turn was interrupted before the agent finished."],
    ["refused", "seeded: The model declined to continue."],
    // STOPPED. A cancel is what the reader asked for, and an end marotte could not
    // read reports nothing about success, so neither earns a notification.
    ["cancelled", ""],
    ["unknown", ""],
    // `running` cannot reach a `turn_close` in practice; the row keeps the handler from
    // claiming a verdict for a turn that has not ended.
    ["running", ""],
  ];

  beforeEach(() => {
    notifyGate.agentFinished = true;
  });
  afterEach(() => {
    notifyGate.agentFinished = false;
  });

  for (const [outcome, want] of cases) {
    it(`says ${want === "" ? "nothing" : `"${want}"`} for a turn that closed ${outcome}`, () => {
      const chatID = `notify-${outcome}`;
      setSessions([makeSession(chatID)]);
      openTurnOn(chatID);
      fireClose(chatID, { payload: { outcome } });
      if (want === "") {
        expect(mockNotifyIfHidden).not.toHaveBeenCalled();
        return;
      }
      expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", want, {
        kind: "chat",
        chatID,
      });
    });
  }

  it("never claims a broken turn finished", () => {
    // The property behind the rows above, and the direction the defect ran in: the
    // wording matters less than never saying `Agent finished` over a failure.
    for (const [outcome] of cases) {
      if (severityOf(outcome) !== "broken") {
        continue;
      }
      mockNotifyIfHidden.mockClear();
      const chatID = `broken-${outcome}`;
      setSessions([makeSession(chatID)]);
      openTurnOn(chatID);
      fireClose(chatID, { payload: { outcome } });
      const body = String(mockNotifyIfHidden.mock.calls[0]?.[1] ?? "");
      expect(body, `${outcome} notified nothing at all`).not.toBe("");
      expect(body, `${outcome} claimed the agent finished`).not.toContain("Agent finished");
    }
  });

  it("covers a broken outcome, or the property above passes vacuously", () => {
    expect(cases.filter(([o]) => severityOf(o) === "broken").length).toBeGreaterThan(0);
  });

  it("still notifies nothing at all when the master switch is off", () => {
    // The gate the plan required to survive the rewrite: severity decides WHAT is
    // said, never WHETHER the user has asked to be told.
    notifyGate.agentFinished = false;
    setSessions([makeSession("gate-off")]);
    openTurnOn("gate-off");
    fireClose("gate-off", { payload: { outcome: "failed" } });
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  it("keeps the 2s dedup window, which an SSE replay burst needs", () => {
    // The replayed close is a REDELIVERY the store drops by id, so the second frame
    // reaches the cue with the turn already settled — which is the burst this window
    // exists for.
    setSessions([makeSession("dedup")]);
    openTurnOn("dedup");
    fireClose("dedup", { payload: { outcome: "failed" } });
    fireClose("dedup", { payload: { outcome: "failed" } });
    expect(mockNotifyIfHidden).toHaveBeenCalledTimes(1);
  });

  it("says nothing at all for a close that settles nothing", () => {
    // The cue is a statement about a turn this handler SETTLED, so it sits inside the
    // gate with the writes it reads.
    setSessions([makeSession("still-open")]);
    openTurnOn("still-open");
    openTurnOn("still-open", "t2", 2);
    fireClose("still-open");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// THE REPORTED DEFECT: a chat that launched a workflow raised the cue at its own turn's
// end, which is when `run_workflow` returned rather than when the work was done — so an
// off-screen reader was told the agent had finished up to forty minutes early. The
// handler hands ONE fact to `agent-finished-cue.ts` now and decides nothing. Nothing
// mocks `../run-store.js` here, so the cases drive the real live-run inventory.
// ---------------------------------------------------------------------------

describe("the notification waits for the work, not just the turn", () => {
  beforeEach(() => {
    notifyGate.agentFinished = true;
  });
  afterEach(() => {
    notifyGate.agentFinished = false;
    for (const id of liveRunsSeeded) {
      noteRunSettled(id);
    }
    liveRunsSeeded.length = 0;
  });

  const liveRunsSeeded: string[] = [];
  function seedLiveRun(workflowID: string, chatID: string, executing = true): void {
    liveRunsSeeded.push(workflowID);
    noteRunLive(workflowID, chatID, executing);
  }

  /** A settled chat with one closed turn, which is what a cue is a statement about. */
  function closeOn(chatID: string, outcome: TurnOutcome = "completed"): void {
    setSessions([makeSession(chatID)]);
    openTurnOn(chatID);
    fireClose(chatID, { payload: { outcome } });
  }

  it("says nothing when a run this chat launched is still live", () => {
    seedLiveRun("wf-defer-clean", "defer-clean");
    closeOn("defer-clean");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  // The withhold is about the reader's attention rather than the turn's verdict, so a
  // broken turn that launched a still-running run makes the same false claim.
  it("says nothing for a BROKEN turn while a run is live", () => {
    seedLiveRun("wf-defer-broken", "defer-broken");
    closeOn("defer-broken", "failed");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  // A run stopped on a person is exactly what a "finished" notification must not
  // claim is over, and it is the case the narrow store-eviction predicate answers
  // the wrong way.
  it("says nothing while the run is merely PAUSED", () => {
    seedLiveRun("wf-defer-parked", "defer-parked", false);
    closeOn("defer-parked");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  it("notifies immediately when nothing is outstanding", () => {
    closeOn("defer-none");
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", "seeded: Agent finished", {
      kind: "chat",
      chatID: "defer-none",
    });
  });

  // One busy conversation must not mute the rest of the workspace.
  it("notifies when the live run belongs to another chat", () => {
    seedLiveRun("wf-defer-other", "some-other-chat");
    closeOn("defer-other");
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", "seeded: Agent finished", {
      kind: "chat",
      chatID: "defer-other",
    });
  });

  // A manual or scheduled launch is parentless, so its lease names no chat and its
  // outcome travels on its own push rather than on a chat's.
  it("notifies when the live run is PARENTLESS", () => {
    seedLiveRun("wf-defer-parentless", "");
    closeOn("defer-parentless");
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("marotte", "seeded: Agent finished", {
      kind: "chat",
      chatID: "defer-parentless",
    });
  });

  // The switch decides WHETHER the reader is told; the deferral decides WHEN. A cue
  // must not be parked for a channel that is off, or switching it back on would
  // deliver a notification about work that finished while it was off.
  it("parks nothing at all when the master switch is off", () => {
    notifyGate.agentFinished = false;
    seedLiveRun("wf-defer-gate-off", "defer-gate-off");
    closeOn("defer-gate-off");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
    expect(hasDeferredCue("defer-gate-off")).toBe(false);
  });

  // The two silences are different states and only one of them is a deferral: a turn
  // that says nothing has nothing to park, so it must leave no cue behind that a
  // later settle could fire.
  it("parks nothing for a turn that says nothing", () => {
    seedLiveRun("wf-defer-cancelled", "defer-cancelled");
    closeOn("defer-cancelled", "cancelled");
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
    expect(hasDeferredCue("defer-cancelled")).toBe(false);
  });

  // The positive half of the withhold: a cue IS parked, so the release effect the
  // composition root installs has something to fire. Without this the two silent
  // cases above pass equally for a handler that dropped the notification entirely.
  it("parks the cue rather than dropping it", () => {
    seedLiveRun("wf-defer-parked-cue", "defer-parked-cue");
    closeOn("defer-parked-cue");
    expect(hasDeferredCue("defer-parked-cue")).toBe(true);
    forgetDeferredCue("defer-parked-cue");
  });
});

describe("decision_settled handler", () => {
  it("hands the settled request to the dock, kind and attribution intact", () => {
    fireSSE("decision_settled", "chat-1", {
      kind: "user_input",
      settled_by: "unattended",
      request_id: 42,
    });
    // The handler routes and nothing else: the dock owns the queue, so the
    // arguments arriving unchanged IS the contract.
    expect(mockCollapseSettled).toHaveBeenCalledWith("chat-1", "user_input", 42, "unattended");
  });

  it("retracts the banners about the settled chat, and only there", () => {
    mockCollapseSettled.mockReturnValueOnce("");
    fireSSE("decision_settled", "chat-1", {
      kind: "permission",
      settled_by: "user",
      request_id: 43,
    });
    expect(mockCloseNotificationsFor).toHaveBeenCalledTimes(1);
    expect(mockCloseNotificationsFor).toHaveBeenCalledWith({ kind: "chat", chatID: "chat-1" });
    // The Cedar policy reload names no ask, so it retracts nothing.
    mockCloseNotificationsFor.mockClear();
    fireSSE("permissions_changed", "", { status: "ok", errors: [] });
    expect(mockCloseNotificationsFor).not.toHaveBeenCalled();
  });

  it("retracts a run-attributed ask's banner by the run the dock recorded for it", () => {
    // The frame carries the chat the ask travelled on; the banner was tagged by the
    // run it was about (`askTarget`), and the dock is what still knows which run.
    mockCollapseSettled.mockReturnValueOnce("wf_1");
    fireSSE("decision_settled", "chat-1", {
      kind: "permission",
      settled_by: "user",
      request_id: 44,
    });
    expect(mockCloseNotificationsFor).toHaveBeenCalledWith({ kind: "run", workflowID: "wf_1" });
  });

  it("falls back to the chat's banner for an ask this dock never held", () => {
    mockCollapseSettled.mockReturnValueOnce(undefined);
    fireSSE("decision_settled", "chat-1", {
      kind: "elicitation",
      settled_by: "moot",
      request_id: 45,
    });
    expect(mockCloseNotificationsFor).toHaveBeenCalledWith({ kind: "chat", chatID: "chat-1" });
  });
});
