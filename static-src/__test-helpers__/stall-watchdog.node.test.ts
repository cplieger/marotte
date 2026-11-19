import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReportedHookContext, TestCase, TestModule, Vitest } from "vitest/node";

import {
  CHECK_EVERY_MS,
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

const started: StallWatchdog[] = [];

afterEach(() => {
  for (const dog of started.splice(0)) dog.onTestRunEnd();
  vi.useRealTimers();
});

function watchdog(snapshot = (): string => ""): {
  dog: StallWatchdog;
  at: (ms: number) => void;
} {
  let now = 0;
  const dog = new StallWatchdog({ now: () => now, snapshot });
  started.push(dog);
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
    vi.useFakeTimers();
    const { dog, at } = watchdog();
    const mod = module("run-view-scale.test.ts");
    const slow = testCase(mod, "slow", { timeout: 300_000 });
    dog.onTestRunStart();
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

  it("names an open run that sends nothing for the floor while no file runs", () => {
    vi.useFakeTimers();
    const { dog, at } = watchdog();
    const mod = module("frame-dispatch.test.ts");
    dog.onTestRunStart();
    dog.onTestModuleQueued(mod);
    at(1_000);
    dog.onTestModuleEnd(mod);

    expect(dog.check(1_000 + SILENCE_FLOOR_MS)).toBeUndefined();
    expect(dog.check(1_001 + SILENCE_FLOOR_MS)).toContain(
      "no file running: the run sent no event for 150s since browser frame-dispatch.test.ts it ended",
    );
  });

  it("never reports a closed run", () => {
    vi.useFakeTimers();
    const { dog } = watchdog();
    dog.onTestRunStart();
    dog.onTestRunEnd();

    expect(dog.check(2 * SILENCE_FLOOR_MS)).toBeUndefined();
  });

  it("counts a file it never saw queued toward the run's silence only", () => {
    vi.useFakeTimers();
    const { dog, at } = watchdog();
    dog.onTestRunStart();
    at(100_000);
    dog.onTestModuleStart(module("unqueued.test.ts"));

    expect(dog.check(100_000 + SILENCE_FLOOR_MS)).toBeUndefined();
    const stall = dog.check(100_001 + SILENCE_FLOOR_MS);
    expect(stall).toContain("no file running");
    expect(stall).not.toContain("unqueued.test.ts: silent");
  });
});

describe("StallWatchdog run", () => {
  it("exits 1 after writing the stall and the snapshot to stderr", () => {
    vi.useFakeTimers();
    const written: string[] = [];
    vi.spyOn(process.stderr, "write").mockImplementation(((chunk: string, done?: () => void) => {
      written.push(chunk);
      done?.();
      return true;
    }) as typeof process.stderr.write);
    const exit = vi
      .spyOn(process, "exit")
      .mockImplementation((() => undefined) as typeof process.exit);
    const exitCode = process.exitCode;
    try {
      const { dog, at } = watchdog(() => "PROCESS SNAPSHOT");
      dog.onTestRunStart();
      dog.onTestModuleQueued(module("rail-position-css.test.ts"));
      at(SILENCE_FLOOR_MS + 1);
      vi.advanceTimersByTime(CHECK_EVERY_MS);

      expect(process.exitCode).toBe(1);
      expect(exit).toHaveBeenCalledExactlyOnceWith(1);
      expect(written.join("")).toContain("browser rail-position-css.test.ts: silent");
      expect(written.join("")).toContain("PROCESS SNAPSHOT");
    } finally {
      process.exitCode = exitCode;
    }
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
  /** A reporter name as vitest's resolved config holds it. */
  const named = ["default", {}];
  const configure = (config: object, reporters: unknown[] = [named]): unknown[] => {
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

  it("adds none when another project's copy of the module already added one", () => {
    const otherCopy = { [Symbol.for("marotte.stall-watchdog")]: true };
    expect(configure({}, [named, otherCopy])).toEqual([named, otherCopy]);
  });

  it("stays out of watch mode and an inspector session", () => {
    expect(configure({ watch: true })).toEqual([named]);
    expect(configure({ inspector: { enabled: true } })).toEqual([named]);
  });
});
