// The hook outcome rules: a quiet hook completed with no output; only a quiet hook with no tool to fold into
// is dropped at the entry when Show hook status is off.

import { describe, it, expect } from "vitest";
import { hookQuiet, hookRenders, isToolTriggerHook } from "./hook-fold.js";
import type { ToolCall } from "./types.js";

function hook(over: Partial<ToolCall> = {}): ToolCall {
  return Object.assign(
    { id: "hook-op", title: "Hook fired: g", kind: "hook", status: "completed", ts: 1 } as ToolCall,
    over,
  );
}

describe("hookQuiet", () => {
  it.each<[string, Partial<ToolCall>, boolean]>([
    ["completed, no output", {}, true],
    ["completed, whitespace output", { output: " \n" }, true],
    ["completed, printed output", { output: "2 warnings" }, false],
    ["failed", { status: "failed" }, false],
    ["aborted", { status: "aborted" }, false],
    ["still running", { status: "in_progress" }, false],
  ])("%s → %s", (_name, over, want) => {
    expect(hookQuiet(hook(over))).toBe(want);
  });
});

describe("isToolTriggerHook", () => {
  it.each<[string | undefined, boolean]>([
    ["PreToolUse", true],
    ["PostToolUse", true],
    ["UserPromptSubmit", false],
    ["Stop", false],
    ["SessionStart", false],
    ["PostFileSave", false],
    [undefined, false],
  ])("%s → %s", (trigger, want) => {
    expect(isToolTriggerHook(hook(trigger === undefined ? {} : { hook_trigger: trigger }))).toBe(
      want,
    );
  });
});

describe("hookRenders", () => {
  it.each<[string, Partial<ToolCall>, boolean, boolean]>([
    ["quiet prompt hook, switch on", { hook_trigger: "UserPromptSubmit" }, true, true],
    ["quiet prompt hook, switch off", { hook_trigger: "UserPromptSubmit" }, false, false],
    ["quiet unresolved hook, switch off", {}, false, false],
    ["loud prompt hook, switch off", { hook_trigger: "Stop", output: "keep going" }, false, true],
    ["failed hook, switch off", { status: "failed" }, false, true],
    // Mounted either way: its group decides between the badge, a row and hidden.
    ["quiet tool hook, switch off", { hook_trigger: "PreToolUse" }, false, true],
  ])("%s → %s", (_name, over, shown, want) => {
    expect(hookRenders(hook(over), shown)).toBe(want);
  });
});
