// GitHub's API rate limit as Settings -> Tools words it. The tools engine fails a
// job, or answers a request 503, with the code below and a rate_limit object; the
// fix depends on whether the refused request carried the connected account's token.

import type { GitHubRateLimit, Job } from "./wire/types.gen.js";

/** The error code a job or a 503 carries when GitHub's rate limit refused it. */
export const RATE_LIMITED = "github_rate_limited";

/** The rate limit behind a failed job, or null when GitHub's limit did not fail it. */
export function jobRateLimit(job: Job): GitHubRateLimit | null {
  return job.state === "failed" && job.error_code === RATE_LIMITED
    ? (job.rate_limit ?? null)
    : null;
}

/** The rate limit a 503 body carries. The body is server-controlled, so the
 *  object is read as unknown and every field is checked. */
export function bodyRateLimit(code: string | undefined, body: unknown): GitHubRateLimit | null {
  if (code !== RATE_LIMITED || typeof body !== "object" || body === null) {
    return null;
  }
  const rl: unknown = (body as Record<string, unknown>)["rate_limit"];
  if (typeof rl !== "object" || rl === null) {
    return null;
  }
  const o = rl as Record<string, unknown>;
  if (typeof o["authenticated"] !== "boolean") {
    return null;
  }
  const out: GitHubRateLimit = { authenticated: o["authenticated"] };
  if (o["secondary"] === true) {
    out.secondary = true;
  }
  if (typeof o["reset_at"] === "number" && Number.isFinite(o["reset_at"]) && o["reset_at"] > 0) {
    out.reset_at = o["reset_at"];
  }
  return out;
}

/** A reset instant as the reader's own clock shows it. */
function localClock(when: Date): string {
  return when.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** Whether connecting a GitHub account is the fix: only for the hourly quota of
 *  requests sent without a token. An account does not raise the secondary limit. */
export function accountRaisesLimit(rl: GitHubRateLimit): boolean {
  return !rl.authenticated && rl.secondary !== true;
}

/** What the reader is told. Without a token the fix is an account; with one,
 *  or for the secondary limit, it is waiting, so the reset time is the useful fact. */
export function rateLimitText(
  rl: GitHubRateLimit,
  clock: (when: Date) => string = localClock,
): string {
  const reset = rl.reset_at !== undefined && rl.reset_at > 0 ? new Date(rl.reset_at) : null;
  if (rl.secondary === true) {
    return reset === null
      ? "GitHub's secondary rate limit was reached. Try again in a minute."
      : `GitHub's secondary rate limit was reached. Try again after ${clock(reset)}.`;
  }
  if (!rl.authenticated) {
    return "GitHub's limit for requests without an account was reached. Connecting a GitHub account raises it.";
  }
  return reset === null
    ? "GitHub's rate limit was reached."
    : `GitHub's rate limit was reached. It resets at ${clock(reset)}.`;
}
