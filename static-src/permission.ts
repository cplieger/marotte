// Permission card: the agent is asking to do something, rendered in the interaction dock
// (decision-dock.ts owns the queue, the host and the settle -once guard; this file only builds DOM
// and reports the choice).

import type {
  AlwaysAllowBlock,
  ApprovalFile,
  PermissionNeededPayload,
  PermissionOption,
} from "./types.js";
import { el } from "@cplieger/reactive";
import { formatMCPToolName } from "./tool-schema.js";
import { editNativeRule } from "./actions/permissions.js";
import { openChange } from "./navigate.js";
import { openSetting } from "./settings-highlight.js";
import { get } from "./store.js";
import { payloadOf } from "./turns.js";
import { ICON_DIFF } from "./icons.js";
import { iconEl } from "./icon-el.js";

const PREVIEW_CHAR_CAP = 500;

/** One answer to a permission ask. `fileDecisions` rides only a turn approval; `rejectionReason`
 *  only a deny of an ask that accepts a note. */
export interface PermissionAnswer {
  readonly optionID: string;
  readonly fileDecisions?: Record<string, boolean>;
  readonly rejectionReason?: string;
}

/** The deny note's cap, the server's `MaxRejectionReasonRunes`. */
const REJECTION_REASON_MAX = 1000;

type SelectFn = (answer: PermissionAnswer) => void;

/** Build the dock card for one permission request. */
export function buildPermissionCard(
  chatID: string,
  payload: PermissionNeededPayload,
  onSelect: SelectFn,
): HTMLElement {
  const files = payload.files ?? [];
  if (files.length > 0) {
    return buildTurnApprovalCard(payload, files, onSelect);
  }
  // Resolved HERE, at render time, rather than when the ask was enqueued: a queued permission can
  // be built long after it arrived, and the tool call carrying the input may not have been ingested
  // yet at that point.
  return buildToolPermissionCard(
    payload,
    lookupToolInput(chatID, payload.tool_call_id ?? ""),
    onSelect,
  );
}

/** The agent's own arguments for the tool it is asking to run — the thing the user is actually
 *  approving. Walks back from the newest turn because the ask is about the turn in flight. */
function lookupToolInput(chatID: string, toolCallID: string): unknown {
  if (toolCallID === "") {
    return undefined;
  }
  const s = get(chatID);
  if (s === undefined) {
    return undefined;
  }
  for (let i = s.turn_order.length - 1; i >= 0; i--) {
    const turnID = s.turn_order[i];
    const t = turnID === undefined ? undefined : s.turns.get(turnID);
    if (t === undefined) {
      continue;
    }
    for (let j = t.entries.length - 1; j >= 0; j--) {
      const e = t.entries[j];
      const call = e === undefined ? undefined : payloadOf(e, "tool_call");
      if (call?.id === toolCallID) {
        return call.input;
      }
    }
  }
  return undefined;
}

