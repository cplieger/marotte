// Gap recovery through the RUN's own reads: a step that opened and closed entirely inside a
// connection gap, and a pane opened by RELOAD while a step is still streaming.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import type * as ApiClient from "./api-client.js";
import type { RevalidateContext } from "@cplieger/sse";
import type { Entry, OpenEntry, SubjectStamp } from "./wire/types.gen.js";

const api = vi.hoisted(() => ({
  getTyped: vi.fn(),
  getTypedOrError: vi.fn(),
  getOrError: vi.fn(),
}));

vi.mock("./api-client.js", async (importOriginal) => {
  const orig = await importOriginal<typeof ApiClient>();
  return {
    ...orig,
    apiGetTyped: api.getTyped,
    apiGetTypedOrError: api.getTypedOrError,
    apiGetOrError: api.getOrError,
  };
});

// Nothing else is mocked: the run store, the step GET, the range read, the version map and the
// adapter's digest arm ARE the subject.
import {
  invalidateRun,
  peekRunState,
  registerRunTurnRepair,
  runTurnHoles,
  runTurns,
} from "./run-store.js";
import { requestRunTurnRange } from "./run-turn-range.js";
import {
  clearStepTranscripts,
  rereadStepTranscript,
  requestStepTranscript,
  stepRead,
} from "./run-step-transcript.js";
import { _revalidateForTest, _resetForTest as resetAdapter, markHydrated } from "./sse-adapter.js";
import { hasSubject, versionMap, _resetForTest as resetVersions } from "./subject-versions.js";
import { decodeRunStepTranscript } from "./wire/decoders.gen.js";
import { createScriptedFetch, json, type ScriptedFetch } from "./__test-helpers__/sse-fetch.js";

const EPOCH = "0123456789abcdef";
const PATH = "root/step-b";
const TURN = "t-b";

/** A fresh run id per case: `run-store.ts` has no test reset, so a shared id would carry one
 *  case's log into the next. */
let runID = "";
let runs = 0;

