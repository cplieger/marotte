import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type * as ApiClient from "./api-client.js";
import type { RunNode, RunProgressFrame, RunState } from "./run-store.js";
import type { RunAsks } from "./run-asks.js";
import type { ExecNode } from "./exec-view/model.js";
import { nodePathKey } from "./run-node-key.js";

let responses: RunState[] = [];
let resolvers: (() => void)[] = [];

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetOrError: vi.fn(async () => {
    await new Promise<void>((r) => resolvers.push(r));
    const state = responses.shift();
    return state === undefined
      ? { ok: false, status: 502, data: null, error: "" }
      : { ok: true, status: 200, data: { workflowId: state.workflowId, state }, error: "" };
  }),
  apiGetTyped: vi.fn(async () => null),
}));

const store = await import("./run-store.js");
const { runToExec } = await import("./run-exec-source.js");
const { buildExecPage } = await import("./exec-view/page.js");

const NO_ASKS: RunAsks = { count: 0, asked: [], label: "" };
/** A node path as the tests spell it, "/"-joined, in the key the exec page addresses rows by. */
const k = (path: string): string => nodePathKey(path.split("/"));
const T0 = Date.parse("2026-10-08T10:00:00Z");
const at = (ms: number): string => new Date(T0 + ms).toISOString();

type Leaf = Pick<RunNode, "status" | "startedAt" | "endedAt" | "sessionId">;

function tree(plan: Leaf, build: Leaf, ship: Leaf): RunState {
  const leaf = (nodeId: string, l: Leaf): RunNode => ({ nodeId, type: "step", ...l });
  return {
    workflowId: "wf",
    status: "running",
    root: {
      nodeId: "wf",
      type: "sequence",
      status: "running",
      children: [
        leaf("plan", plan),
        {
          nodeId: "fan",
          type: "parallel",
          status: "running",
          children: [leaf("build", build), leaf("ship", ship)],
        },
      ],
    },
  };
}

const PLANNED: Leaf = { status: "completed", startedAt: at(0), endedAt: at(40) };
const PENDING: Leaf = { status: "pending" };

type Frame = Omit<RunProgressFrame, "workflow_id">;

/** `racedRead` answers after `during` landed, and `reread` is what any read issued after them
 *  answers. */
type Event =
  | { read: RunState }
  | { frame: Frame }
  | { pick: string }
  | { racedRead: RunState; during: Frame[]; reread: RunState };

let page: ReturnType<typeof buildExecPage>;
let selected: ExecNode | undefined;

async function answerReads(): Promise<void> {
  while (resolvers.length > 0) {
    for (const r of resolvers.splice(0)) {
      r();
    }
    // A task boundary, so every continuation of the read, a trailing one included, has run.
    await new Promise((r) => setTimeout(r, 0));
  }
}

function land(f: Frame): void {
  expect(store.applyRunProgress({ workflow_id: "wf", ...f }), "the frame lands").toBe(true);
}

async function play(events: readonly Event[]): Promise<void> {
  for (const e of events) {
    if ("read" in e) {
      responses.push(e.read);
      store.invalidateRun("wf");
      await answerReads();
      expect(responses, "the read was taken").toHaveLength(0);
    } else if ("racedRead" in e) {
      responses.push(e.racedRead);
      store.invalidateRun("wf");
      e.during.forEach(land);
      responses.push(e.reread);
      await answerReads();
    } else if ("frame" in e) {
      land(e.frame);
    } else {
      page.root
        .querySelector<HTMLElement>(`.ev-row[data-path="${e.pick}"] > .ev-row-main`)
        ?.click();
      expect(selected?.path, "the pick took").toBe(e.pick);
      continue;
    }
    const state = store.peekRunState("wf");
    if (state !== undefined) {
      page.render(runToExec("wf", state, undefined, NO_ASKS));
    }
  }
}

beforeEach(() => {
  responses = [];
  resolvers = [];
  selected = undefined;
  store.forgetRun("wf");
  page = buildExecPage({
    emptyNote: () => "",
    onSelect: (n) => {
      selected = n;
    },
    follow: "newest",
  });
  document.body.append(page.root);
});

afterEach(() => {
  page.dispose();
  page.root.remove();
});

