import { describe, it, expect, afterEach } from "vitest";
import { lastTailLine, watchResult, watchSummary, watchTailFence } from "./watch-result.js";
import { renderMarkdownInto } from "./markdown.js";

/** The exit record exactly as KAS builds it: compact JSON.stringify, no indent. */
function exitRecord(fields: Record<string, unknown>): string {
  return JSON.stringify({
    terminalId: "t1",
    status: "exited",
    exitCode: 0,
    signal: null,
    startedAt: "2026-10-06T00:00:00.000Z",
    outputTail: "",
    ...fields,
  });
}

describe("watchResult", () => {
  it("decodes the background-process exit record", () => {
    const r = watchResult(
      exitRecord({ exitCode: 1, outputTail: "ok\nFAIL x", outputFile: "/tmp/o.log" }),
    );
    expect(r).toEqual({
      kind: "process",
      status: "exited",
      exitCode: 1,
      signal: null,
      outputFile: "/tmp/o.log",
      tail: "ok\nFAIL x",
    });
  });

  it("decodes the idle-timeout record, pretty-printed or not", () => {
    expect(watchResult('{\n  "outcome": "idle-timeout",\n  "idleTimeoutSec": 30\n}')).toEqual({
      kind: "idle-timeout",
      seconds: 30,
    });
  });

  it.each([
    ["plain text", "build passed"],
    ["a github-pr style object", '{"state":"merged","number":4}'],
    ["a string exit code", exitRecord({ exitCode: "1" })],
    [
      "a missing tail",
      '{"terminalId":"t1","status":"exited","exitCode":0,"signal":null,"startedAt":"x"}',
    ],
    ["an array", "[1,2]"],
    ["a JSON null", "null"],
  ])("leaves %s undecoded", (_name, payload) => {
    expect(watchResult(payload)).toBeUndefined();
  });
});

describe("watchSummary", () => {
  it.each([
    [{ exitCode: 1 }, "exited 1"],
    [{ exitCode: 0 }, "exited 0"],
    [{ exitCode: null, signal: "SIGTERM" }, "killed by SIGTERM"],
    [{ status: "timed_out", exitCode: null }, "timed out"],
    [{ status: "stopped", exitCode: null }, "stopped"],
    [{ status: "lost", exitCode: null }, "lost"],
  ])("words %j as %s", (fields, want) => {
    const r = watchResult(exitRecord(fields));
    expect(r === undefined ? "" : watchSummary(r)).toBe(want);
  });

  it("words an idle give-up", () => {
    expect(watchSummary({ kind: "idle-timeout", seconds: 45 })).toBe("gave up after 45s idle");
  });
});

describe("lastTailLine", () => {
  it("skips trailing blank lines", () => {
    expect(lastTailLine("ok\nFAIL x\n\n")).toBe("FAIL x");
    expect(lastTailLine("")).toBe("");
  });
});

describe("watchTailFence", () => {
  let host: HTMLElement | undefined;
  afterEach(() => {
    host?.remove();
  });

  it("keeps a tail holding a fence inside one code block", () => {
    const tail = "before\n```\ninside\n```\nafter";
    host = document.createElement("div");
    document.body.appendChild(host);
    renderMarkdownInto(host, watchTailFence(tail));
    const pres = host.querySelectorAll("pre");
    expect(pres).toHaveLength(1);
    expect(pres[0]?.textContent).toContain("after");
    expect(pres[0]?.textContent).toContain("before");
  });

  it("is empty for an empty tail, so the empty-output state renders", () => {
    expect(watchTailFence("\n")).toBe("");
  });
});