function buildToolPermissionCard(
  payload: PermissionNeededPayload,
  input: unknown,
  onSelect: SelectFn,
): HTMLElement {
  const title = payload.title ?? "Tool";
  const kind = payload.kind ?? "";
  const isModeSwitch = kind === "switch_mode";

  const body = el("div", { className: "approval-body" });
  const mcp = payload.mcp_tool;
  const heading = isModeSwitch
    ? "Switch session mode"
    : mcp !== undefined
      ? formatMCPToolName(mcp.tool_name)
      : title;
  body.appendChild(el("strong", null, heading));

  if (isModeSwitch) {
    body.appendChild(el("div", { className: "approval-origin" }, title));
  } else if (mcp !== undefined) {
    body.appendChild(
      el(
        "div",
        { className: "approval-origin" },
        "from ",
        el("strong", null, mcp.server_name),
        " MCP integration",
      ),
    );
  }

  // An administrator's ask outranks every allow rule, so neither Always allow nor the profile
  // picker can stop it asking; the card says who asks instead.
  const adminAsk = payload.admin_required === true;
  if (adminAsk) {
    body.appendChild(
      el(
        "div",
        { className: "approval-origin approval-admin" },
        "Your administrator requires approval for this",
      ),
    );
  }

  const watch = payload.watch;
  if (watch !== undefined) {
    body.appendChild(
      el(
        "div",
        { className: "approval-origin approval-watch" },
        `workflow watch: ${watch.workflow_id} / ${watch.node_id}`,
      ),
    );
  }

  const round = payload.consent_round ?? 0;
  if (round > 1) {
    body.appendChild(
      el(
        "div",
        { className: "approval-origin approval-round" },
        `Another approval for this same tool call (${String(round)})`,
      ),
    );
  }

  const toolCallID = payload.tool_call_id ?? "";
  if (toolCallID !== "") {
    body.appendChild(el("div", { className: "approval-id" }, toolCallID));
  }

  const locations = payload.locations ?? [];
  if (locations.length > 0) {
    const list = el("ul", {
      className: "dock-file-list approval-locations",
      "aria-label": "Files this tool call touches",
    });
    for (const path of locations) {
      list.appendChild(
        el("li", { className: "dock-file-row" }, el("span", { className: "dock-file-path" }, path)),
      );
    }
    body.appendChild(list);
  }

  const preview = formatInputPreview(input);
  if (preview !== "") {
    body.appendChild(el("pre", { className: "approval-input" }, preview));
  }

  const note = payload.accepts_rejection_reason === true ? buildRejectionNote() : null;
  const actions = el("div", { className: "approval-actions" });
  for (const opt of payload.options) {
    const btn = el(
      "button",
      {
        type: "button",
        className: opt.kind.startsWith("allow")
          ? "btn-small confirm-allow"
          : "btn-small confirm-danger",
      },
      opt.name,
    );
    btn.addEventListener("click", () => {
      // Only a reject_once answer carries the note: KAS ignores one anywhere else.
      const reason = opt.kind === "reject_once" ? (note?.value.trim() ?? "") : "";
      onSelect(
        reason !== ""
          ? { optionID: opt.option_id, rejectionReason: reason }
          : { optionID: opt.option_id },
      );
    });
    actions.appendChild(btn);
  }

  if (kind === "execute" && !isModeSwitch && !adminAsk) {
    const alwaysRow = buildAlwaysAllowRow(
      title,
      payload.options,
      payload.always_allow_blocked,
      onSelect,
    );
    if (alwaysRow !== null) {
      actions.appendChild(alwaysRow);
    }
  }

  const card =
    note === null
      ? el("div", { className: "dock-card dock-permission" }, body, actions)
      : el("div", { className: "dock-card dock-permission" }, body, note, actions);
  if (isModeSwitch) {
    card.classList.add("mode-switch");
  }
  if (!isModeSwitch && !adminAsk) {
    card.appendChild(buildPolicyPointer());
  }
  return card;
}

/** The optional deny note above the answer buttons. */
function buildRejectionNote(): HTMLTextAreaElement {
  const note = el("textarea", {
    className: "approval-reason",
    rows: 1,
    maxLength: REJECTION_REASON_MAX,
    placeholder: "Optional: tell the agent why you're denying",
  }) as HTMLTextAreaElement;
  note.setAttribute("aria-label", "Reason for denying (optional)");
  return note;
}

/** A pointer to the security profile picker, for the reader who is tired of being asked. Its own
 *  row BELOW the answer buttons, deliberately not among them. It aims at `security-profile-list`
 *  because that is the control that exists. */
function buildPolicyPointer(): HTMLElement {
  const link = el("button", { type: "button", className: "approval-policy-link" }, "Settings");
  link.addEventListener("click", () => {
    openSetting("permissions", "security-profile-list");
  });
  return el(
    "div",
    { className: "approval-policy-pointer" },
    "Asked too often? Pick a looser security profile in ",
    link,
    ".",
  );
}

/** Files sharing one action id: the atomic review unit. */
interface ActionGroup {
  actionID: string;
  paths: string[];
}

/** Group by action id, preserving first-seen order so the list is stable across the re-render a
 *  queue change causes. */
function groupByAction(files: readonly ApprovalFile[]): ActionGroup[] {
  const byID = new Map<string, ActionGroup>();
  for (const f of files) {
    const existing = byID.get(f.action_id);
    if (existing === undefined) {
      byID.set(f.action_id, { actionID: f.action_id, paths: [f.path] });
    } else {
      existing.paths.push(f.path);
    }
  }
  return [...byID.values()];
}

