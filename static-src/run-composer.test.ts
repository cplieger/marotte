import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { ExecState } from "./exec-view/status.js";
import type * as Runs from "./actions/runs.js";
import type * as Subject from "./actions/subject.js";
import stepVerbsRaw from "../internal/agent/testdata/step_message_verbs.json?raw";
import { classifyRunNodeStatus, classifyRunStatus, runStatusTerminal } from "./run-status.js";
import { workNodes, type ExecNode } from "./exec-view/model.js";
import { runToExec } from "./run-exec-source.js";
import { makeRunState } from "./__test-helpers__/model.js";
import type { RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";
import type { RunStepMessageVerb } from "./wire/types.gen.js";

type Outcome =
  | { status: "success"; value: { verb: string; steer_id?: string } }
  | { status: "error"; error: { message: string; code?: string; status?: number } };

const m = vi.hoisted(() => ({
  outcome: undefined as unknown as Outcome,
  /** The next dispatch's reply when the test releases it, taken in place of `outcome` once. */
  deferred: undefined as Promise<Outcome> | undefined,
  /** Runs inside dispatch, before the reply: a frame the server sent while the POST was out. */
  during: undefined as (() => void) | undefined,
  sent: [] as Record<string, unknown>[],
  notices: [] as [string, string, string][],
}));

vi.mock("./actions/runs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Runs>()),
  messageRunStep: {
    name: "runs.message_step",
    dispatch: (arg: Record<string, unknown>) => {
      m.sent.push(arg);
      m.during?.();
      const outcome = m.deferred ?? Promise.resolve(m.outcome);
      m.deferred = undefined;
      return { outcome };
    },
  },
}));

vi.mock("./actions/subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Subject>()),
  noticeAbout: (subject: string, message: string, level: string) => {
    m.notices.push([subject, message, level]);
  },
}));

const { composerMode, mountRunComposer, setRunComposerTarget, takeIntoRunComposer } =
  await import("./run-composer.js");

function deferNextReply(): (o: Outcome) => void {
  let answer: (o: Outcome) => void = () => undefined;
  m.deferred = new Promise<Outcome>((resolve) => {
    answer = resolve;
  });
  return answer;
}
const { recordStepSteerQueued, stepSteers } = await import("./run-step-steers.js");

function step(state: ExecState, verb?: RunStepMessageVerb, label = "review"): ExecNode {
  const node: ExecNode = { path: "root/review", label, kind: "step", state, children: [] };
  if (verb !== undefined) {
    node.verb = verb;
  }
  return node;
}

function asking(): ExecNode {
  return step("input", "answer");
}

/** The shipped form, sliced out of the page, so the test drives the real markup. */
function mountForm(): {
  form: HTMLFormElement;
  input: HTMLTextAreaElement;
  send: HTMLButtonElement;
} {
  const open = indexHtml.indexOf('<form id="run-composer"');
  const close = indexHtml.indexOf("</form>", open);
  const host = document.createElement("div");
  host.id = "run-composer-host";
  host.innerHTML = indexHtml.slice(open, close + "</form>".length);
  document.body.appendChild(host);
  return {
    form: host.querySelector<HTMLFormElement>("form")!,
    input: host.querySelector<HTMLTextAreaElement>("textarea")!,
    send: host.querySelector<HTMLButtonElement>("button")!,
  };
}

