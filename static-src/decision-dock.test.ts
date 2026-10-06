// The dock's queue, not its cards: a second request must not overwrite the first, a background chat's ask must not
// vanish, a replayed ask must not stack, and one request id is answered once. Every lookup is scoped to the LIVE card
// (`:scope > .dock-card`) because an answered card stays on screen for a phase. The phase timer is the dock's only
// clock (the page links no stylesheet, so no transition event fires), so timers are faked.

import { vi, describe, it, expect, beforeEach, afterEach, beforeAll, afterAll } from "vitest";
import { userEvent } from "vitest/browser";

// The store is real: the chat-switch trigger is an effect over the real `activeSession` computed.
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so Browser Mode's real ESM linking succeeds for a name another module imports.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: vi.fn(),
}));
vi.mock("./actions/permissions.js", () => ({ editNativeRule: { dispatch: vi.fn() } }));
// Mocked to be asserted: the attribution toast is the observable half of a card collapsing under the reader.
const { mockToastInfo } = vi.hoisted(() => ({ mockToastInfo: vi.fn() }));
// The canonical factory: failure-notice.ts, in this graph, imports `errorWithAction`.
vi.mock("./toast.js", () =>
  import("./__test-helpers__/toast-mock.js").then((m) => ({
    ...m.toastMock(),
    info: mockToastInfo,
  })),
);

import {
  mountChatDecisionDock,
  mountDecisionDock,
  mountRunDecisionDock,
  rerenderDocks,
  pushDecision,
  dropDecisions,
  dropRunDecisions,
  collapseSettledDecision,
  collapseSettledRunInput,
  dropRunAsks,
  DOCK_PHASE_MS,
  runPendingAsks,
  _resetForTest,
} from "./decision-dock.js";
import { BUS_USER_INPUT_ANSWERED, onBus } from "./bus.js";
import { RUN_INPUT_FALLBACK } from "./dock-ask.js";
import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { clampObservationCount } from "./clamp-text.js";
// Read directly (`loadCSS` globs `*.css`; MANIFEST has no extension): its file order is a cascade fact for the
// reduced-motion disarm below.
import cssManifest from "./css/MANIFEST?raw";
import { setSessions, setActive } from "./store.js";
import type { PermissionNeededPayload, RunInputNeededPayload, Session } from "./types.js";

function session(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function host(): HTMLElement {
  return document.getElementById("decision-dock") as HTMLElement;
}

function perm(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    options: [
      { option_id: "allow_once", name: "Allow", kind: "allow_once" },
      { option_id: "reject_once", name: "Reject", kind: "reject_once" },
    ],
    title: "ls -la",
    kind: "execute",
    ...over,
  };
}

function pushPerm(chatID: string, requestID: number, submit = vi.fn()): typeof submit {
  pushDecision({
    kind: "permission",
    chatID,
    requestID,
    payload: perm({ request_id: requestID }),
    submit,
  });
  return submit;
}

/** The live cards: `:scope >` excludes an answered one, which sits inside `.dock-outgoing`. */
function liveCards(h: HTMLElement = host()): HTMLElement[] {
  return [...h.querySelectorAll<HTMLElement>(":scope > .dock-card")];
}

function liveCard(h: HTMLElement = host()): HTMLElement | null {
  return h.querySelector<HTMLElement>(":scope > .dock-card");
}

/** Scoped likewise: a stale depth row inside `.dock-outgoing` sits earlier in document order. */
function liveDepth(h: HTMLElement = host()): HTMLElement | null {
  return h.querySelector<HTMLElement>(":scope > .dock-depth");
}

function outgoings(h: HTMLElement = host()): HTMLElement[] {
  return [...h.querySelectorAll<HTMLElement>(".dock-outgoing")];
}

function clickButton(label: string, h: HTMLElement = host()): void {
  const scope: HTMLElement = liveCard(h) ?? h;
  const btn = [...scope.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent === label,
  );
  btn?.click();
}

/** The response went out on the click, but `.hidden` and the answered card's removal land at the phase's end. */
function settleMotion(): void {
  vi.advanceTimersByTime(Math.max(...Object.values(DOCK_PHASE_MS)) + 1);
}

beforeEach(() => {
  // Only the two functions the dock uses; faking Date and performance would reach the reactive graph.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  _resetForTest();
  mockToastInfo.mockClear();
  document.body.innerHTML = `<div id="decision-dock" class="hidden"></div>`;
  setSessions([session("c1"), session("c2")]);
  setActive("c1");
  mountDecisionDock(host());
});

afterEach(() => {
  // `restoreMocks` does not undo fake timers, and a leaked fake clock breaks every later file in this worker.
  vi.useRealTimers();
});

describe("the dock's visibility", () => {
  it("stays hidden and empty until something asks", () => {
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });

  it("reveals on a decision and hides again once answered", () => {
    pushPerm("c1", 1);
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();

    clickButton("Allow");
    // `.hidden` lands at the collapse's end: `display: none !important` cannot be animated out.
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });
});

describe("the queue", () => {
  it("shows one at a time and advances on answer, instead of losing the first", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);

    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.textContent).toBe("1 more waiting");

    clickButton("Allow");
    expect(first).toHaveBeenCalledWith({ optionID: "allow_once" });
    expect(second).not.toHaveBeenCalled();

    // The answered card is still on screen behind the new head for the advance.
    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith({ optionID: "reject_once" });
  });

  it("answers a request at most once", () => {
    const submit = pushPerm("c1", 1);
    const allow = [...host().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Allow",
    );
    // The card is detached by the first click; its retained handle must not reply twice on one request id.
    allow?.click();
    allow?.click();
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("ignores a re-delivered request (SSE reconnect replays unanswered asks)", () => {
    pushPerm("c1", 7);
    pushPerm("c1", 7);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
  });
});

describe("per-chat routing", () => {
  it("holds a background chat's ask instead of dropping it, and shows it on switch", () => {
    const bg = pushPerm("c2", 1);
    expect(host().classList.contains("hidden")).toBe(true);

    setActive("c2");
    expect(host().classList.contains("hidden")).toBe(false);
    clickButton("Allow");
    expect(bg).toHaveBeenCalledWith({ optionID: "allow_once" });
  });

  it("keeps an unanswered ask across a switch away and back", () => {
    pushPerm("c1", 1);
    setActive("c2");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    setActive("c1");
    expect(liveCard()).not.toBeNull();
  });

  it("dropDecisions clears a chat's asks without answering them", () => {
    const submit = pushPerm("c1", 1);
    dropDecisions("c1");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(submit).not.toHaveBeenCalled();
  });
});

