import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { loadSchedules, saveSchedule } from "./schedules.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

const daily = { freq: "daily", hour: 2, minute: 0 } as const;

function answer(body: unknown): void {
  mockFetch.mockResolvedValue(new Response(JSON.stringify(body), { status: 200 }));
}

function calls(): { url: unknown; method: unknown; body: unknown }[] {
  return mockFetch.mock.calls.map((call) => {
    const [url, init] = call as [string, RequestInit];
    return {
      url,
      method: init.method,
      body: typeof init.body === "string" ? (JSON.parse(init.body) as unknown) : undefined,
    };
  });
}

describe("saveSchedule", () => {
  const view = {
    id: "r1",
    source: "bundled://nightly",
    enabled: true,
    when: { clock: daily },
    held: 0,
  };

  it("saveSchedule sends PUT by id and POST without", async () => {
    answer(view);
    await saveSchedule.dispatch({
      id: "r1",
      source: "bundled://nightly",
      spec: daily,
      enabled: true,
      model: "opus",
    });
    expect(calls()).toEqual([
      {
        url: "/api/launch-rules/r1",
        method: "PUT",
        body: { source: "bundled://nightly", enabled: true, model: "opus", when: { clock: daily } },
      },
    ]);

    mockFetch.mockClear();
    answer(view);
    await saveSchedule.dispatch({ source: "bundled://nightly", spec: daily, enabled: false });
    expect(calls()).toEqual([
      {
        url: "/api/launch-rules",
        method: "POST",
        body: { source: "bundled://nightly", enabled: false, when: { clock: daily } },
      },
    ]);
  });

  it("sends one POST for two saves of a recipe made before the first answers", async () => {
    answer(view);
    const args = { source: "bundled://nightly", spec: daily, enabled: true };
    await Promise.all([saveSchedule.dispatch(args), saveSchedule.dispatch(args)]);
    expect(calls()).toEqual([
      {
        url: "/api/launch-rules",
        method: "POST",
        body: { source: "bundled://nightly", enabled: true, when: { clock: daily } },
      },
    ]);
  });

  it("joins neither another recipe's create nor a replace to a create in flight", async () => {
    mockFetch.mockImplementation(async () => new Response(JSON.stringify(view), { status: 200 }));
    await Promise.all([
      saveSchedule.dispatch({ source: "bundled://nightly", spec: daily, enabled: true }),
      saveSchedule.dispatch({ source: "bundled://weekly", spec: daily, enabled: true }),
      saveSchedule.dispatch({ id: "r1", source: "bundled://nightly", spec: daily, enabled: true }),
    ]);
    expect(calls().map((c) => [c.method, c.url])).toEqual([
      ["POST", "/api/launch-rules"],
      ["POST", "/api/launch-rules"],
      ["PUT", "/api/launch-rules/r1"],
    ]);
  });

  it("sends a clock rule's fixed inputs, and no inputs key without them", async () => {
    answer(view);
    await saveSchedule.dispatch({
      id: "r1",
      source: "bundled://nightly",
      spec: daily,
      enabled: true,
      inputs: { tag: "v1" },
    });
    await saveSchedule.dispatch({ source: "bundled://nightly", spec: daily, enabled: true });
    const [kept, none] = calls();
    expect(kept?.body).toMatchObject({ inputs: { tag: "v1" } });
    expect(none?.body).not.toHaveProperty("inputs");
  });

  it("answers the saved rule as the form reads it", async () => {
    answer({ ...view, id: "minted", inputs: { tag: "v1" }, next_run_at: "2026-11-06T02:00:00Z" });
    const got = await saveSchedule.dispatch({
      source: "bundled://nightly",
      spec: daily,
      enabled: true,
    });
    expect(got).toEqual({
      id: "minted",
      source: "bundled://nightly",
      enabled: true,
      spec: daily,
      inputs: { tag: "v1" },
      next_run_at: "2026-11-06T02:00:00Z",
    });
  });

  it("encodes the id into the path", async () => {
    answer(view);
    await saveSchedule.dispatch({
      id: "a/b",
      source: "bundled://nightly",
      spec: daily,
      enabled: true,
    });
    expect(calls()[0]?.url).toBe("/api/launch-rules/a%2Fb");
  });
});

describe("loadSchedules", () => {
  it("reads the clock rules of /api/launch-rules as schedules", async () => {
    answer({
      rules: [
        {
          id: "c1",
          source: "bundled://nightly",
          name: "nightly",
          enabled: true,
          model: "opus",
          inputs: { tag: "v1" },
          when: { clock: daily },
          next_run_at: "2026-11-06T02:00:00Z",
          status: {
            anchor: "2026-11-01T00:00:00Z",
            last_run_at: "2026-11-05T02:00:00Z",
            last_status: "completed",
            last_reason: "done",
            waiting_for: "2026-11-05T02:00:00Z",
          },
          held: 0,
        },
        {
          id: "e1",
          source: "bundled://nightly",
          enabled: true,
          when: { event: { connection: "github:github.com", repo: "o/r", kind: "pr_opened" } },
          held: 2,
        },
        {
          id: "c2",
          source: "bundled://weekly",
          enabled: false,
          when: { clock: daily },
          error: "could not read it",
          held: 0,
        },
      ],
    });
    const got = await loadSchedules.dispatch(undefined);
    expect(calls()[0]).toMatchObject({ url: "/api/launch-rules", method: "GET" });
    expect(got).toEqual({
      schedules: [
        {
          id: "c1",
          source: "bundled://nightly",
          name: "nightly",
          enabled: true,
          spec: daily,
          model: "opus",
          inputs: { tag: "v1" },
          next_run_at: "2026-11-06T02:00:00Z",
          last_run_at: "2026-11-05T02:00:00Z",
          last_status: "completed",
          last_reason: "done",
          waiting_for: "2026-11-05T02:00:00Z",
        },
        {
          id: "c2",
          source: "bundled://weekly",
          enabled: false,
          spec: daily,
          last_status: "failed",
          last_reason: "could not read it",
        },
      ],
    });
  });
});
