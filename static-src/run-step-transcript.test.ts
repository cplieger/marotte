// The on-demand step-transcript reader: which URL it asks for, when it declines to
// ask, how it GRADES a failure, and what it commits to the run store.
//
// `api-client.js` is mocked and everything else is real, `run-store.js` included —
// the adoption is the behaviour, so the assertions read the run's own log back rather
// than a mock's calls. The GENERATED decoder is real too, which is the point of asking
// for a typed read: a reply the decoder rejects must reach the caller as the transient
// verdict rather than as content.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Entry, OpenEntry, RunStepTranscript } from "./types.js";

const apiGetTypedOrError = vi.fn<(url: string, decode: unknown) => Promise<unknown>>();

vi.mock("./api-client.js", () => ({
  apiGetTypedOrError: (url: string, decode: unknown) => apiGetTypedOrError(url, decode),
  // Present-but-inert so real-ESM linking succeeds whatever else this graph reaches
  // (`run-store.js` imports two of these). No case here calls them.
  apiGet: vi.fn(),
  apiGetOrError: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPost: vi.fn(),
  apiDelete: vi.fn(),
}));

const {
  clearStepTranscripts,
  requestStepTranscript,
  rereadStepTranscript,
  stepRead,
  stepTranscriptVersion,
} = await import("./run-step-transcript.js");
const { appendRunEntry, runTurnHoles, runTurns } = await import("./run-store.js");

/** The envelope `apiGetTypedOrError` answers with, structurally — `ApiResult` is
 *  private to `api-client.ts` and the consumer destructures rather than naming it. */
interface Envelope {
  readonly ok: boolean;
  readonly status: number;
  readonly data: RunStepTranscript | null;
  readonly error: string;
}

/** A 2xx envelope carrying a decoded reply in the shape the endpoint answers with. */
function ok(over: Partial<RunStepTranscript> = {}): Envelope {
  const body: RunStepTranscript = {
    workflow_id: "wf_1",
    node_path: "wf_1/a",
    state: "ready",
    source: "log",
    entries: [],
    open_entries: [],
    subject: [],
    ...over,
  };
  return { ok: true, status: 200, data: body, error: "" };
}

/** A FAILURE envelope at `status`. `data` is null by construction on this side:
 *  `toApiResult` collapses it, because a caller handed a status has no business
 *  reading a body the transport rejected. */
function fail(status: number): Envelope {
  return { ok: false, status, data: null, error: "refused" };
}

/** The `turn_open` that opens a step's turn: `seq` 0 by definition. */
function turnOpen(turn: string, nodePath: string): Entry {
  return {
    id: `${turn}-open`,
    turn,
    kind: "turn_open",
    seq: 0,
    ts: 0,
    payload: { source: "workflow_step", node_path: nodePath, n: 1 },
  };
}

/** One sealed `text` entry at the position its own `seq` claims. */
function text(turn: string, seq: number, body: string): Entry {
  return { id: `${turn}-${String(seq)}`, turn, kind: "text", seq, ts: 0, payload: { text: body } };
}

/** A lane's open tail, which never reaches the log and has no `seq`. */
function tail(turn: string, body: string): OpenEntry {
  return { turn, id: `${turn}-tail`, kind: "text", text: body, n: 1 };
}

/** The entries of one turn of a run's log, by id, in file order. */
function entryIDs(workflowID: string, turnID: string): string[] {
  const found = runTurns(workflowID).find(([id]) => id === turnID);
  return (found?.[1].entries ?? []).map((e) => e.id);
}

/** Ask, then wait for the answer that was queued for this call. */
async function ask(workflowID: string, nodePath: string, answer: unknown): Promise<void> {
  apiGetTypedOrError.mockResolvedValueOnce(answer);
  requestStepTranscript(workflowID, nodePath);
  // Three turns: the fetch's own await, the `finally`, and the caller's read.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

/** Ask, with the fetch queued to THROW rather than answer. */
async function reject(workflowID: string, nodePath: string): Promise<void> {
  apiGetTypedOrError.mockRejectedValueOnce(new Error("boom"));
  requestStepTranscript(workflowID, nodePath);
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  clearStepTranscripts();
  apiGetTypedOrError.mockReset();
  stepTranscriptVersion.value = 0;
});

describe("run-step-transcript: the URL", () => {
  it("keeps the separators raw and encodes each segment", async () => {
    await ask("wf_1", "wf_1/loop/iter-0/build", ok());
    expect(apiGetTypedOrError.mock.calls[0]?.[0]).toBe(
      "/api/runs/wf_1/steps/wf_1/loop/iter-0/build",
    );
  });

  // The one encoding rule this route has. A raw `#` would truncate the path at the
  // fragment; an encoded "/" would be refused by the server's canonical-path gate,
  // which compares the DECODED path against what its router would match.
  it("encodes a segment's own metacharacters without encoding the separators", async () => {
    await ask("wf_1", "wf_1/a b#0/build", ok());
    expect(apiGetTypedOrError.mock.calls[0]?.[0]).toBe("/api/runs/wf_1/steps/wf_1/a%20b%230/build");
  });
});

