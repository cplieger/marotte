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

/** The longest a running file, or a run with no file running, may send no event. Above the suite's
 *  longest hook timeout (120 s, in `frame-dispatch.test.ts`) and a cold module import: a hook given a longer timeout trips the watchdog until this is raised. */
export const SILENCE_FLOOR_MS = 150_000;

/** Time for a timed-out test's result to reach Node: its afterEach hooks and the runner's batched update. */
export const RESULT_SLACK_MS = 30_000;

export const CHECK_EVERY_MS = 5_000;

/** Marks an instance across module copies: vitest evaluates the config, and so this module, once per project. */
const BRAND = Symbol.for("marotte.stall-watchdog");

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

/** Fails a `--run` whose running file sends no event for longer than it may stay silent, or that sends
 *  none at all for the floor while no file runs, naming what it last did, instead of leaving the run to
 *  the CI job's timeout. */
export class StallWatchdog implements Reporter {
  readonly [BRAND] = true;
  private readonly running = new Map<string, Running>();
  private timer: ReturnType<typeof setInterval> | undefined;
  private open = false;
  private last = 0;
  private doing = "";
  private readonly now: () => number;
  private readonly snapshot: () => string;

  constructor({
    now = Date.now,
    snapshot = processSnapshot,
  }: { now?: () => number; snapshot?: () => string } = {}) {
    this.now = now;
    this.snapshot = snapshot;
  }

  onTestRunStart(): void {
    this.open = true;
    this.last = this.now();
    this.doing = "the run started";
    this.timer ??= setInterval(() => {
      const stall = this.check(this.now());
      if (stall !== undefined) {
        this.stop();
        fail(`${stall}\n${this.snapshot()}`);
      }
    }, CHECK_EVERY_MS).unref();
  }

  onTestRunEnd(): void {
    this.stop();
  }

  onTestModuleQueued(module: TestModule): void {
    this.running.set(module.id, { module, last: 0, doing: "", tests: new Map() });
    this.touch(module, "its import started");
  }

  onTestModuleCollected(module: TestModule): void {
    this.touch(module, "its import ended");
  }

  onTestModuleStart(module: TestModule): void {
    this.touch(module, "its tests started");
  }

  onTestModuleEnd(module: TestModule): void {
    this.touch(module, "it ended");
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

  /** What stalled at `now`, or undefined while every running file, and an open run with none, is inside its silence. */
  check(now: number): string | undefined {
    const secs = (since: number): string => String(Math.round((now - since) / 1000));
    const lines = [...this.running.values()]
      .filter((r) => now - r.last > Math.max(SILENCE_FLOOR_MS, ...r.tests.values()))
      .map((r) => `  ${label(r.module)}: silent ${secs(r.last)}s since ${r.doing}`);
    if (this.open && this.running.size === 0 && now - this.last > SILENCE_FLOOR_MS) {
      lines.push(
        `  no file running: the run sent no event for ${secs(this.last)}s since ${this.doing}`,
      );
    }
    if (lines.length === 0) {
      return undefined;
    }
    return [
      "Stall watchdog: the run sent no event for longer than its tests and hooks may take,",
      "so a page or worker stopped answering. Failing the run instead of waiting for the job timeout.",
      ...lines,
    ].join("\n");
  }

  private stop(): void {
    clearInterval(this.timer);
    this.timer = undefined;
    this.open = false;
    this.running.clear();
  }

  private touch(module: TestModule, doing: string): Running | undefined {
    this.last = this.now();
    this.doing = `${label(module)} ${doing}`;
    const r = this.running.get(module.id);
    if (r !== undefined) {
      r.last = this.last;
      r.doing = doing;
    }
    return r;
  }
}

function label(module: TestModule): string {
  return `${module.project.name} ${module.relativeModuleId}`;
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
        const [pid = "0", ppid = "0", , , , ...args] = row.trim().split(/\s+/);
        return { row, pid: Number(pid), ppid: Number(ppid), args: args.join(" ") };
      });
      const ours = new Set([process.pid]);
      for (let size = 0; size !== ours.size;) {
        size = ours.size;
        for (const p of procs) if (ours.has(p.ppid)) ours.add(p.pid);
      }
      const isThisPs = (p: (typeof procs)[number]): boolean =>
        p.ppid === process.pid && p.args.startsWith("ps -eo");
      const tree = procs.filter((p) => p.pid !== process.pid && ours.has(p.pid) && !isThisPs(p));
      return [header, ...tree.map((p) => p.row.slice(0, 200))].join("\n");
    } catch (e) {
      return `ps failed: ${String(e)}`;
    }
  };
  const first = take();
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5_000);
  return `${first}\n-- 5 s later --\n${take()}`;
}

/** Exits 1 once the report has reached stderr: a pipe write is asynchronous on POSIX, so exiting at once
 *  can cut it off (https://nodejs.org/api/process.html#a-note-on-process-io). */
function fail(report: string): void {
  process.exitCode = 1;
  process.stderr.write(`\n${report}\n`, () => process.exit(1));
}

/** Installs one {@link StallWatchdog} in a `--run` with no inspector, whichever `--reporter` the command
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
        config.reporters.some((r) => BRAND in r)
      ) {
        return;
      }
      config.reporters.push(new StallWatchdog());
    },
  };
}