function entry(seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${TURN}-e${String(seq)}`, turn: TURN, kind, seq, ts: seq + 1, payload };
}

/** The server's log for the step's turn, and the tail it has not sealed yet. Both mutable: the
 *  reload case's gap is one seal, which moves the tail into the log and the turn's version with
 *  it. */
let log: Entry[] = [];
let tail: OpenEntry | undefined;

/** A step that RAN AND FINISHED: its `turn_close` is in the log, so the turn is settled and the
 *  run holds no `run_turn` ref for it. */
function closedStep(): void {
  log = [
    entry(0, "turn_open", { source: "workflow_step", n: 1 }),
    entry(1, "text", { text: "done" }),
    entry(2, "turn_close", { outcome: "completed" }),
  ];
  tail = undefined;
}

/** A step still STREAMING: three sealed entries and an open tail, which is the state a pane
 *  opened by reload finds. */
function streamingStep(): void {
  log = [
    entry(0, "turn_open", { source: "workflow_step", n: 1 }),
    entry(1, "text", { text: "one" }),
    entry(2, "text", { text: "two" }),
  ];
  tail = { turn: TURN, id: `${TURN}-open3`, kind: "text", text: "thr", n: 1 };
}

/** One entry seals on the server while the client holds the pane and receives no frame. The turn
 *  stays open. */
function sealDuringGap(): void {
  log = [...log, entry(3, "text", { text: "three" })];
  tail = undefined;
}

/** A `run_turn` stamp's version is `<turn>:<newest sealed seq>`, which is what the server
 *  answers for both turn kinds and what the store mints from the entries it adopts. */
function turnVersion(): string {
  return `${TURN}:${String((log.at(-1) as Entry).seq)}`;
}

function runTurnRef(): string {
  return `${runID}/${TURN}`;
}

/** The stamps the step GET certifies: one `run_turn` per OPEN turn of the path, and NONE at all
 *  for a settled path. */
function stepStamps(): SubjectStamp[] {
  return log.some((e) => e.kind === "turn_close")
    ? []
    : [{ kind: "run_turn", ref: runTurnRef(), version: turnVersion(), epoch: EPOCH }];
}

/** The step GET's answer, through the wire's own decoder so a renamed field fails here rather
 *  than in production. */
function stepAnswer(): ReturnType<typeof decodeRunStepTranscript> {
  return decodeRunStepTranscript({
    workflow_id: runID,
    node_path: PATH,
    state: "ready",
    source: "log",
    entries: log,
    open_entries: tail === undefined ? [] : [tail],
    subject: stepStamps(),
  });
}

/** What the two folds are compared on: the entries at the positions their `seq` claims, the open
 *  tails per lane, and where the closer landed. */
function heldFold(): unknown {
  const state = runTurns(runID).find(([id]) => id === TURN)?.[1];
  return {
    entries: state?.entries,
    open: [...(state?.openEntries.values() ?? [])],
    closeAt: state?.closeAt,
  };
}

/** The same fold read off the fixture server, which is what a client that lost no frame holds. */
function serverFold(): unknown {
  return {
    entries: log,
    open: tail === undefined ? [] : [{ ...tail, lane: "" }],
    closeAt: log.find((e) => e.kind === "turn_close")?.seq,
  };
}

function stepReads(): string[] {
  return api.getTypedOrError.mock.calls.map((c) => String(c[0]));
}

function rangeReads(): string[] {
  return api.getTyped.mock.calls.map((c) => String(c[0]));
}

interface Held {
  readonly kind: string;
  readonly ref: string;
  readonly version: string;
}

let scripted: ScriptedFetch;
/** Every digest exchange: what the client asked about, and what the server answered. */
let digests: { held: Held[]; changed: Held[] }[];

function ctx(over: Partial<RevalidateContext> = {}): RevalidateContext {
  return {
    cause: "visible",
    epoch: versionMap().epoch(),
    generation: 1,
    full: false,
    signal: new AbortController().signal,
    ...over,
  };
}

/** Give the run a fetched state, which is the freshness gate a `run_turn` stamp's refetch is
 *  behind: the adapter drops a stamp naming a run this client holds nothing for. */
async function seedRunState(): Promise<void> {
  invalidateRun(runID);
  await vi.waitFor(() => {
    expect(peekRunState(runID)).toBeDefined();
  });
}

beforeEach(() => {
  runs += 1;
  runID = `wf-${String(runs)}`;
  resetVersions();
  resetAdapter();
  clearStepTranscripts();
  registerRunTurnRepair(requestRunTurnRange);
  api.getTyped.mockReset();
  api.getTypedOrError.mockReset();
  api.getOrError.mockReset();
  // The run's own state, for the freshness gate; the tree itself is not this arm's subject.
  api.getOrError.mockImplementation(() =>
    Promise.resolve({ ok: true, status: 200, data: { state: { status: "running" } }, error: "" }),
  );
  api.getTypedOrError.mockImplementation(() =>
    Promise.resolve({ ok: true, status: 200, data: stepAnswer(), error: "" }),
  );
  // The range read, answered from the same log so the two reads cannot disagree about what the
  // server holds.
  api.getTyped.mockImplementation((path: string) => {
    const cut = path.indexOf("?after=");
    const after = cut === -1 ? -1 : Number(path.slice(cut + "?after=".length));
    return Promise.resolve({
      entries: log.filter((e) => e.seq > after),
      open_entries: tail === undefined ? [] : [tail],
    });
  });
  digests = [];
  scripted = createScriptedFetch();
  // The real server compares each held version against its log and names what moved, so the answer
  // is a reading of the request rather than a fixture.
  scripted.respond("/api/sync", (req) => {
    const body = JSON.parse(req.body ?? "{}") as { subjects?: Held[] };
    const held = body.subjects ?? [];
    const changed = held
      .filter((h) => h.kind === "run_turn" && h.ref === runTurnRef() && h.version !== turnVersion())
      .map((h) => ({ kind: h.kind, ref: h.ref, version: turnVersion() }));
    digests.push({ held, changed });
    return json({
      epoch: EPOCH,
      head: "9",
      must_refetch: false,
      checked: held.length,
      changed,
      removed: [],
    });
  });
  vi.stubGlobal("fetch", scripted.fetch);
  // The run's stamps carry no epoch of their own, so the map is bound as the stream binds it.
  versionMap().bind(EPOCH);
  markHydrated();
});

afterEach(() => {
  registerRunTurnRepair(() => undefined);
  resetAdapter();
});

describe("a step that opened and closed entirely inside the gap", () => {
  beforeEach(() => {
    closedStep();
  });

  it("seats the whole closed turn from the step GET, the only door to it", async () => {
    // The gap's shape: no frame of this turn arrived, so the store holds nothing for it and the
    // pane's own read is what fills it.
    expect(runTurns(runID)).toEqual([]);

    requestStepTranscript(runID, PATH);

    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });
    expect(stepReads()).toEqual([`/api/runs/${runID}/steps/root/step-b`]);
    // Equality against the server's own log is the claim, so an entry seated at the wrong position
    // fails here: the closer included, and the turn reads settled.
    expect(heldFold()).toEqual(serverFold());
    expect(runTurnHoles(runID)).toEqual([]);
  });

  it("holds no run_turn stamp for it, which is what the answer's empty subject says", async () => {
    requestStepTranscript(runID, PATH);
    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });

    // The pairing is the claim rather than either half: the answer certifies no open turn of this
    // path, and the store minted no ref for one.
    expect(stepAnswer().subject).toEqual([]);
    expect(hasSubject("run_turn", runTurnRef())).toBe(false);

    // So the digest has nothing to ask about for this turn, and a closed step cannot be named by
    // one.
    await _revalidateForTest(ctx());
    expect(digests.flatMap((d) => d.held.map((h) => h.ref))).not.toContain(runTurnRef());
  });

  it("asks ONCE, because a settled verdict refuses a second read", async () => {
    requestStepTranscript(runID, PATH);
    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });

    requestStepTranscript(runID, PATH);
    // Asserted after the second call, which is the only place a refetch would show: the answer is
    // already settled, so the pane's read fires once for the step.
    expect(stepReads()).toHaveLength(1);
  });
});

describe("a pane opened by reload while a step streams", () => {
  beforeEach(async () => {
    streamingStep();
    await seedRunState();
  });

  it("holds the run_turn stamp the step GET gave it", async () => {
    const answered = stepAnswer().subject[0];
    expect(answered?.ref).toBe(runTurnRef());

    requestStepTranscript(runID, PATH);
    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });

    expect(hasSubject("run_turn", runTurnRef())).toBe(true);
    // The VERSION is read off the answer rather than written twice: the store mints its own stamp
    // from the entries it adopted, and this is the claim that the two agree.
    await _revalidateForTest(ctx());
    expect(digests).toHaveLength(1);
    expect(digests[0]?.held).toEqual([
      { kind: "run_turn", ref: runTurnRef(), version: answered?.version },
    ]);
    expect(digests[0]?.changed).toEqual([]);
    expect(rangeReads()).toEqual([]);
  });

  it("repairs a sealed-in-the-gap entry with ONE range read past the seq it holds", async () => {
    requestStepTranscript(runID, PATH);
    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });
    sealDuringGap();

    await _revalidateForTest(ctx());

    // The stamp the step GET left is what the digest names, and the read asks past the newest `seq`
    // this client holds rather than for the whole turn.
    expect(digests[0]?.changed).toEqual([
      { kind: "run_turn", ref: runTurnRef(), version: `${TURN}:3` },
    ]);
    expect(rangeReads()).toEqual([`/api/runs/${runID}/turns/${TURN}?after=2`]);

    // The oracle is the server's own log: the sealed entry at the position its `seq` claims, the
    // tail gone with it, and the turn still open.
    await vi.waitFor(() => {
      expect(heldFold()).toEqual(serverFold());
    });
    // Asserted AFTER the answer landed as well, because a seat that refused the answer's first
    // entry re-asks and the count is the one place that second read shows.
    expect(rangeReads()).toHaveLength(1);
    expect(runTurnHoles(runID)).toEqual([]);
  });

  it("drops the tail its own entry sealed when the pane re-reads the step", async () => {
    requestStepTranscript(runID, PATH);
    await vi.waitFor(() => {
      expect(stepRead(runID, PATH)?.state).toBe("ready");
    });
    sealDuringGap();

    rereadStepTranscript(runID, PATH);

    // The step GET is the run's other whole-turn read and answers the same tails, so the same rule
    // holds through it: the tail this client holds sealed into the entry the answer carries, and
    // must not survive beside it.
    await vi.waitFor(() => {
      expect(heldFold()).toEqual(serverFold());
    });
    expect(stepReads()).toHaveLength(2);
    // No digest ran, so the repair is the step GET's alone.
    expect(rangeReads()).toEqual([]);
  });
});