describe("run-step-transcript: when it asks", () => {
  it("records `loading` before the answer arrives", () => {
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("loading");
  });

  it("never refetches a settled answer", async () => {
    for (const state of ["ready", "gone"] as const) {
      clearStepTranscripts();
      apiGetTypedOrError.mockReset();
      await ask("wf_1", "wf_1/a", ok({ state }));
      expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
      requestStepTranscript("wf_1", "wf_1/a");
      requestStepTranscript("wf_1", "wf_1/a");
      expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
    }
  });

  // `unavailable` is the one verdict that means "could not be completed", which is
  // transient by definition — so it is the one a later ask retries. What bounds that
  // retry is the CALLER arming a read once per shown (node, state), not a backoff
  // here.
  it("retries an unavailable answer", async () => {
    await ask("wf_1", "wf_1/a", ok({ state: "unavailable" }));
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
    await ask("wf_1", "wf_1/a", ok({ state: "ready" }));
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
  });

  // A repaint during a fetch must not start a second one, or a page that repaints on
  // every store invalidation would issue a request per frame.
  it("issues one request while one is outstanding", () => {
    apiGetTypedOrError.mockResolvedValue(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
  });

  it("asks nothing for an empty run or an empty path", () => {
    requestStepTranscript("", "wf_1/a");
    requestStepTranscript("wf_1", "");
    expect(apiGetTypedOrError).not.toHaveBeenCalled();
  });

  it("keys reads per step, so one step's verdict is not another's", async () => {
    await ask("wf_1", "wf_1/a", ok({ state: "ready" }));
    await ask("wf_1", "wf_1/b", ok({ state: "gone" }));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
    expect(stepRead("wf_1", "wf_1/b")?.state).toBe("gone");
  });

  // A 5xx is the server failing to answer, which is transient by definition, so it
  // keeps the retryable verdict AND a later ask retries it.
  it("records a 5xx as unavailable and retries it", async () => {
    await ask("wf_1", "wf_1/a", fail(500));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // A 2xx the DECODER rejected arrives on the failure side carrying its real 2xx
  // status, so it must not be graded as the caller's mistake.
  it("records an undecodable 2xx as unavailable", async () => {
    await ask("wf_1", "wf_1/a", fail(200));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
  });

  // A 4xx is the SERVER refusing the address: 404 is `errStepUnknown` (this run has
  // no step at that path) and 400 is the first-segment assertion. Asking again fails
  // identically, so the verdict is settled and the reader is offered no retry — which
  // is the whole point of the state. The second half of each case is the one that
  // matters: `settled()` must swallow the next ask.
  it.each([404, 400])("records a %i as the settled unaddressable verdict", async (status) => {
    await ask("wf_1", "wf_1/a", fail(status));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unaddressable");
    requestStepTranscript("wf_1", "wf_1/a");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
  });

  // A throw never reached the server, so it grades as status 0 — transient, like the
  // 5xx above and unlike a 4xx. It must also be CAUGHT, because the caller `void`s
  // the fetch by design, so an escaping rejection is an unhandled one; this suite
  // fails the file on one, which is the other half of the assertion.
  it("records a thrown fetch as unavailable and retries it", async () => {
    await reject("wf_1", "wf_1/a");
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // A status-0 envelope is the same fact reported through the RESULT rather than a
  // throw (a dead network, an aborted request), so it takes the same verdict.
  it("records a status-0 transport failure as unavailable", async () => {
    await ask("wf_1", "wf_1/a", fail(0));
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("unavailable");
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  // Left at `loading` with no request behind it, that step could never be asked
  // about again for the life of the tab.
  it("does not wedge a step when the fetch rejects", async () => {
    await reject("wf_1", "wf_1/a");
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
    expect(stepRead("wf_1", "wf_1/a")?.state).toBe("ready");
  });
});

describe("run-step-transcript: the version signal", () => {
  it("bumps once per resolved fetch, not per request", async () => {
    await ask("wf_1", "wf_1/a", ok());
    expect(stepTranscriptVersion.peek()).toBe(1);
    // A declined request resolves nothing, so it bumps nothing.
    requestStepTranscript("wf_1", "wf_1/a");
    await Promise.resolve();
    expect(stepTranscriptVersion.peek()).toBe(1);
    await ask("wf_1", "wf_1/b", ok());
    expect(stepTranscriptVersion.peek()).toBe(2);
  });

  it("bumps on a failed read too, so the note can say so", async () => {
    await ask("wf_1", "wf_1/a", fail(500));
    expect(stepTranscriptVersion.peek()).toBe(1);
  });
});

// The answer's CONTENT goes into the run store through the same operations the live
// frames use, so the pane has one place to read from and a re-read cannot disagree
// with a stream. Each case uses its own run id, because a run's log is durable state
// that no verdict clear touches.
describe("run-step-transcript: adopting the answer into the run store", () => {
  it("commits the answer's entries under their own turn, in file order", async () => {
    const entries = [turnOpen("t1", "wf/a"), text("t1", 1, "one"), text("t1", 2, "two")];
    await ask("wf_adopt", "wf/a", ok({ entries }));
    expect(entryIDs("wf_adopt", "t1")).toEqual(["t1-open", "t1-1", "t1-2"]);
    expect(runTurnHoles("wf_adopt")).toEqual([]);
  });

  // Seated BEFORE the sealed entries, an open tail lands on a turn the answer has not
  // created yet, which the store records as a hole rather than a tail.
  it("seats an open tail on the turn the same answer created", async () => {
    await ask(
      "wf_tail",
      "wf/a",
      ok({ entries: [turnOpen("t1", "wf/a")], open_entries: [tail("t1", "still writing")] }),
    );
    const found = runTurns("wf_tail").find(([id]) => id === "t1");
    expect(found?.[1].openEntries.get("")?.text).toBe("still writing");
    expect(runTurnHoles("wf_tail")).toEqual([]);
  });

  // The repair is a HOLE FILL rather than a rewrite: the prefix the store already
  // holds is recognised as a redelivery and skipped, and the append resumes at the
  // gap. Without that, a second read would mark a hole at `seq` 0 and the step would
  // never settle.
  it("recognises the prefix it already holds and resumes at the gap", async () => {
    const first = [turnOpen("t1", "wf/a"), text("t1", 1, "one")];
    await ask("wf_again", "wf/a", ok({ entries: first }));
    apiGetTypedOrError.mockResolvedValueOnce(ok({ entries: [...first, text("t1", 2, "two")] }));
    rereadStepTranscript("wf_again", "wf/a");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(entryIDs("wf_again", "t1")).toEqual(["t1-open", "t1-1", "t1-2"]);
    expect(runTurnHoles("wf_again")).toEqual([]);
  });

  it("clears the hole its answer repairs", async () => {
    // An entry for a turn the store never opened is exactly the gap this GET exists
    // to close.
    appendRunEntry("wf_hole", text("t1", 3, "orphan"));
    expect(runTurnHoles("wf_hole")).toEqual(["t1"]);
    await ask("wf_hole", "wf/a", ok({ entries: [turnOpen("t1", "wf/a"), text("t1", 1, "one")] }));
    expect(runTurnHoles("wf_hole")).toEqual([]);
  });

  // A `ready` answer carrying nothing is its own fact — the step ran and wrote
  // nothing — and the pane's note is keyed on the verdict, so the verdict is what
  // this module records.
  it("records `ready` for an answer that carries no entries", async () => {
    await ask("wf_empty", "wf/a", ok({ state: "ready", entries: [] }));
    expect(stepRead("wf_empty", "wf/a")?.state).toBe("ready");
    expect(runTurns("wf_empty")).toEqual([]);
  });
});

describe("run-step-transcript: the re-read door", () => {
  // A hole and a lost `turn_close` are both repaired by the same whole-turn answer,
  // and a settled verdict would otherwise swallow the ask forever.
  it("re-asks a step whose settled verdict this client has found wanting", async () => {
    await ask("wf_1", "wf_1/a", ok({ state: "ready" }));
    requestStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    rereadStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });

  it("declines a re-read while a request is outstanding", () => {
    apiGetTypedOrError.mockResolvedValue(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    rereadStepTranscript("wf_1", "wf_1/a");
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(1);
  });
});

describe("run-step-transcript: the bound", () => {
  it("clearStepTranscripts empties the verdict cache", async () => {
    await ask("wf_1", "wf_1/a", ok());
    expect(stepRead("wf_1", "wf_1/a")).toBeDefined();
    clearStepTranscripts();
    expect(stepRead("wf_1", "wf_1/a")).toBeUndefined();
  });

  // The verdicts are this module's; the entries are the RUN's record, held under the
  // run's own eviction rules, and a page retarget says nothing about them.
  it("keeps the run's turns when the verdicts are cleared", async () => {
    await ask("wf_keep", "wf/a", ok({ entries: [turnOpen("t1", "wf/a")] }));
    clearStepTranscripts();
    expect(entryIDs("wf_keep", "t1")).toEqual(["t1-open"]);
  });

  // The in-flight set goes with it, or a read outstanding across a retarget would
  // block the new page from ever asking for that step.
  it("clears the in-flight set too", async () => {
    apiGetTypedOrError.mockResolvedValueOnce(ok());
    requestStepTranscript("wf_1", "wf_1/a");
    clearStepTranscripts();
    await ask("wf_1", "wf_1/a", ok());
    expect(apiGetTypedOrError).toHaveBeenCalledTimes(2);
  });
});
