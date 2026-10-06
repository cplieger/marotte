// What the reader is told when a retry lands: the outcome is decoded, reported through the
// channel it deserves, and followed by a refetch. A zero-node retry must not be silent.

import { vi, describe, it, expect, beforeEach } from "vitest";

const m = vi.hoisted(() => ({
  /** The next `POST /api/runs/{id}/retry` outcome, or an error to throw. */
  result: { current: undefined as unknown, err: undefined as unknown },
  invalidated: [] as string[],
  controlsInvalidated: [] as string[],
}));

// The action is NOT mocked: its decode / notify / onSuccess wiring is the subject, run over a
// canned response through `configureApi`'s fetch seam.

vi.mock("../toast.js", () => ({
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}));

vi.mock("../run-store.js", () => ({
  invalidateRun: vi.fn((id: string) => {
    m.invalidated.push(id);
  }),
  invalidateRunControls: vi.fn((id: string) => {
    m.controlsInvalidated.push(id);
  }),
}));

import { configure, configureApi } from "@cplieger/actions";
import { retryRun } from "./runs.js";
import { configureSubjectNotice } from "./subject.js";
import { error as toastError, success as toastSuccess } from "../toast.js";

/** Answer the next fetch with a 2xx body, or with a JSON error envelope. */
function stubFetch(): void {
  configureApi({
    fetchFn: () => {
      const err = m.result.err as { status: number; body: unknown } | undefined;
      if (err !== undefined) {
        return Promise.resolve(
          new Response(JSON.stringify(err.body), {
            status: err.status,
            headers: { "content-type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(m.result.current), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    },
  });
}

beforeEach(() => {
  m.result.current = undefined;
  m.result.err = undefined;
  m.invalidated.length = 0;
  m.controlsInvalidated.length = 0;
  // Re-armed per test: configureApi REPLACES the library's fetch and exports no reset.
  stubFetch();
  // The app's boot notifier, so a refusal's sentence is observable (the headless default drops it).
  configure({
    success: (msg) => {
      toastSuccess(msg);
    },
    error: (msg) => {
      toastError(msg);
    },
  });
  configureSubjectNotice(
    (_subject, msg, level) => {
      if (level === "success") {
        toastSuccess(msg);
      } else {
        toastError(msg);
      }
    },
    () => "",
  );
});

describe("retrying a run", () => {
  it("states how many steps a real retry reset", async () => {
    m.result.current = { status: "running", retried_node_ids: ["phase-c", "phase-d"] };
    const res = await retryRun.dispatch("wf_1");

    expect(res).not.toBeNull();
    expect(vi.mocked(toastSuccess)).toHaveBeenCalledWith("Retrying 2 steps");
    expect(vi.mocked(toastError)).not.toHaveBeenCalled();
  });

  // A zero-node retry is a no-op from the reader's seat; reporting it as a success is the defect.
  it("does not report a retry that reset NOTHING as a success", async () => {
    m.result.current = { status: "aborted", retried_node_ids: [] };
    await retryRun.dispatch("wf_1");

    expect(vi.mocked(toastSuccess)).not.toHaveBeenCalled();
    const said = vi.mocked(toastError).mock.calls[0]?.[0] ?? "";
    expect(said).toContain("Nothing to retry");
  });

  // A no-op retry produces no `run_progress` frame, so only this refetch moves the screen.
  it("refetches the run AND its affordance, even after a no-op", async () => {
    for (const nodes of [["phase-c"], []]) {
      m.invalidated.length = 0;
      m.controlsInvalidated.length = 0;
      m.result.current = { status: "running", retried_node_ids: nodes };
      await retryRun.dispatch("wf_1");
      expect(m.invalidated).toEqual(["wf_1"]);
      expect(m.controlsInvalidated).toEqual(["wf_1"]);
    }
  });

  // The refusal reads as the SERVER's sentence alone (`answerRunInput`'s rule).
  it("shows a refusal as the server's sentence alone", async () => {
    const sentence = "Workflow wf_1 is not registered. Load or create it first.";
    m.result.err = { status: 409, body: { error: sentence } };
    const res = await retryRun.dispatch("wf_1");

    expect(res).toBeNull();
    expect(m.invalidated).toEqual([]);
    // Exactly the sentence: a static `error` string PREFIXES the server's message.
    expect(vi.mocked(toastError)).toHaveBeenCalledWith(sentence);
  });

  // No server sentence on a transport failure: the fallback names the verb, not the placeholder.
  it("falls back to naming the verb when there is no server sentence", async () => {
    m.result.err = { status: 500, body: {} };
    await retryRun.dispatch("wf_1");

    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Could not retry the run");
  });

  // A non-outcome 2xx fails at the boundary rather than reporting "Retrying NaN steps".
  it("rejects a 2xx body that is not an outcome, and still says something", async () => {
    m.result.current = { ok: true };
    const res = await retryRun.dispatch("wf_1");

    expect(res).toBeNull();
    expect(vi.mocked(toastSuccess)).not.toHaveBeenCalled();
    expect(m.invalidated).toEqual([]);
    // A decode failure carries no server sentence, so the fallback must name the verb.
    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Could not retry the run");
  });
});
