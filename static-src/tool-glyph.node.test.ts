import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { ICON_TAB_DOCS, toolIcon, workflowIcon, type WorkflowBadge } from "./icons.js";
import { renderInfoFor } from "./tool-schema.js";
import type { ToolCall, ToolKind } from "./types.js";

// The tool_call entries the server produced from recorded KAS frames: Go's
// TestToolIdentity_RecordedFramesMatchTheGolden writes and checks this file
// (UPDATE_GOLDEN=1 go test ./internal/translate/ -run TestToolIdentity).
const GOLDEN = "../internal/translate/testdata/kas/tool_identity.golden.json";

interface GoldenRow {
  name: string;
  call: ToolCall;
}

function rows(): GoldenRow[] {
  return JSON.parse(readFileSync(new URL(GOLDEN, import.meta.url), "utf8")) as GoldenRow[];
}

/** The glyph a call should draw, from the rulings: a kind's own glyph, a workflow action's badge, or the book. */
type Want = { kind: ToolKind } | { workflow: WorkflowBadge } | { book: true } | { mcp: string };

function wantGlyph(w: Want): string {
  if ("workflow" in w) {
    return workflowIcon(w.workflow);
  }
  if ("book" in w) {
    return ICON_TAB_DOCS;
  }
  return toolIcon({ kind: "mcp" in w ? "mcp" : w.kind, toolName: "" });
}

const WANT: Record<string, Want> = {
  "read_file (wire)": { kind: "read" },
  "tool_load (wire)": { kind: "search" },
  "MCP via tool_call (wire)": { mcp: "capture" },
  "update_workflow via tool_call": { workflow: "update" },
  "update_workflow direct": { workflow: "update" },
  "inspect_workflow via tool_call": { workflow: "inspect" },
  "inspect_workflow direct": { workflow: "inspect" },
  run_workflow: { workflow: "run" },
  send_message: { workflow: "message" },
  save_workflow_definition: { workflow: "save" },
  "web_search direct": { kind: "fetch" },
  "web_search via tool_call": { kind: "fetch" },
  "mailbox send_message via tool_call": { mcp: "mailbox" },
  "mailbox fetch_inbox via tool_call": { mcp: "mailbox" },
  "unloaded built-in via tool_call": { kind: "other" },
  "unloaded MCP tool via tool_call": { kind: "other" },
  tool_load: { kind: "search" },
  "disclose_context skill": { book: true },
  "disclose_context steering": { book: true },
  read_file: { kind: "read" },
  fs_write: { kind: "edit" },
  str_replace: { kind: "edit" },
  fs_append: { kind: "edit" },
  delete_file: { kind: "delete" },
  grep_search: { kind: "search" },
  file_search: { kind: "search" },
  list_directory: { kind: "search" },
  web_fetch: { kind: "fetch" },
  "described shell call": { kind: "execute" },
  "control process via tool_call": { kind: "execute" },
  list_processes: { kind: "other" },
  update_session_information: { kind: "other" },
};

/** What the card draws for a call: its glyph and the MCP server its badge names. */
function drawn(call: ToolCall): { glyph: string; server: string | null } {
  const info = renderInfoFor(call.kind, call.input as Record<string, unknown> | undefined, {
    toolName: call.tool_name,
    mcp: call.mcp,
  });
  return { glyph: toolIcon(info), server: info.mcp?.server_name ?? null };
}

describe("every recorded tool call draws its ruled glyph", () => {
  const all = rows();

  it("has an expectation for every recorded row and no stale one", () => {
    expect(new Set(all.map((r) => r.name))).toEqual(new Set(Object.keys(WANT)));
  });

  for (const r of all) {
    it(r.name, () => {
      const want = WANT[r.name];
      expect(want, "a ruled glyph for this row").toBeDefined();
      if (want === undefined) {
        return;
      }
      const got = drawn(r.call);
      expect(got.glyph).toBe(wantGlyph(want));
      expect(got.server).toBe("mcp" in want ? want.mcp : null);
    });
  }

  // marotte mints the hook card itself (translate's hookToolCall); this is a recorded one.
  it("a hook card draws the fish-hook, clear of the unknown-tool wrench", () => {
    const hook = drawn({
      id: "hook-op-1",
      title: "Hook fired: lint",
      kind: "hook",
      status: "completed",
      ts: 0,
    });
    expect(hook.glyph).toBe(toolIcon({ kind: "hook", toolName: "" }));
    expect(hook.glyph).not.toBe(toolIcon({ kind: "other", toolName: "" }));
  });
});

describe("the workflow family is one base with distinct badges", () => {
  const badges: WorkflowBadge[] = ["run", "inspect", "update", "validate", "save", "message"];

  it("draws a different glyph for every action", () => {
    expect(new Set(badges.map((b) => workflowIcon(b))).size).toBe(badges.length);
  });

  it("keeps the plain tree and every glyph a non-workflow tool draws out of the family", () => {
    const kinds: ToolKind[] = [
      "read",
      "edit",
      "delete",
      "move",
      "search",
      "execute",
      "hook",
      "think",
      "fetch",
      "switch_mode",
      "mcp",
      "other",
    ];
    const others = new Set(kinds.map((kind) => toolIcon({ kind, toolName: "" })));
    for (const b of badges) {
      expect(others.has(workflowIcon(b)), b).toBe(false);
    }
  });
});
