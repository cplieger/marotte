// ERROR_ROUTES, the three asks, decision_settled, the error handler and turn_closed, over the REAL
// handlers and store. Liveness is the LOG; sibling subsystems stay mocked (a call is a command).

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
// FIRST, above every import: the factory below closes over `createBusMock`, and the mocker
// resolves it while linking `../store.js`'s graph.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());
import {
  setSessions,
  setActive,
  get,
  defaultUsage,
  openTurn,
  recordSteerQueued,
  registerTurnRepair,
  steerCount,
  setAgentStatus,
  tabStatusFor,
} from "../store.js";
import { noteRunLive, noteRunSettled } from "../run-store.js";
import type { Session } from "../types.js";
import type { Entry, EntryTurnClose, NotificationPayload, TurnOutcome } from "../wire/types.gen.js";
import type * as ApiClient from "../api-client.js";
import type * as ChatActions from "../actions/chat.js";

// Spread, so other consumers keep the real module. `vi.hoisted`: the static run-store import
// resolves this factory during linking, before plain consts initialize.
const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockApiGetTyped,
}));

// The three answers' dispatches, replaced so a case decides what the server made of one.
const { mockRespondPermission, mockRespondElicitation, mockRespondUserInput } = vi.hoisted(() => ({
  mockRespondPermission: vi.fn(),
  mockRespondElicitation: vi.fn(),
  mockRespondUserInput: vi.fn(),
}));
vi.mock("../actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  respondPermission: { dispatch: mockRespondPermission },
  respondElicitation: { dispatch: mockRespondElicitation },
  respondUserInput: { dispatch: mockRespondUserInput },
}));

// scroll.ts touches DOM elements at import; use the shared mock.
vi.mock(
  "../scroll.js",
  async () => (await import("../__test-helpers__/scroll-mock.js")).scrollMock,
);

// Through `vi.hoisted` for the reason the notify trio below states: the mocker resolves
// these factories during LINKING, above this file's own top-level initializers, where a
// plain `const` is still in its temporal dead zone.
const { mockCollapseSettled, mockHasPendingDecision, mockDropTurnDecisions, mockPushDecision } =
  vi.hoisted(() => ({
    mockCollapseSettled: vi.fn(),
    mockHasPendingDecision: vi.fn(() => false),
    mockDropTurnDecisions: vi.fn(),
    mockPushDecision: vi.fn(),
  }));
vi.mock("../decision-dock.js", () => ({
  pushDecision: mockPushDecision,
  collapseSettledDecision: mockCollapseSettled,
  hasPendingDecision: mockHasPendingDecision,
  dropTurnDecisions: mockDropTurnDecisions,
}));

vi.mock("../attachments.js", () => ({
  addAttachment: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: composer-state.ts is in
  // this graph now (the tab projection reaches it), and it imports the rest.
  addAttachmentTo: vi.fn(),
  removeAttachmentFrom: vi.fn(),
  attachmentGeneration: vi.fn(() => 0),
  takeAttachments: vi.fn(() => []),
  hasAttachments: vi.fn(() => false),
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
  reportSendRefused: vi.fn(),
}));

const { mockReportFailure } = vi.hoisted(() => ({ mockReportFailure: vi.fn() }));
vi.mock("../failure-notice.js", () => ({
  reportFailure: mockReportFailure,
  // Present-but-undefined so real-ESM linking succeeds: actions/chat.js is in this
  // graph and imports the name, and Browser Mode links for real rather than
  // reading properties off a namespace object. No path under test calls it.
  clearFailure: undefined,
}));

// A HOLDER so the agent-finished notification can be on in one block only (the permission block
// needs the contrast). `vi.hoisted` because the factory closes over it.
const { mockNotifyOffScreen, mockCloseNotificationsFor, notifyGate } = vi.hoisted(() => ({
  mockNotifyOffScreen: vi.fn(),
  mockCloseNotificationsFor: vi.fn(() => Promise.resolve()),
  notifyGate: { agentFinished: false },
}));
vi.mock("../notify.js", () => ({
  notifyOffScreen: mockNotifyOffScreen,
  closeNotificationsFor: mockCloseNotificationsFor,
  setBadge: vi.fn(),
  isAgentFinishedEnabled: () => notifyGate.agentFinished,
}));

