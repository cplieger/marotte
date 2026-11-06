import { describe, expect, it } from "vitest";
import { toolIcon } from "./icons.js";
import type { ToolKind } from "./types.js";

const OTHER_KINDS: readonly ToolKind[] = [
  "execute",
  "shell",
  "read",
  "search",
  "fetch",
  "edit",
  "think",
  "hook",
  "write",
  "delete",
  "move",
  "command",
  "browser",
  "switch_mode",
  "mcp",
  "other",
];

describe("the steering glyph", () => {
  const steering = toolIcon({ kind: "steering", toolName: "" });

  it.each(OTHER_KINDS)("differs from the %s glyph", (kind) => {
    expect(steering).not.toBe(toolIcon({ kind, toolName: "" }));
  });

  it("differs from the disclose_context glyph", () => {
    expect(steering).not.toBe(toolIcon({ kind: "other", toolName: "disclose_context" }));
  });

  it("is a ui-tier stroked glyph with no fill of its own", () => {
    expect(steering).toMatch(/^<svg class="ic-ui"/);
    expect(steering).toContain('stroke="currentColor"');
    expect(steering).not.toMatch(/fill="(?!none)/);
  });
});