describe("one start counts once, and a new one counts however it was learned", () => {
  const rows: { name: string; events: Event[]; want: string }[] = [
    {
      name: "frame seen, persisted correction: the read's earlier time is the same start",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        { read: tree(PLANNED, { status: "running", startedAt: at(100) }, PENDING) },
      ],
      want: k("wf/plan"),
    },
    {
      name: "frame missed, persisted correction: a late frame for a start the read showed is the same start",
      events: [
        { read: tree(PLANNED, { status: "running", startedAt: at(100) }, PENDING) },
        { pick: k("wf/plan") },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { read: tree(PLANNED, { status: "running", startedAt: at(100) }, PENDING) },
      ],
      want: k("wf/plan"),
    },
    {
      name: "frame seen, different path",
      events: [
        { read: tree(PLANNED, { status: "running", startedAt: at(100) }, PENDING) },
        { pick: k("wf/plan") },
        { frame: { node_path: ["wf", "fan", "ship"], status: "running", started_at: at(208) } },
      ],
      want: k("wf/fan/ship"),
    },
    {
      name: "frame missed, different path: a start older than another's arrival stamp still counts",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(100) },
            { status: "running", startedAt: at(105) },
          ),
        },
      ],
      want: k("wf/fan/ship"),
    },
    {
      name: "frame seen, same-path retry",
      events: [
        { read: tree(PLANNED, { status: "running", startedAt: at(100) }, PENDING) },
        { pick: k("wf/plan") },
        { frame: { node_path: ["wf", "fan", "build"], status: "failed", ended_at: at(150) } },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(300) } },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "frame missed, same-path retry: a restart older than another's arrival stamp still counts",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(50) } },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(45), sessionId: "sess-a" },
            PENDING,
          ),
        },
        { frame: { node_path: ["wf", "fan", "ship"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(105), sessionId: "sess-b" },
            { status: "completed", startedAt: at(100), endedAt: at(102) },
          ),
        },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "frame seen, ended, same-path retry read at a persisted time before the frame's arrival stamp",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        { frame: { node_path: ["wf", "fan", "build"], status: "failed", ended_at: at(110) } },
        { read: tree(PLANNED, { status: "running", startedAt: at(105) }, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(112) } },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a read showing the retry before its frames arrive: the queued frames still count it",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        { read: tree(PLANNED, { status: "running", startedAt: at(105) }, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "failed", ended_at: at(110) } },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(112) } },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a read issued before a step ended cannot restart it",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { frame: { node_path: ["wf", "fan", "ship"], status: "running", started_at: at(110) } },
        { pick: k("wf/plan") },
        {
          racedRead: tree(
            PLANNED,
            { status: "running", startedAt: at(100) },
            { status: "running", startedAt: at(105) },
          ),
          during: [{ node_path: ["wf", "fan", "build"], status: "completed", ended_at: at(120) }],
          reread: tree(
            PLANNED,
            { status: "completed", startedAt: at(100), endedAt: at(119) },
            { status: "running", startedAt: at(105) },
          ),
        },
      ],
      want: k("wf/plan"),
    },
    {
      name: "a retry a read showed while the step's end raced it counts once re-read",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        {
          racedRead: tree(PLANNED, { status: "running", startedAt: at(105) }, PENDING),
          during: [{ node_path: ["wf", "fan", "build"], status: "failed", ended_at: at(110) }],
          reread: tree(PLANNED, { status: "running", startedAt: at(105) }, PENDING),
        },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a retry whose end and restart frames a gap hid counts once a read shows its new session",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        {
          frame: {
            node_path: ["wf", "fan", "build"],
            status: "running",
            started_at: at(108),
            session_id: "sess-a",
          },
        },
        { pick: k("wf/plan") },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(300), sessionId: "sess-b" },
            PENDING,
          ),
        },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a read of the session a frame announced is the same execution",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        {
          frame: {
            node_path: ["wf", "fan", "build"],
            status: "running",
            started_at: at(109),
            session_id: "sess-a",
          },
        },
        { pick: k("wf/plan") },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(100), sessionId: "sess-a" },
            PENDING,
          ),
        },
      ],
      want: k("wf/plan"),
    },
    {
      name: "the session the second node_start names tells a later read's retry apart",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        {
          frame: {
            node_path: ["wf", "fan", "build"],
            status: "running",
            started_at: at(109),
            session_id: "sess-a",
          },
        },
        { pick: k("wf/plan") },
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(300), sessionId: "sess-b" },
            PENDING,
          ),
        },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a start frame naming a new session on a live path is a new execution",
      events: [
        {
          read: tree(
            PLANNED,
            { status: "running", startedAt: at(100), sessionId: "sess-a" },
            PENDING,
          ),
        },
        { pick: k("wf/plan") },
        {
          frame: {
            node_path: ["wf", "fan", "build"],
            status: "running",
            started_at: at(300),
            session_id: "sess-b",
          },
        },
      ],
      want: k("wf/fan/build"),
    },
    {
      name: "a step a read shows reset to pending starts afresh on its next frame",
      events: [
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(108) } },
        { pick: k("wf/plan") },
        { read: tree(PLANNED, PENDING, PENDING) },
        { frame: { node_path: ["wf", "fan", "build"], status: "running", started_at: at(115) } },
      ],
      want: k("wf/fan/build"),
    },
  ];

  for (const row of rows) {
    it(row.name, async () => {
      await play(row.events);
      expect(selected?.path).toBe(row.want);
    });
  }
});
