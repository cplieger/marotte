// Every surface that draws a workflow update names its run, and the name opens the run's tab.

import { describe, it, expect, vi, beforeEach, onTestFinished } from "vitest";
import type { Entry, Session, TurnState } from "./types.js";
import type { Turn } from "./turns.js";
import type * as ApiClientModule from "./api-client.js";
import type * as RunLabelModule from "./workflow-run-label.js";
import type * as RunViewModule from "./run-view.js";
import { makeSession } from "./__test-helpers__/model.js";
import { useSteppableClock } from "./__test-helpers__/fake-clock.js";
import { toolResultID } from "./entry-ids.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

// Replaced so a click records the tab it asks for instead of opening the real run page.
const openRunView = vi.hoisted(() => vi.fn());
vi.mock("./run-view.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunViewModule>()),
  openRunView,
}));

/** The run names `GET /api/runs/{id}` answers; a run absent here answers a failed read. */
const names = vi.hoisted(() => new Map<string, string>());
/** Every run id a read was issued for, in order. */
const reads = vi.hoisted(() => [] as string[]);
/** While `holding`, a run read stays out until its release is called, as a slow server leaves it. */
const gate = vi.hoisted(() => ({ holding: false, releases: [] as (() => void)[] }));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetOrError: vi.fn((path: string) => {
    const hit = /^\/api\/runs\/([^/?]+)$/.exec(path);
    const id = decodeURIComponent(hit?.[1] ?? "");
    const name = names.get(id);
    reads.push(id);
    const answer =
      name === undefined
        ? { ok: false, status: 0, data: null, error: "" }
        : {
            ok: true,
            status: 200,
            data: { workflowId: id, state: { workflowId: id, status: "running", runLabel: name } },
            error: "",
          };
    if (!gate.holding) {
      return Promise.resolve(answer);
    }
    return new Promise((resolve) => {
      gate.releases.push(() => {
        resolve(answer);
      });
    });
  }),
}));

// Wrapped rather than replaced: the real watcher runs, and a test reads which notes were released.
const released = vi.hoisted(() => [] as HTMLElement[]);
vi.mock("./workflow-run-label.js", async (importOriginal) => {
  const actual = await importOriginal<typeof RunLabelModule>();
  return {
    ...actual,
    unwatchStepRunLabel: (note: HTMLElement) => {
      released.push(note);
      actual.unwatchStepRunLabel(note);
    },
  };
});

import { loadCSS } from "./__test-helpers__/css-rules.js";

const style = document.createElement("style");
style.textContent = loadCSS("13-messages.css") + "\n#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const runStore = await import("./run-store.js");
const { unwatchStepRunLabel } = await import("./workflow-run-label.js");
const messages = await import("./messages.js");
const blocks = await import("./messages-blocks.js");

messages.mountChatView();

const SENT = Date.UTC(2026, 9, 9, 12, 0, 5);

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown, id: string) {
  return { id, turn: turnID, kind, seq: at, ts: at + 1, payload } as Entry;
}

function open(): Entry {
  return sealed(
    "t1",
    0,
    "turn_open",
    { prompt: { id: "t1-p", text: "go" }, source: "prompt", n: 1 },
    "t1-open",
  );
}

function close(at: number): Entry {
  return sealed("t1", at, "turn_close", { outcome: "completed" }, `t1-close-${String(at)}`);
}

/** A step's message to the launching chat, from `run`. */
function stepUpdate(at: number, run: string, step: string): Entry {
  return sealed(
    "t1",
    at,
    "steer",
    { text: `${step} done`, origin: "step", step, origin_run: run, produced_ts: SENT },
    `wfmsg-${run}-${step}`,
  );
}

/** A workflow result: the origin the transcript labels "Workflow result". */
function result(at: number, run: string): Entry {
  return sealed(
    "t1",
    at,
    "steer",
    { text: "run finished", origin: "agent", origin_run: run, produced_ts: SENT },
    `result-${run}`,
  );
}

