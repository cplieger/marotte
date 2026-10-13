// push-subject.ts's own suite. The cross-language PREFIX contract is push-kinds.test.ts's; what is
// pinned here is what the module DECIDES.

import { describe, it, expect } from "vitest";
import { join } from "@cplieger/keyenc";
import pushTypesGo from "../internal/marotte/push_types.go?raw";
import {
  askTarget,
  chatTarget,
  parsePushTarget,
  prIdentity,
  pushTargetRoute,
  pushTargetTag,
  runAskOfTag,
  runAskTarget,
  runTarget,
} from "./push-subject.js";

describe("parsePushTarget", () => {
  it("reads every prefix, an empty remainder and an empty pair", () => {
    expect(parsePushTarget({ chatId: "", subject: "pr:gh:github.com:a/b#1" })).toEqual({
      kind: "pr",
      identity: "gh:github.com:a/b#1",
    });
    expect(parsePushTarget({ chatId: "", subject: "run:wf_1" })).toEqual({
      kind: "run",
      workflowID: "wf_1",
    });
    expect(parsePushTarget({ chatId: "c1", subject: "" })).toEqual({ kind: "chat", chatID: "c1" });
    // A bare prefix is reachable only from a malformed envelope, and it resolves to the workspace
    // rather than to /git or /run/.
    expect(parsePushTarget({ chatId: "", subject: "pr:" })).toEqual({ kind: "workspace" });
    expect(parsePushTarget({ chatId: "", subject: "run:" })).toEqual({ kind: "workspace" });
    expect(parsePushTarget({ chatId: "", subject: "" })).toEqual({ kind: "workspace" });
  });

  it("falls to the chat id for a subject with no recognised prefix", () => {
    expect(parsePushTarget({ chatId: "c1", subject: "something-else" })).toEqual({
      kind: "chat",
      chatID: "c1",
    });
  });
});

describe("pushTargetRoute", () => {
  it("maps each kind to its destination", () => {
    expect(pushTargetRoute({ kind: "chat", chatID: "c1" })).toEqual({ kind: "chat", id: "c1" });
    expect(pushTargetRoute({ kind: "pr", identity: "gh:github.com:a/b#1" })).toEqual({
      kind: "git",
      tab: "prs",
      pr: "gh:github.com:a/b#1",
    });
    expect(pushTargetRoute({ kind: "run", workflowID: "wf_1" })).toEqual({
      kind: "run",
      id: "wf_1",
    });
    // The default chat route: "/" is what the worker opens when no page is up.
    expect(pushTargetRoute({ kind: "workspace" })).toEqual({ kind: "chat", id: "" });
  });
});

describe("pushTargetTag", () => {
  it("gives a keyed subject, a chat and the workspace their own tray slot", () => {
    expect(pushTargetTag({ kind: "pr", identity: "gh:github.com:a/b#1" })).toBe(
      "marotte:pr:gh:github.com:a/b#1",
    );
    expect(pushTargetTag({ kind: "run", workflowID: "wf_1" })).toBe("marotte:run:wf_1");
    expect(pushTargetTag({ kind: "chat", chatID: "c1" })).toBe("marotte:c1");
    expect(pushTargetTag({ kind: "workspace" })).toBe("marotte");
  });
});

describe("prIdentity", () => {
  it("is marotte.PRSubject's composition minus the prefix, keyed on the repository id", () => {
    // The Go side reads `PRSubjectPrefix + forgeID + ":" + repoID + "#" + strconv.Itoa(number)`;
    // this is the same key with the prefix stripped, which is what makes the identity comparable
    // against one the PRs tab builds for its own rows from their repo_id.
    const m =
      /func PRSubject\([^)]*\)[^{]*\{\s*return PushSubject\{Key: PRSubjectPrefix \+ ([^}]+)\}/.exec(
        pushTypesGo,
      );
    expect(m, "marotte.PRSubject's composition not found").not.toBeNull();
    expect((m?.[1] ?? "").replace(/\s+/g, " ").trim()).toBe(
      'forgeID + ":" + repoID + "#" + strconv.Itoa(number)',
    );
    expect(prIdentity("github:github.com", "v1.63706c69656765722f6d61726f747465", 42)).toBe(
      "github:github.com:v1.63706c69656765722f6d61726f747465#42",
    );
  });
});

describe("the constructors", () => {
  it("refuses a synthetic run: chat id and answers the RUN", () => {
    // internal/agent/run_host.go registers a parentless run's bridge under
    // `run:<workflowId>`, so that run's asks arrive as the envelope chat id. This is
    // the only mechanical proof of that routing.
    expect(chatTarget("run:wf_1")).toEqual({ kind: "run", workflowID: "wf_1" });
  });

  it("answers the workspace for an id that cannot be addressed", () => {
    expect(chatTarget("")).toEqual({ kind: "workspace" });
    expect(runTarget("")).toEqual({ kind: "workspace" });
  });

  it("lets a non-empty run id win over the envelope's chat id", () => {
    expect(askTarget("c-7", "wf_1")).toEqual({ kind: "run", workflowID: "wf_1" });
    expect(askTarget("c-7", "")).toEqual({ kind: "chat", chatID: "c-7" });
    expect(askTarget("c-7", undefined)).toEqual({ kind: "chat", chatID: "c-7" });
  });
});

// One step's wait in a run: the server's `runask:` subject, whose key is the ask's position.
describe("a step question's subject", () => {
  const position = join("wf:1", join("wf:1", "review"), "sess-1");

  it("reads the run off the position's first component", () => {
    expect(parsePushTarget({ chatId: "", subject: `runask:${position}` })).toEqual({
      kind: "run_ask",
      workflowID: "wf:1",
      position,
    });
  });

  it("routes to the run's tab, and gives each wait its own tray slot", () => {
    const target = runAskTarget(position);
    expect(pushTargetRoute(target)).toEqual({ kind: "run", id: "wf:1" });
    expect(pushTargetTag(target)).toBe(`marotte:runask:${position}`);
    expect(pushTargetTag(target)).not.toBe(
      pushTargetTag(runAskTarget(join("wf:1", join("wf:1", "build"), "sess-2"))),
    );
  });

  it("is a different subject from the run's own outcome", () => {
    expect(parsePushTarget({ chatId: "", subject: "run:wf_1" }).kind).toBe("run");
  });

  it("names no run for a hashed position, which routes nowhere", () => {
    const hashed = join("x".repeat(9000));
    expect(runAskTarget(hashed)).toEqual({ kind: "run_ask", workflowID: "", position: hashed });
    expect(pushTargetRoute(runAskTarget(hashed))).toEqual({ kind: "chat", id: "" });
  });

  it("is read back off its tag, and off no other tag", () => {
    expect(runAskOfTag(`marotte:runask:${position}`)).toEqual(runAskTarget(position));
    expect(runAskOfTag("marotte:run:wf_1")).toBeUndefined();
    expect(runAskOfTag("marotte:c1")).toBeUndefined();
  });

  it("is the prefix marotte.RunAskSubject composes", () => {
    expect(pushTypesGo).toContain('const runAskSubjectPrefix = "runask:"');
    expect(pushTypesGo).toContain("PushSubject{Key: runAskSubjectPrefix + position}");
  });
});
