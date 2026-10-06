// The tool-kind NOUN vocabulary, read by the tool group's mixed summary and the turn footer. A LEAF
// (imports only `ToolKind`), so the `fundamentals/` footer pulls in no disclosure machinery. TOTAL
// over `ToolKind`: no fallback, and no prototype key to reach.

import type { ToolKind } from "./types.js";

/** Singular and plural per kind, spelled out rather than derived. `other` is "call", the honest
 *  word for a kind named by the absence of one; the rest match `TOOL_KIND_LABELS`. */
const KIND_NOUNS: Readonly<Record<ToolKind, { one: string; many: string }>> = {
  read: { one: "read", many: "reads" },
  edit: { one: "edit", many: "edits" },
  write: { one: "write", many: "writes" },
  delete: { one: "delete", many: "deletes" },
  move: { one: "move", many: "moves" },
  search: { one: "search", many: "searches" },
  execute: { one: "command", many: "commands" },
  shell: { one: "shell command", many: "shell commands" },
  hook: { one: "hook", many: "hooks" },
  fetch: { one: "fetch", many: "fetches" },
  think: { one: "thinking step", many: "thinking steps" },
  switch_mode: { one: "mode switch", many: "mode switches" },
  mcp: { one: "integration call", many: "integration calls" },
  browser: { one: "page", many: "pages" },
  command: { one: "command", many: "commands" },
  other: { one: "call", many: "calls" },
};

/** What to call `count` calls of this kind. */
export function kindNoun(kind: ToolKind, count: number): string {
  const noun = KIND_NOUNS[kind];
  return count === 1 ? noun.one : noun.many;
}
