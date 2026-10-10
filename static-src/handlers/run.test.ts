// The run handlers' invalidation model: a lifecycle event says only "refetch", and which surface
// refetches depends on the event (`run_start` re-fires on resume; `node_complete` carries neither
// iteration nor branch). A run's own LOG is its own describe below.

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("../run-store.js", () => ({
  // `false` by default: the frame did not land, which is the arm that still
  // refetches. A case that pins the APPLY path sets it true.
  applyRunProgress: vi.fn(() => false),
  invalidateRun: vi.fn(),
  // The affordance refetch a run's ENDING triggers: the verb set changes at exactly
  // that moment (a live run's Pause/Cancel becomes a failed run's Retry) and nothing
  // else in the frame stream says so.
  invalidateRunControls: vi.fn(),
  invalidateCachedRuns: vi.fn(),
  noteRunChat: vi.fn(),
  noteRunLabel: vi.fn(),
  noteRunLive: vi.fn(),
  noteRunSettled: vi.fn(),
  hasLiveRunForChat: vi.fn(() => false),
  // The chat-parented discriminator, and the handler's ONLY input for it: a
  // non-empty answer both says the run was launched from a conversation and names
  // it. `""` is the parentless population.
  runChatID: vi.fn(() => "c1"),
  runLabelOf: vi.fn(() => ""),
  // A run's own LOG: the five operations the six entry events reach. Present here
  // because Browser Mode links a mock for real, so a factory missing one name fails
  // the whole file's collection rather than one case.
  openRunTurn: vi.fn(),
  openRunEntry: vi.fn(),
  applyRunDelta: vi.fn(),
  sealRunEntry: vi.fn(),
  appendRunEntry: vi.fn(),
}));
// submitPrompt is mocked: the deferral's contract is which CHAT the prompt reaches and its text.
// `vi.hoisted` because the factory is lifted and the value is asserted on.
const { mockSubmitPrompt } = vi.hoisted(() => ({
  mockSubmitPrompt: vi.fn(async (): Promise<"sent" | "steered" | "failed"> => "sent"),
}));
vi.mock("../submit.js", () => ({ submitPrompt: mockSubmitPrompt }));
// The launching chat's own liveness, for the orphan sweep's two gates. Mocked
// because what this suite pins is WHEN the sweep fires, not how a chat comes to
// be thinking.
vi.mock("../store.js", () => ({ isThinking: vi.fn(() => false) }));
vi.mock("../run-dots.js", () => ({ trackRun: vi.fn() }));
vi.mock("../toast.js", () => ({ info: vi.fn(), success: vi.fn(), error: vi.fn() }));
// The dock is mocked (`decision-dock.test.ts`); this pins one decision with the ENVELOPE's chat id
// and that its submit reaches the right verb.
vi.mock("../decision-dock.js", () => ({
  pushDecision: vi.fn(),
  collapseSettledRunInput: vi.fn(),
  dropRunAsks: vi.fn(),
  dropTurnDecisions: vi.fn(),
}));
vi.mock("../actions/runs.js", () => ({
  answerRunInput: { dispatch: vi.fn() },
  continueRunStep: { dispatch: vi.fn() },
}));
vi.mock("../notify.js", () => ({
  notifyIfHidden: vi.fn(),
  closeNotificationsFor: vi.fn(() => Promise.resolve()),
  NOTIFY_TITLE: "Marotte",
}));

import "./run.js";
import { dispatch, onBus, BUS_RUNS_CHANGED } from "../bus.js";
import type { SSEPayloads } from "../bus.js";
import {
  appendRunEntry,
  applyRunDelta,
  applyRunProgress,
  invalidateRun,
  invalidateRunControls,
  noteRunChat,
  noteRunLabel,
  noteRunLive,
  noteRunSettled,
  hasLiveRunForChat,
  openRunEntry,
  openRunTurn,
  runChatID,
  runLabelOf,
  sealRunEntry,
} from "../run-store.js";
import { isThinking } from "../store.js";
import { trackRun } from "../run-dots.js";
import { info, success, error } from "../toast.js";
import {
  pushDecision,
  collapseSettledRunInput,
  dropRunAsks,
  dropTurnDecisions,
} from "../decision-dock.js";
import { answerRunInput, continueRunStep } from "../actions/runs.js";
import { closeNotificationsFor, notifyIfHidden } from "../notify.js";