/** `entries` close turn t1; `later` adds the turns after it. */
async function paint(
  chatID: string,
  entries: Entry[],
  later: [string, Entry[]][] = [],
): Promise<HTMLElement> {
  const turn: TurnState = { entries: [open(), close(1), ...entries], openEntries: new Map() };
  const turns = new Map<string, TurnState>([["t1", turn]]);
  for (const [id, body] of later) {
    turns.set(id, { entries: body, openEntries: new Map() });
  }
  store.setSessions([
    session(chatID, {
      turns,
      turn_order: ["t1", ...later.map(([id]) => id)],
      turn_count: turns.size,
    }),
  ]);
  store.setActive(chatID);
  store.bumpMessages(chatID, "load");
  await Promise.resolve();
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

function runButtons(view: HTMLElement): HTMLButtonElement[] {
  return [...view.querySelectorAll<HTMLButtonElement>("button.steer-note-run")];
}

let seq = 0;
/** A fresh run id per case: `run-store.ts` keeps one cell per id for the page's life. */
function runID(tag: string): string {
  seq++;
  return `wf_${tag}${String(seq)}`;
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  openRunView.mockReset();
  names.clear();
  reads.length = 0;
  gate.holding = false;
  gate.releases.length = 0;
  released.length = 0;
});

describe("a step's message card", () => {
  it("names its run beside the step, and the name opens the run's tab", async () => {
    const run = runID("card");
    names.set(run, "forge-f2-build");
    const c = "c-label-card";
    const view = await paint(c, [stepUpdate(2, run, "P4-REVIEW-GPT")]);

    await vi.waitFor(() => {
      expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    });
    expect(view.querySelector(".steer-note-label")?.textContent).toBe("P4-REVIEW-GPT");

    runButtons(view)[0]?.click();

    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(run, "forge-f2-build", c);
    });
  });

  it("reads the run's short id while its name is unknown", async () => {
    const run = runID("anon");
    const view = await paint("c-label-anon", [stepUpdate(2, run, "review")]);

    expect(runButtons(view).map((b) => b.textContent)).toEqual([run.slice(0, 11)]);
  });

  it("opens an unnamed run's tab under the short id the button shows", async () => {
    const run = runID("anonopen");
    const c = "c-label-anon-open";
    const view = await paint(c, [stepUpdate(2, run, "review")]);

    runButtons(view)[0]?.click();

    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(run, run.slice(0, 11), c);
    });
  });
});

describe("a step's message card, to a screen reader", () => {
  it("names the run in the card's accessible name once the run's name arrives", async () => {
    const run = runID("aria");
    names.set(run, "forge-f2-build");
    const view = await paint("c-label-aria", [stepUpdate(2, run, "P4-REVIEW-GPT")]);

    await vi.waitFor(() => {
      expect(view.querySelector(".steer-note")?.getAttribute("aria-label")).toBe(
        "forge-f2-build, P4-REVIEW-GPT: P4-REVIEW-GPT done",
      );
    });
  });
});

describe("the grouped step updates box", () => {
  it("names the one run in its header and each row's run in the row", async () => {
    const run = runID("box");
    names.set(run, "forge-f2-build");
    const c = "c-label-box";
    const view = await paint(c, [
      stepUpdate(2, run, "P4-REVIEW-GPT"),
      stepUpdate(3, run, "P4-AGGREGATE"),
    ]);

    await vi.waitFor(() => {
      expect(view.querySelector(".steer-group-header")?.textContent).toBe(
        "2 updates from forge-f2-build",
      );
    });
    const buttons = runButtons(view);
    expect(buttons.map((b) => b.textContent)).toEqual(["forge-f2-build", "forge-f2-build"]);
    expect(buttons.every((b) => b.closest(".steer-group-body") !== null)).toBe(true);

    buttons[1]?.click();

    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(run, "forge-f2-build", c);
    });
  });

  it("tells two runs' rows apart by their own labels", async () => {
    const a = runID("a");
    const b = runID("b");
    names.set(a, "forge-f2-build");
    names.set(b, "ui-fix-pill-commit-on-close");
    const view = await paint("c-label-two", [
      stepUpdate(2, a, "P4-AGGREGATE"),
      stepUpdate(3, b, "P1-IMPL"),
    ]);

    await vi.waitFor(() => {
      expect(runButtons(view).map((x) => x.textContent)).toEqual([
        "forge-f2-build",
        "ui-fix-pill-commit-on-close",
      ]);
    });
    expect(view.querySelector(".steer-group-header")?.textContent).toBe(
      "2 updates from 2 workflows",
    );
  });
});