describe("turn approval", () => {
  const files = [
    { path: "a.ts", action_id: "act-1" },
    { path: "b.ts", action_id: "act-2" },
  ];

  it("sends a decision for every offered action, because an omitted id is a rollback", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith({
      optionID: "allow_once",
      fileDecisions: { "act-1": true, "act-2": true },
    });
  });

  it("an unchecked row becomes a false decision, not an absent one", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    const boxes = liveCard()?.querySelectorAll<HTMLInputElement>(".dock-file-check") ?? [];
    expect(boxes.length).toBe(2);
    boxes[1]?.click();
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith({
      optionID: "allow_once",
      fileDecisions: { "act-1": true, "act-2": false },
    });
  });

  it("groups files sharing one action id into a single undividable row", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 6,
      payload: perm({
        request_id: 6,
        title: "Review changes",
        // KAS keys the decision map by action, so these two paths cannot disagree.
        files: [
          { path: "old.py", action_id: "ren-1" },
          { path: "new.py", action_id: "ren-1" },
        ],
      }),
      submit,
    });
    expect(liveCard()?.querySelectorAll(".dock-file-row").length).toBe(1);
    expect(liveCard()?.querySelectorAll(".dock-file-check").length).toBe(1);
    expect(host().querySelector(".dock-file-atomic")).not.toBeNull();
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith({
      optionID: "allow_once",
      fileDecisions: { "ren-1": true },
    });
  });

  it("Roll back all answers with the reject option and no map", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Roll back all");
    expect(submit).toHaveBeenCalledWith({ optionID: "reject_once" });
  });
});

describe("the spec page's dock", () => {
  function mountSpecHost(chat: () => string): HTMLElement {
    const el = document.createElement("div");
    el.className = "hidden";
    document.body.appendChild(el);
    mountChatDecisionDock(el, chat);
    return el;
  }

  it("renders the named chat's ask, whether or not that chat is active", () => {
    // This dock filters on the chat its host was mounted for, not the active chat, so a background spec page shows its
    // checkpoint.
    setSessions([session("c1"), session("c2")]);
    setActive("c2");
    const specHost = mountSpecHost(() => "c1");
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 21,
      payload: perm({ request_id: 21 }),
      submit: vi.fn(),
    });
    expect(specHost.classList.contains("hidden")).toBe(false);
    expect(liveCard(specHost)).not.toBeNull();
    expect(host().classList.contains("hidden")).toBe(true);
  });

  it("shows nothing for another chat's ask", () => {
    const specHost = mountSpecHost(() => "c1");
    pushDecision({
      kind: "permission",
      chatID: "c-other",
      requestID: 22,
      payload: perm({ request_id: 22 }),
      submit: vi.fn(),
    });
    expect(specHost.classList.contains("hidden")).toBe(true);
  });

  it("shows nothing while its getter answers empty, and re-keys when it fills", () => {
    // A getter because re-parenting changes the id; an empty answer must match no decision rather than every chat-less one.
    let shown = "";
    const specHost = mountSpecHost(() => shown);
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 23,
      payload: perm({ request_id: 23 }),
      submit: vi.fn(),
    });
    expect(specHost.classList.contains("hidden")).toBe(true);
    shown = "c1";
    rerenderDocks();
    expect(specHost.classList.contains("hidden")).toBe(false);
  });
});

