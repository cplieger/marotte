import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as Chat from "./chat.js";
import type * as Tabs from "./tabs.js";
import type * as DecisionDock from "./decision-dock.js";
import type * as Runs from "./actions/runs.js";
import type * as RunChatSteps from "./run-chat-steps.js";
import type * as RunStepTranscript from "./run-step-transcript.js";
import { nodePathKey } from "./run-node-key.js";

interface RunInspectReply {
  workflowId: string;
  state?: { workflowId: string; status?: string; root?: unknown };
}

interface Outcome {
  status: "success" | "error";
  error?: { status?: number; message: string };
}

const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  opened: [] as string[],
  dispatched: [] as string[],
  /** The answer each step-message action gives next; unset answers success at once. */
  outcome: new Map<string, Promise<Outcome>>(),
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiGetTypedOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  apiGetOrError: vi.fn(),
}));

vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  refreshChatView: vi.fn(),
}));

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  openRunTab: vi.fn((id: string) => {
    m.opened.push(id);
    return Promise.resolve();
  }),
  tabIdFor: vi.fn(() => ""),
  tabSetVersion: vi.fn(() => 0),
  openRunRefs: vi.fn(() => []),
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  closeTab: vi.fn(),
  getActiveTabId: vi.fn(() => ""),
  openEditorView: vi.fn(),
  setTabDirty: vi.fn(),
  openGitView: vi.fn(),
  hasTab: vi.fn(() => false),
  parentChatRef: vi.fn(() => ""),
  openTab: vi.fn(() => Promise.resolve("failed")),
}));

vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDock>()),
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
  runPendingAsks: vi.fn(() => ({ count: 0, asked: [], label: "" })),
}));

vi.mock("./actions/runs.js", async (importOriginal) => {
  const original = await importOriginal<typeof Runs>();
  const stub = (verb: string) => ({
    name: `runs.${verb}`,
    dispatch: vi.fn((arg: unknown) => {
      m.dispatched.push(`${verb}:${typeof arg === "string" ? arg : JSON.stringify(arg)}`);
      const outcome = m.outcome.get(verb) ?? Promise.resolve<Outcome>({ status: "success" });
      m.outcome.delete(verb);
      return { outcome };
    }),
  });
  return {
    ...original,
    cancelRun: stub("cancel"),
    pauseRun: stub("pause"),
    resumeRun: stub("resume"),
    retryRun: stub("retry"),
    extendRunRepeat: stub("extend"),
    finishRunRepeat: stub("finish_loop"),
    messageRunStep: stub("message"),
    removeRunStepSteer: stub("steer_remove"),
    clearRunStepSteers: stub("steer_clear"),
  };
});

// The stream is stubbed: this suite paints no transcript.
vi.mock("./run-chat-steps.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunChatSteps>()),
  createRunStepStream: vi.fn(() => ({ apply: vi.fn(), dispose: vi.fn() })),
}));

vi.mock("./run-step-transcript.js", async (importOriginal) => {
  const { signal } = await import("@cplieger/reactive");
  return {
    ...(await importOriginal<typeof RunStepTranscript>()),
    stepTranscriptVersion: signal(0),
    stepRead: vi.fn(() => undefined),
    requestStepTranscript: vi.fn(),
    rereadStepTranscript: vi.fn(),
    clearStepTranscripts: vi.fn(),
  };
});

import { openRunView, refreshRun, showRun } from "./run-view.js";
import { runPendingAsks } from "./decision-dock.js";
import { apiGetOrError } from "./api-client.js";
import { stepTranscriptVersion } from "./run-step-transcript.js";
import { beginStepMessage, recordStepSteerQueued } from "./run-step-steers.js";
import { invalidateRun } from "./run-store.js";

// The bar is mounted once per module, as in the app, so every case re-attaches the same elements.
const form = document.createElement("form");
form.id = "run-composer";
const input = document.createElement("textarea");
input.id = "run-composer-input";
const send = document.createElement("button");
send.id = "run-send-btn";
form.append(input, send);
const stack = document.createElement("ul");
stack.id = "run-steer-stack";

beforeEach(() => {
  m.opened.length = 0;
  m.dispatched.length = 0;
  m.outcome.clear();
  vi.mocked(apiGetOrError).mockImplementation(() =>
    Promise.resolve(
      m.reply.current === undefined
        ? { ok: false, status: 0, data: null, error: "" }
        : { ok: true, status: 200, data: m.reply.current, error: "" },
    ),
  );
  document.body.replaceChildren();
  const body = document.createElement("div");
  body.id = "run-body";
  const dock = document.createElement("div");
  dock.id = "run-dock";
  input.value = "";
  document.body.append(body, dock, stack, form);
});

afterEach(() => {
  document.body.replaceChildren();
});

