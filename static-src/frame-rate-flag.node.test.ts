import { expect, test } from "vitest";

import config from "./vitest.config.js";

function browserLaunchArgs(cfg: unknown): unknown {
  const projects = (cfg as { test?: { projects?: unknown } }).test?.projects;
  if (!Array.isArray(projects)) {
    throw new Error("test.projects is not an array");
  }

  for (const project of projects as unknown[]) {
    const t = (project as { test?: Record<string, unknown> }).test;
    if (t?.["name"] !== "browser") {
      continue;
    }
    const provider = (t["browser"] as { provider?: Record<string, unknown> } | undefined)?.provider;
    const options = provider?.["options"] as { launchOptions?: { args?: unknown } } | undefined;
    return options?.launchOptions?.args;
  }
  throw new Error("no project named 'browser'");
}

// Nothing distinguishes the flag being honoured from it being absent until the
// throttle fires, so asserting the line is the only guard against losing it.
test("the browser project passes the frame-rate flag to Chromium", () => {
  const args = browserLaunchArgs(config);
  expect(Array.isArray(args)).toBe(true);
  expect(args as unknown[]).toContain("--disable-frame-rate-limit");
});
