import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { clearRunStepSteers, messageRunStep, removeRunStepSteer } from "./runs.js";
import { headerValue, resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import stepRoutesRaw from "../../internal/agent/testdata/step_routes.json?raw";
import { nodePathKey } from "../run-node-key.js";

/** One row of the address oracle `internal/agent/run_http_steps_test.go` drives the server's routes
 *  with. */
interface StepRouteCase {
  id: string;
  workflow_id: string;
  node_path: string[];
  step_url: string;
}

const stepRoutes = JSON.parse(stepRoutesRaw) as StepRouteCase[];

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

/** The URL and the decoded body of the one request a dispatch made. */
function sent(): { url: string; body: unknown } {
  const [input, init] = mockFetch.mock.calls[0] as [RequestInfo | URL, RequestInit | undefined];
  const url = input instanceof Request ? input.url : String(input);
  const raw = init?.body;
  return {
    url: new URL(url, location.origin).pathname,
    body: typeof raw === "string" ? (JSON.parse(raw) as unknown) : undefined,
  };
}

describe("runs.message_step", () => {
  it("sends the message id as its Idempotency-Key", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ verb: "steer" }), { status: 200 }));
    await messageRunStep.dispatch({
      workflowID: "wf_1",
      nodePath: nodePathKey(["root", "review"]),
      text: "check the logs",
      message_id: "m-7f3a",
    }).outcome;
    expect(headerValue(mockFetch.mock.calls[0]?.[1] as RequestInit, "idempotency-key")).toBe(
      "m-7f3a",
    );
  });
});

describe.each(stepRoutes)("the step actions for $id", (c) => {
  const at = { workflowID: c.workflow_id, nodePath: nodePathKey(c.node_path) };

  it("message the step at its own address", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ verb: "steer" }), { status: 200 }));
    await messageRunStep.dispatch({ ...at, text: "hi", message_id: "m-1" }).outcome;
    expect(sent()).toEqual({
      url: `${c.step_url}/message`,
      body: { text: "hi", message_id: "m-1" },
    });
  });

  it("delete one of the step's rows at its own address", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ deleted: "steer-m-1" }), { status: 200 }),
    );
    await removeRunStepSteer.dispatch({ ...at, steer_id: "steer-m-1" }).outcome;
    expect(sent()).toEqual({ url: `${c.step_url}/steer-remove`, body: { steer_id: "steer-m-1" } });
  });

  it("clear the step's rows at its own address", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    await clearRunStepSteers.dispatch(at).outcome;
    expect(sent()).toEqual({ url: `${c.step_url}/steer-clear`, body: undefined });
  });
});
