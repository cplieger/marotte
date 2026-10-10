// Mode catalog: maps a session mode id to a label, description, and icon. The icon is drawn by
// the prompt-bar mode pill and its picker (role-picker.ts).

import {
  ICON_TAB_CHAT,
  ICON_TAB_PLAN,
  ICON_TAB_SPEC,
  ICON_TAB_AGENT,
  ICON_TAB_QUICK_SPEC,
  ICON_TAB_BUG,
  ICON_TAB_AUTONOMOUS,
  ICON_TAB_REVIEW,
  ICON_SUBAGENT_INTROSPECT,
  ICON_SUBAGENT_GATHERER,
  ICON_SUBAGENT_TASK,
  ICON_SUBAGENT_CREATOR,
} from "./icons.js";
import { signal } from "@cplieger/reactive";
import type { SessionMode, ToolCall } from "./types.js";
import { humanName } from "./strings.js";

/** The engine-default mode id. kiro-cli v3's session/new starts here; a chat with an empty
 *  current_mode_id is in this mode. Labelled "Default". */
const DEFAULT_MODE_ID = "vibe";

/** The bundled v3 workflow modes, in canonical order. Seeds the picker for an empty chat that
 *  has no live session yet (availableModes arrives only with session/new). */
const BUILTIN_MODES: readonly SessionMode[] = [
  { id: "vibe", name: "Default", description: "General coding assistance", source: "bundled" },
  { id: "spec", name: "Spec", description: "Structured feature development", source: "bundled" },
  {
    id: "quick-spec",
    name: "Quick Spec",
    description: "Fast spec workflow: clarify, then auto-generate requirements, design, and tasks",
    source: "bundled",
  },
  {
    id: "bug-fix",
    name: "Bug Fix",
    description: "Structured bug-fix workflow: investigate, diagnose, and resolve bugs",
    source: "bundled",
  },
  {
    id: "plan",
    name: "Plan",
    description:
      "Plan-only mode that helps break ideas down into an implementation plan without making any changes",
    source: "bundled",
  },
  {
    id: "autonomous",
    name: "Autonomous",
    description: "Autonomous agent execution",
    source: "bundled",
  },
  // Kiro ships this one as a bundled AGENT rather than a workflow mode.
  {
    id: "semantic_reviewer",
    name: "Semantic Reviewer",
    description: "Reviews code changes at the behavioral level rather than the syntactic level",
    source: "bundled",
  },
];

/** The WORKSPACE mode catalog, served once by /api/config-template: the bundled modes + bundled
 *  agents + the user's global ~/.kiro/agents, with real names/descriptions/source tags, and —
 *  once any session has run — the live list KAS reported, which additionally carries the
 *  workspace agents with the shadowing already resolved. A signal rather than a plain binding
 *  because the label call sites are inside reactive effects, and the catalog arrives after the
 *  first paint. */
const catalogModes = signal<readonly SessionMode[]>([]);

export function setCatalogModes(modes: readonly SessionMode[]): void {
  catalogModes.value = modes;
}

/** The workspace mode base: the fetched catalog when it has landed, else the static bundled
 *  list. */
export function catalogBaseModes(): readonly SessionMode[] {
  const modes = catalogModes.value;
  return modes.length > 0 ? modes : BUILTIN_MODES;
}

/** One offered mode plus what the pre-session merge had to DECIDE about it. */
export interface PickerMode {
  mode: SessionMode;
  /** The SOURCE of the catalog entry this workspace agent shadows (`global` or `bundled`), when
   *  it shadows one. Absent when nothing is shadowed. */
  shadowed?: string;
}

/** The description a workspace agent carries before a session can supply its own. Exported so a
 *  test can assert the merge's output shape without pinning the sentence. */
export const WORKSPACE_AGENT_DESC = "Custom agent from your workspace .kiro/agents/ folder.";

/** Merge the pre-session catalog with the workspace agents, MODELLING the collision instead of
 *  deduping it away. The rule is KAS's own last-write-wins: when a workspace agent and a catalog
 *  entry (a bundled mode, or the user's global ~/.kiro/agents) share an id, the WORKSPACE
 *  definition is what a session loads. */