describe("the run tab's dock", () => {
  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  it("renders a MANUAL run's ask — keyed to the synthetic run chat", () => {
    const host = mountRunHost(() => "wf_1");
    pushDecision({
      kind: "permission",
      chatID: "run:wf_1",
      requestID: 1,
      payload: perm({ request_id: 1 }),
      submit: vi.fn(),
    });
    expect(host.classList.contains("hidden")).toBe(false);
    expect(liveCard(host)).not.toBeNull();
  });

  it("renders an AGENT-LAUNCHED run's ask — keyed to the launching chat — in sync with the chat's dock", () => {
    // One decision object, two hosts, one answer clearing both.
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_2");

    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_2",
      requestID: 5,
      payload: perm({ request_id: 5 }),
      submit,
    });

    expect(liveCard()).not.toBeNull();
    expect(liveCard(runHost)).not.toBeNull();

    // Each host owns its own phase, so both have to be let through it.
    liveCard()?.querySelector<HTMLButtonElement>("button")?.click();
    expect(submit).toHaveBeenCalledTimes(1);
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("shows nothing for another run's ask", () => {
    const runHost = mountRunHost(() => "wf_3");
    pushDecision({
      kind: "permission",
      chatID: "run:wf_OTHER",
      requestID: 9,
      payload: perm({ request_id: 9 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("re-keys when the shared view shows a different run", () => {
    // One #run-dock serves every run tab; the run id is a getter and a tab switch re-renders through rerenderDocks.
    let shown = "wf_a";
    const runHost = mountRunHost(() => shown);
    pushDecision({
      kind: "permission",
      chatID: "run:wf_b",
      requestID: 11,
      payload: perm({ request_id: 11 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(true);
    shown = "wf_b";
    rerenderDocks();
    expect(runHost.classList.contains("hidden")).toBe(false);
  });

  // The transcript's run card reads this: it has no surface to answer on and needs which step is blocked.
  describe("runPendingAsks", () => {
    it("joins both keyings and names the steps", () => {
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 1,
        payload: perm({ request_id: 1, node_id: "build", title: "Run git push" }),
        submit: vi.fn(),
      });
      pushDecision({
        kind: "permission",
        chatID: "c1",
        runID: "wf_1",
        requestID: 2,
        payload: perm({ request_id: 2, node_id: "test" }),
        submit: vi.fn(),
      });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(2);
      expect([...a.nodes].sort()).toEqual(["build", "test"]);
      expect(a.label).toBe("Run git push");
    });

    it("counts an ask the wire could not attribute to a step", () => {
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 3,
        payload: perm({ request_id: 3 }),
        submit: vi.fn(),
      });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(1);
      expect(a.nodes.size).toBe(0);
    });

    it("ignores a chat's own ask and another run's", () => {
      pushDecision({
        kind: "permission",
        chatID: "c1",
        requestID: 4,
        payload: perm({ request_id: 4, node_id: "build" }),
        submit: vi.fn(),
      });
      pushDecision({
        kind: "permission",
        chatID: "run:wf_OTHER",
        requestID: 5,
        payload: perm({ request_id: 5 }),
        submit: vi.fn(),
      });
      expect(runPendingAsks("wf_1").count).toBe(0);
      expect(runPendingAsks("").count).toBe(0);
    });

    it("clears once the ask is answered", () => {
      const host = mountRunHost(() => "wf_1");
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 6,
        payload: perm({ request_id: 6, node_id: "build" }),
        submit: vi.fn(),
      });
      expect(runPendingAsks("wf_1").count).toBe(1);
      host.querySelector<HTMLButtonElement>("button")?.click();
      expect(runPendingAsks("wf_1").count).toBe(0);
    });
  });

  it("lets the run tab answer an ask sitting BEHIND the chat's own head", () => {
    // Settle guards on membership, not head position: each request id is its own JSON-RPC exchange, so answering out of
    // queue order is correct and refusing it would leave a dead button in the run tab.
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_4");

    const chatSubmit = pushPerm("c1", 20);
    const stepSubmit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_4",
      requestID: 21,
      payload: perm({ request_id: 21 }),
      submit: stepSubmit,
    });

    runHost.querySelector<HTMLButtonElement>("button")?.click();
    expect(stepSubmit).toHaveBeenCalledTimes(1);
    expect(chatSubmit).not.toHaveBeenCalled();
    expect(liveCard()).not.toBeNull();
  });
});

// A workflow step's question: not request-shaped (a string ask id, not an int64), hence the per-kind key. The prompt
// must reach the parent tab, the chat that launched the run.

describe("a workflow step's question", () => {
  function runInput(over: Partial<RunInputNeededPayload> = {}): RunInputNeededPayload {
    return {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      node_id: "review",
      step_session_id: "sess-1",
      agent_name: "reviewer",
      question: "Ship it?",
      asked_at: "2026-09-03T10:00:00Z",
      ...over,
    };
  }

  function pushAsk(
    chatID: string,
    over: Partial<RunInputNeededPayload> = {},
    submit: (text: string | null) => void = vi.fn(),
    defer?: () => void | Promise<void>,
  ): typeof submit {
    const payload = runInput(over);
    pushDecision({
      kind: "run_input",
      chatID,
      runID: payload.workflow_id,
      askID: payload.ask_id,
      payload,
      submit,
      ...(defer === undefined ? {} : { defer }),
    });
    return submit;
  }

  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  function textarea(h: HTMLElement = host()): HTMLTextAreaElement | null {
    return liveCard(h)?.querySelector<HTMLTextAreaElement>(".dock-ask-text") ?? null;
  }

  it("renders in the PARENT TAB, keyed to the launching chat", () => {
    // The envelope's chat id is the launching chat, so the composer dock's own matcher puts the card in the parent tab.
    pushAsk("c1");
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
    expect(liveCard()?.textContent).toContain("Ship it?");
    expect(liveCard()?.textContent).toContain("reviewer \u00b7 step review");
  });

  it("renders a PARENTLESS run's ask too, keyed to the synthetic run chat", () => {
    // A manual or scheduled run has no launching chat, so its ask is keyed `run:<workflowId>` and the run tab is its surface.
    const runHost = mountRunHost(() => "wf_1");
    pushAsk("run:wf_1");
    expect(host().classList.contains("hidden")).toBe(true);
    expect(liveCard(runHost)).not.toBeNull();
  });

  it("shows one ask in BOTH hosts and one answer clears both", () => {
    const runHost = mountRunHost(() => "wf_1");
    const submit = pushAsk("c1");
    expect(liveCard()).not.toBeNull();
    expect(liveCard(runHost)).not.toBeNull();

    const box = textarea();
    if (box !== null) {
      box.value = "  yes, ship it  ";
    }
    clickButton("Send answer");
    expect(submit).toHaveBeenCalledWith("yes, ship it");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("sends null for continue-without-answering", () => {
    // The ask registry is in memory, so after a restart the question is gone; `null` re-drives the step with KAS's continuation.
    const submit = pushAsk("c1", { question: "" });
    expect(liveCard()?.textContent).toContain(RUN_INPUT_FALLBACK);
    expect(liveCard()?.textContent).toContain("lost when the server restarted");
    clickButton("Continue without answering");
    expect(submit).toHaveBeenCalledWith(null);
  });

  it("refuses an empty answer instead of sending one", () => {
    const submit = pushAsk("c1");
    clickButton("Send answer");
    expect(submit).not.toHaveBeenCalled();
    expect(liveCard()).not.toBeNull();
  });

  it("answers an ask at most once", () => {
    const submit = pushAsk("c1");
    const box = textarea();
    if (box !== null) {
      box.value = "ok";
    }
    const send = [...(liveCard()?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (b) => b.textContent === "Send answer",
    );
    send?.click();
    send?.click();
    expect(submit).toHaveBeenCalledTimes(1);
  });

  // `settle` splices the entry before the answer goes out, so on a retryable refusal the same ask returns on a fresh
  // `run_input_needed` and the held text must come back with it, in both directions.
  describe("the words a refused send is holding", () => {
    function sendAnswer(text: string): void {
      const box = textarea();
      if (box !== null) {
        box.value = text;
      }
      clickButton("Send answer");
    }

    it("comes back with the ask the server re-offered", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      // restoreAsk re-broadcasts the same ask on a between-steps refusal.
      pushAsk("c1");
      expect(textarea()?.value).toBe("the release branch");
    });

    it("is dropped once the ask is genuinely settled", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      collapseSettledRunInput("wf_1", "notify:7", "user");
      // A second ask with that id is a new question, so the old answer must not seed it.
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is dropped when the run itself ends", () => {
      // A run's terminal sweep drops cards without a per-ask settle frame, so nothing else frees the words.
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      dropRunAsks("wf_1");
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is dropped by continue-without-answering, which retires the question", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      pushAsk("c1");
      clickButton("Continue without answering");
      settleMotion();
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is held per ASK, so a second parked step cannot evict the first's", () => {
      // Keyed by ask, not one slot, or the older answer is lost here.
      pushAsk("c1", { ask_id: "notify:1" });
      sendAnswer("the release branch");
      settleMotion();
      pushAsk("c1", { ask_id: "notify:2" });
      sendAnswer("main");
      settleMotion();

      pushAsk("c1", { ask_id: "notify:1" });
      expect(textarea()?.value).toBe("the release branch");
    });
  });

  // Deferring is a pass-through and must not settle: splicing would take the card off every surface while the run is
  // still parked.
  describe("deferring to the launching agent", () => {
    it("hands the deferral through and leaves the ask OPEN", () => {
      const defer = vi.fn();
      const submit = pushAsk("c1", {}, vi.fn(), defer);
      clickButton("Defer to parent agent");

      expect(defer).toHaveBeenCalledTimes(1);
      expect(submit).not.toHaveBeenCalled();
      settleMotion();
      // Both halves: the DOM could be a phase's leftover and the count a card nobody can see.
      expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
      expect(runPendingAsks("wf_1").count).toBe(1);
    });

    it("offers no deferral for an ask the decision carries none for", () => {
      // A parentless run has no launching agent; the callback passes through verbatim.
      const runHost = mountRunHost(() => "wf_1");
      pushAsk("run:wf_1");
      const labels = [
        ...(liveCard(runHost)?.querySelectorAll(".dock-ask-actions button") ?? []),
      ].map((b) => b.textContent);
      expect(labels).toEqual(["Send answer", "Continue without answering"]);
    });
  });

  it("ignores a re-delivered ask (the connect replay re-offers every parked one)", () => {
    // The server replays every parked question on connect; identity is the ask id.
    pushAsk("c1");
    pushAsk("c1");
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
  });

  it("keys separately from a permission carrying the same number", () => {
    // An ask id is arbitrary text: permission 1 beside ask "1" must stay two decisions, or the per-kind key collapsed.
    pushPerm("c1", 1);
    pushAsk("c1", { ask_id: "1" });
    expect(liveDepth()?.textContent).toBe("1 more waiting");
  });

  it("survives a switch away from the parent tab and back", () => {
    pushAsk("c1");
    setActive("c2");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    setActive("c1");
    expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
  });

  describe("runPendingAsks", () => {
    it("counts an ask under either keying and names its step", () => {
      pushAsk("c1", { ask_id: "notify:1", node_id: "review" });
      pushAsk("run:wf_1", { ask_id: "notify:2", node_id: "build" });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(2);
      expect([...a.nodes].sort()).toEqual(["build", "review"]);
      expect(a.label).toBe("Ship it?");
    });

    it("labels a question-less ask with the shared fallback", () => {
      // The card heading and the run card's alert share one exported fallback constant.
      pushAsk("run:wf_1", { question: "" });
      expect(runPendingAsks("wf_1").label).toBe(RUN_INPUT_FALLBACK);
    });

    it("clears once the ask is answered", () => {
      const runHost = mountRunHost(() => "wf_1");
      pushAsk("run:wf_1");
      expect(runPendingAsks("wf_1").count).toBe(1);
      const box = textarea(runHost);
      if (box !== null) {
        box.value = "done";
      }
      clickButton("Send answer", runHost);
      expect(runPendingAsks("wf_1").count).toBe(0);
    });
  });

  describe("dropRunDecisions", () => {
    it("clears a run-keyed ask, which the per-chat sweep cannot reach", () => {
      // A transport gap's sweep walks the chat store, and `run:<workflowId>` is no chat, so an ask answered during the outage
      // kept its card and its click answered 409.
      const submit = pushAsk("run:wf_1");
      dropRunDecisions();
      settleMotion();
      expect(runPendingAsks("wf_1").count).toBe(0);
      expect(submit).not.toHaveBeenCalled();
    });

    it("leaves an ask keyed to a launching CHAT alone", () => {
      // The chat's queue is `dropDecisions`'; dropping it here too would drop it twice and, keyed on runID, reach every
      // chat-keyed run ask.
      pushAsk("c1");
      dropRunDecisions();
      expect(runPendingAsks("wf_1").count).toBe(1);
      expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
    });
  });

  describe("collapseSettledRunInput", () => {
    it("retires the card and says a person answered elsewhere", () => {
      const submit = pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "user");
      settleMotion();

      expect(host().classList.contains("hidden")).toBe(true);
      expect(submit).not.toHaveBeenCalled();
      expect(mockToastInfo).toHaveBeenCalledWith(
        "c1: The workflow step's question was answered in another window.",
      );
    });

    it("says a machine answered when the unattended floor did", () => {
      pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "unattended");
      expect(mockToastInfo).toHaveBeenCalledWith(
        "c1: The workflow step's question was answered automatically because nobody was watching.",
      );
    });

    it("claims NO answer when the question merely stopped being answerable", () => {
      // A run ask needs a third settler: the step can move on or the run end while it is parked, with nobody replying, so
      // "answered in another window" would be false.
      pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "moot");
      expect(mockToastInfo).toHaveBeenCalledWith(
        "c1: The workflow step's question is no longer waiting for an answer.",
      );
    });

    it("finds a PARENTLESS run's ask, which no chat id names", () => {
      // The settle event carries only the run and a parentless ask is keyed `run:<id>`, so every queue is scanned.
      const runHost = mountRunHost(() => "wf_1");
      pushAsk("run:wf_1");
      collapseSettledRunInput("wf_1", "notify:7", "user");
      settleMotion();
      expect(runHost.classList.contains("hidden")).toBe(true);
      expect(runPendingAsks("wf_1").count).toBe(0);
    });

    it("leaves an ask belonging to another run alone", () => {
      pushAsk("c1");
      collapseSettledRunInput("wf_OTHER", "notify:7", "user");
      collapseSettledRunInput("wf_1", "notify:OTHER", "user");
      collapseSettledRunInput("", "notify:7", "user");
      collapseSettledRunInput("wf_1", "", "user");

      expect(liveCard()).not.toBeNull();
      expect(mockToastInfo).not.toHaveBeenCalled();
    });

    it("does not retire a permission with the same number as the ask id", () => {
      const submit = pushPerm("c1", 7);
      collapseSettledRunInput("wf_1", "7", "user");
      expect(liveCard()).not.toBeNull();
      clickButton("Allow");
      expect(submit).toHaveBeenCalledTimes(1);
    });
  });
});

