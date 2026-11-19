// vitest bounds a browser file's tests and hooks with timers inside its page, so a Chromium page that
// stops running tasks (seen during `page.viewport()`) leaves the file, and the run, waiting forever:
// once the page acks a file, nothing on the Node side bounds its answer.
import { execFileSync } from "node:child_process";
import type {
  ReportedHookContext,
  Reporter,
  TestCase,
  TestModule,
  TestSuite,
  Vite,
} from "vitest/node";

/** The longest a running file may send no event. Above the suite's longest hook timeout (120 s, in
 *  `frame-dispatch.test.ts`) and a cold module import: a hook given a longer timeout trips the watchdog until this is raised. */
export const SILENCE_FLOOR_MS = 150_000;

/** Time for a timed-out test's result to reach Node: its afterEach hooks and the runner's batched update. */
export const RESULT_SLACK_MS = 30_000;

const CHECK_EVERY_MS = 5_000;

interface Running {
  readonly module: TestModule;
  last: number;
  doing: string;
  /** The silence each running test may keep, by test id. */
  readonly tests: Map<string, number>;
}

/** The silence a running test may keep: every attempt running to its timeout, plus the slack. */
export function testSilenceMs(test: TestCase): number {
  const { timeout, retry, repeats } = test.options;
  const retries = typeof retry === "number" ? retry : (retry?.count ?? 0);
  const attempts = (1 + retries) * (1 + (repeats ?? 0));
  return (timeout ?? test.project.config.testTimeout) * attempts + RESULT_SLACK_MS;
}

/** Fails a `--run` whose running file sends no event for longer than it may stay silent, naming the
 *  file and the last thing it did, instead of leaving the run to the CI job's timeout. */
export class StallWatchdog implements Reporter {
  private readonly running = new Map<string, Running>();
  private timer: ReturnType<typeof setInterval> | undefined;
  private readonly now: () => number;

  constructor(now: () => number = Date.now) {
    this.now = now;
  }

  onTestRunStart(): void {
    this.timer ??= setInterval(() => {
      const stall = this.check(this.now());
      if (stall !== undefined) {
        fail(stall);
      }
    }, CHECK_EVERY_MS).unref();
  }

  onTestRunEnd(): void {
    clearInterval(this.timer);
    this.timer = undefined;
    this.running.clear();
  }

  onTestModuleQueued(module: TestModule): void {
    this.running.set(module.id, {
      module,
      last: this.now(),
      doing: "its import started",
      tests: new Map(),
    });
  }

  onTestModuleCollected(module: TestModule): void {
    this.touch(module, "its import ended");
  }

  onTestModuleStart(module: TestModule): void {
    this.touch(module, "its tests started");
  }

  onTestModuleEnd(module: TestModule): void {
    this.running.delete(module.id);
  }

  onTestSuiteReady(suite: TestSuite): void {
    this.touch(suite.module, `suite "${suite.fullName}" started`);
  }

  onTestSuiteResult(suite: TestSuite): void {
    this.touch(suite.module, `suite "${suite.fullName}" ended`);
  }

  onHookStart(hook: ReportedHookContext): void {
    this.touch(moduleOf(hook), `${hook.name} hook started`);
  }

  onHookEnd(hook: ReportedHookContext): void {
    this.touch(moduleOf(hook), `${hook.name} hook ended`);
  }

  onTestCaseReady(test: TestCase): void {
    this.touch(test.module, `test "${test.fullName}" started`)?.tests.set(
      test.id,
      testSilenceMs(test),
    );
  }

  onTestCaseResult(test: TestCase): void {
    this.touch(test.module, `test "${test.fullName}" ended`)?.tests.delete(test.id);
  }

  /** What stalled at `now`, or undefined while every running file is inside its silence. */
  check(now: number): string | undefined {
    const stalled = [...this.running.values()].filter(
      (r) => now - r.last > Math.max(SILENCE_FLOOR_MS, ...r.tests.values()),
    );
    if (stalled.length === 0) {
      return undefined;
    }
    const lines = stalled.map(
      (r) =>
        `  ${r.module.project.name} ${r.module.relativeModuleId}: silent ${String(Math.round((now - r.last) / 1000))}s since ${r.doing}`,
    );
    return [
      "Stall watchdog: a running test file sent no event for longer than its tests and hooks may take,",
      "so its page or worker stopped answering. Failing the run instead of waiting for the job timeout.",
      ...lines,
    ].join("\n");
  }

  private touch(module: TestModule, doing: string): Running | undefined {
    const r = this.running.get(module.id);
    if (r !== undefined) {
      r.last = this.now();
      r.doing = doing;
    }
    return r;
  }
}

function moduleOf(hook: ReportedHookContext): TestModule {
  return hook.entity.type === "module" ? hook.entity : hook.entity.module;
}

/** This process's descendants (the browsers) twice, 5 s apart: a renderer whose TIME grows between the
 *  two is busy, one whose TIME holds is blocked. */
function processSnapshot(): string {
  const take = (): string => {
    try {
      const [header = "", ...rows] = execFileSync("ps", ["-eo", "pid,ppid,stat,time,etime,args"], {
        encoding: "utf8",
      }).split("\n");
      const procs = rows.map((row) => {
        const [pid = 0, ppid = 0] = row.trim().split(/\s+/, 2).map(Number);
        return { row, pid, ppid };
      });
      const ours = new Set([process.pid]);
      for (let size = 0; size !== ours.size;) {
        size = ours.size;
        for (const p of procs) if (ours.has(p.ppid)) ours.add(p.pid);
      }
      const tree = procs.filter((p) => p.pid !== process.pid && ours.has(p.pid));
      return [header, ...tree.map((p) => p.row.slice(0, 200))].join("\n");
    } catch (e) {
      return `ps failed: ${String(e)}`;
    }
  };
  const first = take();
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5_000);
  return `${first}\n-- 5 s later --\n${take()}`;
}

function fail(stall: string): never {
  process.stderr.write(`\n${stall}\n${processSnapshot()}\n`);
  process.exit(1);
}

/** Installs {@link StallWatchdog} in a `--run` with no inspector, whichever `--reporter` the command
 *  line names (a CLI reporter list replaces the config's). */
export function stallWatchdog(): Vite.Plugin {
  return {
    name: "marotte:stall-watchdog",
    configureVitest({ vitest }) {
      const { config } = vitest;
      // The hook runs once per project.
      if (
        config.watch ||
        config.inspector.enabled === true ||
        config.reporters.some((r) => r instanceof StallWatchdog)
      ) {
        return;
      }
      config.reporters.push(new StallWatchdog());
    },
  };
}
