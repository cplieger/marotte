// The one owner of what the client knows about each tool kind and name.

import type { ToolDenial, ToolDisclosed, ToolKind, ToolStatus } from "./types.js";
import { humanName, truncate } from "./strings.js";
export type { ToolKind };

/** What a tool card reveals when opened ("depth 1"), per KIND. `none` is claim-only (no
 *  toggle); `diff` is windowed to whole hunks; `output` is the first and last N lines;
 *  `search` per-file counts and first matches; `move` from -> to; `fetch` URL, status and
 *  body head; `mcp` server badge, input, output; `generic` the raw input/output block. */
export type ToolDepth1 =
  "none" | "diff" | "output" | "search" | "move" | "fetch" | "mcp" | "generic";

/** Depth-1 content per kind; the Record type makes a new ToolKind without an entry a
 *  compile error. */
const TOOL_DEPTH1: Readonly<Record<ToolKind, ToolDepth1>> = {
  // Claim-only: reads arrive in bursts, so they carry their fact on the claim line and
  // group (tool-group).
  read: "none",
  // `delete_file` takes ONE targetFile, which the claim already names.
  delete: "none",
  hook: "none",
  think: "none",
  switch_mode: "none",

  edit: "diff",
  write: "diff",
  move: "move",
  search: "search",
  fetch: "fetch",
  mcp: "mcp",

  execute: "output",
  shell: "output",
  command: "output",

  browser: "generic",
  other: "generic",
};

/** What a tool card reveals on expand. */
export function toolDepth1(kind: ToolKind): ToolDepth1 {
  return TOOL_DEPTH1[kind];
}

/** Whether a kind has anything to reveal; a card with no depth 1 gets no toggle. */
export function hasDepth1(kind: ToolKind): boolean {
  return TOOL_DEPTH1[kind] !== "none";
}

interface ToolProfile {
  /** Normalized kind used for summaries, icons, and grouping. */
  kind: ToolKind;
  /** Whether this tool produces a diff-able (old, new) pair. */
  writesFile: boolean;
}

export interface ToolRenderInfo {
  kind: ToolKind;
  writesFile: boolean;
  /** Normalized workspace-relative path of the affected file, or "". */
  filePath: string;
  /** Basename derived from filePath, or "". */
  fileBasename: string;
  /** Source pair for a diff render, or null when not applicable. */
  diffSources: { oldText: string; newText: string } | null;
  /** MCP server and tool names for an MCP call, else null. */
  mcp: { server: string; tool: string } | null;
  /** The skill or steering doc a `disclose_context` call loaded, or null. */
  disclosed: ToolDisclosed | null;
  /** The policy verdict that refused this call, or null; the card then reads as a
   *  refusal, not a failure. */
  denial: ToolDenial | null;
}

/** Lookup profile by tool title (kiro-cli names). Unknown titles resolve
 *  through the kind field the server already provides. */
const TITLE_PROFILES: Readonly<Record<string, ToolProfile>> = {
  // File reads
  readFile: { kind: "read", writesFile: false },
  readCode: { kind: "read", writesFile: false },
  readMultipleFiles: { kind: "read", writesFile: false },
  listDirectory: { kind: "read", writesFile: false },

  // File writes
  fsWrite: { kind: "write", writesFile: true },
  fsAppend: { kind: "write", writesFile: true },
  strReplace: { kind: "edit", writesFile: true },
  FileEdit: { kind: "edit", writesFile: true },
  FileWrite: { kind: "write", writesFile: true },

  // File lifecycle
  deleteFile: { kind: "delete", writesFile: false },
  smartRelocate: { kind: "move", writesFile: false },
  semanticRename: { kind: "edit", writesFile: false },

  // Discovery
  fileSearch: { kind: "search", writesFile: false },
  grepSearch: { kind: "search", writesFile: false },

  // Shell / web / reasoning
  executePwsh: { kind: "execute", writesFile: false },
  webFetch: { kind: "fetch", writesFile: false },
  remote_web_search: { kind: "fetch", writesFile: false },

  // Deferred MCP tool discovery (tool_search is the older mode).
  tool_load: { kind: "search", writesFile: false },
  "Tool Load": { kind: "search", writesFile: false },
  tool_search: { kind: "search", writesFile: false },
  "Tool Search": { kind: "search", writesFile: false },
};