export function mergeCatalogAndWorkspace(
  base: readonly SessionMode[],
  workspaceAgents: readonly string[],
): PickerMode[] {
  const workspace = new Set(workspaceAgents);
  const out: PickerMode[] = [];
  for (const m of base) {
    if (workspace.has(m.id)) {
      continue; // shadowed by the workspace definition appended below
    }
    out.push({ mode: m });
  }
  for (const name of workspaceAgents) {
    const shadows = base.find((m) => m.id === name);
    // A `workspace` value is the entry shadowing ITSELF: ids are unique within one .kiro/agents
    // tree, and handleConfigTemplate serves KAS's live availableModes once any session has run,
    // which already carries these.
    const shadowedSource = shadows === undefined ? undefined : (shadows.source ?? "bundled");
    out.push({
      mode: {
        id: name,
        name,
        description: WORKSPACE_AGENT_DESC,
        source: "workspace",
      },
      ...(shadowedSource !== undefined &&
        shadowedSource !== "workspace" && { shadowed: shadowedSource }),
    });
  }
  return out;
}

/** Whether a mode's `source` puts it under the picker's "Custom agents" divider rather than in
 *  the bundled top group. */
export function isCustomSource(source: string | undefined): boolean {
  return source !== undefined && source !== "" && source !== "bundled";
}

/** Human-facing label for a mode's scope. The wire's `source` values are `bundled` | `global` |
 *  `workspace`; a bundled mode needs no label (it is the top group and every row in it is
 *  bundled), which is why this answers "" for it rather than "bundled". */
export function scopeLabel(source: string | undefined): string {
  switch (source) {
    case "workspace":
      return "workspace";
    case "global":
      return "global";
    default:
      return "";
  }
}

/** Normalize an empty / legacy mode id to the canonical default. Maps the v2 default-agent ids
 *  onto the default so mixed-engine state resolves to one highlighted entry in the picker. */
export function normalizeModeID(id: string): string {
  if (id === "") {
    return DEFAULT_MODE_ID;
  }
  return id;
}

/** Icon (SVG string) for a mode/role, keyed by id. Each mode in `BUILTIN_MODES` gets a distinct
 *  glyph, and default-v2 shares Default's; every other agent shares the hexagon. */
export function iconForMode(id: string): string {
  switch (id) {
    case "":
    case "vibe":
    case "default-v2":
      return ICON_TAB_CHAT;
    case "spec":
      return ICON_TAB_SPEC;
    case "quick-spec":
      return ICON_TAB_QUICK_SPEC;
    case "bug-fix":
      return ICON_TAB_BUG;
    case "plan":
      return ICON_TAB_PLAN;
    case "autonomous":
      return ICON_TAB_AUTONOMOUS;
    case "semantic_reviewer":
      return ICON_TAB_REVIEW;
    default:
      return ICON_TAB_AGENT;
  }
}

/** Icon (SVG string) for a subagent, keyed by the invoke_sub_agent tool's input name (raw id,
 *  e.g. "introspect", "context-gatherer"). Mirrors iconForMode's convention on the SubagentBlock
 *  header: each pre-built kiro-cli subagent gets a distinct glyph, custom/unknown subagents
 *  share the agent hexagon. */
export function iconForSubagent(name: string): string {
  switch (name) {
    case "introspect":
      return ICON_SUBAGENT_INTROSPECT;
    case "context-gatherer":
      return ICON_SUBAGENT_GATHERER;
    case "general-task-execution":
      return ICON_SUBAGENT_TASK;
    case "custom-agent-creator":
      return ICON_SUBAGENT_CREATOR;
    default:
      return ICON_TAB_AGENT;
  }
}

/** The raw subagent id from the invocation tool's input (e.g. "introspect", "context-gatherer"),
 *  or "" when the input carries none. Keys the header icon (iconForSubagent above);
 *  subagentLabel humanizes the same value. */
export function subagentName(tc: ToolCall): string {
  const input = tc.input;
  if (input !== undefined && input !== null && typeof input === "object") {
    const nm = (input as Record<string, unknown>)["name"];
    if (typeof nm === "string" && nm !== "") {
      return nm;
    }
  }
  // KAS names an unnamed inline agent this, so the label agrees with its own.
  return inlineAgentOf(tc) === null ? "" : INLINE_AGENT_NAME;
}