const invalidate = vi.mocked(invalidateRun);
const invalidateControls = vi.mocked(invalidateRunControls);
const applyProgress = vi.mocked(applyRunProgress);
const noteChat = vi.mocked(noteRunChat);
const noteLabel = vi.mocked(noteRunLabel);
const noteLive = vi.mocked(noteRunLive);
const noteSettled = vi.mocked(noteRunSettled);
const track = vi.mocked(trackRun);
const toastInfo = vi.mocked(info);
const toastSuccess = vi.mocked(success);
const toastError = vi.mocked(error);
const enqueue = vi.mocked(pushDecision);
const retireAsk = vi.mocked(collapseSettledRunInput);
const dropAsks = vi.mocked(dropRunAsks);
const sweepOrphans = vi.mocked(dropTurnDecisions);
const chatThinking = vi.mocked(isThinking);
const siblingRunLive = vi.mocked(hasLiveRunForChat);
const launchingChat = vi.mocked(runChatID);
const fetchedLabel = vi.mocked(runLabelOf);
const runTurnOpened = vi.mocked(openRunTurn);
const runEntryOpened = vi.mocked(openRunEntry);
const runDelta = vi.mocked(applyRunDelta);
const runSealed = vi.mocked(sealRunEntry);
const runAppended = vi.mocked(appendRunEntry);
const answer = vi.mocked(answerRunInput.dispatch);
const waive = vi.mocked(continueRunStep.dispatch);
const notify = vi.mocked(notifyIfHidden);
const closeNotifications = vi.mocked(closeNotificationsFor);

/** Every toast raised, in order, whatever its level. */
function toasts(): string[] {
  return [...toastInfo.mock.calls, ...toastSuccess.mock.calls, ...toastError.mock.calls].map(
    (c) => c[0],
  );
}

// The list side goes over the bus, so the test subscribes exactly as the history
// page does rather than asserting on a mocked import.
let listRefetches = 0;
onBus(BUS_RUNS_CHANGED, () => {
  listRefetches++;
});

beforeEach(() => {
  invalidate.mockClear();
  invalidateControls.mockClear();
  noteChat.mockClear();
  noteLive.mockClear();
  noteSettled.mockClear();
  track.mockClear();
  toastInfo.mockClear();
  toastSuccess.mockClear();
  toastError.mockClear();
  enqueue.mockClear();
  retireAsk.mockClear();
  dropAsks.mockClear();
  sweepOrphans.mockClear();
  chatThinking.mockReset();
  chatThinking.mockReturnValue(false);
  siblingRunLive.mockReset();
  siblingRunLive.mockReturnValue(false);
  launchingChat.mockReset();
  launchingChat.mockReturnValue("c1");
  fetchedLabel.mockReset();
  fetchedLabel.mockReturnValue("");
  mockSubmitPrompt.mockClear();
  mockSubmitPrompt.mockResolvedValue("sent");
  answer.mockClear();
  waive.mockClear();
  notify.mockClear();
  listRefetches = 0;
});

type RunEvent =
  | "run_started"
  | "run_progress"
  | "run_finished"
  | "run_input_needed"
  | "run_input_settled"
  | "turn_opened"
  | "entry_opened"
  | "entry_delta"
  | "entry_sealed"
  | "entry_appended"
  | "turn_closed";

function send(type: RunEvent, payload: Record<string, unknown>, chatID = "c1"): void {
  dispatch({ type, chat_id: chatID, payload });
}

// Every subscribed event is a key of the typed SSE surface, so a Go-side rename breaks this file
// rather than silently unsubscribing; the six entry events carry the run's LOG.
const _keys: readonly (keyof SSEPayloads)[] = [
  "run_started",
  "run_progress",
  "run_finished",
  "run_input_needed",
  "run_input_settled",
  "turn_opened",
  "entry_opened",
  "entry_delta",
  "entry_sealed",
  "entry_appended",
  "turn_closed",
];
void _keys;