describe("a decision another surface answered", () => {
  // Only the first answer across surfaces is accepted, so every other surface's card must retire.

  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  it("collapses the card and says a person answered elsewhere", () => {
    const submit = pushPerm("c1", 1);
    expect(liveCard()).not.toBeNull();

    collapseSettledDecision("c1", "permission", 1, "user");
    settleMotion();

    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(submit).not.toHaveBeenCalled();
    expect(mockToastInfo).toHaveBeenCalledWith(
      "c1: The permission request was answered in another window.",
    );
  });

  it("says a machine answered when the unattended floor did", () => {
    // The reader of a collapsing card must learn a deadline decided it, not a colleague.
    pushDecision({
      kind: "user_input",
      chatID: "c1",
      requestID: 3,
      payload: { request_id: 3, question: "Which region?" },
      submit: vi.fn(),
    });
    collapseSettledDecision("c1", "user_input", 3, "unattended");

    expect(mockToastInfo).toHaveBeenCalledWith(
      "c1: The agent's question was answered automatically because nobody was watching.",
    );
  });

  it("stays quiet about a card the reader never saw", () => {
    const head = pushPerm("c1", 1);
    pushPerm("c1", 2);

    collapseSettledDecision("c1", "permission", 2, "user");

    expect(mockToastInfo).not.toHaveBeenCalled();
    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    clickButton("Allow");
    expect(head).toHaveBeenCalledTimes(1);
  });

  it("ignores a request it is not holding", () => {
    // The answering surface finds nothing to remove (answering splices first); it must not announce or disturb another ask.
    pushPerm("c1", 1);
    collapseSettledDecision("c1", "permission", 999, "user");
    collapseSettledDecision("c-unknown", "permission", 1, "user");

    expect(mockToastInfo).not.toHaveBeenCalled();
    expect(liveCard()).not.toBeNull();
  });

  it("answers the settled ask's run attribution, and nothing for an ask it never held", () => {
    // The settle frame names the chat the ask travelled on, the banner was tagged by the run; only the dock knows both.
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_1",
      requestID: 7,
      payload: perm({ request_id: 7 }),
      submit: vi.fn(),
    });
    pushPerm("c1", 8);

    expect(collapseSettledDecision("c1", "permission", 7, "user")).toBe("wf_1");
    expect(collapseSettledDecision("c1", "permission", 8, "user")).toBe("");
    expect(collapseSettledDecision("c1", "permission", 8, "user")).toBeUndefined();
  });

  it("matches on kind as well as request id", () => {
    // Request ids are per-bridge JSON-RPC ids, so one id can name a permission and an elicitation.
    const submit = pushPerm("c1", 1);
    collapseSettledDecision("c1", "elicitation", 1, "user");

    expect(liveCard()).not.toBeNull();
    expect(mockToastInfo).not.toHaveBeenCalled();
    clickButton("Allow");
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("retires the ask on the run tab too, from one event", () => {
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_9");
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_9",
      requestID: 30,
      payload: perm({ request_id: 30 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(false);

    collapseSettledDecision("c1", "permission", 30, "user");
    settleMotion();

    expect(runHost.classList.contains("hidden")).toBe(true);
    expect(host().classList.contains("hidden")).toBe(true);
  });
});

describe("an answered agent question on the bus", () => {
  // Announces rather than calls: `spec-view.ts` imports this module, so the reverse edge would close a cycle.

  function pushAsk(chatID = "c1", submit = vi.fn()): typeof submit {
    pushDecision({
      kind: "user_input",
      chatID,
      requestID: 7,
      payload: {
        request_id: 7,
        question: "The spec is ready to implement. Run the tasks now?",
        options: [
          { title: "Run required tasks" },
          { title: "Run required and optional tasks" },
          { title: "Not now" },
        ],
      },
      submit,
    });
    return submit;
  }

  function clickOption(title: string): void {
    const scope = liveCard();
    const btn = [...(scope?.querySelectorAll<HTMLButtonElement>(".user-input-option") ?? [])].find(
      (b) => b.querySelector(".user-input-option-title")?.textContent === title,
    );
    btn?.click();
  }

  it("carries the chat and the answer verbatim, after the answer went out", () => {
    const seen: { chatID: string; answer: string }[] = [];
    const order: string[] = [];
    const off = onBus(BUS_USER_INPUT_ANSWERED, (p) => {
      order.push("bus");
      seen.push(p);
    });
    // The composer's dock shows the active chat's queue.
    setActive("c2");
    const submit = pushAsk(
      "c2",
      vi.fn(() => order.push("submit")),
    );

    clickOption("Run required and optional tasks");
    off();

    // Verbatim: the spec page matches the option's own title.
    expect(seen).toEqual([{ chatID: "c2", answer: "Run required and optional tasks" }]);
    expect(submit).toHaveBeenCalledWith("answered", "Run required and optional tasks");
    // The agent has the answer before any consumer acts on it.
    expect(order).toEqual(["submit", "bus"]);
  });

  it("announces nothing for a dismissal", () => {
    const seen: unknown[] = [];
    const off = onBus(BUS_USER_INPUT_ANSWERED, (p) => seen.push(p));
    const submit = pushAsk();

    clickButton("Skip");
    off();

    expect(submit).toHaveBeenCalledWith("dismissed", undefined);
    expect(seen).toEqual([]);
  });

  it("announces once however many times the answered card is clicked", () => {
    // The emit sits inside the callback `settle` guards: an answered card keeps its listeners for the leaving phase, so a
    // hoisted emit would send a second Run all.
    const seen: unknown[] = [];
    const off = onBus(BUS_USER_INPUT_ANSWERED, (p) => seen.push(p));
    const submit = pushAsk();
    const btn = [
      ...(liveCard()?.querySelectorAll<HTMLButtonElement>(".user-input-option") ?? []),
    ][0];
    if (btn === undefined) {
      throw new Error("no option button");
    }

    btn.click();
    btn.click();
    off();

    expect(btn.isConnected).toBe(true);
    expect(submit).toHaveBeenCalledTimes(1);
    expect(seen).toHaveLength(1);
  });
});

// Motion: the phases, dispatch order, and that an advance never takes the tray through zero. These pin the state
// machine, not wall-clock timing; the phase window is pinned at the bottom of this file.

describe("the enter phase", () => {
  it("grows from collapsed with the content in place, then cleans up after itself", () => {
    pushPerm("c1", 1);

    expect(host().dataset["dockPhase"]).toBe("entering");
    // Un-hidden for the whole phase: the box has to be laid out to animate.
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
    expect(outgoings().length).toBe(0);

    settleMotion();
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
  });
});

describe("the exit phase", () => {
  it("keeps the answered card on screen while the tray shrinks, and hides only at the end", () => {
    pushPerm("c1", 1);
    settleMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBe("leaving");
    // The host must not be `.hidden` yet, or `display: none` ends the animation on its first frame.
    expect(host().classList.contains("hidden")).toBe(false);
    expect(outgoings().length).toBe(1);
    expect(outgoings()[0]?.querySelector(".dock-card")).not.toBeNull();
    expect(liveCard()).toBeNull();

    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
  });
});

describe("the advance phase", () => {
  it("morphs between two cards instead of collapsing and re-growing", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBe("advancing");
    // One of each, coexisting, so a frame with neither is unreachable.
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);
    expect(host().classList.contains("hidden")).toBe(false);

    settleMotion();
    expect(outgoings().length).toBe(0);
    expect(liveCards().length).toBe(1);
  });

  it("never adds .hidden at any point — requirement 3, watched rather than sampled", async () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();

    // Collected raw: `oldValue` is the only trustworthy reading, since `r.target`'s className is the current one by
    // callback time.
    const records: MutationRecord[] = [];
    const obs = new MutationObserver((batch) => {
      records.push(...batch);
    });
    obs.observe(host(), {
      attributes: true,
      attributeFilter: ["class"],
      attributeOldValue: true,
    });

    clickButton("Allow");
    expect(host().classList.contains("hidden")).toBe(false);
    await Promise.resolve();
    settleMotion();
    await Promise.resolve();
    records.push(...obs.takeRecords());
    obs.disconnect();

    const held = [...records.map((r) => r.oldValue ?? ""), host().className];
    expect(held.filter((cls) => cls.split(/\s+/).includes("hidden"))).toEqual([]);
    expect(liveCard()).not.toBeNull();
  });

  it("updates the LIVE depth row mid-advance, not the answered card's stale one", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    pushPerm("c1", 3);
    settleMotion();
    expect(liveDepth()?.textContent).toBe("2 more waiting");

    clickButton("Allow");
    expect(liveDepth()?.textContent).toBe("1 more waiting");

    // A depth change for the same head updates in place (a rebuild discards typing). The answered card is prepended, so an
    // unscoped lookup would write into the outgoing card.
    collapseSettledDecision("c1", "permission", 3, "user");

    expect(liveDepth()?.textContent).toBe("");
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    expect(liveCards().length).toBe(1);
  });

  it("does not drop the next decision, and it is answerable", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    settleMotion();

    clickButton("Allow");
    expect(first).toHaveBeenCalledTimes(1);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith({ optionID: "reject_once" });
  });
});