function composerInput(): HTMLTextAreaElement {
  const el = document.getElementById("run-composer-input");
  if (!(el instanceof HTMLTextAreaElement)) {
    throw new Error("Setup: the run composer's input is missing");
  }
  return el;
}

function pick(workflowID: string, nodeID: string): void {
  const row = document.querySelector<HTMLElement>(
    `.ev-row[data-path="${nodePathKey([workflowID, nodeID])}"] > :first-child`,
  );
  if (row === null) {
    throw new Error(`Setup: step ${nodeID} is not on the page`);
  }
  row.click();
}

function type(text: string): void {
  composerInput().value = text;
  composerInput().dispatchEvent(new Event("input"));
}

describe("run view composer target", () => {
  const ID = "wf_answer";
  const reply = (status: string): RunInspectReply => ({
    workflowId: ID,
    state: {
      workflowId: ID,
      status: "running",
      root: {
        nodeId: ID,
        type: "sequence",
        status: "running",
        children: [{ nodeId: "review", type: "step", status, sessionId: "sess_review" }],
      },
    },
  });

  beforeEach(() => {
    vi.mocked(runPendingAsks).mockReturnValue({
      count: 1,
      asked: [{ nodeID: "review", sessionID: "sess_review", answer: true }],
      label: "Which colour?",
    });
  });

  // An ask can mark a running step `input` before the step parks; once the next inspect shows it
  // paused the server answers it, while the step's path and its `input` state stay the same.
  it("answers a step whose ask became answerable with its path and state unchanged", async () => {
    m.reply.current = reply("running");
    openRunView(ID, "nightly");
    const tab = m.opened.at(-1);
    if (tab === undefined) {
      throw new Error("the opener did not open a tab");
    }
    showRun(tab);
    // Stands in for `rerenderDocks`, which re-runs the view effect for the newly shown run.
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
    refreshRun(tab);
    await vi.waitFor(() => {
      expect(document.querySelector(".ev-d-title")?.textContent).toBe("review");
    });
    const detailState = (): string | undefined =>
      document.querySelector<HTMLElement>(".ev-detail")?.dataset["state"];
    expect(detailState()).toBe("input");
    expect(composerInput().placeholder).toBe("Message review\u2026");

    m.reply.current = reply("paused");
    invalidateRun(ID);
    await vi.waitFor(() => {
      expect(composerInput().placeholder).toBe("Answer review\u2026");
    });
    expect(document.getElementById("run-composer")?.dataset["mode"]).toBe("answer");
    // The premise: the shown step's path and state, all the page's change-of-attention guard reads.
    expect(document.querySelector(".ev-d-title")?.textContent).toBe("review");
    expect(detailState()).toBe("input");
  });

  // A run whose first read has not resolved: nothing the previous run's tab drew may address it.
  describe("switching to a run that is still loading", () => {
    const LOADING = "wf_loading";

    async function showLoaded(): Promise<void> {
      // Two steps, so the page draws its step tree.
      m.reply.current = {
        workflowId: ID,
        state: {
          workflowId: ID,
          status: "running",
          root: {
            nodeId: ID,
            type: "sequence",
            status: "running",
            children: [
              { nodeId: "review", type: "step", status: "running", sessionId: "sess_review" },
              { nodeId: "ship", type: "step", status: "pending" },
            ],
          },
        },
      } satisfies RunInspectReply;
      vi.mocked(apiGetOrError).mockImplementation((path: string) =>
        path === `/api/runs/${LOADING}`
          ? new Promise(() => undefined)
          : Promise.resolve({ ok: true, status: 200, data: m.reply.current, error: "" }),
      );
      openRunView(ID, "nightly");
      showRun(m.opened.at(-1) ?? "");
      stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
      refreshRun(ID);
      await vi.waitFor(() => {
        expect(composerInput().placeholder).toBe("Message review\u2026");
      });
    }

    function stackRows(): number {
      return document.getElementById("run-steer-stack")?.querySelectorAll("li").length ?? 0;
    }

    it("sends nothing and offers no step row until the new run publishes a step", async () => {
      await showLoaded();
      beginStepMessage(ID, nodePathKey([ID, "review"]), "m-prev", "use the fixture", true);
      expect(stackRows()).toBe(1);

      openRunView(LOADING, "other");
      showRun(LOADING);
      refreshRun(LOADING);

      expect(composerInput().disabled).toBe(true);
      expect(document.getElementById("run-composer")?.dataset["mode"]).toBe("disabled");
      expect(stackRows()).toBe(0);
      composerInput().value = "for the old run";
      document
        .getElementById("run-composer")
        ?.dispatchEvent(new Event("submit", { cancelable: true }));
      await Promise.resolve();
      expect(m.dispatched.filter((d) => d.startsWith("message:"))).toEqual([]);
    });

    it("ignores a pick on the previous run's page while the new run loads", async () => {
      await showLoaded();
      openRunView(LOADING, "other");
      showRun(LOADING);
      pick(ID, "review");
      // Still the loading run's own answer, not one built from the previous run's step.
      expect(composerInput().placeholder).toBe("This run has no step to message yet");
    });

    it("takes the previous run's page down once the new run paints", async () => {
      await showLoaded();
      openRunView(LOADING, "other");
      showRun(LOADING);
      stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
      await vi.waitFor(() => {
        expect(document.querySelector("[id='run-body'] .list-empty")?.textContent).toBe(
          "Loading run\u2026",
        );
      });
      expect(document.querySelector(".ev-page")).toBeNull();
    });
  });
});