const { mockOpenSetting } = vi.hoisted(() => ({ mockOpenSetting: vi.fn() }));
vi.mock("../settings-highlight.js", () => ({ openSetting: mockOpenSetting }));

// The sign-in CTA's destination. Mocked because a call into it is a command at the
// handler's boundary, and the real module wires a whoami poll at import.
const { mockShowLoginModal } = vi.hoisted(() => ({ mockShowLoginModal: vi.fn() }));
vi.mock("../modals.js", () => ({ showLoginModal: mockShowLoginModal }));

vi.mock("../git.js", () => ({ refreshGitBadge: vi.fn() }));

// `refreshTurnRail` is read as a spy, and the real fetchers would leave one request per frame for
// the teardown to abort as an unhandled AbortError. A rail call is a command at the boundary.
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
await import("./turn.js");
const { ERROR_ROUTES } = await import("./error-routing.js");
// After the mocks for the same reason turn.ts is: the cue module imports ../notify.js,
// so a STATIC import here links it against the real module before the mocker is ready
// and the whole file dies in module linking.
const { forgetDeferredCue, hasDeferredCue } = await import("../agent-finished-cue.js");
await import("./notification.js");

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

/** `workflow_id` is the RUN partition and is absent for a chat's own turn, so the default omits it
 *  rather than sending "". */
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

describe("a decision's answer", () => {
  // The dock holds an answered card until the server settles it, so a dispatch that resolved nothing (failed or
  // cancelled) must come back as `failed`, or the card stays inert with the ask still open.
  const asks = {
    permission: {
      event: "permission_needed",
      payload: { request_id: 4, options: [], title: "ls", kind: "execute" },
      dispatch: mockRespondPermission,
      answer: (submit: (...args: unknown[]) => Promise<unknown>) =>
        submit({ optionID: "allow_once" }),
    },
    elicitation: {
      event: "elicitation_needed",
      payload: { request_id: 4, message: "token?" },
      dispatch: mockRespondElicitation,
      answer: (submit: (...args: unknown[]) => Promise<unknown>) => submit("decline"),
    },
    userInput: {
      event: "user_input_needed",
      payload: { request_id: 4, question: "Which region?" },
      dispatch: mockRespondUserInput,
      answer: (submit: (...args: unknown[]) => Promise<unknown>) => submit("dismissed"),
    },
  };

  for (const [name, ask] of Object.entries(asks)) {
    it(`${name}: a dispatch that resolved nothing is a failed answer`, async () => {
      ask.dispatch.mockResolvedValue(null);
      fireSSE(ask.event, "c1", ask.payload);
      const decision = mockPushDecision.mock.lastCall?.[0] as {
        submit: (...args: unknown[]) => Promise<unknown>;
      };

      await expect(ask.answer(decision.submit)).resolves.toBe("failed");
    });

    it(`${name}: the server's own answer passes through`, async () => {
      ask.dispatch.mockResolvedValue("superseded");
      fireSSE(ask.event, "c1", ask.payload);
      const decision = mockPushDecision.mock.lastCall?.[0] as {
        submit: (...args: unknown[]) => Promise<unknown>;
      };

      await expect(ask.answer(decision.submit)).resolves.toBe("superseded");
    });
  }
});