describe("the dispatch is never gated on the animation", () => {
  it("submits in the same tick as the click, before any timer runs", () => {
    const submit = pushPerm("c1", 1);
    settleMotion();

    clickButton("Allow");
    // `settle` splices, dispatches and bumps synchronously in the click handler; the animation's effect runs after.
    expect(submit).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledWith({ optionID: "allow_once" });
    expect(host().dataset["dockPhase"]).toBe("leaving");
  });
});

describe("interruption and cleanup", () => {
  it("survives rapid Allow-Allow-Allow with nothing half-faded left over", () => {
    const subs = [pushPerm("c1", 1), pushPerm("c1", 2), pushPerm("c1", 3)];
    settleMotion();

    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);

    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);

    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(0);

    for (const s of subs) {
      expect(s).toHaveBeenCalledTimes(1);
    }

    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(document.querySelectorAll(".dock-outgoing").length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(host().style.height).toBe("");
    expect(host().style.marginBlockEnd).toBe("");
    expect(host().style.transition).toBe("");
  });

  it("re-enters when a decision arrives mid-exit instead of completing the exit", () => {
    pushPerm("c1", 1);
    settleMotion();
    clickButton("Allow");
    expect(host().dataset["dockPhase"]).toBe("leaving");

    // The exit's timer must not hide a dock that has content again.
    pushPerm("c1", 2);
    expect(host().dataset["dockPhase"]).toBe("entering");
    expect(liveCard()).not.toBeNull();

    settleMotion();
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
    expect(outgoings().length).toBe(0);
  });

  it("neutralises the answered card so it is neither read again nor focusable", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();
    liveCard()?.querySelector<HTMLButtonElement>("button")?.focus();

    clickButton("Allow");

    const [out] = outgoings();
    expect(out?.getAttribute("aria-hidden")).toBe("true");
    expect(out?.hasAttribute("inert")).toBe(true);
    expect(out?.contains(document.activeElement)).toBe(false);
  });

  it("takes no second answer from a handle retained on the answered card", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    settleMotion();

    const allow = liveCard()?.querySelector<HTMLButtonElement>("button");
    allow?.click();
    // A scripted click dispatches through `inert`, so `settle`'s membership check is the guard; `user-input.ts` keeps no
    // reporter in module state so the outgoing card cannot answer the incoming decision.
    allow?.click();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).not.toHaveBeenCalled();
  });

  it("_resetForTest leaves no timer that can fire into a later test", () => {
    const dock = host();
    pushPerm("c1", 1);
    settleMotion();
    clickButton("Allow");
    expect(dock.dataset["dockPhase"]).toBe("leaving");

    _resetForTest();
    // The host is deliberately not hidden, so a surviving timer would show as a stray `.hidden`.
    expect(dock.querySelectorAll(".dock-outgoing").length).toBe(0);
    expect(dock.dataset["dockPhase"]).toBeUndefined();

    settleMotion();
    expect(dock.classList.contains("hidden")).toBe(false);
  });
});

