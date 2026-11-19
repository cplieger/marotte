import { describe, expect, it } from "vitest";
import type { ReportedHookContext, TestCase, TestModule, Vitest } from "vitest/node";

import {
  RESULT_SLACK_MS,
  SILENCE_FLOOR_MS,
  StallWatchdog,
  stallWatchdog,
  testSilenceMs,
} from "./stall-watchdog.js";

const project = { name: "browser", config: { testTimeout: 15_000 } };

function module(path: string): TestModule {
  return { type: "module", id: path, relativeModuleId: path, project } as unknown as TestModule;
}

function testCase(
  mod: TestModule,
  name: string,
  options: { timeout?: number; retry?: number | { count?: number }; repeats?: number } = {},
): TestCase {
  return {
    type: "test",
    id: `${mod.id}#${name}`,
    fullName: name,
    module: mod,
    project,
    options: { timeout: 15_000, retry: 0, repeats: 0, ...options },
  } as unknown as TestCase;
}

function watchdog(): { dog: StallWatchdog; at: (ms: number) => void } {
  let now = 0;
  const dog = new StallWatchdog(() => now);
  return {
    dog,
    at: (ms) => {
      now = ms;
    },
  };
}

describe("StallWatchdog.check", () => {
  it("names a running file silent past the floor, with what it last did", () => {
    const { dog, at } = watchdog();
    const mod = module("rail-position-css.test.ts");
    dog.onTestModuleQueued(mod);
    at(1_000);
    dog.onHookStart({ name: "afterAll", entity: mod } as ReportedHookContext);

    expect(dog.check(1_000 + SILENCE_FLOOR_MS)).toBeUndefined();
    const stall = dog.check(1_001 + SILENCE_FLOOR_MS);
    expect(stall).toContain("browser rail-position-css.test.ts");
    expect(stall).toContain("since afterAll hook started");
  });

  it("restarts the silence on every event of the file", () => {
    const { dog, at } = watchdog();
    const mod = module("a.test.ts");
    dog.onTestModuleQueued(mod);
    at(SILENCE_FLOOR_MS);
    dog.onTestCaseReady(testCase(mod, "t"));

    expect(dog.check(2 * SILENCE_FLOOR_MS)).toBeUndefined();
  });

  it("gives a running test whose timeout outlasts the floor its own silence, then the floor again", () => {
    const { dog, at } = watchdog();
    const mod = module("run-view-scale.test.ts");
    const slow = testCase(mod, "slow", { timeout: 300_000 });
    dog.onTestModuleQueued(mod);
    dog.onTestCaseReady(slow);

    expect(dog.check(300_000 + RESULT_SLACK_MS)).toBeUndefined();
    expect(dog.check(300_001 + RESULT_SLACK_MS)).toContain('since test "slow" started');

    at(1_000_000);
    dog.onTestCaseResult(slow);
    expect(dog.check(1_000_001 + SILENCE_FLOOR_MS)).toContain('since test "slow" ended');
  });

  it("never reports a file that ended or a run that ended", () => {
    const { dog } = watchdog();
    const ended = module("ended.test.ts");
    const other = module("other.test.ts");
    dog.onTestModuleQueued(ended);
    dog.onTestModuleQueued(other);
    dog.onTestModuleEnd(ended);

    expect(dog.check(SILENCE_FLOOR_MS + 1)).not.toContain("ended.test.ts");
    dog.onTestRunEnd();
    expect(dog.check(SILENCE_FLOOR_MS + 1)).toBeUndefined();
  });

  it("ignores events of a file it never saw queued", () => {
    const { dog } = watchdog();
    dog.onTestModuleStart(module("unqueued.test.ts"));

    expect(dog.check(SILENCE_FLOOR_MS + 1)).toBeUndefined();
  });
});

describe("testSilenceMs", () => {
  const mod = module("a.test.ts");

  it("lets every attempt run to its timeout, plus the slack", () => {
    expect(testSilenceMs(testCase(mod, "once", { timeout: 10_000 }))).toBe(
      10_000 + RESULT_SLACK_MS,
    );
    expect(testSilenceMs(testCase(mod, "retried", { timeout: 10_000, retry: 1 }))).toBe(
      20_000 + RESULT_SLACK_MS,
    );
    expect(testSilenceMs(testCase(mod, "counted", { timeout: 10_000, retry: { count: 2 } }))).toBe(
      30_000 + RESULT_SLACK_MS,
    );
    expect(
      testSilenceMs(testCase(mod, "repeated", { timeout: 10_000, retry: 1, repeats: 1 })),
    ).toBe(40_000 + RESULT_SLACK_MS);
  });
});

describe("stallWatchdog plugin", () => {
  type Ctx = { vitest: Vitest };
  const configure = (config: object): unknown[] => {
    const reporters: unknown[] = ["default"];
    const ctx = { vitest: { config: { watch: false, inspector: {}, reporters, ...config } } };
    const hook = stallWatchdog().configureVitest as (c: Ctx) => void;
    hook(ctx as unknown as Ctx);
    hook(ctx as unknown as Ctx);
    return reporters;
  };

  it("adds one watchdog to a run however many projects call it", () => {
    const reporters = configure({});
    expect(reporters.filter((r) => r instanceof StallWatchdog)).toHaveLength(1);
  });

  it("stays out of watch mode and an inspector session", () => {
    expect(configure({ watch: true })).toEqual(["default"]);
    expect(configure({ inspector: { enabled: true } })).toEqual(["default"]);
  });
});
