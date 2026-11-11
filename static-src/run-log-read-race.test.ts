// A step or range read answers a snapshot taken while the stream keeps moving: a frame that lands
// while the read is in flight must still be the newest word on its entry once the answer adopts, and
// a frame the answer already reflects, arriving after it, changes nothing.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import type { Entry, OpenEntry } from "./types.js";
import type { RunStepTranscript } from "./wire/types.gen.js";

const apiGetTypedOrError = vi.fn<(url: string, decode: unknown) => Promise<unknown>>();
const apiGetTyped =
  vi.fn<(url: string, decode: unknown, signal?: AbortSignal) => Promise<unknown>>();

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetTypedOrError: (url: string, decode: unknown) => apiGetTypedOrError(url, decode),
  apiGetTyped: (url: string, decode: unknown, signal?: AbortSignal) =>
    apiGetTyped(url, decode, signal),
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  apiPost: vi.fn(),
}));

await import("./handlers/run.js");
const { dispatch } = await import("./bus.js");
const { clearStepTranscripts, requestStepTranscript } = await import("./run-step-transcript.js");
const { requestRunTurnRange } = await import("./run-turn-range.js");
const { forgetRun, openRunTurn, registerRunTurnRepair, runStepTurns } =
  await import("./run-store.js");
const { setRunContentScope } = await import("./run-content-scope.js");

const RUN = "wf_race";
const PATH = `${RUN}:lanes:lane-a:iter-0:build`;
const TURN = "t-build";

const turnOpen: Entry = {
  id: `${TURN}-0`,
  turn: TURN,
  lane: "",
  kind: "turn_open",
  seq: 0,
  ts: 0,
  payload: { source: "workflow_step", node_path: PATH, n: 1 },
};

function text(seq: number, body: string): Entry {
  return {
    id: `${TURN}-${String(seq)}`,
    turn: TURN,
    lane: "",
    kind: "text",
    seq,
    ts: 0,
    payload: { text: body },
  };
}

/** Entry 1 open with `body`, as the server's log held it when the read was answered. */
function openTail(body: string, n: number): OpenEntry {
  return { turn: TURN, id: `${TURN}-1`, lane: "", kind: "text", text: body, n };
}

function answer(entries: Entry[], open: OpenEntry[]): unknown {
  const data: RunStepTranscript = {
    workflow_id: RUN,
    node_path: PATH,
    state: "ready",
    source: "log",
    entries,
    open_entries: open,
    subject: [],
  };
  return { ok: true, status: 200, data, error: "" };
}

/** A read the test answers when it chooses, so frames can land while it is in flight. */
function heldAnswer(read: "step" | "range"): { readonly answer: (v: unknown) => void } {
  let resolve: (v: unknown) => void = () => undefined;
  const p = new Promise<unknown>((r) => {
    resolve = r;
  });
  (read === "step" ? apiGetTypedOrError : apiGetTyped).mockReturnValueOnce(p);
  return { answer: resolve };
}

function frame(type: string, payload: Record<string, unknown>): void {
  dispatch({ type, payload: { workflow_id: RUN, ...payload } } as Parameters<typeof dispatch>[0]);
}

async function settle(): Promise<void> {
  await vi.dynamicImportSettled();
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
}

function heldTurn(): { entries: readonly Entry[]; open: readonly OpenEntry[] } {
  const state = runStepTurns(RUN, PATH).find(([id]) => id === TURN)?.[1];
  return {
    entries: state?.entries ?? [],
    open: [...(state?.openEntries.values() ?? [])],
  };
}

beforeEach(() => {
  apiGetTypedOrError.mockReset();
  apiGetTyped.mockReset();
  forgetRun(RUN);
  clearStepTranscripts();
  setRunContentScope(RUN, PATH);
  // As boot wires it, so a hole the store marks is a range read the case can see.
  registerRunTurnRepair(requestRunTurnRange);
});

afterEach(() => {
  registerRunTurnRepair(() => undefined);
  setRunContentScope("", "");
  forgetRun(RUN);
});