const INLINE_AGENT_NAME = "inline_agent";

/** The model and effort the parent chose for an inline helper (the invocation's `inlineAgent`
 *  input, kiro-cli 2.27), or null for a saved agent. Values are passed through as given; KAS
 *  resolves shorthand model ids itself. */
export function inlineAgentOf(tc: ToolCall): { model: string; effort: string } | null {
  const input = tc.input;
  if (input === undefined || input === null || typeof input !== "object") {
    return null;
  }
  const inline = (input as Record<string, unknown>)["inlineAgent"];
  if (inline === undefined || inline === null || typeof inline !== "object") {
    return null;
  }
  const rec = inline as Record<string, unknown>;
  const str = (v: unknown): string => (typeof v === "string" ? v : "");
  return { model: str(rec["model"]), effort: str(rec["effort"]) };
}

/** The Auto model category the parent asked a delegate to run on (`modelCategory`, kiro-cli 2.28
 *  dynamic delegation), or "" when absent or `auto`, which KAS ignores. A request, not proof of
 *  placement: KAS honours it only when the agent pins no model. */
export function modelCategoryOf(tc: ToolCall): string {
  const input = tc.input;
  if (input === undefined || input === null || typeof input !== "object") {
    return "";
  }
  const raw = (input as Record<string, unknown>)["modelCategory"];
  const category = typeof raw === "string" ? raw.trim() : "";
  return category === "auto" ? "" : category;
}

/** A delegate's model line for its card, or "": an inline helper's model and effort, and the
 *  requested category unless an inline model overrides it. */
export function delegateDetail(tc: ToolCall): string {
  const inline = inlineAgentOf(tc);
  const model = inline?.model ?? "";
  const category = model === "" ? modelCategoryOf(tc) : "";
  return [model, inline?.effort ?? "", category === "" ? "" : `Category: ${category}`]
    .filter((v) => v !== "")
    .join(" · ");
}

/** A delegate's display name: the invocation's own `Sub-agent: <name>` title first, then its
 *  declared id humanized, then whatever title it carries. An inline helper is marked as one,
 *  since its name is only a label. */
export function subagentLabel(tc: ToolCall): string {
  const base = baseSubagentLabel(tc);
  return inlineAgentOf(tc) === null ? base : `${base} (inline agent)`;
}

function baseSubagentLabel(tc: ToolCall): string {
  const title = tc.title;
  if (title.startsWith("Sub-agent:")) {
    const name = title.slice("Sub-agent:".length).trim();
    if (name !== "") {
      return name;
    }
  }
  const nm = subagentName(tc);
  if (nm !== "") {
    return humanName(nm);
  }
  if (title !== "" && title !== "invokeSubAgent" && title !== "invoke_sub_agent") {
    return title;
  }
  return FALLBACK_SUBAGENT_NAME;
}

/** What a delegate with no invocation tool call in the store reads as. Exported because the tab
 *  factory falls back to it for a restored tab whose chat has not been fetched yet, and the two
 *  must agree or a tab renames itself on load. */
export const FALLBACK_SUBAGENT_NAME = "Subagent";

/** Display form of a mode's name, for names that are really identifiers. The six bundled
 *  workflow modes carry hand-written names (`bug-fix` is "Bug Fix"), so they never reach this. */
export function displayModeName(name: string): string {
  if (name === "" || /\s/.test(name) || /[A-Z]/.test(name)) {
    return name;
  }
  return name
    .split(/[-_.]+/)
    .filter((word) => word !== "")
    .map((word) => word.replace(/^./, (first) => first.toUpperCase()))
    .join(" ");
}

/** Human-facing label for a mode id, from the workspace catalog (custom agents carry their own
 *  name), falling back to the bundled list and then to the raw id. */
export function labelForMode(
  id: string,
  modes: readonly SessionMode[] = catalogModes.value,
): string {
  const key = normalizeModeID(id);
  const found = modes.find((m) => m.id === key) ?? BUILTIN_MODES.find((m) => m.id === key);
  return displayModeName(found?.name ?? id);
}