describe("ERROR_ROUTES", () => {
  // A route is a SURFACE plus an optional remedy; turn-scopedness is per emission, stated per frame
  // (see the "error handler" block's no-turn cases).
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
    // Unauthenticated runtime: the only fix is signing in, which no Settings control does, hence the
    // discriminated action. Turn-scoped AND actionable, so the toast is raised beside the inline reason.
    [
      "auth_token_unavailable",
      {
        surface: "toast",
        action: { kind: "sign-in", label: "Sign in" },
      },
    ],
    ["rate_limit", { surface: "toast" }],
    ["compaction_failed", { surface: "toast" }],
    // The four failed-ATTEMPT codes leave a promptable chat, so none reaches the send button.
    ["switch_failed", { surface: "toast" }],
    ["prompt_failed", { surface: "toast" }],
    // A pick refused before it reached the wire: same surface as switch_failed,
    // which is the other half of choosing a model.
    ["model_not_served", { surface: "toast" }],
    ["tangent_merge_failed", { surface: "toast" }],
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
    // Mapped, not fallthrough: the generic failure surface would claim the turn failed.
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

// THE CLOSE IS AN APPEND: `appendEntry` is the handler's whole write; the footer reads the
// entry's payload.

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
    // The chat still SETTLES on the frame, and the close's absence from the log is what makes the
    // settle test's `id !== closedTurn` term load-bearing.
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

  // Written at the CALL SITE, not in `clearTurnState`, which also runs on `BUS_RECONCILE`.
  it("marks the server's turn_open statement closed", () => {
    seedLive();
    fireClose("chat-1");
    expect(get("chat-1")?.turn_open).toBe(false);
  });

  // The close KEEPS the dock: the server owns every queued row and states each one's
  // fate (read, re-queued, resent, unsent), so a client-side drop here would erase a row
  // the server is about to resend.
  it("keeps the chat's waiting steers", () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });

    fireClose("chat-1");
    expect(steerCount("chat-1")).toBe(1);
  });

  // A closed turn is waiting on no ask; a leftover card would hold the `input` dot indefinitely.
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

// A close that leaves another turn open settles nothing, read from the LOG via `anotherTurnOpen`:
// a prompt's turn is stored from admission, so an agent turn closing meanwhile leaves the chat live.

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

  it("retires no pending ask", () => {
    // The sweep keeps only RUN-scoped asks, so ungated it strands the live JSON-RPC
    // request of the turn that is still running.
    seedTwoOpen();
    fireClose("chat-1");
    expect(mockDropTurnDecisions).not.toHaveBeenCalled();
  });

  it("still appends the close and runs the ungated effects", () => {
    // The gate covers the teardown, not the handler; without this row an early return would pass.
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
    fireClose("chat-1", { turnID: "t2" });
    fireClose("chat-1");

    expect(get("chat-1")?.thinking).toBe(false);
    expect(get("chat-1")?.turn_open).toBe(false);
    expect(mockDropTurnDecisions).toHaveBeenCalledWith("chat-1");
  });
});

// A run's close is `handlers/run.ts`'s: its frames carry an EMPTY chat id.

