// The client names a steering document as the server's `steeringDocName` does
// (internal/translate/steering_inclusion_test.go holds the same table), or the box's title falls
// back to plain text.

import { describe, expect, it } from "vitest";
import { docName } from "./steering-docs.js";

describe("docName", () => {
  it.each([
    ["file:///w/.kiro/steering/go.md", "go.md"],
    ["file:///w/.kiro/steering/my%20notes.md", "my notes.md"],
    ["kiro-default-steering", "kiro-default-steering"],
    ["file:///", "file:///"],
    ["file://elsewhere/w/go.md", "file://elsewhere/w/go.md"],
    ["file://localhost/w/go.md", "go.md"],
    ["file:/w/go.md", "file:/w/go.md"],
    ["file:///w/.kiro/steering/", "steering"],
  ])("names %s as %s", (id, want) => {
    expect(docName(id)).toBe(want);
  });
});
