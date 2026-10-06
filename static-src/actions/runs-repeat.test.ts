// The actions run for real over `configureApi`'s fetch seam, so the path, the body
// and the refusal sentence asserted are the ones production sends.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type * as Toast from "../toast.js";
import type * as RunStore from "../run-store.js";

const m = vi.hoisted(() => ({
  err: undefined as { status: number; body: unknown } | undefined,
  requests: [] as { url: string; method: string; body: unknown }[],
  controlsInvalidated: [] as string[],
}));

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  success: vi.fn(),
  error: vi.fn(),
  info: vi.fn(),
}));

vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  invalidateRun: vi.fn(),
  invalidateRunControls: vi.fn((id: string) => {
    m.controlsInvalidated.push(id);
  }),
}));

import { configure, configureApi } from "@cplieger/actions";
import { extendRunRepeat, finishRunRepeat, launchRun } from "./runs.js";
import { error as toastError } from "../toast.js";

beforeEach(() => {
  m.err = undefined;
  m.requests.length = 0;
  m.controlsInvalidated.length = 0;
  configureApi({
    fetchFn: (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      const raw = typeof init?.body === "string" ? init.body : "";
      m.requests.push({
        url: new URL(url, "http://x").pathname,
        method: init?.method ?? "GET",
        body: raw === "" ? undefined : (JSON.parse(raw) as unknown),
      });
      const status = m.err?.status ?? 200;
      const body = m.err?.body ?? { ok: true };
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status,
          headers: { "content-type": "application/json" },
        }),
      );
    },
  });
  configure({
    success: vi.fn(),
    error: (msg) => {
      toastError(msg);
    },
  });
});

describe("the repeat verbs", () => {
  it("POSTs the paused node and the count to the run's extend route", async () => {
    await extendRunRepeat.dispatch({ workflowID: "wf 1", nodeID: "loop-1", iterations: 5 });
    expect(m.requests).toEqual([
      {
        url: "/api/runs/wf%201/extend",
        method: "POST",
        body: { node_id: "loop-1", iterations: 5 },
      },
    ]);
    expect(m.controlsInvalidated).toEqual(["wf 1"]);
  });

  it("POSTs the paused node to the run's finish-loop route", async () => {
    await finishRunRepeat.dispatch({ workflowID: "wf_1", nodeID: "loop-1" });
    expect(m.requests).toEqual([
      { url: "/api/runs/wf_1/finish-loop", method: "POST", body: { node_id: "loop-1" } },
    ]);
    expect(m.controlsInvalidated).toEqual(["wf_1"]);
  });

  it("shows an extend refusal as the server's sentence alone", async () => {
    m.err = { status: 409, body: { error: "The loop is not paused." } };
    await extendRunRepeat.dispatch({ workflowID: "wf_1", nodeID: "loop-1", iterations: 1 });
    expect(vi.mocked(toastError)).toHaveBeenCalledWith("The loop is not paused.");
    expect(m.controlsInvalidated).toEqual([]);
  });

  it("shows a finish refusal as the server's sentence alone", async () => {
    m.err = { status: 409, body: { error: "Node loop-1 is not a repeat." } };
    await finishRunRepeat.dispatch({ workflowID: "wf_1", nodeID: "loop-1" });
    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Node loop-1 is not a repeat.");
  });
});

describe("launching a run", () => {
  it("shows a launch refusal as the server's sentence alone", async () => {
    m.err = { status: 502, body: { error: "Agent reviewer is not registered." } };
    await launchRun.dispatch({ source: "agent://reviewer", inputs: { prompt: "go" } });
    expect(vi.mocked(toastError)).toHaveBeenCalledWith("Agent reviewer is not registered.");
  });
});
