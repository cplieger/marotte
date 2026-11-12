import { afterEach, describe, expect, it, vi } from "vitest";
import type { TestProject } from "vitest/node";

import setup from "./marotte-server.setup.js";

const project = {
  provide: () => {
    throw new Error("an unset SSE_FIXTURE starts no server and provides nothing");
  },
} as unknown as TestProject;

describe("marotte-server setup without SSE_FIXTURE", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("refuses a CI run, which builds the fixture, so e2e-sse never skips there", async () => {
    vi.stubEnv("CI", "true");
    vi.stubEnv("SSE_FIXTURE", "");
    await expect(setup(project)).rejects.toThrow(/SSE_FIXTURE/);
  });

  it("refuses a CI run with SSE_FIXTURE absent from the environment", async () => {
    vi.stubEnv("CI", "true");
    vi.stubEnv("SSE_FIXTURE", undefined);
    await expect(setup(project)).rejects.toThrow(/SSE_FIXTURE/);
  });

  it("skips the suite outside CI and says how to run it", async () => {
    vi.stubEnv("CI", undefined);
    vi.stubEnv("SSE_FIXTURE", undefined);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    await expect(setup(project)).resolves.toBeUndefined();
    expect(warn).toHaveBeenCalledWith(expect.stringContaining("SSE_FIXTURE"));
  });
});
