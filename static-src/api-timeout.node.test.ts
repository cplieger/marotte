import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { API_TIMEOUT_MS } from "@cplieger/fetch";

// The cross-language half of the admission-wait bound: the Go side
// (internal/command/prompt_admission_test.go) keeps AdmissionWait under this fixture's
// value, and this side asserts the fixture equals the installed library's API_TIMEOUT_MS.
const FIXTURE_PATH = "../internal/command/testdata/client_api_timeout.json";

describe("client API timeout fixture", () => {
  it("matches the installed @cplieger/fetch API_TIMEOUT_MS", () => {
    const fixture = JSON.parse(readFileSync(FIXTURE_PATH, "utf8")) as Record<string, unknown>;
    expect(fixture["api_timeout_ms"]).toBe(API_TIMEOUT_MS);
  });
});