describe("run view refused Edit", () => {
  const ID = "wf_edit";

  async function showEditable(): Promise<void> {
    m.reply.current = {
      workflowId: ID,
      state: {
        workflowId: ID,
        status: "running",
        root: {
          nodeId: ID,
          type: "sequence",
          status: "running",
          children: [
            { nodeId: "review", type: "step", status: "running", sessionId: "sess_review" },
            { nodeId: "ship", type: "step", status: "running", sessionId: "sess_ship" },
          ],
        },
      },
    } satisfies RunInspectReply;
    openRunView(ID, "nightly");
    showRun(m.opened.at(-1) ?? "");
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;
    refreshRun(ID);
    await vi.waitFor(() => {
      pick(ID, "review");
      expect(composerInput().placeholder).toBe("Message review\u2026");
    });
    recordStepSteerQueued(ID, nodePathKey([ID, "review"]), {
      id: "steer-a",
      text: "use the fixture",
      origin: "user",
    });
  }

  async function editRefusedLater(): Promise<() => Promise<void>> {
    let refuse: (o: Outcome) => void = () => undefined;
    m.outcome.set(
      "steer_remove",
      new Promise<Outcome>((resolve) => {
        refuse = resolve;
      }),
    );
    const edit = await vi.waitFor(() => {
      const btn = stack.querySelector<HTMLButtonElement>('button[aria-label="Edit this message"]');
      if (btn === null) {
        throw new Error("Setup: the queued row offers no Edit");
      }
      return btn;
    });
    edit.click();
    expect(composerInput().value).toBe("use the fixture");
    return async () => {
      refuse({
        status: "error",
        error: { status: 409, message: "That message was already read." },
      });
      await vi.waitFor(() => {
        expect(m.dispatched.some((d) => d.startsWith("steer_remove:"))).toBe(true);
      });
      for (let i = 0; i < 4; i++) {
        await Promise.resolve();
      }
    };
  }

  it("puts back the draft it replaced in the box it took", async () => {
    await showEditable();
    type("draft for review");
    const refused = await editRefusedLater();

    await refused();

    expect(composerInput().value).toBe("draft for review");
  });

  it("keeps what the reader typed over the taken words", async () => {
    await showEditable();
    type("draft for review");
    const refused = await editRefusedLater();
    type("use the fixture and the logs");

    await refused();

    expect(composerInput().value).toBe("use the fixture and the logs");
  });

  it("takes nothing back while the step's own send is out", async () => {
    await showEditable();
    let answer: (o: Outcome) => void = () => undefined;
    m.outcome.set(
      "message",
      new Promise<Outcome>((resolve) => {
        answer = resolve;
      }),
    );
    type("check the logs");
    form.requestSubmit();
    await vi.waitFor(() => {
      expect(m.dispatched.some((d) => d.startsWith("message:"))).toBe(true);
    });
    const edit = stack.querySelector<HTMLButtonElement>('button[aria-label="Edit this message"]');
    if (edit === null) {
      throw new Error("Setup: the queued row offers no Edit");
    }

    edit.click();
    await Promise.resolve();
    const removals = m.dispatched.filter((d) => d.startsWith("steer_remove:"));
    const boxDuringSend = composerInput().value;
    // Answered before asserting, so a failure here leaves no send out for the next case's step.
    answer({ status: "error", error: { status: 409, message: "That step is busy." } });
    await vi.waitFor(() => {
      expect(composerInput().value).toBe("check the logs");
    });

    expect(removals).toEqual([]);
    expect(boxDuringSend).toBe("");
  });

  it("leaves another step's draft alone when the reader moved there with the same words", async () => {
    await showEditable();
    type("draft for review");
    const refused = await editRefusedLater();
    pick(ID, "ship");
    type("use the fixture");

    await refused();

    expect(composerInput().value).toBe("use the fixture");
    pick(ID, "review");
    expect(composerInput().value).toBe("draft for review");
  });
});
