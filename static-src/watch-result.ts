// A workflow watch's capture, read where KAS itself authored it. Two shapes are KAS's own: the
// background-process handler's exit record (kiro-cli 2.28) and the idle-timeout record any handler
// leaves. Anything else is a user command's output and stays verbatim.

/** A decoded watch capture. `status` is KAS's open vocabulary (exited, stopped, timed_out, …). */
export type WatchResult =
  | {
      kind: "process";
      status: string;
      exitCode: number | null;
      signal: string | null;
      outputFile?: string;
      tail: string;
    }
  | { kind: "idle-timeout"; seconds: number };

/** The capture decoded, or undefined when it is not wholly one of KAS's two shapes. Strict on every
 *  field, so a user command's own JSON is never mistaken for one. */
export function watchResult(payload: string): WatchResult | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(payload);
  } catch {
    return undefined;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return undefined;
  }
  const o = parsed as Record<string, unknown>;
  if (o["outcome"] === "idle-timeout") {
    const seconds = o["idleTimeoutSec"];
    return typeof seconds === "number" && Number.isFinite(seconds)
      ? { kind: "idle-timeout", seconds }
      : undefined;
  }
  const exitCode = o["exitCode"];
  const signal = o["signal"];
  const outputFile = o["outputFile"];
  if (
    typeof o["terminalId"] !== "string" ||
    typeof o["startedAt"] !== "string" ||
    typeof o["status"] !== "string" ||
    typeof o["outputTail"] !== "string" ||
    (exitCode !== null && typeof exitCode !== "number") ||
    (signal !== null && typeof signal !== "string") ||
    (outputFile !== undefined && typeof outputFile !== "string")
  ) {
    return undefined;
  }
  return {
    kind: "process",
    status: o["status"],
    exitCode,
    signal,
    tail: o["outputTail"],
    ...(outputFile !== undefined && outputFile !== "" && { outputFile }),
  };
}

/** One line saying how the watch ended. KAS's state mark is left alone: a nonzero exit is the
 *  ordinary "not yet" of an until-loop, and the next step decides what the code means. */
export function watchSummary(r: WatchResult): string {
  if (r.kind === "idle-timeout") {
    return `gave up after ${String(r.seconds)}s idle`;
  }
  switch (r.status) {
    case "exited":
      if (r.signal !== null && r.signal !== "") {
        return `killed by ${r.signal}`;
      }
      return r.exitCode === null ? "exited" : `exited ${String(r.exitCode)}`;
    case "timed_out":
      return "timed out";
    default:
      return r.status;
  }
}

export function lastTailLine(tail: string): string {
  const lines = tail.split("\n").filter((l) => l.trim() !== "");
  return lines.at(-1) ?? "";
}

/** The tail as one fenced `text` block, its fence one backtick longer than any run inside it so the
 *  tail cannot close it early; "" for an empty tail. */
export function watchTailFence(tail: string): string {
  if (tail.trim() === "") {
    return "";
  }
  const longest = Math.max(0, ...[...tail.matchAll(/`+/g)].map((m) => m[0].length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return `${fence}text\n${tail.replace(/\n+$/, "")}\n${fence}`;
}