describe("a workflow result card", () => {
  it("names its run, and the name opens the run's tab", async () => {
    const run = runID("res");
    names.set(run, "forge-f2-build");
    const c = "c-label-result";
    const view = await paint(c, [result(2, run)]);

    await vi.waitFor(() => {
      expect(runButtons(view).map((x) => x.textContent)).toEqual(["forge-f2-build"]);
    });
    expect(view.querySelector(".steer-note-label")?.textContent).toBe("Workflow result");

    runButtons(view)[0]?.click();

    await vi.waitFor(() => {
      expect(openRunView).toHaveBeenCalledWith(run, "forge-f2-build", c);
    });
  });
});

/** A launch's two entries: the call, then the result carrying the run id the transcript draws a card for. */
function launched(at: number, run: string): Entry[] {
  const call = `t1-c${String(at)}`;
  return [
    sealed(
      "t1",
      at,
      "tool_call",
      { id: `tool-${run}`, title: "Run Workflow", status: "completed", kind: "execute", ts: 1 },
      call,
    ),
    sealed(
      "t1",
      at + 1,
      "tool_result",
      { status: "completed", workflow_id: run },
      toolResultID(call),
    ),
  ];
}

/** A turn whose delegate lane `sub-A` launched `run`, as a subagent view renders it. */
function subagentTurn(run: string): Turn {
  const body = launched(1, run).map((e) => ({ ...e, turn: "td", lane: "sub-A" }));
  return {
    id: "td",
    n: 1,
    trigger: { id: "td-p", text: "go" },
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

function notes(view: HTMLElement): HTMLElement[] {
  return [...view.querySelectorAll<HTMLElement>(".steer-note")];
}

describe("the run state a step card holds", () => {
  it("lets the run go when the last note is torn down", async () => {
    const run = runID("last");
    names.set(run, "forge-f2-build");
    const view = await paint("c-hold-last", [stepUpdate(2, run, "P4-REVIEW-GPT")]);
    await vi.waitFor(() => {
      expect(runButtons(view)).toHaveLength(1);
      expect(runStore.peekRunState(run)).toBeDefined();
    });

    messages.teardownAll();

    expect(runStore.peekRunState(run)).toBeUndefined();
  });

  it("lets a run its card fetched go once card and note are both torn down", async () => {
    const run = runID("card");
    names.set(run, "forge-f2-build");
    const view = await paint("c-hold-card", [
      ...launched(2, run),
      stepUpdate(4, run, "P4-REVIEW-GPT"),
    ]);
    await vi.waitFor(() => {
      expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    });

    messages.teardownAll();

    expect(runStore.peekRunState(run)).toBeUndefined();
  });

  it("keeps the run a transcript card still reads when its note is released", async () => {
    const run = runID("kept");
    names.set(run, "forge-f2-build");
    const view = await paint("c-hold-kept", [
      ...launched(2, run),
      stepUpdate(4, run, "P4-REVIEW-GPT"),
    ]);
    await vi.waitFor(() => {
      expect(runButtons(view)).toHaveLength(1);
    });

    for (const n of notes(view)) {
      unwatchStepRunLabel(n);
    }

    expect(runStore.peekRunState(run)).toBeDefined();
  });

  it("keeps the run until every note watching it is released", async () => {
    const run = runID("two");
    names.set(run, "forge-f2-build");
    const view = await paint("c-hold-two", [
      stepUpdate(2, run, "P4-REVIEW-GPT"),
      stepUpdate(3, run, "P4-AGGREGATE"),
    ]);
    await vi.waitFor(() => {
      expect(runButtons(view)).toHaveLength(2);
      expect(runStore.peekRunState(run)).toBeDefined();
    });
    const [first, second] = notes(view);

    unwatchStepRunLabel(first as HTMLElement);
    expect(runStore.peekRunState(run)).toBeDefined();

    unwatchStepRunLabel(second as HTMLElement);
    expect(runStore.peekRunState(run)).toBeUndefined();
  });

  it("drops a read that lands after the last note is gone, whether it answers or fails", async () => {
    useSteppableClock();
    onTestFinished(() => {
      vi.useRealTimers();
    });
    for (const answers of [true, false]) {
      const run = runID(answers ? "late-ok" : "late-bad");
      if (answers) {
        names.set(run, "forge-f2-build");
      }
      gate.holding = true;
      gate.releases.length = 0;
      await paint(`c-hold-late-${String(answers)}`, [stepUpdate(2, run, "P4-REVIEW-GPT")]);
      await vi.waitFor(() => {
        expect(gate.releases).toHaveLength(1);
      });

      messages.teardownAll();
      gate.holding = false;
      gate.releases[0]?.();
      await vi.advanceTimersByTimeAsync(1300);

      expect(runStore.peekRunState(run)).toBeUndefined();
      // A failed read the store kept retrying would be a second read for a run nobody shows.
      expect(reads.filter((id) => id === run)).toHaveLength(1);
    }
  });

  it("does not re-read a run nobody holds when a lifecycle event lands during its late read", async () => {
    useSteppableClock();
    onTestFinished(() => {
      vi.useRealTimers();
    });
    const run = runID("late-event");
    names.set(run, "forge-f2-build");
    gate.holding = true;
    gate.releases.length = 0;
    await paint("c-hold-late-event", [stepUpdate(2, run, "P4-REVIEW-GPT")]);
    await vi.waitFor(() => {
      expect(gate.releases).toHaveLength(1);
    });

    messages.teardownAll();
    runStore.invalidateRun(run);
    gate.holding = false;
    gate.releases[0]?.();
    await vi.advanceTimersByTimeAsync(100);

    expect(runStore.peekRunState(run)).toBeUndefined();
    expect(reads.filter((id) => id === run)).toHaveLength(1);
  });

  it("gives a run card that returns during a held read the state that read fetched for it", async () => {
    const run = runID("back-card");
    names.set(run, "forge-f2-build");
    gate.holding = true;
    gate.releases.length = 0;
    const c = "c-hold-back-card";
    await paint(c, launched(2, run));
    await vi.waitFor(() => {
      expect(gate.releases).toHaveLength(1);
    });

    messages.teardownAll();
    await paint(c, launched(2, run));
    gate.holding = false;
    gate.releases[0]?.();

    await vi.waitFor(() => {
      expect(runStore.peekRunState(run)).toBeDefined();
    });
  });

  it("gives a step note that returns during a held read the run's name", async () => {
    const run = runID("back-note");
    names.set(run, "forge-f2-build");
    gate.holding = true;
    gate.releases.length = 0;
    const c = "c-hold-back-note";
    await paint(c, [stepUpdate(2, run, "P4-REVIEW-GPT")]);
    await vi.waitFor(() => {
      expect(gate.releases).toHaveLength(1);
    });

    messages.teardownAll();
    const view = await paint(c, [stepUpdate(2, run, "P4-REVIEW-GPT")]);
    gate.holding = false;
    gate.releases[0]?.();

    await vi.waitFor(() => {
      expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    });
  });

  it("gives a subagent view's run card the state a read fetched while its transcript note was released", async () => {
    const run = runID("detached");
    names.set(run, "forge-f2-build");
    gate.holding = true;
    gate.releases.length = 0;
    const view = await paint("c-hold-detached", [stepUpdate(2, run, "P4-REVIEW-GPT")]);
    await vi.waitFor(() => {
      expect(gate.releases).toHaveLength(1);
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, subagentTurn(run), "c-detached-view", "sub-A", false);

    for (const n of notes(view)) {
      unwatchStepRunLabel(n);
    }
    gate.holding = false;
    gate.releases[0]?.();

    await vi.waitFor(() => {
      expect(runStore.peekRunState(run)).toBeDefined();
    });
    blocks.disposeDetachedBody("td", "sub-A");
    host.remove();
  });

  it("lets a run go when the subagent view holding its card is closed", async () => {
    const run = runID("detached-close");
    names.set(run, "forge-f2-build");
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, subagentTurn(run), "c-detached-close", "sub-A", false);
    await vi.waitFor(() => {
      expect(runStore.peekRunState(run)).toBeDefined();
    });

    blocks.disposeDetachedBody("td", "sub-A");
    host.remove();

    expect(runStore.peekRunState(run)).toBeUndefined();
  });

  it("keeps a nested run's state across a rebuild of the detached body that held it", async () => {
    const run = runID("detached-rebuild");
    names.set(run, "forge-f2-build");
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, subagentTurn(run), "c-detached-rebuild", "sub-A", false);
    await vi.waitFor(() => {
      expect(runStore.peekRunState(run)).toBeDefined();
    });

    blocks.rebuildDetached(() => {
      blocks.disposeDetachedBody("td", "sub-A");
      host.replaceChildren();
      // Between the old render's release and the new one's claim, nothing else holds the cell.
      expect(runStore.peekRunState(run)).toBeDefined();
      blocks.buildDetachedBody(host, subagentTurn(run), "c-detached-rebuild", "sub-A", false);
    });

    expect(runStore.peekRunState(run)).toBeDefined();
    blocks.disposeDetachedBody("td", "sub-A");
    host.remove();
    expect(runStore.peekRunState(run)).toBeUndefined();
  });

  it("lets a run go that a rebuilt detached body no longer shows", async () => {
    const run = runID("detached-dropped");
    names.set(run, "forge-f2-build");
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(host, subagentTurn(run), "c-detached-dropped", "sub-A", false);
    await vi.waitFor(() => {
      expect(runStore.peekRunState(run)).toBeDefined();
    });

    blocks.rebuildDetached(() => {
      blocks.disposeDetachedBody("td", "sub-A");
      host.replaceChildren();
    });

    expect(runStore.peekRunState(run)).toBeUndefined();
    host.remove();
  });

  it("reads a run its card already reads once, not twice", async () => {
    useSteppableClock();
    onTestFinished(() => {
      vi.useRealTimers();
    });
    const run = runID("once");
    names.set(run, "forge-f2-build");
    const view = await paint("c-hold-once", [
      ...launched(2, run),
      stepUpdate(4, run, "P4-REVIEW-GPT"),
    ]);
    await vi.waitFor(() => {
      expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    });
    // A trailing read an invalidation queued would land after the first settles.
    await vi.advanceTimersByTimeAsync(50);

    expect(reads.filter((id) => id === run)).toHaveLength(1);
  });
});

describe("a step card redrawn when its message is taken up", () => {
  it("keeps the run's name through the redraw, with no new read", async () => {
    const run = runID("redraw");
    names.set(run, "forge-f2-build");
    const c = "c-label-redraw";
    const view = await paint(
      c,
      [stepUpdate(2, run, "P4-REVIEW-GPT")],
      [
        [
          "t2",
          [
            sealed(
              "t2",
              0,
              "turn_open",
              { prompt: { id: "t2-p", text: "next" }, source: "prompt", n: 2 },
              "t2-open",
            ),
          ],
        ],
      ],
    );
    await vi.waitFor(() => {
      expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    });
    const before = view.querySelector(".steer-note");
    const issued = reads.length;

    store.appendEntry(
      c,
      sealed(
        "t2",
        1,
        "steer_delivered",
        { steer_id: `wfmsg-${run}-P4-REVIEW-GPT`, kas_id: "notify-1", read_ts: SENT + 1000 },
        "delivered-1",
      ),
    );
    await Promise.resolve();

    const after = view.querySelector(".steer-note");
    expect(after).not.toBe(before);
    expect(runButtons(view).map((b) => b.textContent)).toEqual(["forge-f2-build"]);
    expect(reads).toHaveLength(issued);
  });

  it("leaves only the mounted note to release after repeated redraws", async () => {
    const run = runID("owned");
    names.set(run, "forge-f2-build");
    const c = "c-label-owned";
    const view = await paint(
      c,
      [stepUpdate(2, run, "P4-REVIEW-GPT")],
      [
        [
          "t2",
          [
            sealed(
              "t2",
              0,
              "turn_open",
              { prompt: { id: "t2-p", text: "next" }, source: "prompt", n: 2 },
              "t2-open",
            ),
          ],
        ],
      ],
    );
    await vi.waitFor(() => {
      expect(runButtons(view)).toHaveLength(1);
    });
    for (const n of [1, 2]) {
      store.appendEntry(
        c,
        sealed(
          "t2",
          n,
          "steer_delivered",
          {
            steer_id: `wfmsg-${run}-P4-REVIEW-GPT`,
            kas_id: `notify-${String(n)}`,
            read_ts: SENT + n,
          },
          `delivered-${String(n)}`,
        ),
      );
      await Promise.resolve();
    }
    const mounted = view.querySelector<HTMLElement>(".steer-note");
    released.length = 0;

    messages.teardownAll();

    expect(released).toEqual([mounted]);
  });
});