describe("composerMode", () => {
  const live = { workflowID: "wf_1", runLive: true, runPaused: false };

  it("answers an open question, named for the step", () => {
    expect(composerMode({ ...live, step: asking() })).toEqual({
      kind: "answer",
      verb: "answer",
      placeholder: "Answer review\u2026",
    });
  });

  it("steers a running step whose ask the box does not answer, though it shows as input", () => {
    expect(composerMode({ ...live, step: step("input", "steer") })).toEqual({
      kind: "message",
      verb: "steer",
      placeholder: "Message review\u2026",
    });
  });

  it("steers a running step", () => {
    expect(composerMode({ ...live, step: step("running", "steer") })).toEqual({
      kind: "message",
      verb: "steer",
      placeholder: "Message review\u2026",
    });
  });

  it("says a message resumes a step parked on a paused run", () => {
    expect(composerMode({ ...live, runPaused: true, step: step("waiting", "prompt") })).toEqual({
      kind: "message",
      verb: "prompt",
      placeholder: "Message review to resume it\u2026",
    });
  });

  it("refuses a step that has not started, with the reason", () => {
    expect(composerMode({ ...live, step: step("pending") })).toEqual({
      kind: "disabled",
      reason: "review has not started yet",
    });
  });

  it("refuses a finished step: a finished step stays finished", () => {
    expect(composerMode({ ...live, step: step("ok") })).toEqual({
      kind: "disabled",
      reason: "review has finished",
    });
  });

  it("refuses everything on a finished run", () => {
    expect(composerMode({ ...live, runLive: false, step: step("running", "steer") })).toEqual({
      kind: "disabled",
      reason: "This run has finished",
    });
  });

  it("refuses a paused step while a sibling branch keeps the run going", () => {
    expect(composerMode({ ...live, step: step("waiting") })).toEqual({
      kind: "disabled",
      reason: "review can take a message once the run pauses",
    });
  });

  it("refuses an asking step no verb reaches", () => {
    expect(composerMode({ ...live, step: step("input") })).toEqual({
      kind: "disabled",
      reason: "review cannot take a message right now",
    });
  });

  it("refuses before the run has a step", () => {
    expect(composerMode({ ...live, step: undefined })).toEqual({
      kind: "disabled",
      reason: "This run has no step to message yet",
    });
  });
});

/** One row of `internal/agent/testdata/step_message_verbs.json`, the table the server's
 *  stepMessageVerb is held to. */
interface StepVerbCase {
  id: string;
  run: string;
  node: string;
  session: boolean;
  asked: boolean;
  verb?: string;
  refusal?: string;
}

describe("composerMode against the server's availability table", () => {
  const cases = JSON.parse(stepVerbsRaw) as StepVerbCase[];

  // Through the run tab's own fold, so the verb predicted is the one the projected tree carries.
  it.each(cases)("$id", (c) => {
    const run = classifyRunStatus(c.run) ?? "unknown";
    const session = c.session ? "sess_step" : "";
    const state: RunState = {
      ...makeRunState({ status: run }),
      root: {
        nodeId: "root",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "review",
            type: "step",
            status: classifyRunNodeStatus(c.node),
            ...(c.session ? { sessionId: session } : {}),
            children: [],
          },
        ],
      },
    };
    const asks: RunAsks = c.asked
      ? { count: 1, asked: [{ nodeID: "review", sessionID: session, answer: true }], label: "" }
      : { count: 0, asked: [], label: "" };
    const mode = composerMode({
      workflowID: "wf_1",
      runLive: !runStatusTerminal(run),
      runPaused: run === "paused",
      step: workNodes(runToExec("wf_1", state, undefined, asks).nodes)[0],
    });
    expect(mode.kind === "disabled" ? undefined : mode.verb).toBe(c.verb);
  });
});

// One module-level box, mounted once, as in production.
const ui = mountForm();
mountRunComposer(ui.form, ui.input, ui.send);