describe("run SSE handlers", () => {
  it("invalidates the run store at both ENDS of a run", () => {
    const order: RunEvent[] = ["run_started", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_1", status: "completed", name: "x" });
      expect(invalidate).toHaveBeenCalledTimes(i + 1);
      expect(invalidate).toHaveBeenLastCalledWith("wf_1");
    }
  });

  // The verb set changes only when a run ENDS, so that is the affordance's second and last refetch
  // (the first is a tab opening); per frame would add a round trip to every node event.
  it("refetches the affordance when a run ENDS, and never mid-run", () => {
    send("run_progress", { workflow_id: "wf_1", kind: "node_start", node_path: ["seq", "coder"] });
    send("run_started", { workflow_id: "wf_1", name: "x" });
    expect(invalidateControls).not.toHaveBeenCalled();

    send("run_finished", { workflow_id: "wf_1", status: "failed", name: "x" });
    expect(invalidateControls).toHaveBeenCalledTimes(1);
    expect(invalidateControls).toHaveBeenLastCalledWith("wf_1");
  });

  // A notice for the run can arrive before its inspect read settles, so the label
  // the lifecycle frame carries has to be in the store the moment the frame lands.
  it("records the lifecycle frame's label for the run on start and finish", () => {
    send("run_started", { workflow_id: "wf_1", name: "publish" });
    expect(noteLabel).toHaveBeenLastCalledWith("wf_1", "publish");
    send("run_finished", { workflow_id: "wf_1", status: "completed", name: "publish v2" });
    expect(noteLabel).toHaveBeenLastCalledWith("wf_1", "publish v2");
  });

  // A progress frame is APPLIED, and the refetch is what happens only when it cannot be:
  // a burst of node events otherwise costs one `GET /api/runs/{id}` each — a JSON-RPC round
  // trip to KAS for the whole state tree — with up to five runs doing it concurrently.
  it("applies a progress frame and does NOT refetch when it landed", () => {
    applyProgress.mockReturnValue(true);
    send("run_progress", {
      workflow_id: "wf_1",
      kind: "node_start",
      node_path: ["seq", "coder"],
      status: "running",
    });
    expect(applyProgress).toHaveBeenCalledTimes(1);
    expect(invalidate).not.toHaveBeenCalled();
  });

  // The store refuses a frame it cannot express — a shape change, a run-level
  // pause, a run it holds nothing for — and the refetch is the recovery.
  it("refetches when the frame could not be applied", () => {
    applyProgress.mockReturnValue(false);
    send("run_progress", { workflow_id: "wf_1", kind: "loop_iteration" });
    expect(applyProgress).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith("wf_1");
  });

  // Every run's tab carries a dot, agent-launched included, so every event tracks the run:
  // the origin decides nothing, because a chat's own dot cannot cover a run that outlives
  // its turn.
  it("tracks the run for its dot on every event", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_1", kind: "node_start", status: "completed" });
      expect(track).toHaveBeenCalledTimes(i + 1);
      expect(track).toHaveBeenLastCalledWith("wf_1");
    }
  });

  // Recorded on EVERY event including the finish, because it is what a later
  // re-open nests under: a reader who closed the automatic tab and then clicks the
  // run in its transcript should land beside the same conversation.
  it("records the launching chat on every event, the finish included", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_4", kind: "node_start", status: "completed" });
      expect(noteChat).toHaveBeenCalledTimes(i + 1);
      expect(noteChat).toHaveBeenLastCalledWith("wf_4", "c1");
    }
  });

  it("refetches the history list only at the two ENDS of a run", () => {
    // A start adds a row and a finish settles its outcome; the seven kinds in
    // between change the run, not the list, and a busy run emits many of them.
    send("run_started", { workflow_id: "wf_1", name: "publish" });
    expect(listRefetches).toBe(1);
    send("run_finished", { workflow_id: "wf_1", status: "completed" });
    expect(listRefetches).toBe(2);

    listRefetches = 0;
    for (const kind of ["node_start", "node_complete", "watch_poll", "loop_iteration"]) {
      send("run_progress", { workflow_id: "wf_1", kind });
    }
    expect(listRefetches).toBe(0);
  });

  it("passes the workflow id through so a surface showing another run ignores it", () => {
    send("run_progress", { workflow_id: "wf_other", kind: "node_start" });
    expect(invalidate).toHaveBeenCalledWith("wf_other");
  });

  // The live-runs inventory exempts a chat with a run in flight from eviction. Both events carry the
  // chat (run_started is not replayed mid-run) and both say EXECUTING: a start fires on launch and
  // every resume, a node-level progress frame is a step moving.
  it("records the run LIVE and EXECUTING with its chat, on start and on progress", () => {
    send("run_started", { workflow_id: "wf_live", name: "publish" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", true);
    send("run_progress", { workflow_id: "wf_live", kind: "node_start" });
    expect(noteLive).toHaveBeenLastCalledWith("wf_live", "c1", true);
    expect(noteLive).toHaveBeenCalledTimes(2);
    expect(noteSettled).not.toHaveBeenCalled();
  });

  // A RUN-LEVEL pause folds into run_progress: live but not executing, or a parked run keeps its
  // chat's window resident for hours.
  it("records a run-level pause as live but NOT executing", () => {
    send("run_progress", { workflow_id: "wf_live", kind: "paused" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", false);
    expect(noteSettled).not.toHaveBeenCalled();
  });

  // Its counterpart, and the reason the gate is the run-level kind rather than any
  // pause: a node-level pause is one step waiting inside a run that is still going.
  it("records a NODE-level pause as still executing", () => {
    send("run_progress", { workflow_id: "wf_live", kind: "node_paused" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", true);
  });

  it("settles the run on every terminal finish, recognised or not", () => {
    for (const status of ["completed", "failed", "aborted", "cancelled", "exploded"]) {
      noteSettled.mockClear();
      send("run_finished", { workflow_id: "wf_end", status });
      expect(noteSettled, status).toHaveBeenCalledWith("wf_end");
    }
  });

  it("keeps a PAUSED run live but stops calling it executing", () => {
    // Presence means non-terminal, so the row and dot stay; the eviction exemption lapses, since a
    // policy stop (`onMaxIterations`) writes nothing more.
    send("run_finished", { workflow_id: "wf_pause", status: "paused" });
    expect(noteSettled).not.toHaveBeenCalled();
    expect(noteLive).toHaveBeenCalledWith("wf_pause", "c1", false);
  });

  // A parked run's mark can only say `waiting` through this frame's ONE re-read (an executing run
  // paints from `row.executing`, `chat-run-dots.test.ts`); this is the arm that stays live.
  it("still issues the one read a PARKED run's mark waits on", () => {
    send("run_finished", { workflow_id: "wf_pause", status: "paused" });
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith("wf_pause");
  });

  // A run's ask is filed under the LAUNCHING chat and `dropTurnDecisions` exempts run-scoped asks,
  // so only a terminal finish releases it (else the chat stays `input` amber).
  it("drops the run's unanswered asks on every terminal finish", () => {
    for (const status of ["completed", "failed", "aborted", "cancelled", "exploded"]) {
      dropAsks.mockClear();
      send("run_finished", { workflow_id: "wf_ask", status });
      expect(dropAsks, status).toHaveBeenCalledWith("wf_ask");
    }
  });

  it("leaves a PAUSED run's asks queued: that pause is what wants an answer", () => {
    send("run_finished", { workflow_id: "wf_pause", status: "paused" });
    expect(dropAsks).not.toHaveBeenCalled();
  });

  it("drops nothing on the frames that are not an ending", () => {
    send("run_started", { workflow_id: "wf_ask", name: "publish" });
    send("run_progress", { workflow_id: "wf_ask", kind: "node_complete" });
    expect(dropAsks).not.toHaveBeenCalled();
  });

  // The ORPHAN backstop: a step's ask with an EMPTY `run_id` is filed under the launching chat and
  // reachable only by `dropTurnDecisions`, whose other trigger never fires for a step-driven turn.
  // So these cases are about WHEN the sweep fires.
  describe("a run's terminal frame sweeps the launching chat's orphaned asks", () => {
    it("sweeps when the launching chat is idle and no sibling run is live", () => {
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).toHaveBeenCalledWith("c-parent");
    });

    it("leaves the chat's OWN live turn alone", () => {
      // The user prompted the launching chat while its run was going, so that
      // turn's permission ask is live and answerable — sweeping it would strand a
      // JSON-RPC request nothing can answer.
      chatThinking.mockReturnValue(true);
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("leaves a SIBLING run's orphan alone while that run is still executing", () => {
      // A sibling run's orphan also has an empty runID; `noteRunSettled` already ran for THIS run, so the
      // predicate answers about siblings only.
      siblingRunLive.mockReturnValue(true);
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("does not sweep on a PAUSE: the run is still going and its step is waiting", () => {
      send("run_finished", { workflow_id: "wf_orphan", status: "paused" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("sweeps nothing for a PARENTLESS run, which has no chat to sweep", () => {
      // A manual or scheduled run's lifecycle frames are workspace-global (empty
      // envelope chat id) and its asks are keyed to the synthetic `run:<id>`, which
      // `dropRunAsks` already reaches.
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });
  });
});

// The asymmetry is the contract: a start is announced only for a run nobody launched by hand; a
// completion for every run, since nothing else says a run ended.
describe("run toasts", () => {
  // Each case uses its own workflow ids. The start guard is module state that
  // outlives a test (it is keyed on the run, and a run does not restart because a
  // test ended), so sharing an id between cases would have one suppress another.
  it("announces a SCHEDULED run's start and names it", () => {
    send("run_started", { workflow_id: "wf_ann_1", name: "nightly-publish", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(1);
    expect(toastInfo.mock.calls[0]?.[0]).toBe("Scheduled run started: nightly-publish");
  });

  // A manual launch already has the user's attention: they pressed Run, and a run
  // tab opened in front of them. The flag is absent on the wire for such a run
  // rather than false, so an older server reads as manual too.
  it("says nothing when a run started manually", () => {
    send("run_started", { workflow_id: "wf_man_1", name: "publish" });
    send("run_started", { workflow_id: "wf_man_2", name: "publish", scheduled: false });
    expect(toasts()).toEqual([]);
  });

  // `run_start` re-fires on every resume — three frames were measured for one run
  // — and toast.ts coalesces nothing, so the guard is what stops one scheduled run
  // from producing a stack of identical toasts.
  it("announces a scheduled start once however often the frame re-fires", () => {
    for (let i = 0; i < 3; i++) {
      send("run_started", { workflow_id: "wf_resume", name: "nightly", scheduled: true });
    }
    expect(toastInfo).toHaveBeenCalledTimes(1);

    // A different run is a different announcement.
    send("run_started", { workflow_id: "wf_resume_other", name: "other", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(2);
  });

  it("announces a completion for a scheduled AND a manual run", () => {
    send("run_started", { workflow_id: "wf_sched", name: "nightly", scheduled: true });
    send("run_started", { workflow_id: "wf_manual", name: "by-hand" });
    toastInfo.mockClear();

    send("run_finished", { workflow_id: "wf_sched", status: "completed", name: "nightly" });
    send("run_finished", { workflow_id: "wf_manual", status: "completed", name: "by-hand" });
    expect(toastSuccess.mock.calls.map((c) => c[0])).toEqual([
      "nightly finished",
      "by-hand finished",
    ]);
  });

  it("names a labelled run by the fetched run's label over the frame's name", () => {
    fetchedLabel.mockImplementation((id) => (id === "wf_labelled" ? "publish-docs" : ""));
    send("run_started", { workflow_id: "wf_labelled", name: "publish", scheduled: true });
    send("run_finished", { workflow_id: "wf_labelled", status: "completed", name: "publish" });
    expect(toasts()).toEqual(["Scheduled run started: publish-docs", "publish-docs finished"]);
  });

  // The level follows the OUTCOME, not the event: "finished" is not a verdict.
  it("maps each terminal status to the level it deserves", () => {
    const cases: { status: string; want: string; level: "success" | "error" | "info" }[] = [
      { status: "completed", want: "publish finished", level: "success" },
      { status: "failed", want: "publish failed", level: "error" },
      { status: "aborted", want: "publish was aborted", level: "error" },
      { status: "cancelled", want: "publish was cancelled", level: "info" },
    ];
    for (const c of cases) {
      toastInfo.mockClear();
      toastSuccess.mockClear();
      toastError.mockClear();
      send("run_finished", { workflow_id: "wf_level", status: c.status, name: "publish" });
      const mock = { success: toastSuccess, error: toastError, info: toastInfo }[c.level];
      expect(mock.mock.calls.map((x) => x[0])).toEqual([c.want]);
      expect(toasts()).toHaveLength(1);
    }
  });

  // A paused run is not a completion. KAS reports an onMaxIterations policy stop
  // through this same frame, and the run is still resumable, so calling it
  // finished would be a false statement about work that has not ended.
  it("stays silent on a policy pause", () => {
    send("run_finished", { workflow_id: "wf_pause", status: "paused", name: "publish" });
    expect(toasts()).toEqual([]);
  });

  // A pause is also not the end of the run's start signal: the client stopped
  // believing it was running, so a resumed scheduled run announces itself again.
  it("re-announces a scheduled start after the run stopped", () => {
    send("run_started", { workflow_id: "wf_restart", name: "nightly", scheduled: true });
    send("run_finished", { workflow_id: "wf_restart", status: "paused", name: "nightly" });
    send("run_started", { workflow_id: "wf_restart", name: "nightly", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(2);
  });

  // A page opened mid-run, or a frame KAS sent no state with, has no name to use.
  // A generic label beats a bare workflow uuid and beats saying nothing.
  it("falls back to a generic label when the frame carries no name", () => {
    send("run_finished", { workflow_id: "wf_noname", status: "completed" });
    expect(toastSuccess.mock.calls[0]?.[0]).toBe("Workflow run finished");
    send("run_started", { workflow_id: "wf_noname_2", scheduled: true });
    expect(toastInfo.mock.calls[0]?.[0]).toBe("Scheduled run started: Workflow run");
  });

  // An unrecognised status is still an ending; naming it verbatim beats silence.
  it("passes an unknown status through rather than dropping it", () => {
    send("run_finished", { workflow_id: "wf_odd", status: "exploded", name: "publish" });
    expect(toastInfo.mock.calls[0]?.[0]).toBe("publish finished: exploded");
  });

  // The seven frames between the ends are invalidations, and a busy run emits
  // many: one toast each would bury the two that mean something.
  it("never toasts a progress frame", () => {
    for (const kind of ["node_start", "node_complete", "watch_poll", "loop_iteration", "paused"]) {
      send("run_progress", { workflow_id: "wf_prog", kind });
    }
    expect(toasts()).toEqual([]);
  });
});

// A run's own LOG: the six entry events behind the workflow-id guard, the MIRROR of the chat
// handler's, so two subscribers partition every frame.
describe("a run's own log", () => {
  const RUN = "wf_log";
  const TURN = "step-turn";

  function entry(kind: string, payload: unknown, seq = 1): Record<string, unknown> {
    return { id: `e${String(seq)}`, turn: TURN, kind, seq, ts: seq + 1, payload };
  }

  it("opens the run's turn, never a chat's", () => {
    const e = entry("turn_open", { source: "wire_turn_start", n: 1 }, 0);

    send("turn_opened", { workflow_id: RUN, entry: e }, "");

    expect(runTurnOpened).toHaveBeenCalledWith(RUN, e);
  });

  it("opens an entry into the run's log", () => {
    const open = { turn: TURN, id: "e1", kind: "text", text: "hi", n: 1 };

    send("entry_opened", { workflow_id: RUN, open }, "");

    expect(runEntryOpened).toHaveBeenCalledWith(RUN, open);
  });

  it("passes a delta's own five values through, lane included", () => {
    send(
      "entry_delta",
      { workflow_id: RUN, turn: TURN, entry_id: "e1", lane: "sub-7", n: 3, delta: "more" },
      "",
    );

    expect(runDelta).toHaveBeenCalledWith(RUN, TURN, "e1", "sub-7", 3, "more");
  });

  it("passes a seal's position and count through", () => {
    send(
      "entry_sealed",
      { workflow_id: RUN, turn: TURN, entry_id: "e1", lane: "sub-7", seq: 4, ts: 99, n: 6 },
      "",
    );

    expect(runSealed).toHaveBeenCalledWith(RUN, TURN, "e1", "sub-7", 4, 99, 6);
  });

  it("appends a born-sealed entry", () => {
    const e = entry("tool_call", { id: "tc1", title: "Run Command", kind: "execute" });

    send("entry_appended", { workflow_id: RUN, entry: e }, "");

    expect(runAppended).toHaveBeenCalledWith(RUN, e);
  });

  it("appends a turn_close like any other entry, so settled is read off the log", () => {
    // A step reads settled from its own `turn_close` rather than from a flag, which is
    // why this arm is an APPEND and not a status write.
    const e = entry("turn_close", { outcome: "completed" }, 2);

    send("turn_closed", { workflow_id: RUN, entry: e }, "");

    expect(runAppended).toHaveBeenCalledWith(RUN, e);
  });

  it("refetches the run when a step's turn closes broken, and only then", () => {
    // KAS's `node_complete` still reads `completed` for a step the server graded failed, so the
    // tree turns red only from a run read's `step_ends`.
    send(
      "turn_closed",
      { workflow_id: RUN, entry: entry("turn_close", { outcome: "completed" }, 2) },
      "",
    );
    send(
      "turn_closed",
      { workflow_id: RUN, entry: entry("turn_close", { outcome: "cancelled" }, 3) },
      "",
    );
    expect(invalidate).not.toHaveBeenCalled();

    send(
      "turn_closed",
      {
        workflow_id: RUN,
        entry: entry("turn_close", { outcome: "failed", failure_kind: "model_call_limit" }, 4),
      },
      "",
    );

    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenLastCalledWith(RUN);
  });

  it("leaves a frame carrying no workflow id to the chat's own handler", () => {
    // The other half of the partition, and the half that cannot be asserted from the
    // chat handler's suite: this module has to do NOTHING for a chat's log, or one frame
    // would land in two stores.
    send("entry_appended", { entry: entry("text", { text: "x" }) }, "c1");
    send("turn_opened", { entry: entry("turn_open", { source: "prompt", n: 1 }, 0) }, "c1");
    send("turn_closed", { entry: entry("turn_close", { outcome: "completed" }, 2) }, "c1");

    for (const op of [runTurnOpened, runEntryOpened, runDelta, runSealed, runAppended]) {
      expect(op).not.toHaveBeenCalled();
    }
    // And nothing about a run moved either: no refetch, no toast, no tracking.
    expect(invalidate).not.toHaveBeenCalled();
    expect(toasts()).toEqual([]);
  });
});

// The one run event carrying a payload: KAS parks with a fixed `pauseReason` and an empty
// `pauseDetail`, so `inspect` never says what was asked. The chat id comes off the ENVELOPE (the
// launching chat, or `run:<workflowId>` for a parentless run).
describe("a step's question", () => {
  interface AskDecision {
    kind: string;
    chatID: string;
    runID?: string;
    askID: string;
    submit: (text: string | null) => void;
    defer?: () => void | Promise<void>;
  }

  function ask(over: Record<string, unknown> = {}, chatID = "c1"): AskDecision {
    send(
      "run_input_needed",
      {
        workflow_id: "wf_1",
        ask_id: "notify:7",
        node_id: "review",
        step_session_id: "sess-1",
        agent_name: "reviewer",
        question: "Ship it?",
        asked_at: "2026-09-03T10:00:00Z",
        ...over,
      },
      chatID,
    );
    return enqueue.mock.calls.at(-1)?.[0] as unknown as AskDecision;
  }

  it("enqueues one decision keyed to the LAUNCHING chat", () => {
    const d = ask();
    expect(enqueue).toHaveBeenCalledTimes(1);
    expect(d.kind).toBe("run_input");
    expect(d.chatID).toBe("c1");
    // Both, because they are what the two hosts match on: the composer's dock
    // takes the chat id, a run tab's takes either.
    expect(d.runID).toBe("wf_1");
    expect(d.askID).toBe("notify:7");
  });

  it("keys a PARENTLESS run's ask to the synthetic run chat the server sent", () => {
    // A manual or scheduled run has no launching chat, so the envelope carries
    // `run:<workflowId>` and the run tab's dock is the surface that matches.
    const d = ask({}, "run:wf_1");
    expect(d.chatID).toBe("run:wf_1");
    expect(d.runID).toBe("wf_1");
  });

  it("tracks the run so its tab dot reports the block", () => {
    ask();
    expect(track).toHaveBeenCalledWith("wf_1");
  });

  // On the connect replay this frame can be the FIRST one a client sees for a run,
  // so without it the run card's footer link opens the tab top-level instead of
  // beside the conversation that launched it.
  it("records the launching chat, like the three lifecycle events do", () => {
    ask();
    expect(noteChat).toHaveBeenCalledWith("wf_1", "c1");
  });

  it("hands the synthetic run chat through unchanged and lets the store refuse it", () => {
    // `run:<workflowId>` is not a chat id, and the run store is where that rule
    // lives — a caller-side filter would be a second copy of it. See run-store.ts.
    ask({}, "run:wf_1");
    expect(noteChat).toHaveBeenCalledWith("wf_1", "run:wf_1");
  });

  // The ask is also the moment a client that missed every lifecycle frame learns the
  // run exists, so the inventory has to hear about it — PARKED, because a run waiting
  // on a person writes nothing, so the row's `executing` flag is false.
  it("notes the run live and PARKED, so the eviction exemption keeps answering no", () => {
    ask();
    expect(noteLive).toHaveBeenCalledWith("wf_1", "c1", false);
    expect(noteSettled).not.toHaveBeenCalled();
  });

  it("notes a parentless run's ask against no chat at all, not the synthetic key", () => {
    // The row still belongs in the inventory (its own tab dot reads it), and
    // `runChatID` is what keeps `run:<workflowId>` out of the chat field.
    launchingChat.mockReturnValue("");
    ask({}, "run:wf_1");
    expect(noteLive).toHaveBeenCalledWith("wf_1", "", false);
  });

  it("pushes a notification, because this ask blocks a run indefinitely", () => {
    ask();
    expect(notify).toHaveBeenCalledWith("Marotte", "A workflow step is waiting for your answer", {
      kind: "run",
      workflowID: "wf_1",
    });
  });

  it("refetches nothing: the question is on no endpoint", () => {
    ask();
    expect(invalidate).not.toHaveBeenCalled();
    expect(listRefetches).toBe(0);
    expect(toasts()).toEqual([]);
  });

  it("sends text to the ANSWER verb, addressed by ask id", () => {
    ask().submit("yes, ship it");
    expect(answer).toHaveBeenCalledWith({
      workflowID: "wf_1",
      ask_id: "notify:7",
      text: "yes, ship it",
    });
    expect(waive).not.toHaveBeenCalled();
  });

  it("sends null to the CONTINUE verb, addressed by NODE", () => {
    // The step-status verb takes a node, not an ask: it clears the node's
    // need-input signal so the step re-runs with its own default continuation.
    ask().submit(null);
    expect(waive).toHaveBeenCalledWith({ workflowID: "wf_1", nodeID: "review" });
    expect(answer).not.toHaveBeenCalled();
  });

  // Deferring to the launching agent: the prompt reaches the LAUNCHING CHAT and carries the two
  // routes, not the question the reader chose not to read.
  describe("deferring to the launching agent", () => {
    it("prompts the LAUNCHING chat and names the run, not the question", async () => {
      const d = ask();
      await d.defer?.();

      expect(mockSubmitPrompt).toHaveBeenCalledTimes(1);
      const [chat, text] = mockSubmitPrompt.mock.calls[0] as unknown as [string, string];
      // The run id would address the run to nobody: a prompt is delivered to a chat.
      expect(chat).toBe("c1");
      expect(text).toContain("wf_1");
      // Neither the ask id (opaque, and long) nor the question, which is the thing
      // the reader handed over rather than read.
      expect(text).not.toContain("notify:7");
      expect(text).not.toContain("Ship it?");
      // The two routes, so the agent can act on it without being told how.
      expect(text).toContain("GET /api/runs/wf_1");
      expect(text).toContain("POST /api/runs/wf_1/answer");
      // The body's field NAMES, quoted, because they are unguessable (`text`, not `answer`).
      expect(text).toContain('"ask_id"');
      expect(text).toContain('"text"');
    });

    it("carries NO deferral for a parentless run, which has no agent to ask", () => {
      // `runChatID` answering "" is the whole discriminator: noteRunChat refuses the
      // synthetic `run:` key, so a run nothing launched from a conversation has no
      // chat to prompt and the card must not offer the button at all.
      launchingChat.mockReturnValue("");
      const d = ask({}, "run:wf_1");
      expect(d.defer).toBeUndefined();
      // The property is ABSENT rather than undefined, which is what the card reads.
      expect("defer" in d).toBe(false);
    });

    it("throws on a refusal, so the card hands its button back", () => {
      // submit.ts returns "failed" rather than throwing and reports the refusal
      // through send-state, so the throw is what the card's failure arm needs and no
      // toast is owed here.
      mockSubmitPrompt.mockResolvedValue("failed");
      const d = ask();
      return expect(d.defer?.()).rejects.toThrow(/wf_1/);
    });
  });

  it("retires the card on the settle frame, and the banner tagged with the run", () => {
    send("run_input_settled", {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      settled_by: "user",
    });
    expect(retireAsk).toHaveBeenCalledWith("wf_1", "notify:7", "user");
    expect(closeNotifications).toHaveBeenCalledWith({ kind: "run", workflowID: "wf_1" });
    // Not an invalidation either: the run's own status never described the ask.
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("carries the unattended settler through rather than flattening it", () => {
    send("run_input_settled", {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      settled_by: "unattended",
    });
    expect(retireAsk).toHaveBeenCalledWith("wf_1", "notify:7", "unattended");
  });
});