describe("motion off: reduced motion and a background tab", () => {
  function reduceMotion(): void {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q.includes("prefers-reduced-motion"),
      media: q,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  }

  it("swaps instantly on an exit, with the same DOM and the same response", () => {
    const submit = pushPerm("c1", 1);
    reduceMotion();

    clickButton("Allow");

    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("swaps instantly on an advance too", () => {
    pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    reduceMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(liveCards().length).toBe(1);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith({ optionID: "reject_once" });
  });

  it("takes no phase in a background tab, where setTimeout runs but animations do not", () => {
    // An accessor on the prototype, so spying on the instance does not work.
    const submit = pushPerm("c1", 1);
    vi.spyOn(Document.prototype, "hidden", "get").mockReturnValue(true);

    clickButton("Allow");

    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(submit).toHaveBeenCalledTimes(1);
  });
});

// A long question once pushed the answer row past the view with nothing to scroll. The clamp is the refinement; the
// card's ceiling is the guarantee, so geometry is asserted with the question open.

const PARAGRAPH =
  "The review found three call sites that disagree about whether a bridgeless chat is an " +
  "error or an ordinary idle state, and each one reports it to the reader differently. " +
  "Answering this decides which of the three becomes the one the other two adopt, and " +
  "which of the two error surfaces the losing pair stop writing to. ";
const LONG_QUESTION = `${PARAGRAPH}\n\n`.repeat(7);

const OPTIONS = Array.from({ length: 10 }, (_, i) => ({
  title: `Adopt the ${String(i + 1)}th call site's reading`,
  description: "Keeps that surface's wording and rewrites the other two to match it.",
}));

function longAsk(chatID: string, submit: (text: string | null) => void = vi.fn()): void {
  pushDecision({
    kind: "run_input",
    chatID,
    runID: "wf_long",
    askID: "notify:long",
    payload: {
      workflow_id: "wf_long",
      ask_id: "notify:long",
      node_id: "review",
      step_session_id: "sess-long",
      agent_name: "reviewer",
      question: LONG_QUESTION,
      asked_at: "2026-09-03T10:00:00Z",
    },
    submit,
  });
}

function question(h: HTMLElement = host()): HTMLElement | null {
  return liveCard(h)?.querySelector<HTMLElement>(".dock-ask-question") ?? null;
}

function opener(h: HTMLElement = host()): HTMLButtonElement | null {
  return liveCard(h)?.querySelector<HTMLButtonElement>(".dock-ask-more") ?? null;
}

describe("a question longer than the card", () => {
  it("clamps it and offers the opener", () => {
    longAsk("c1");
    expect(question()?.hasAttribute("data-clamped")).toBe(true);
    expect(opener()?.hidden).toBe(false);
    expect(opener()?.textContent).toBe("Show more");
  });

  it("keeps the whole question in the DOM, so opening it reveals rather than refetches", () => {
    longAsk("c1");
    expect(question()?.textContent).toBe(LONG_QUESTION);

    opener()?.click();
    expect(question()?.hasAttribute("data-clamped")).toBe(false);
    expect(opener()?.textContent).toBe("Show less");
  });

  it("leaves a SHORT question unclamped, so the opener is not permanent furniture", () => {
    pushDecision({
      kind: "run_input",
      chatID: "c1",
      runID: "wf_1",
      askID: "notify:1",
      payload: {
        workflow_id: "wf_1",
        ask_id: "notify:1",
        node_id: "review",
        step_session_id: "sess-1",
        agent_name: "reviewer",
        question: "Ship it?",
        asked_at: "2026-09-03T10:00:00Z",
      },
      submit: vi.fn(),
    });
    expect(opener()?.hidden).toBe(true);
  });

  // The observer holds targets strongly and its zero-size callback may never arrive, so only the explicit release drops a
  // leaving card. A delta, not a count: every mounted dock's effect stays live and clamps.
  it("releases the clamp when the card leaves", () => {
    const before = clampObservationCount();
    longAsk("c1");
    expect(clampObservationCount()).toBeGreaterThan(before);

    // An empty box focuses itself and sends nothing, so the answer is typed.
    const box = liveCard()?.querySelector<HTMLTextAreaElement>(".dock-ask-text");
    if (box === null || box === undefined) {
      throw new Error("no answer box");
    }
    box.value = "the release branch";
    clickButton("Send answer");
    settleMotion();
    expect(clampObservationCount()).toBe(before);
  });
});

// Measured against the assembled cascade: whether the answer row survives both bounds is a layout fact.

describe("the card's ceiling keeps the answer row on screen", () => {
  let style: HTMLStyleElement;
  let disarm: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
    // Disarms the phase transition and entry animation, which otherwise make the first rect read a transition frame.
    // Mounted after `mountAppCSS` so it wins the equal-specificity tie.
    disarm = document.createElement("style");
    disarm.textContent =
      ".decision-dock { transition: none } .decision-dock > .dock-card { animation: none }";
    document.head.appendChild(disarm);
  });

  afterAll(() => {
    style.remove();
    disarm.remove();
  });

  /** The reported phone width: at 1280px the same question is four lines and overflows nothing. */
  function boundedHost(): HTMLElement {
    const frame = document.createElement("div");
    frame.style.width = "390px";
    const el = document.createElement("div");
    el.className = "decision-dock hidden";
    frame.appendChild(el);
    document.body.appendChild(frame);
    mountDecisionDock(el);
    return el;
  }

  interface Parts {
    readonly card: HTMLElement;
    readonly body: HTMLElement;
    readonly actions: HTMLElement;
  }

  function openCard(): Parts {
    const h = boundedHost();
    longAsk("c1");
    opener(h)?.click();
    const card = liveCard(h);
    const body = card?.querySelector<HTMLElement>(".dock-ask-body");
    const actions = card?.querySelector<HTMLElement>(".dock-ask-actions");
    if (
      card === null ||
      card === undefined ||
      body === null ||
      body === undefined ||
      actions === null ||
      actions === undefined
    ) {
      throw new Error("no card");
    }
    return { card, body, actions };
  }

  // Premise: a shorter fixture, or one the region could contain, would pass with the bound deleted.
  it("the fixture is the reported length and overflows the region", () => {
    expect(LONG_QUESTION.length).toBeGreaterThan(2000);
    const { body } = openCard();
    expect(body.scrollHeight).toBeGreaterThan(400);
  });

  it("does not grow the card to fit the question", () => {
    const { card, body } = openCard();
    const max = Number.parseFloat(getComputedStyle(card).maxBlockSize);
    expect(Number.isFinite(max)).toBe(true);
    expect(card.getBoundingClientRect().height).toBeLessThanOrEqual(max + 1);
    // The box is shorter than its prose, so the bar cannot grow past the view.
    expect(card.getBoundingClientRect().height).toBeLessThan(body.scrollHeight);
  });

  it("never pushes the answer row out of the card", () => {
    const { card, actions } = openCard();
    expect(actions.getBoundingClientRect().bottom).toBeLessThanOrEqual(
      card.getBoundingClientRect().bottom + 1,
    );
  });

  // Two capped regions whose caps exceed the card's ceiling, so both must shrink for Skip to stay in the box.
  it("shrinks both regions rather than pushing Skip out", () => {
    const h = boundedHost();
    pushDecision({
      kind: "user_input",
      chatID: "c1",
      requestID: 9,
      payload: {
        request_id: 9,
        question: LONG_QUESTION,
        options: OPTIONS,
      },
      submit: vi.fn(),
    });
    const card = liveCard(h);
    const actions = card?.querySelector<HTMLElement>(".dock-ask-actions");
    const options = card?.querySelector<HTMLElement>(".user-input-options");
    if (
      card === null ||
      card === undefined ||
      actions === null ||
      actions === undefined ||
      options === null ||
      options === undefined
    ) {
      throw new Error("no card");
    }

    expect(options.scrollHeight).toBeGreaterThan(options.clientHeight);
    expect(actions.getBoundingClientRect().bottom).toBeLessThanOrEqual(
      card.getBoundingClientRect().bottom + 1,
    );

    // A textarea is a scroll container with an automatic minimum size of 0, so a hit test reads "can still type". Scrolled
    // into view first: `elementFromPoint` answers null outside the viewport, and two cards stack here.
    const box = card.querySelector<HTMLTextAreaElement>(".dock-ask-text");
    if (box === null) {
      throw new Error("no answer box");
    }
    card.scrollIntoView({ block: "center" });
    const r = box.getBoundingClientRect();
    expect(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)).toBe(box);
  });

  it("gives the question a real scroller rather than clipping it", () => {
    const { body } = openCard();
    expect(getComputedStyle(body).overflowY).toBe("auto");
    expect(body.scrollHeight).toBeGreaterThan(body.clientHeight);
  });
});

