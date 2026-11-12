import { describe, expect, it } from "vitest";

import base from "./vitest.config.js";
import stryker from "./vitest.stryker.config.js";

function names(projects: unknown[] | undefined): unknown[] {
  return (projects ?? []).map((p) =>
    typeof p === "object" && p !== null && "test" in p
      ? (p as { test?: { name?: unknown } }).test?.name
      : p,
  );
}

describe("the Stryker vitest config", () => {
  it("leaves out e2e-sse, whose setup refuses a CI run with no SSE_FIXTURE server", () => {
    expect(names(base.test?.projects)).toContain("e2e-sse");
    expect(names(stryker.test.projects)).not.toContain("e2e-sse");
  });

  it("keeps every other project of the base config", () => {
    const kept = names(base.test?.projects).filter((n) => n !== "e2e-sse");
    expect(names(stryker.test.projects)).toEqual(kept);
  });
});
