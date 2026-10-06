import { describe, it, expect } from "vitest";
import { askBucket, interactionFact } from "./interaction.js";

describe("interactionFact", () => {
  it.each([
    [{ type: "tool_approval", outcome: "selected", choice: "allow_once" }, "Allowed"],
    [{ type: "tool_approval", outcome: "selected", choice: "allow_always" }, "Always allowed"],
    [{ type: "tool_approval", outcome: "selected", choice: "reject_once" }, "Rejected"],
    [{ type: "tool_approval", outcome: "selected", choice: "reject_always" }, "Rejected"],
    [{ type: "tool_approval", outcome: "selected", choice: "opt-7" }, "Answered"],
    [{ type: "user_input", outcome: "answered", choice: "Use Go" }, "Answered: Use Go"],
    [{ type: "user_input", outcome: "answered" }, "Answered"],
    [{ type: "user_input", outcome: "dismissed", choice: "ignored" }, "Skipped"],
  ])("states %o as %s", (i, want) => {
    expect(interactionFact(i)).toBe(want);
  });

  it("files an approval option kind it does not know as answered, never as allowed", () => {
    expect(askBucket({ type: "tool_approval", outcome: "selected", choice: "x" })).toBe("answered");
  });
});