// The stylesheet as source (the page links none). See __test-helpers__/css-rules.ts.

describe("26-dock.css carries the phases the module names", () => {
  const dock = loadCSS("26-dock.css");

  it("declares the enter, exit and advance rules", () => {
    expect(
      /height var\(--dur-standard\)/.test(ruleContaining(dock, ".decision-dock", "top").body),
    ).toBe(true);
    expect(
      /animation: vk-slide-up/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="entering"] > .dock-card').body,
      ),
    ).toBe(true);
    expect(
      /height var\(--dock-exit-dur\)/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]').body,
      ),
    ).toBe(true);
    expect(
      /height var\(--dur-exit\)/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"]').body,
      ),
    ).toBe(true);
  });

  it("does not make the exit a display toggle", () => {
    // `.hidden` is an unlayered `display: none !important` that beats any animated rule, so the phase attribute carries
    // the motion and the class lands after.
    const leaving = ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]');
    expect(/display:/.test(leaving.body)).toBe(false);
  });

  it("takes the outgoing card out of flow only for the advance", () => {
    // In flow for the exit, so a shrinking box clips it; out of flow for the advance, so the two cards overlap.
    expect(/position:/.test(ruleContaining(dock, ".dock-outgoing").body)).toBe(false);
    expect(
      /position: absolute/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"] > .dock-outgoing').body,
      ),
    ).toBe(true);
  });

  it("offsets the two advance halves in opposite directions", () => {
    expect(/translate: var\(--dock-shift\) 0/.test(dock)).toBe(true);
    expect(/translate: calc\(-1 \* var\(--dock-shift\)\) 0/.test(dock)).toBe(true);
  });

  it("is disarmed under reduced motion, which the global duration sweep cannot do", () => {
    // A zeroed animation runs to completion, and the sweep does not reach the module's inline pixel height.
    const a11y = loadCSS("40-a11y.css");
    expect(
      /transition: none/.test(
        ruleContaining(a11y, ".decision-dock[data-dock-phase]", "prefers-reduced-motion").body,
      ),
    ).toBe(true);
    expect(
      /display: none/.test(ruleContaining(a11y, ".dock-outgoing", "prefers-reduced-motion").body),
    ).toBe(true);
  });

  // The disarm must beat a live phase: a bare `.decision-dock` (0,1,0) loses to every phase rule (0,2,0), and a body
  // assertion cannot see that, so the cascade facts are pinned.
  it("spells the reduced-motion disarm specifically enough to beat a live phase", () => {
    const a11y = loadCSS("40-a11y.css");
    const disarm = ruleContaining(
      a11y,
      ".decision-dock[data-dock-phase]",
      "prefers-reduced-motion",
    ).selector;
    expect(disarm).toMatch(/\.decision-dock\[data-dock-phase/u);
  });

  it("keeps 40-a11y.css after 26-dock.css, which is what breaks the specificity tie", () => {
    // Both files are unlayered and the disarm ties at (0,2,0), so the MANIFEST order decides; swapping them re-arms motion.
    const order = cssManifest.split("\n").map((l) => l.trim());
    expect(order.indexOf("40-a11y.css")).toBeGreaterThan(order.indexOf("26-dock.css"));
  });
});

