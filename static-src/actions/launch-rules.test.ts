import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Toast from "../toast.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { loadLaunchRules, saveLaunchRule } from "./launch-rules.js";
import { scheduleViewOf } from "../launch-rule-types.js";
import type { LaunchRuleView } from "../launch-rule-types.js";

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

describe("saveLaunchRule", () => {
  const view = {
    id: "r1",
    source: "bundled://nightly",
    enabled: true,
    when: { clock: daily },
    held: 0,
  };

  it("sends confirm_drop on an edit only", async () => {
    answer(view);
    await saveLaunchRule.dispatch({
      id: "r1",
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: true,
      confirm_drop: 3,
    });
    answer(view);
    await saveLaunchRule.dispatch({
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: true,
      confirm_drop: 3,
    });
    expect(calls().map((c) => c.body)).toEqual([
      { source: "bundled://nightly", enabled: true, when: { clock: daily }, confirm_drop: 3 },
      { source: "bundled://nightly", enabled: true, when: { clock: daily } },
    ]);
  });

  it("saveLaunchRule sends PUT by id and POST without", async () => {
    answer(view);
    await saveLaunchRule.dispatch({
      id: "r1",
      source: "bundled://nightly",
      when: { clock: daily },
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
    await saveLaunchRule.dispatch({
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: false,
    });
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
    const args = { source: "bundled://nightly", when: { clock: daily }, enabled: true };
    await Promise.all([saveLaunchRule.dispatch(args), saveLaunchRule.dispatch(args)]);
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
      saveLaunchRule.dispatch({
        source: "bundled://nightly",
        when: { clock: daily },
        enabled: true,
      }),
      saveLaunchRule.dispatch({
        source: "bundled://weekly",
        when: { clock: daily },
        enabled: true,
      }),
      saveLaunchRule.dispatch({
        id: "r1",
        source: "bundled://nightly",
        when: { clock: daily },
        enabled: true,
      }),
    ]);
    expect(calls().map((c) => [c.method, c.url])).toEqual([
      ["POST", "/api/launch-rules"],
      ["POST", "/api/launch-rules"],
      ["PUT", "/api/launch-rules/r1"],
    ]);
  });

  it("sends a rule's fixed inputs, and no inputs key without them", async () => {
    answer(view);
    await saveLaunchRule.dispatch({
      id: "r1",
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: true,
      inputs: { tag: "v1" },
    });
    await saveLaunchRule.dispatch({
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: true,
    });
    const [kept, none] = calls();
    expect(kept?.body).toMatchObject({ inputs: { tag: "v1" } });
    expect(none?.body).not.toHaveProperty("inputs");
  });

  it("encodes the id into the path", async () => {
    answer(view);
    await saveLaunchRule.dispatch({
      id: "a/b",
      source: "bundled://nightly",
      when: { clock: daily },
      enabled: true,
    });
    expect(calls()[0]?.url).toBe("/api/launch-rules/a%2Fb");
  });
});

describe("loadLaunchRules", () => {
  it("reads every rule of /api/launch-rules, and the clock ones as the schedule form does", async () => {
    const rules: LaunchRuleView[] = [
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
        when: {
          event: { connection: "github:github.com", repo: "o/r", kind: "pr_opened", filter: {} },
        },
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
    ];
    answer({ rules });
    const got = await loadLaunchRules.dispatch(undefined);
    expect(calls()[0]).toMatchObject({ url: "/api/launch-rules", method: "GET" });
    expect(got).toEqual({ rules });
    expect(rules.map(scheduleViewOf)).toEqual([
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
      null,
      {
        id: "c2",
        source: "bundled://weekly",
        enabled: false,
        spec: daily,
        last_status: "failed",
        last_reason: "could not read it",
      },
    ]);
  });
});
