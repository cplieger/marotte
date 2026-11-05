// A workflow watch's capture, read where KAS itself authored it. Three shapes are KAS's own: the
// background-process handler's exit record, the github-pr handler's activity payload and the
// idle-timeout record any handler leaves. Anything else is a user command's output and stays verbatim.

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
  | {
      kind: "github-pr";
      url: string;
      state: string;
      /** Absent from a payload KAS recorded before it reported mergeability. */
      mergeable?: string;
      mergeState?: string;
      comments: number;
      reviews: number;
      inline: number;
      failedChecks: string[];
      backlog: number;
      passing?: number;
      pending?: number;
      headSha: string | null;
    }
  | { kind: "idle-timeout"; seconds: number };

const isCount = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

/** The github-pr payload decoded, its wake-relevant arrays counted; excluded items never woke the
 *  watch, so they are not counted. */
function githubPR(o: Record<string, unknown>): WatchResult | undefined {
  const { url, state, headSha, mergeable, mergeStateStatus, backlog } = o;
  const { newComments, newReviews, inlineComments, newFailedChecks } = o;
  const passing = o["passingCheckCount"];
  const pending = o["pendingCheckCount"];
  const remaining =
    backlog !== null && typeof backlog === "object" && !Array.isArray(backlog)
      ? (backlog as Record<string, unknown>)["remaining"]
      : undefined;
  if (
    typeof url !== "string" ||
    typeof state !== "string" ||
    !Array.isArray(newComments) ||
    !Array.isArray(newReviews) ||
    !Array.isArray(inlineComments) ||
    !Array.isArray(newFailedChecks) ||
    (o["inlineCommentsFetch"] !== "ok" && o["inlineCommentsFetch"] !== "failed") ||
    !isCount(remaining) ||
    (headSha !== null && typeof headSha !== "string") ||
    (mergeable !== undefined && typeof mergeable !== "string") ||
    (mergeStateStatus !== undefined && typeof mergeStateStatus !== "string") ||
    (passing !== undefined && !isCount(passing)) ||
    (pending !== undefined && !isCount(pending))
  ) {
    return undefined;
  }
  const failedChecks: string[] = [];
  for (const check of newFailedChecks as unknown[]) {
    const name =
      check !== null && typeof check === "object"
        ? (check as Record<string, unknown>)["name"]
        : undefined;
    if (typeof name !== "string") {
      return undefined;
    }
    failedChecks.push(name);
  }
  return {
    kind: "github-pr",
    url,
    state,
    comments: newComments.length,
    reviews: newReviews.length,
    inline: inlineComments.length,
    failedChecks,
    backlog: remaining,
    headSha,
    ...(mergeable !== undefined && { mergeable }),
    ...(mergeStateStatus !== undefined && { mergeState: mergeStateStatus }),
    ...(passing !== undefined && { passing }),
    ...(pending !== undefined && { pending }),
  };
}

/** The capture decoded, or undefined when it is not wholly one of KAS's three shapes. Strict on
 *  every field, so a user command's own JSON is never mistaken for one. */
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
  if (Object.hasOwn(o, "inlineCommentsFetch")) {
    return githubPR(o);
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
  if (r.kind === "github-pr") {
    return prSummary(r);
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

function counted(n: number, one: string): string | undefined {
  return n > 0 ? `${String(n)} ${one}${n === 1 ? "" : "s"}` : undefined;
}

/** What woke a github-pr watch, the PR's end first; the state alone when nothing else is new. */
function prSummary(r: Extract<WatchResult, { kind: "github-pr" }>): string {
  const state = r.state.toUpperCase();
  const parts = [
    state === "MERGED" || state === "CLOSED" ? state.toLowerCase() : undefined,
    r.mergeable === "CONFLICTING" ? "merge conflict" : undefined,
    counted(r.comments, "comment"),
    counted(r.reviews, "review"),
    counted(r.inline, "inline comment"),
    counted(r.failedChecks.length, "failed check"),
    r.backlog > 0 ? `+${String(r.backlog)} more` : undefined,
  ].filter((p) => p !== undefined);
  return parts.length === 0 ? r.state.toLowerCase() : parts.join(" \u00b7 ");
}

export function lastTailLine(tail: string): string {
  const lines = tail.split("\n").filter((l) => l.trim() !== "");
  return lines.at(-1) ?? "";
}

/** The body as one fenced `lang` block, its fence one backtick longer than any run inside it so the
 *  body cannot close it early; "" for an empty body. */
export function watchFence(body: string, lang: string): string {
  if (body.trim() === "") {
    return "";
  }
  const longest = Math.max(0, ...[...body.matchAll(/`+/g)].map((m) => m[0].length));
  const fence = "`".repeat(Math.max(3, longest + 1));
  return `${fence}${lang}\n${body.replace(/\n+$/, "")}\n${fence}`;
}