/** Fallback profile keyed on the ACP kind, for titles the client does not know. */
const KIND_FALLBACK: Readonly<Record<string, ToolProfile>> = {
  read: { kind: "read", writesFile: false },
  edit: { kind: "edit", writesFile: true },
  write: { kind: "write", writesFile: true },
  delete: { kind: "delete", writesFile: false },
  move: { kind: "move", writesFile: false },
  search: { kind: "search", writesFile: false },
  execute: { kind: "execute", writesFile: false },
  // KAS never emits shell, but older chats carry it. hook is minted server-side for the
  // `Hook fired` card; KAS's own hook ASK arrives as kind:"other".
  shell: { kind: "shell", writesFile: false },
  hook: { kind: "hook", writesFile: false },
  command: { kind: "command", writesFile: false },
  browser: { kind: "browser", writesFile: false },
  fetch: { kind: "fetch", writesFile: false },
  think: { kind: "think", writesFile: false },
  switch_mode: { kind: "switch_mode", writesFile: false },
};

const OTHER: ToolProfile = { kind: "other", writesFile: false };

const REPO_MUTATING_KINDS: ReadonlySet<string> = new Set(["edit", "write", "delete", "move"]);

/** Reports whether a tool kind mutates the workspace. */
export function isRepoMutatingKind(kind: string): boolean {
  return REPO_MUTATING_KINDS.has(kind);
}

/** Reports whether a tool call is still running (pending or in progress). */
export function isToolActive(s: ToolStatus): boolean {
  return s === "pending" || s === "in_progress";
}

/** Reports whether a tool call has settled (completed, failed or aborted). */
export function isToolDone(s: ToolStatus): boolean {
  return s === "completed" || s === "failed" || s === "aborted";
}

/** Resolve the profile for a (title, kind) pair: a known title wins, else the ACP kind.
 *  MCP-prefixed titles resolve to the synthetic "mcp" kind. */
export function profileFor(title: string, kind: string): ToolProfile {
  if (mcpToolInfo(title) !== null) {
    return { kind: "mcp", writesFile: false };
  }
  return TITLE_PROFILES[title] ?? KIND_FALLBACK[kind] ?? OTHER;
}

// MCP tool names come as `mcp__<server>__<tool>` or `mcp:<server>:<tool>`. Exactly two
// separators, so a built-in name containing `__` is not misread.

const MCP_UNDERSCORE_RE = /^mcp__([A-Za-z0-9][A-Za-z0-9_.-]*)__([A-Za-z0-9][A-Za-z0-9_.-]*)$/;
const MCP_COLON_RE = /^mcp:([A-Za-z0-9][A-Za-z0-9_.-]*):([A-Za-z0-9][A-Za-z0-9_.-]*)$/;

/** The server and tool names of an MCP-prefixed title, else null. The caller strips any
 *  "Running: " prefix. */
export function mcpToolInfo(title: string): { server: string; tool: string } | null {
  const u = MCP_UNDERSCORE_RE.exec(title);
  if (u !== null) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    return { server: u[1]!, tool: u[2]! };
  }
  const c = MCP_COLON_RE.exec(title);
  if (c !== null) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    return { server: c[1]!, tool: c[2]! };
  }
  return null;
}

// KAS runs every deferred MCP tool through one `tool_call` tool whose input is
// {tool_id: "<server>::<tool>", arguments}, so the card names the server's
// tool rather than the envelope. Its title is model-composed, so the input's
// own `arguments` member also identifies the envelope.
const DEFERRED_CALL_TITLES: ReadonlySet<string> = new Set(["tool_call", "Tool Call"]);
const DEFERRED_ID_RE = /^([^:]+)::([^:]+)$/;

/** The MCP server and tool a deferred `tool_call` runs, or null when this is
 *  not that envelope. A builtin routed through the same envelope is not an
 *  integration, so `builtin::` resolves to null. */
