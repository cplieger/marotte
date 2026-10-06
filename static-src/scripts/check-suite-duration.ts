// Guards Chromium's `--disable-frame-rate-limit` (`vitest.config.ts`), whose loss is silent: the
// suite still PASSES, about four times slower. MAX_MS sits near the geometric midpoint of a run
// with and without it. CI-only (a dev box shares cores); it always prints the number.

import { readFileSync } from "node:fs";

const MAX_MS = 600_000;

/** Per-file cost that reads as frame-bound rather than work-bound. */
const SLOW_FILE_MS = 10_000;

const NAMED_ON_FAILURE = 10;

const seconds = (ms: number): string => `${(ms / 1000).toFixed(1)}s`;

function fileSpan(entry: unknown): { name: string; start: number; end: number } | null {
  if (typeof entry !== "object" || entry === null) {
    return null;
  }
  const rec = entry as Record<string, unknown>;
  const start = Number(rec["startTime"]);
  const end = Number(rec["endTime"]);
  if (!Number.isFinite(start) || !Number.isFinite(end)) {
    return null;
  }
  return { name: String(rec["name"]).replace(/^.*static-src\//, ""), start, end };
}

/** Pass, so a report that moved is not read as a performance regression. */
function abstain(why: string): never {
  console.error(`suite duration: ${why}`);
  process.exit(0);
}

const reportPath = process.argv[2];
if (reportPath === undefined) {
  console.error("usage: check-suite-duration.ts <vitest-json-report>");
  process.exit(2);
}

let parsed: unknown;
try {
  parsed = JSON.parse(readFileSync(reportPath, "utf8"));
} catch (err) {
  abstain(`no usable report at ${reportPath} (${String(err)})`);
}

const results = (parsed as { testResults?: unknown }).testResults;
if (!Array.isArray(results)) {
  abstain("report carries no testResults array");
}

const perFile = new Map<string, number>();
let first = Infinity;
let last = -Infinity;
for (const entry of results as unknown[]) {
  const span = fileSpan(entry);
  if (span === null) {
    continue;
  }
  first = Math.min(first, span.start);
  last = Math.max(last, span.end);
  perFile.set(span.name, (perFile.get(span.name) ?? 0) + (span.end - span.start));
}

if (perFile.size === 0) {
  abstain("report carries no usable timestamps");
}

const elapsedMs = last - first;
const slow = [...perFile.entries()]
  .filter(([, ms]) => ms > SLOW_FILE_MS)
  .sort((a, b) => b[1] - a[1]);

console.log(
  `suite duration: ${seconds(elapsedMs)} over ${String(perFile.size)} files ` +
    `(bound ${seconds(MAX_MS)}), ${String(slow.length)} over ${seconds(SLOW_FILE_MS)}`,
);

if (elapsedMs <= MAX_MS) {
  process.exit(0);
}

console.error(
  `\nThe suite took ${seconds(elapsedMs)}, past its ${seconds(MAX_MS)} bound.\n` +
    "Chromium's --disable-frame-rate-limit has most likely stopped working: the\n" +
    "throttle it removes costs the frame-waiting files about 60x each. Slowest files:\n" +
    slow
      .slice(0, NAMED_ON_FAILURE)
      .map(([name, ms]) => `  ${seconds(ms).padStart(8)}  ${name}`)
      .join("\n") +
    "\n\nDo not raise the bound. Check the flag is still in the launch args, then fix\n" +
    "the frame waits or shard the run.",
);

if (process.env["CI"] === undefined) {
  console.error("\n(not CI, so reporting only)");
  process.exit(0);
}
process.exit(1);
