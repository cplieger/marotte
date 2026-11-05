import { describe, it, expect, afterEach } from "vitest";
import { lastTailLine, watchFence, watchResult, watchSummary } from "./watch-result.js";
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

/** The github-pr payload as KAS 2.37 builds it: JSON.stringify with a 2-space indent, every key. */
function prPayload(fields: Record<string, unknown>): string {
  return JSON.stringify(
    {
      url: "https://github.com/o/r/pull/7",
      state: "OPEN",
      newComments: [],
      newReviews: [],
      excludedComments: [],
      excludedReviews: [],
      inlineComments: [],
      excludedInlineComments: [],
      inlineCommentsFetch: "ok",
      backlog: { remaining: 0 },
      headSha: "abc123",
      mergeable: "MERGEABLE",
      mergeStateStatus: "CLEAN",
      newFailedChecks: [],
      passingCheckCount: 4,
      pendingCheckCount: 1,
      ...fields,
    },
    null,
    2,
  );
}

const comment = (id: number) => ({ id, author: { login: "rev" }, body: "nit", createdAt: "x" });
const failedCheck = (name: string) => ({ name, workflowName: "ci", conclusion: "FAILURE" });

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
    ["a partial github-pr object", '{"state":"merged","number":4}'],
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

describe("the github-pr payload", () => {
  it("decodes a conflict wake", () => {
    const r = watchResult(prPayload({ mergeable: "CONFLICTING", mergeStateStatus: "DIRTY" }));
    expect(r).toEqual({
      kind: "github-pr",
      url: "https://github.com/o/r/pull/7",
      state: "OPEN",
      mergeable: "CONFLICTING",
      mergeState: "DIRTY",
      comments: 0,
      reviews: 0,
      inline: 0,
      failedChecks: [],
      backlog: 0,
      passing: 4,
      pending: 1,
      headSha: "abc123",
    });
    expect(r === undefined ? "" : watchSummary(r)).toBe("merge conflict");
  });

  it("decodes a payload recorded before mergeable existed", () => {
    const o = JSON.parse(
      prPayload({ newComments: [comment(1), comment(2)], newFailedChecks: [failedCheck("lint")] }),
    ) as Record<string, unknown>;
    delete o["mergeable"];
    delete o["mergeStateStatus"];
    const r = watchResult(JSON.stringify(o, null, 2));
    expect(r?.kind === "github-pr" ? r.failedChecks : undefined).toEqual(["lint"]);
    expect(r === undefined ? "" : watchSummary(r)).toBe("2 comments \u00b7 1 failed check");
  });

  it.each([
    [{ state: "MERGED" }, "merged"],
    [{ state: "CLOSED", newReviews: [comment(1)] }, "closed \u00b7 1 review"],
    [
      { inlineComments: [comment(1)], backlog: { remaining: 3 } },
      "1 inline comment \u00b7 +3 more",
    ],
    [{ excludedComments: [comment(1)] }, "open"],
  ])("words %j as %s", (fields, want) => {
    const r = watchResult(prPayload(fields));
    expect(r === undefined ? "" : watchSummary(r)).toBe(want);
  });

  it.each([
    ["no inlineCommentsFetch", { inlineCommentsFetch: undefined }],
    ["an unknown inlineCommentsFetch", { inlineCommentsFetch: "partial" }],
    ["backlog as a string", { backlog: "3" }],
    ["newComments as an object", { newComments: { id: 1 } }],
    ["a failed check with no name", { newFailedChecks: [{ conclusion: "FAILURE" }] }],
    ["a numeric headSha", { headSha: 7 }],
    ["a numeric mergeable", { mergeable: 1 }],
  ])("leaves a near miss with %s undecoded", (_name, fields) => {
    expect(watchResult(prPayload(fields))).toBeUndefined();
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

describe("watchFence", () => {
  let host: HTMLElement | undefined;
  afterEach(() => {
    host?.remove();
  });

  it("keeps a tail holding a fence inside one code block", () => {
    const tail = "before\n```\ninside\n```\nafter";
    host = document.createElement("div");
    document.body.appendChild(host);
    renderMarkdownInto(host, watchFence(tail, "text"));
    const pres = host.querySelectorAll("pre");
    expect(pres).toHaveLength(1);
    expect(pres[0]?.textContent).toContain("after");
    expect(pres[0]?.textContent).toContain("before");
  });

  it("is empty for an empty body, so the empty-output state renders", () => {
    expect(watchFence("\n", "text")).toBe("");
  });
});