describe("turn_closed carrying a workflow id", () => {
  it("is left to the run's handler entirely", () => {
    seedLive();

    fireClose("chat-1", { workflowID: "wf_1" });

    expect(get("chat-1")?.turns.get("t1")?.closeAt).toBeUndefined();
    expect(get("chat-1")?.thinking).toBe(true);
    expect(get("chat-1")?.turn_open).toBe(true);
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

// The dot's verdict is the header's `last_turn_outcome` (store.test.ts owns the table); this file
// owns the gate: `failed` and `done` are unreachable while a turn is open.

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
  // `thinking` decides whether a bubble subscribes to its deltas, so clearing it for every code would
  // freeze the first turn on a `.kiro/agents` typo (`agent_config_error`).
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

  // KAS answers an auth failure by running unauthenticated, so without this toast every turn fails
  // silently.
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
      // Turn-scoped and still raised: failure-notice.ts never suppresses an action-bearing notice.
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

  // A throttle / 5xx / capacity failure goes to the toast with the server's prose VERBATIM (no
  // `code: ` prefix) and leaves the send button alone.
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

  // TURN-SCOPEDNESS COMES OFF THE FRAME: three of the five emitters behind these codes open no turn,
  // so for them the toast is the only surface.

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
    // Fails safe: an absent field (an older server, a forgetful emitter) reads as `false`, leaving the
    // failure REPORTED.
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

  // A background chat's failure reaches the user via a toast (no shared control); the send button
  // still is not touched.
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

describe("turn_closed for a carrier turn no agent ran", () => {
  function openCarrierOn(chatID: string, source: "event" | "revert"): void {
    openTurn(chatID, {
      id: "t1",
      turn: "t1",
      kind: "turn_open",
      seq: 0,
      ts: 1,
      payload: { source, n: 1 },
    });
  }

  for (const source of ["event", "revert"] as const) {
    it(`takes the chat off working and keeps the agent-down notice (${source})`, () => {
      const chatID = `carrier-${source}`;
      setSessions([makeSession(chatID)]);
      openCarrierOn(chatID, source);
      expect(tabStatusFor(get(chatID)), "the open carrier reads working").toBe("working");

      fireClose(chatID, { payload: { outcome: "completed", carrier: true } });

      expect(tabStatusFor(get(chatID))).toBe("idle");
      expect(mockClearAgentDown).not.toHaveBeenCalled();
    });
  }

  it("keeps the agent-down notice when the carrier's turn_opened never reached this client", () => {
    // The close lands as a hole and its range read is asynchronous, so the frame alone decides.
    setSessions([makeSession("carrier-unseen")]);
    setActive("carrier-unseen");
    fireClose("carrier-unseen", {
      turnID: "unseen",
      payload: { outcome: "completed", carrier: true },
    });
    expect(mockClearAgentDown).not.toHaveBeenCalled();
  });

  it("clears it for a replayed `event` turn KAS ran, whose close is not a carrier's", () => {
    setSessions([makeSession("replayed-event")]);
    openCarrierOn("replayed-event", "event");
    fireClose("replayed-event");
    expect(mockClearAgentDown).toHaveBeenCalled();
  });
});
// A chat that launched a workflow must not raise the cue when `run_workflow` returns, only when the
// work is done: the server's notification frame goes through `agent-finished-cue.ts`. The real
// live-run inventory.

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

  /** A settled chat with one closed turn and the server's notification about it. */
  function closeOn(chatID: string, outcome: TurnOutcome = "completed"): NotificationPayload {
    setSessions([makeSession(chatID)]);
    openTurnOn(chatID);
    fireClose(chatID, { payload: { outcome } });
    const notice: NotificationPayload = {
      chat_id: chatID,
      kind: "agent_finished",
      title: "seeded",
      body: outcome === "completed" ? "Response complete" : "Turn failed",
    };
    fireSSE("notification", chatID, notice);
    return notice;
  }

  it("says nothing when a run this chat launched is still live", () => {
    seedLiveRun("wf-defer-clean", "defer-clean");
    closeOn("defer-clean");
    expect(mockNotifyOffScreen).not.toHaveBeenCalled();
  });

  // The withhold is about the reader's attention rather than the turn's verdict, so a
  // broken turn that launched a still-running run makes the same false claim.
  it("says nothing for a BROKEN turn while a run is live", () => {
    seedLiveRun("wf-defer-broken", "defer-broken");
    closeOn("defer-broken", "failed");
    expect(mockNotifyOffScreen).not.toHaveBeenCalled();
  });

  // A run stopped on a person is exactly what a "finished" notification must not
  // claim is over, and it is the case the narrow store-eviction predicate answers
  // the wrong way.
  it("says nothing while the run is merely PAUSED", () => {
    seedLiveRun("wf-defer-parked", "defer-parked", false);
    closeOn("defer-parked");
    expect(mockNotifyOffScreen).not.toHaveBeenCalled();
  });

  it("notifies immediately when nothing is outstanding", () => {
    const notice = closeOn("defer-none");
    expect(mockNotifyOffScreen).toHaveBeenCalledWith(notice);
  });

  // One busy conversation must not mute the rest of the workspace.
  it("notifies when the live run belongs to another chat", () => {
    seedLiveRun("wf-defer-other", "some-other-chat");
    const notice = closeOn("defer-other");
    expect(mockNotifyOffScreen).toHaveBeenCalledWith(notice);
  });

  // A manual or scheduled launch is parentless, so its lease names no chat and its
  // outcome travels on its own push rather than on a chat's.
  it("notifies when the live run is PARENTLESS", () => {
    seedLiveRun("wf-defer-parentless", "");
    const notice = closeOn("defer-parentless");
    expect(mockNotifyOffScreen).toHaveBeenCalledWith(notice);
  });

  // The switch decides WHETHER the reader is told; the deferral decides WHEN. A cue
  // must not be parked for a channel that is off, or switching it back on would
  // deliver a notification about work that finished while it was off.
  it("parks nothing at all when the master switch is off", () => {
    notifyGate.agentFinished = false;
    seedLiveRun("wf-defer-gate-off", "defer-gate-off");
    closeOn("defer-gate-off");
    expect(mockNotifyOffScreen).not.toHaveBeenCalled();
    expect(hasDeferredCue("defer-gate-off")).toBe(false);
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