describe("the cleanup timer and the stylesheet agree on every duration", () => {
  // One timer per host is the only cleanup (no transitionend listener), so this number is duplicated with the CSS and a
  // one-sided retune must fail here.
  const dock = loadCSS("26-dock.css");

  function tokenMs(name: string): number {
    for (const file of ["26-dock.css", "01-tokens.css"]) {
      const hit = new RegExp(`\\${name}:\\s*([0-9.]+)s`).exec(loadCSS(file));
      if (hit?.[1] !== undefined) {
        return Number(hit[1]) * 1000;
      }
    }
    throw new Error(`no time token ${name}`);
  }

  function heightToken(body: string): string {
    const hit = /height (var\((--[a-z-]+)\))/.exec(body);
    expect(hit?.[2], `no height transition token in ${body}`).toBeDefined();
    return hit?.[2] ?? "";
  }

  it("matches DOCK_PHASE_MS to the transition each phase actually runs", () => {
    const pairs: [keyof typeof DOCK_PHASE_MS, string][] = [
      ["entering", heightToken(ruleContaining(dock, ".decision-dock", "top").body)],
      [
        "leaving",
        heightToken(ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]').body),
      ],
      [
        "advancing",
        heightToken(ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"]').body),
      ],
    ];
    for (const [phase, token] of pairs) {
      expect(DOCK_PHASE_MS[phase], `${phase} vs ${token}`).toBe(tokenMs(token));
    }
  });

  it("keeps the enter and exit inside the bands the requirement names", () => {
    expect(DOCK_PHASE_MS.entering).toBeGreaterThanOrEqual(180);
    expect(DOCK_PHASE_MS.entering).toBeLessThanOrEqual(220);
    expect(DOCK_PHASE_MS.leaving).toBeGreaterThanOrEqual(110);
    expect(DOCK_PHASE_MS.leaving).toBeLessThanOrEqual(140);
    // The exit is faster: the tray must be gone before the reader reaches the box beneath it.
    expect(DOCK_PHASE_MS.leaving).toBeLessThan(DOCK_PHASE_MS.entering);
  });
});

// A permission request is a dock region, not a dialog: the reader must be able to
// leave it for the transcript the decision is about and come back, so nothing around
// the card is modal and Tab off its last control leaves the dock.
describe("the dock is not modal", () => {
  it("puts the permission card inside no dialog and no aria-modal region", () => {
    expect.assertions(3);
    pushPerm("c1", 1);
    const card = liveCard();
    expect(card).not.toBeNull();
    expect(card?.closest("dialog")).toBeNull();
    expect(card?.closest('[aria-modal="true"]')).toBeNull();
  });

  it("lets Tab move focus from the card's last control out of the dock", async () => {
    expect.assertions(2);
    const outside = document.createElement("button");
    outside.textContent = "outside";
    document.body.append(outside);
    pushPerm("c1", 1);
    const controls = [...(liveCard()?.querySelectorAll<HTMLButtonElement>("button") ?? [])];
    const last = controls.at(-1);
    expect(last).toBeDefined();
    last?.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(outside);
  });
});
