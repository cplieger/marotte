// The one owner of what the client knows about each tool kind and name.

import type { ToolDenial, ToolDisclosed, ToolKind, ToolStatus } from "./types.js";
import type { MCPToolIdentity } from "./wire/types.gen.js";
import { humanName, truncate } from "./strings.js";
export type { ToolKind };

/** What a tool card reveals when opened ("depth 1"), per KIND. `none` is claim-only (no
 *  toggle); `diff` is windowed to whole hunks; `output` is the first and last N lines;
 *  `search` per-file counts and first matches; `move` from -> to; `fetch` URL, status and
 *  body head; `mcp` server badge, input, output; `load` the ids a tool_load or
 *  load_resource call loaded; `generic` the raw input/output block. */
export type ToolDepth1 =
  "none" | "diff" | "output" | "search" | "move" | "fetch" | "mcp" | "load" | "generic";

/** Depth-1 content per kind; the Record type makes a new ToolKind without an entry a
 *  compile error. */
const TOOL_DEPTH1: Readonly<Record<ToolKind, ToolDepth1>> = {
  // Claim-only: reads arrive in bursts, so they carry their fact on the claim line and
  // group (tool-group).
  read: "none",
  // `delete_file` takes ONE targetFile, which the claim already names.
  delete: "none",
  hook: "none",
  steering: "none",
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

/** What one call's card reveals: a load shows the ids it loaded, whatever its kind. A card
 *  with no depth 1 gets no toggle. */
export function cardDepth1(info: Pick<ToolRenderInfo, "kind" | "loaded">): ToolDepth1 {
  return info.loaded !== null ? "load" : TOOL_DEPTH1[info.kind];
}

interface ToolProfile {
  /** Normalized kind used for summaries, icons, and grouping. */
  kind: ToolKind;
  /** Whether this tool produces a diff-able (old, new) pair. */
  writesFile: boolean;
}

export interface ToolRenderInfo {
  kind: ToolKind;
  /** The built-in tool's id the server named (`update_workflow`), or "". */
  toolName: string;
  writesFile: boolean;
  /** Normalized workspace-relative path of the affected file, or "". */
  filePath: string;
  /** Basename derived from filePath, or "". */
  fileBasename: string;
  /** Source pair for a diff render, or null when not applicable. */
  diffSources: { oldText: string; newText: string } | null;
  /** The MCP server and tool the server named for this call, else null. */
  mcp: MCPToolIdentity | null;
  /** The skill or steering doc a `disclose_context` call loaded, or null. */
  disclosed: ToolDisclosed | null;
  /** What a `tool_load` or `load_resource` call loaded, or null for any other call. */
  loaded: readonly LoadedResource[] | null;
  /** The policy verdict that refused this call, or null; the card then reads as a
   *  refusal, not a failure. */
  denial: ToolDenial | null;
}

/** Profile keyed on the ACP kind. */
const KIND_FALLBACK: ReadonlyMap<string, ToolProfile> = new Map<string, ToolProfile>([
  ["read", { kind: "read", writesFile: false }],
  ["edit", { kind: "edit", writesFile: true }],
  ["write", { kind: "write", writesFile: true }],
  ["delete", { kind: "delete", writesFile: false }],
  ["move", { kind: "move", writesFile: false }],
  ["search", { kind: "search", writesFile: false }],
  ["execute", { kind: "execute", writesFile: false }],
  // KAS never emits shell, but older chats carry it. hook and steering are minted
  // server-side for the `Hook fired` and `Steering loaded` cards; KAS's own hook ASK
  // arrives as kind:"other".
  ["shell", { kind: "shell", writesFile: false }],
  ["hook", { kind: "hook", writesFile: false }],
  ["steering", { kind: "steering", writesFile: false }],
  ["command", { kind: "command", writesFile: false }],
  ["browser", { kind: "browser", writesFile: false }],
  ["fetch", { kind: "fetch", writesFile: false }],
  ["think", { kind: "think", writesFile: false }],
  ["switch_mode", { kind: "switch_mode", writesFile: false }],
]);

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

/** Resolve the profile for a call: an MCP identity, else the ACP kind. */
export function profileFor(kind: string, mcp: MCPToolIdentity | null): ToolProfile {
  if (mcp !== null) {
    return { kind: "mcp", writesFile: false };
  }
  return KIND_FALLBACK.get(kind) ?? OTHER;
}

/** What one id a load named is: an MCP tool, a built-in tool, a skill, a steering doc, a
 *  Power's overview, or an id outside KAS's grammar. */
export type LoadedType = "mcp" | "tool" | "skill" | "steering" | "overview" | "unknown";

/** One resource a `tool_load` or `load_resource` call loaded, read from KAS's resource id. */
export interface LoadedResource {
  /** The id as the call sent it. */
  id: string;
  type: LoadedType;
  /** The tool, skill, document or Power name. */
  name: string;
  /** The MCP server an `mcp` resource belongs to, else "". */
  server: string;
}

/** The built-in tools that load a resource by id: `tool_load`, and `load_resource`, which
 *  replaces it and `disclose_context` under KAS's consolidate_disclosures experiment. */
const LOAD_TOOLS: ReadonlySet<string> = new Set(["tool_load", "load_resource"]);

/** Parse one KAS resource id: `tool::<server>::<tool>`, `tool::<name>`, the older
 *  `builtin::<name>`, `skill::<name>`, `steering::<name>`, `power::<name>::overview`, and a
 *  Power or plugin member, `<power|plugin>::<name>::` before one of the others. */
function parseResourceID(id: string): LoadedResource {
  const unknown: LoadedResource = { id, type: "unknown", name: id, server: "" };
  let parts = id.split("::");
  const pkg = parts[0];
  if ((pkg === "power" || pkg === "plugin") && parts.length >= 3 && parts[1] !== "") {
    if (pkg === "power" && parts.length === 3 && parts[2] === "overview") {
      return { id, type: "overview", name: parts[1] ?? "", server: "" };
    }
    parts = parts.slice(2);
  }
  const [head, a = "", b] = parts;
  if (parts.length === 3 && head === "tool" && a !== "" && b !== undefined && b !== "") {
    return { id, type: "mcp", name: b, server: a };
  }
  if (parts.length !== 2 || a === "") {
    return unknown;
  }
  switch (head) {
    case "tool":
    case "builtin":
      return { id, type: "tool", name: a, server: "" };
    case "skill":
    case "steering":
      return { id, type: head, name: a, server: "" };
    default:
      return unknown;
  }
}

/** What a load call loaded, from its `id` or an older call's `tool_ids`; null for a call
 *  that is not a load or names no id yet. */
export function loadedResources(
  toolName: string,
  input: Record<string, unknown> | undefined,
): readonly LoadedResource[] | null {
  if (!LOAD_TOOLS.has(toolName) || input === undefined) {
    return null;
  }
  const one = input["id"];
  const many = input["tool_ids"];
  const ids =
    typeof one === "string"
      ? [one]
      : Array.isArray(many)
        ? many.filter((v): v is string => typeof v === "string")
        : [];
  const real = ids.filter((id) => id !== "");
  return real.length === 0 ? null : real.map(parseResourceID);
}

function loadedLabel(r: LoadedResource): string {
  switch (r.type) {
    case "mcp":
      return `${r.server} ${r.name}`;
    case "overview":
      return `power overview: ${r.name}`;
    case "unknown":
      return r.id;
    default:
      return `${r.type}: ${r.name}`;
  }
}

/** The claim line for a load: what it loaded, not the tool that loaded it. */
export function loadedClaim(loaded: readonly LoadedResource[]): string {
  return truncate(`Loaded ${loaded.map(loadedLabel).join(", ")}`, 120);
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
  kind: string,
  input: Record<string, unknown> | undefined,
  meta?: {
    disclosed?: ToolDisclosed | undefined;
    denial?: ToolDenial | undefined;
    sourcePath?: string | undefined;
    toolName?: string | undefined;
    mcp?: MCPToolIdentity | undefined;
  },
): ToolRenderInfo {
  const mcp = meta?.mcp ?? null;
  const toolName = meta?.toolName ?? "";
  const profile = profileFor(kind, mcp);
  // A hook card carries no input; its file is the hook definition KAS named.
  const filePath = profile.kind === "hook" ? (meta?.sourcePath ?? "") : pickFilePath(input);
  const fileBasename = filePath !== "" ? (filePath.split("/").pop() ?? filePath) : "";
  const diffSources = profile.writesFile ? pickDiffSources(input) : null;
  return {
    kind: profile.kind,
    toolName,
    writesFile: profile.writesFile,
    filePath,
    fileBasename,
    diffSources,
    mcp,
    disclosed: meta?.disclosed ?? null,
    loaded: loadedResources(toolName, input),
    denial: meta?.denial ?? null,
  };
}

/** The claim line for a disclose_context call: which document, not the tool. */
export function disclosedClaim(d: ToolDisclosed): string {
  const kindWord = d.type === "steering" ? "steering" : "skill";
  return `Loaded ${kindWord}: ${d.display_name}`;
}

/** The tool call that OPENS a subagent, matched by title: nested calls share its
 *  `agent_subtask_id` but never these titles. Here, not roles.ts, so the store does not
 *  import icons.ts. */
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