function buildTurnApprovalCard(
  payload: PermissionNeededPayload,
  files: readonly ApprovalFile[],
  onSelect: SelectFn,
): HTMLElement {
  const groups = groupByAction(files);
  // Default: keep everything. The turn's writes are ALREADY on disk (KAS holds the snapshots, not
  // the bytes), so "keep" is the state the workspace is in — an unchecked default would
  // misrepresent what unchecking costs.
  const keep = new Map<string, boolean>(groups.map((g) => [g.actionID, true]));

  const body = el(
    "div",
    { className: "approval-body" },
    el("strong", null, "Review this turn's changes"),
    el(
      "p",
      { className: "approval-hint" },
      fileCountLabel(files.length, groups.length) +
        " already written. Unchecked entries are rolled back.",
    ),
  );

  const list = el("ul", { className: "dock-file-list" });
  for (const g of groups) {
    list.appendChild(buildGroupRow(g, keep));
  }

  const acceptOpt = payload.options.find((o) => o.kind.startsWith("allow"));
  const rejectOpt = payload.options.find((o) => !o.kind.startsWith("allow"));

  const actions = el("div", { className: "approval-actions" });

  if (rejectOpt !== undefined) {
    const rejectAll = el(
      "button",
      { type: "button", className: "btn-small confirm-danger" },
      "Roll back all",
    );
    rejectAll.addEventListener("click", () => {
      // The reject OPTION is the whole-turn no. Sending the map as well would be redundant, and KAS
      // restores everything on this path anyway.
      onSelect({ optionID: rejectOpt.option_id });
    });
    actions.appendChild(rejectAll);
  }

  if (acceptOpt !== undefined) {
    const apply = el(
      "button",
      { type: "button", className: "btn-small confirm-allow" },
      "Keep selected",
    );
    apply.addEventListener("click", () => {
      // Every offered action gets an entry: an omitted id is a reject, so a sparse map would
      // silently roll back whatever it left out.
      const decisions: Record<string, boolean> = {};
      for (const g of groups) {
        decisions[g.actionID] = keep.get(g.actionID) ?? true;
      }
      onSelect({ optionID: acceptOpt.option_id, fileDecisions: decisions });
    });
    actions.appendChild(apply);
  }

  return el("div", { className: "dock-card dock-approval" }, body, list, actions);
}

function fileCountLabel(fileCount: number, groupCount: number): string {
  const files = fileCount === 1 ? "1 file" : `${String(fileCount)} files`;
  // Only mention actions when they differ from files, which happens exactly when a rename bundled
  // several paths under one id.
  return groupCount === fileCount ? files : `${files} in ${String(groupCount)} changes`;
}

function buildGroupRow(g: ActionGroup, keep: Map<string, boolean>): HTMLElement {
  const box = el("input", {
    type: "checkbox",
    className: "dock-file-check",
    "aria-label": `Keep ${g.paths.join(", ")}`,
  }) as HTMLInputElement;
  box.checked = true;

  const label = el("span", { className: "dock-file-paths" });
  for (const p of g.paths) {
    label.appendChild(el("span", { className: "dock-file-path" }, p));
  }
  // A multi-path group is one decision; say so rather than letting it read as a list the user can
  // split.
  if (g.paths.length > 1) {
    label.appendChild(
      el("span", { className: "dock-file-atomic" }, "moved together, one decision"),
    );
  }

  const diffBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "View diff",
      "aria-label": `View diff for ${g.paths[0] ?? ""}`,
    },
    iconEl(ICON_DIFF),
  );
  diffBtn.addEventListener("click", () => {
    const first = g.paths[0];
    if (first !== undefined) {
      // vs HEAD, because the write already landed: the working tree IS the proposed state, so git
      // shows exactly what this turn did.
      openChange(first);
    }
  });

  const row = el("li", { className: "dock-file-row" }, box, label, diffBtn);
  box.addEventListener("change", () => {
    keep.set(g.actionID, box.checked);
    row.classList.toggle("dock-file-rejected", !box.checked);
  });
  return row;
}

/** Turn a tool input (usually an object, sometimes raw JSON string or undefined) into a
 *  user-readable preview string. Returns "" if there is nothing meaningful to show — caller
 *  skips the preview block entirely. */
function formatInputPreview(input: unknown): string {
  if (input === undefined || input === null) {
    return "";
  }
  let text: string;
  if (typeof input === "string") {
    // kiro-cli sometimes sends rawInput as a pre-serialized JSON string; try to reparse for
    // pretty-print, fall back to the raw string.
    try {
      text = JSON.stringify(JSON.parse(input), null, 2);
    } catch {
      text = input;
    }
  } else if (typeof input === "object") {
    if (Object.keys(input).length === 0) {
      return "";
    }
    text = JSON.stringify(input, null, 2);
  } else {
    // eslint-disable-next-line @typescript-eslint/no-base-to-string
    text = String(input);
  }

  if (text.length > PREVIEW_CHAR_CAP) {
    const remainder = text.length - PREVIEW_CHAR_CAP;
    text = text.slice(0, PREVIEW_CHAR_CAP) + `\n… (${String(remainder)} more characters)`;
  }
  return text;
}

/** Copy for each reason the offer to persist a rule is withdrawn. A Record over the union rather
 *  than a switch, so adding a server-side code without deciding what to tell the reader is a
 *  type error here. */
const ALWAYS_ALLOW_UNAVAILABLE: Record<AlwaysAllowBlock, string> = {
  unparseable:
    "Always allow is unavailable. kiro-cli cannot parse this command, so a saved rule would never match it.",
};