function deferredMCPCall(
  title: string,
  input: Record<string, unknown> | undefined,
): { server: string; tool: string } | null {
  if (input === undefined) {
    return null;
  }
  if (!DEFERRED_CALL_TITLES.has(title) && !Object.hasOwn(input, "arguments")) {
    return null;
  }
  const id = input["tool_id"];
  if (typeof id !== "string") {
    return null;
  }
  const m = DEFERRED_ID_RE.exec(id);
  if (m === null) {
    return null;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const server = m[1]!;
  if (server === "builtin") {
    return null;
  }
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  return { server, tool: m[2]! };
}

/** Format an MCP tool name for display: underscores become spaces. A server name stays
 *  verbatim, because the user chose it. */
export function formatMCPToolName(tool: string): string {
  return tool.replace(/_/g, " ");
}

/** KAS's execute_bash title when the model sent no description. */
const PLACEHOLDER_SHELL_TITLE = "Run Command";

const TOOL_ID_RE = /^[A-Za-z0-9_-]+$/;

/** The displayed text for a tool title. A KAS shell title can be the model's
 *  own sentence, so only a bare tool id is humanized; anything else is shown
 *  as written. */
export function toolTitleText(raw: string): string {
  const t = raw.startsWith("Running: ") ? raw.slice(9) : raw;
  return TOOL_ID_RE.test(t) ? humanName(t) : t;
}

/** The command a description-less shell call ran, as its title: collapsed to
 *  one line and bounded, never humanized. Null when the call is not KAS's
 *  placeholder shell title or carries no command. */
export function commandTitle(title: string, kind: string, input: unknown): string | null {
  if (kind !== "execute" || title !== PLACEHOLDER_SHELL_TITLE) {
    return null;
  }
  if (typeof input !== "object" || input === null) {
    return null;
  }
  const command = (input as Record<string, unknown>)["command"];
  if (typeof command !== "string") {
    return null;
  }
  const line = command.replace(/\s+/g, " ").trim();
  return line === "" ? null : truncate(line, 120);
}

// --- Input-shape extraction ---

const PATH_KEYS: readonly string[] = [
  "path",
  "targetFile",
  "sourcePath",
  "file",
  "destinationPath",
];

function pickFilePath(input: Record<string, unknown> | undefined): string {
  if (input === undefined) {
    return "";
  }
  for (const k of PATH_KEYS) {
    const v = input[k];
    if (typeof v === "string" && v !== "") {
      return v;
    }
  }
  return "";
}

function pickDiffSources(
  input: Record<string, unknown> | undefined,
): { oldText: string; newText: string } | null {
  if (input === undefined) {
    return null;
  }
  const os = input["oldStr"];
  const ns = input["newStr"];
  if (typeof os === "string" && typeof ns === "string") {
    return { oldText: os, newText: ns };
  }
  // fsWrite / fsAppend carry only the new `text`, so render a pure add.
  const t = input["text"];
  if (typeof t === "string") {
    return { oldText: "", newText: t };
  }
  return null;
}

/** The rendering info for a tool call; every rendering path goes through it. */
export function renderInfoFor(
  title: string,
  kind: string,
  input: Record<string, unknown> | undefined,
  meta?: {
    disclosed?: ToolDisclosed | undefined;
    denial?: ToolDenial | undefined;
    sourcePath?: string | undefined;
  },
): ToolRenderInfo {
  const deferred = deferredMCPCall(title, input);
  const profile: ToolProfile =
    deferred !== null ? { kind: "mcp", writesFile: false } : profileFor(title, kind);
  // A hook card carries no input; its file is the hook definition KAS named.
  const filePath = profile.kind === "hook" ? (meta?.sourcePath ?? "") : pickFilePath(input);
  const fileBasename = filePath !== "" ? (filePath.split("/").pop() ?? filePath) : "";
  const diffSources = profile.writesFile ? pickDiffSources(input) : null;
  const mcp = deferred ?? (profile.kind === "mcp" ? mcpToolInfo(title) : null);
  return {
    kind: profile.kind,
    writesFile: profile.writesFile,
    filePath,
    fileBasename,
    diffSources,
    mcp,
    disclosed: meta?.disclosed ?? null,
    denial: meta?.denial ?? null,
  };
}

/** The claim line for a disclose_context call: which document, not the tool. */
export function disclosedClaim(d: ToolDisclosed): string {
  const kindWord = d.type === "steering" ? "steering" : "skill";
  return `Loaded ${kindWord}: ${d.display_name}`;
}

/** The tool call that OPENS a subagent, matched by title: nested calls share its
 *  `agent_subtask_id` but never these titles. `Orchestrate Sub-agent` is the PIPELINE
 *  driver's and stays out. Here, not roles.ts, so the store does not import icons.ts. */
export function isSubagentInvocation(tc: { readonly title: string }): boolean {
  const t = tc.title;
  return (
    t === "invokeSubAgent" ||
    t === "invoke_sub_agent" ||
    t === "Sub-agent execution" ||
    t.startsWith("Sub-agent:")
  );
}

/** Display titles of KAS-internal bookkeeping announced as tool calls. translate drops
 *  them by `_meta.kiro.toolId`; this list catches persisted ones, which carry no tool id,
 *  so the KAS-constant title is the only key. */
const INTERNAL_TOOL_TITLES: ReadonlySet<string> = new Set(["Fetching your cloud config"]);

/** Whether a persisted tool call is engine bookkeeping the transcript never renders. */
export function isInternalToolTitle(title: string): boolean {
  return INTERNAL_TOOL_TITLES.has(title);
}
