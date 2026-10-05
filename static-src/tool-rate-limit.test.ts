import { describe, expect, it } from "vitest";

import { bodyRateLimit, jobRateLimit, rateLimitText } from "./tool-rate-limit.js";
import type { Job } from "./wire/types.gen.js";

const RESET = Date.UTC(2026, 9, 5, 21, 30);

function failedJob(extra: Partial<Job>): Job {
  return { id: "j1", kind: "update", state: "failed", created_at: 1, error: "refused", ...extra };
}

describe("rateLimitText", () => {
  it("tells a request without an account that connecting one raises the limit", () => {
    expect(rateLimitText({ authenticated: false, reset_at: RESET })).toBe(
      "GitHub's limit for requests without an account was reached. Connecting a GitHub account raises it.",
    );
  });

  it("tells a request with an account when the limit resets, on the reader's clock", () => {
    const seen: number[] = [];
    const clock = (when: Date): string => {
      seen.push(when.getTime());
      return "23:30";
    };
    expect(rateLimitText({ authenticated: true, reset_at: RESET }, clock)).toBe(
      "GitHub's rate limit was reached. It resets at 23:30.",
    );
    expect(seen).toEqual([RESET]);
  });

  it("names no reset time GitHub did not send", () => {
    expect(rateLimitText({ authenticated: true })).toBe("GitHub's rate limit was reached.");
  });

  // An account does not raise the secondary limit, so waiting is the fix
  // whether or not the refused request carried a token.
  it("tells a secondary limit when to retry, with or without an account", () => {
    const clock = (): string => "23:30";
    const want = "GitHub's secondary rate limit was reached. Try again after 23:30.";
    expect(rateLimitText({ authenticated: false, secondary: true, reset_at: RESET }, clock)).toBe(
      want,
    );
    expect(rateLimitText({ authenticated: true, secondary: true, reset_at: RESET }, clock)).toBe(
      want,
    );
  });

  it("tells a secondary limit with no reset time to wait a minute", () => {
    expect(rateLimitText({ authenticated: false, secondary: true })).toBe(
      "GitHub's secondary rate limit was reached. Try again in a minute.",
    );
  });
});

describe("jobRateLimit", () => {
  it("reads the limit off a job GitHub's rate limit failed", () => {
    const limit = { authenticated: false, reset_at: RESET, limit: 60 };
    expect(
      jobRateLimit(failedJob({ error_code: "github_rate_limited", rate_limit: limit })),
    ).toEqual(limit);
  });

  it("answers null for any other failure", () => {
    expect(jobRateLimit(failedJob({}))).toBeNull();
    expect(
      jobRateLimit(failedJob({ error_code: "other", rate_limit: { authenticated: false } })),
    ).toBeNull();
  });
});

describe("bodyRateLimit", () => {
  it("reads the limit off a 503 coded github_rate_limited", () => {
    const body = {
      error: "x",
      code: "github_rate_limited",
      rate_limit: { authenticated: true, reset_at: RESET, limit: 5000 },
    };
    expect(bodyRateLimit("github_rate_limited", body)).toEqual({
      authenticated: true,
      reset_at: RESET,
    });
  });

  it("refuses a body that does not say whether a token was sent", () => {
    expect(bodyRateLimit("github_rate_limited", { rate_limit: { reset_at: RESET } })).toBeNull();
    expect(bodyRateLimit("github_rate_limited", { rate_limit: "soon" })).toBeNull();
    expect(bodyRateLimit("github_rate_limited", undefined)).toBeNull();
  });

  it("answers null for another code", () => {
    expect(bodyRateLimit("not_configured", { rate_limit: { authenticated: false } })).toBeNull();
  });

  it("keeps a secondary flag and drops one that is not a boolean", () => {
    expect(
      bodyRateLimit("github_rate_limited", {
        rate_limit: { authenticated: false, secondary: true, reset_at: RESET },
      }),
    ).toEqual({ authenticated: false, secondary: true, reset_at: RESET });
    expect(
      bodyRateLimit("github_rate_limited", {
        rate_limit: { authenticated: false, secondary: "yes" },
      }),
    ).toEqual({ authenticated: false });
  });

  it("drops a reset that is not a positive finite instant", () => {
    expect(
      bodyRateLimit("github_rate_limited", {
        rate_limit: { authenticated: true, reset_at: "later" },
      }),
    ).toEqual({
      authenticated: true,
    });
  });
});