/** The Always-allow slot when a saved rule could never match. */
function buildAlwaysAllowNote(blocked: AlwaysAllowBlock): HTMLElement {
  return el("div", { className: "always-allow-unavailable" }, ALWAYS_ALLOW_UNAVAILABLE[blocked]);
}

/** Characters that make a token unusable as the source of a pattern. */
const PATTERN_UNSAFE_RE = /[*?[\]{}!]/;

/** The preset patterns for one command's argv: base, base + flags, exact. Each is dropped when
 *  the tokens it is derived FROM carry glob syntax, which is why this is per preset rather than
 *  per row: the row, the Allow-once button and the custom-pattern input all survive, so refusing
 *  a derivation never dead-ends the reader. */
function derivePresets(effective: readonly string[]): string[] {
  const derivable = (tokens: readonly string[]): boolean =>
    !tokens.some((t) => PATTERN_UNSAFE_RE.test(t));
  const base = effective[0] ?? "";
  const presets: string[] = [];
  if (derivable([base])) {
    presets.push(`${base} *`);
  }
  if (effective.length > 1) {
    const flags = effective.slice(0, -1);
    const withFlags = flags.join(" ") + " *";
    if (derivable(flags) && withFlags !== `${base} *`) {
      presets.push(withFlags);
    }
    if (derivable(effective)) {
      presets.push(effective.join(" "));
    }
  }
  return presets;
}

/** Build the "Always allow..." expansion for shell commands: each preset persists a
 *  workspace-scope native allow rule (the same permissions.yaml Returns null when there is no
 *  allow option to approve with (the offer was never there to withdraw), and the note above when
 *  `blocked` says a saved rule could never match. */
function buildAlwaysAllowRow(
  command: string,
  options: readonly PermissionOption[],
  blocked: AlwaysAllowBlock | undefined,
  onSelect: SelectFn,
): HTMLElement | null {
  const allowOpt = options.find((o) => o.kind.startsWith("allow"));
  if (allowOpt === undefined) {
    return null;
  }
  if (blocked !== undefined) {
    return buildAlwaysAllowNote(blocked);
  }

  const trimmed = command.trim();
  const parts = trimmed.split(/\s+/);
  // Mirror the IDE: derive patterns from the real command, not the sudo wrapper (a `sudo *` allow
  // would be far broader than intended).
  const baseIdx = parts[0] === "sudo" && parts.length > 1 ? 1 : 0;
  const base = parts[baseIdx] ?? "";
  if (base === "") {
    return null;
  }
  const presets = derivePresets(parts.slice(baseIdx));

  const body = el("div", { className: "always-allow-body" });

  // Persist the allow rule, then approve. The approval WAITS for the rule write: guard_resource
  // makes the server refuse when an explicit ask rule covers this command (the allow would be
  // shadowed), and a failed write leaves the ask standing — the user can still Allow once.
  const persistThenApprove = async (pattern: string): Promise<void> => {
    const buttons = body.querySelectorAll("button");
    for (const b of buttons) {
      b.disabled = true;
    }
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "shell",
      effect: "allow",
      match: [pattern],
      guard_resource: trimmed,
    });
    if (res === null || res.error !== undefined) {
      // Write failed (or was refused): the action's toast explains why. Leave the permission
      // pending; re-enable for another choice.
      for (const b of buttons) {
        b.disabled = false;
      }
      return;
    }
    onSelect({ optionID: allowOpt.option_id });
  };

  for (const pattern of presets) {
    const row = el(
      "button",
      { type: "button", className: "always-allow-preset" },
      el("code", null, pattern),
    );
    row.addEventListener("click", () => {
      void persistThenApprove(pattern);
    });
    body.appendChild(row);
  }

  // Custom pattern input.
  const input = el("input", {
    type: "text",
    className: "chip-input",
    // The first surviving preset, never `${base} *`: with a metacharacter in the base that string
    // is the pattern the derivation just refused, and offering it as a placeholder hands the reader
    // the grant it declined to derive.
    placeholder: presets[0] ?? "command *",
    "aria-label": "Custom command pattern",
  }) as HTMLInputElement;
  const addBtn = el("button", { type: "button", className: "action-pill" }, "Add");
  addBtn.addEventListener("click", () => {
    const val = input.value.trim();
    if (val === "") {
      return;
    }
    void persistThenApprove(val);
  });
  body.appendChild(el("div", { className: "always-allow-custom" }, input, addBtn));

  return el(
    "details",
    { className: "always-allow-details" },
    el("summary", { className: "always-allow-summary" }, "Always allow\u2026"),
    body,
  );
}