describe("the box", () => {
  beforeEach(() => {
    m.sent = [];
    m.notices = [];
    m.during = undefined;
    m.deferred = undefined;
    ui.input.value = "";
  });

  afterEach(() => {
    // Off the step, so the next case's target starts a fresh draft.
    setRunComposerTarget({ workflowID: "", runLive: false, runPaused: false, step: undefined });
  });

  it("takes the attention mode while the step has an open question", () => {
    setRunComposerTarget({
      workflowID: "wf_1",
      runLive: true,
      runPaused: false,
      step: asking(),
    });
    expect(ui.form.dataset["mode"]).toBe("answer");
    expect(ui.input.placeholder).toBe("Answer review\u2026");
    expect(ui.input.disabled).toBe(false);
  });

  it("is disabled with its reason where no message can land", () => {
    setRunComposerTarget({
      workflowID: "wf_1",
      runLive: true,
      runPaused: false,
      step: step("pending"),
    });
    expect(ui.form.dataset["mode"]).toBe("disabled");
    expect(ui.input.disabled).toBe(true);
    expect(ui.send.disabled).toBe(true);
    expect(ui.send.dataset["tooltip"]).toBe("review has not started yet");
    // The disabled state already says it is unavailable; the name stays the control's own.
    expect(ui.send.getAttribute("aria-label")).toBe("Send");
  });

  it("sends a retry of a send whose reply was lost under the same message id", async () => {
    setRunComposerTarget({
      workflowID: "wf_5",
      runLive: true,
      runPaused: true,
      step: step("waiting", "prompt"),
    });
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    m.outcome = { status: "success", value: { verb: "prompt" } };
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(2);
    });
    expect(m.sent[1]?.["message_id"]).toBe(m.sent[0]?.["message_id"]);
  });

  it("keeps a lost send's id for its step's draft while another step is messaged", async () => {
    const review = {
      workflowID: "wf_10",
      runLive: true,
      runPaused: true,
      step: step("waiting", "prompt"),
    };
    const build = {
      ...review,
      step: { ...step("waiting", "prompt", "build"), path: "root/build" },
    };
    setRunComposerTarget(review);
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    setRunComposerTarget(build);
    m.outcome = { status: "success", value: { verb: "prompt" } };
    ui.input.value = "rebuild";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(2);
    });
    setRunComposerTarget(review);
    expect(ui.input.value).toBe("carry on");
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(3);
    });
    expect(m.sent[2]?.["message_id"]).toBe(m.sent[0]?.["message_id"]);
  });

  it("keeps a lost send's id for its step's draft while another step refuses a message", async () => {
    const review = {
      workflowID: "wf_12",
      runLive: true,
      runPaused: true,
      step: step("waiting", "prompt"),
    };
    const build = {
      ...review,
      step: { ...step("waiting", "prompt", "build"), path: "root/build" },
    };
    setRunComposerTarget(review);
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    setRunComposerTarget(build);
    m.outcome = { status: "error", error: { message: "step busy", code: "busy", status: 409 } };
    ui.input.value = "rebuild";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.notices).toHaveLength(2);
    });
    setRunComposerTarget(review);
    m.outcome = { status: "success", value: { verb: "prompt" } };
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(3);
    });
    expect(m.sent[2]?.["message_id"]).toBe(m.sent[0]?.["message_id"]);
  });

  it("keeps a lost send's id for its step's draft while a frame confirms another step's message", async () => {
    const review = {
      workflowID: "wf_13",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    };
    const build = { ...review, step: { ...step("running", "steer", "build"), path: "root/build" } };
    setRunComposerTarget(review);
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    setRunComposerTarget(build);
    m.during = () => {
      const id = `steer-${String(m.sent[1]?.["message_id"])}`;
      recordStepSteerQueued("wf_13", "root/build", { id, text: "rebuild", origin: "user" });
    };
    ui.input.value = "rebuild";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(stepSteers("wf_13", "root/build").map((r) => [r.text, r.pending])).toEqual([
        ["rebuild", undefined],
      ]);
    });
    // A task boundary, past every microtask the settled reply runs.
    await new Promise((resolve) => setTimeout(resolve, 0));
    m.during = undefined;
    setRunComposerTarget(review);
    m.outcome = { status: "success", value: { verb: "steer" } };
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(3);
    });
    expect(m.sent[2]?.["message_id"]).toBe(m.sent[0]?.["message_id"]);
  });

  it("keeps a lost send's id while other words to the same step go through", async () => {
    setRunComposerTarget({
      workflowID: "wf_11",
      runLive: true,
      runPaused: true,
      step: step("waiting", "prompt"),
    });
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    m.outcome = { status: "success", value: { verb: "prompt" } };
    ui.input.value = "and add tests";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(2);
    });
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(3);
    });
    expect(m.sent[2]?.["message_id"]).toBe(m.sent[0]?.["message_id"]);
  });

  it("sends a refused message again under a new id, since the refusal is what a reused id replays", async () => {
    setRunComposerTarget({
      workflowID: "wf_6",
      runLive: true,
      runPaused: true,
      step: step("waiting", "prompt"),
    });
    m.outcome = { status: "error", error: { message: "step busy", code: "busy", status: 409 } };
    ui.input.value = "carry on";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("carry on");
    });
    m.outcome = { status: "success", value: { verb: "prompt" } };
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(2);
    });
    expect(m.sent[1]?.["message_id"]).not.toBe(m.sent[0]?.["message_id"]);
  });

  it("sends to the selected step and keeps its row while the reply says steer", async () => {
    m.outcome = { status: "success", value: { verb: "steer" } };
    setRunComposerTarget({
      workflowID: "wf_1",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    ui.input.value = "check the tests";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });
    expect(m.sent[0]).toMatchObject({
      workflowID: "wf_1",
      nodePath: "root/review",
      text: "check the tests",
    });
    expect(ui.input.value).toBe("");
    await vi.waitFor(() => {
      expect(stepSteers("wf_1", "root/review").map((r) => r.text)).toEqual(["check the tests"]);
    });
  });

  it("keeps a row a frame confirmed before the reply was lost, and gives nothing back", async () => {
    m.during = () => {
      const id = `steer-${String(m.sent[0]?.["message_id"])}`;
      recordStepSteerQueued("wf_7", "root/review", { id, text: "use main", origin: "user" });
    };
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    setRunComposerTarget({
      workflowID: "wf_7",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    ui.input.value = "use main";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });
    // A task boundary, past every microtask the settled reply runs.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(stepSteers("wf_7", "root/review").map((r) => [r.text, r.pending])).toEqual([
      ["use main", undefined],
    ]);
    expect(ui.input.value).toBe("");
    expect(m.notices).toEqual([]);
  });

  // running_with_an_ask_steers: shown as `input`, sent as a steer, and owned like one.
  it("keeps the confirmed row of an asking running step whose reply was lost", async () => {
    m.during = () => {
      const id = `steer-${String(m.sent[0]?.["message_id"])}`;
      recordStepSteerQueued("wf_9", "root/review", { id, text: "skip it", origin: "user" });
    };
    m.outcome = { status: "error", error: { message: "Failed to fetch", status: 0 } };
    setRunComposerTarget({
      workflowID: "wf_9",
      runLive: true,
      runPaused: false,
      step: step("input", "steer"),
    });
    ui.input.value = "skip it";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });
    // A task boundary, past every microtask the settled reply runs.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(stepSteers("wf_9", "root/review").map((r) => [r.text, r.pending])).toEqual([
      ["skip it", undefined],
    ]);
    expect(ui.input.value).toBe("");
    expect(m.notices).toEqual([]);
  });

  it("confirms the row from the steer reply alone, as a replayed reply sends no frame", async () => {
    m.outcome = { status: "success", value: { verb: "steer", steer_id: "steer-srv" } };
    setRunComposerTarget({
      workflowID: "wf_8",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    ui.input.value = "rerun it";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(stepSteers("wf_8", "root/review").map((r) => [r.id, r.pending])).toEqual([
        ["steer-srv", undefined],
      ]);
    });
  });

  it("puts a refused message back in the box and says why", async () => {
    m.outcome = { status: "error", error: { message: "step busy", code: "busy", status: 409 } };
    setRunComposerTarget({
      workflowID: "wf_2",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    ui.input.value = "rerun the build";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("rerun the build");
    });
    expect(stepSteers("wf_2", "root/review")).toEqual([]);
    expect(m.notices).toEqual([
      ["run:wf_2", "That step cannot take a message right now. Try again in a moment.", "error"],
    ]);
  });

  it("holds the step's box until its send settles, so a late refusal gives every word back", async () => {
    setRunComposerTarget({
      workflowID: "wf_13",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    const refuse = deferNextReply();
    ui.input.value = "use main";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });
    expect(ui.input.placeholder).toBe("Sending to review\u2026");
    expect(ui.send.disabled).toBe(true);
    expect(ui.form.getAttribute("aria-busy")).toBe("true");
    ui.input.focus();
    await userEvent.type(ui.input, "also inspect the logs");
    expect(ui.input.value).toBe("");
    ui.form.requestSubmit();
    await Promise.resolve();
    expect(m.sent).toHaveLength(1);
    // Held, not disabled: the reader keeps focus and a phone keeps its keyboard.
    expect(document.activeElement).toBe(ui.input);

    refuse({ status: "error", error: { message: "step busy", code: "busy", status: 409 } });

    await vi.waitFor(() => {
      expect(ui.input.value).toBe("use main");
    });
    expect(ui.input.readOnly).toBe(false);
    expect(ui.send.disabled).toBe(false);
    expect(ui.form.hasAttribute("aria-busy")).toBe(false);
  });

  it("gives a late refusal back to its own step after the reader moved on", async () => {
    const review = {
      workflowID: "wf_14",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    };
    const build = {
      ...review,
      step: { ...step("running", "steer", "build"), path: "root/build" },
    };
    setRunComposerTarget(review);
    const refuse = deferNextReply();
    ui.input.value = "use main";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });
    setRunComposerTarget(build);
    expect(ui.input.readOnly).toBe(false);
    m.outcome = { status: "success", value: { verb: "steer" } };
    ui.input.value = "rebuild";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(2);
    });

    refuse({ status: "error", error: { message: "step busy", code: "busy", status: 409 } });
    await vi.waitFor(() => {
      expect(m.notices).toHaveLength(1);
    });

    expect(ui.input.value).toBe("");
    setRunComposerTarget(review);
    expect(ui.input.value).toBe("use main");
    expect(ui.input.readOnly).toBe(false);
  });

  it("takes no Edit into a step's box while its send is out", async () => {
    setRunComposerTarget({
      workflowID: "wf_15",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    const lose = deferNextReply();
    ui.input.value = "use main";
    ui.form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.sent).toHaveLength(1);
    });

    expect(takeIntoRunComposer("an older message")).toBeUndefined();

    lose({ status: "error", error: { message: "Failed to fetch", status: 0 } });
    await vi.waitFor(() => {
      expect(ui.input.value).toBe("use main");
    });
  });

  it("keeps a draft per step", () => {
    setRunComposerTarget({
      workflowID: "wf_3",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    ui.input.value = "for review";
    ui.input.dispatchEvent(new Event("input"));
    setRunComposerTarget({
      workflowID: "wf_3",
      runLive: true,
      runPaused: false,
      step: { ...step("running", "steer", "build"), path: "root/build" },
    });
    expect(ui.input.value).toBe("");
    setRunComposerTarget({
      workflowID: "wf_3",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    expect(ui.input.value).toBe("for review");
  });
});

describe("the attention border", () => {
  it("is the yellow border on an open question, and typing the answer keeps it", () => {
    const style = mountAppCSS();
    const box = ui.form.querySelector<HTMLElement>(".prompt-box")!;
    // The rendered colour, not mid-transition.
    box.style.transition = "none";
    const probe = document.createElement("span");
    probe.style.color = "var(--c-yellow)";
    ui.form.appendChild(probe);
    const yellow = getComputedStyle(probe).color;
    setRunComposerTarget({
      workflowID: "wf_4",
      runLive: true,
      runPaused: false,
      step: step("running", "steer"),
    });
    expect(getComputedStyle(box).borderTopColor).not.toBe(yellow);
    setRunComposerTarget({
      workflowID: "wf_4",
      runLive: true,
      runPaused: false,
      step: asking(),
    });
    expect(getComputedStyle(box).borderTopColor).toBe(yellow);
    ui.input.focus();
    expect(getComputedStyle(box).borderTopColor).toBe(yellow);
    ui.input.blur();
    probe.remove();
    box.style.removeProperty("transition");
    style.remove();
  });
});