describe("a step read in flight while its turn streams", () => {
  it("leaves an entry the stream sealed during the read sealed, with no open tail", async () => {
    const read = heldAnswer("step");
    requestStepTranscript(RUN, PATH);
    frame("turn_opened", { entry: turnOpen });
    frame("entry_opened", { open: openTail("", 0) });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 1, delta: "hel" });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 2, delta: "lo" });
    frame("entry_sealed", { turn: TURN, entry_id: `${TURN}-1`, lane: "", seq: 1, ts: 5, n: 2 });

    read.answer(answer([turnOpen], [openTail("hel", 1)]));
    await settle();

    const held = heldTurn();
    expect(held.entries.map((e) => e.id)).toEqual([`${TURN}-0`, `${TURN}-1`]);
    expect(held.entries[1]?.payload).toEqual({ text: "hello" });
    expect(held.open).toEqual([]);
  });

  it("keeps the deltas that landed after the snapshot on the entry it still holds open", async () => {
    const read = heldAnswer("step");
    requestStepTranscript(RUN, PATH);
    frame("turn_opened", { entry: turnOpen });
    frame("entry_opened", { open: openTail("", 0) });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 1, delta: "hel" });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 2, delta: "lo" });

    read.answer(answer([turnOpen], [openTail("hel", 1)]));
    await settle();

    expect(heldTurn().open).toEqual([openTail("hello", 2)]);
  });

  it("takes an entry both the stream and the snapshot sealed once, asking no read", async () => {
    const read = heldAnswer("step");
    requestStepTranscript(RUN, PATH);
    frame("turn_opened", { entry: turnOpen });
    frame("entry_opened", { open: openTail("", 0) });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 1, delta: "hello" });
    frame("entry_sealed", { turn: TURN, entry_id: `${TURN}-1`, lane: "", seq: 1, ts: 5, n: 1 });

    read.answer(answer([turnOpen, text(1, "hello")], []));
    await settle();

    const held = heldTurn();
    expect(held.entries.map((e) => e.id)).toEqual([`${TURN}-0`, `${TURN}-1`]);
    expect(held.open).toEqual([]);
    expect(apiGetTyped).not.toHaveBeenCalled();
  });
});

describe("a range read in flight while its turn streams", () => {
  it("leaves an entry the stream sealed during the read sealed, with no open tail", async () => {
    openRunTurn(RUN, turnOpen);
    const read = heldAnswer("range");
    requestRunTurnRange(RUN, TURN, 0);
    frame("entry_opened", { open: openTail("", 0) });
    frame("entry_delta", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 1, delta: "hello" });
    frame("entry_sealed", { turn: TURN, entry_id: `${TURN}-1`, lane: "", seq: 1, ts: 5, n: 1 });

    read.answer({ entries: [], open_entries: [openTail("hello", 1)] });
    await settle();

    const held = heldTurn();
    expect(held.entries.map((e) => e.id)).toEqual([`${TURN}-0`, `${TURN}-1`]);
    expect(held.open).toEqual([]);
  });
});

describe("a frame the adopted answer already reflects, arriving after it", () => {
  const late: readonly [string, string, Record<string, unknown>][] = [
    ["entry_opened", "", { open: openTail("", 0) }],
    ["entry_delta", "", { turn: TURN, entry_id: `${TURN}-1`, lane: "", n: 1, delta: "hello" }],
    ["entry_sealed", "", { turn: TURN, entry_id: `${TURN}-1`, lane: "", seq: 1, ts: 5, n: 1 }],
  ];

  it.each(late)(
    "leaves an entry the answer sealed sealed for a late %s%s, asking no read",
    async (type, _, payload) => {
      apiGetTypedOrError.mockResolvedValueOnce(answer([turnOpen, text(1, "hello")], []));
      requestStepTranscript(RUN, PATH);
      await settle();

      frame(type, payload);
      await settle();

      const held = heldTurn();
      expect(held.entries.map((e) => e.id)).toEqual([`${TURN}-0`, `${TURN}-1`]);
      expect(held.open).toEqual([]);
      expect(apiGetTyped).not.toHaveBeenCalled();
    },
  );

  it.each(late.slice(0, 2))(
    "leaves the open entry at what the answer folded in for a late %s%s, asking no read",
    async (type, _, payload) => {
      apiGetTypedOrError.mockResolvedValueOnce(answer([turnOpen], [openTail("hello", 2)]));
      requestStepTranscript(RUN, PATH);
      await settle();

      frame(type, payload);
      await settle();

      expect(heldTurn().open).toEqual([openTail("hello", 2)]);
      expect(apiGetTyped).not.toHaveBeenCalled();
    },
  );
});
