// Permission card: the agent is asking to do something, rendered in the interaction dock
// (decision-dock.ts owns the queue, the host and the settle -once guard; this file only builds DOM
// and reports the choice).

import type {
  AlwaysAllowBlock,
  ApprovalFile,
  PermissionConsent,
  PermissionNeededPayload,
  PermissionOption,
} from "./types.js";
import { el } from "@cplieger/reactive";
import { formatMCPToolName } from "./tool-schema.js";
import { openChange } from "./navigate.js";
import { openSetting } from "./settings-highlight.js";
import { get } from "./store.js";
import { payloadOf } from "./turns.js";
import { ICON_DIFF } from "./icons.js";
import { iconEl } from "./icon-el.js";

const PREVIEW_CHAR_CAP = 500;

/** One answer to a permission ask. `fileDecisions` rides only a turn approval; `rejectionReason`
 *  only a deny of an ask that accepts a note; `alwaysResource` only an always answer, as the
 *  pattern its saved rule matches. */
export interface PermissionAnswer {
  readonly optionID: string;
  readonly fileDecisions?: Record<string, boolean>;
  readonly rejectionReason?: string;
  readonly alwaysResource?: string;
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
  const subject = payload.consent?.subject ?? "";
  if (subject !== "" && subject !== title) {
    // A compound command asks once per part it has not approved; naming the part is what tells
    // one round from the last.
    body.appendChild(
      el(
        "div",
        { className: "approval-origin approval-subject" },
        round > 1
          ? `Approval ${String(round)} for this command, about `
          : "This approval is about ",
        el("code", null, subject),
      ),
    );
  } else if (round > 1) {
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
  const chooserSlot = el("div", { className: "always-choice-slot" });
  const consent = payload.consent;
  for (const opt of payload.options) {
    const always = ALWAYS_KINDS.has(opt.kind);
    if (always && consent === undefined) {
      // A saved rule needs to know what it is about, and KAS offers one only with consent.
      continue;
    }
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
    if (always && consent !== undefined) {
      btn.setAttribute("aria-expanded", "false");
      btn.addEventListener("click", () => {
        toggleAlwaysChooser(chooserSlot, btn, opt, consent, onSelect);
      });
    } else {
      btn.addEventListener("click", () => {
        // Only a reject_once answer carries the note: KAS ignores one anywhere else.
        const reason = opt.kind === "reject_once" ? (note?.value.trim() ?? "") : "";
        onSelect(
          reason !== ""
            ? { optionID: opt.option_id, rejectionReason: reason }
            : { optionID: opt.option_id },
        );
      });
    }
    actions.appendChild(btn);
  }

  const blocked = payload.always_allow_blocked;
  if (blocked !== undefined && !isModeSwitch && !adminAsk) {
    actions.appendChild(buildAlwaysAllowNote(blocked));
  }

  const card =
    note === null
      ? el("div", { className: "dock-card dock-permission" }, body, actions, chooserSlot)
      : el("div", { className: "dock-card dock-permission" }, body, note, actions, chooserSlot);
  if (isModeSwitch) {
    card.classList.add("mode-switch");
  }
  if (!isModeSwitch && !adminAsk) {
    card.appendChild(buildPolicyPointer());
  }
  return card;
}

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

/** A Record over the union rather than a switch, so adding a server-side code without deciding what
 *  to tell the reader is a type error here. */
const ALWAYS_ALLOW_UNAVAILABLE: Record<AlwaysAllowBlock, string> = {
  unparseable:
    "Always allow is unavailable. kiro-cli cannot parse this command, so a saved rule would never match it.",
};

/** The note standing in for an always answer when a saved rule could never match. */
function buildAlwaysAllowNote(blocked: AlwaysAllowBlock): HTMLElement {
  return el("div", { className: "always-allow-unavailable" }, ALWAYS_ALLOW_UNAVAILABLE[blocked]);
}

const ALWAYS_KINDS = new Set(["allow_always", "reject_always"]);

/** First words a pattern must not generalise from: the command they run is the next word. */
const WRAPPER_COMMANDS = new Set(["sudo", "doas", "env"]);

interface AlwaysPattern {
  readonly resource: string;
  readonly covers: string;
}

/** What `*` covers: a rule matches on capability and resource only, so a wildcard reaches every
 *  tool asking for that capability, not only the one asking now. */
const WILDCARD_COVERS = new Map([
  ["shell", "any command"],
  ["fs_read", "any file read, by any tool"],
  ["fs_write", "any file write, by any tool"],
]);

/** KAS saves a resource as a Cedar `like` pattern whose one wildcard is `*` (2.28's `qFc`), so a
 *  subject carrying one grants every match, not only the command or path asked about now. */
function isPattern(resource: string): boolean {
  return resource.includes("*");
}

/** The patterns an always answer can save for one ask, narrowest first: the exact subject, the
 *  kiro-cli TUI's `<first word> *` for a shell command or the server's folder for a path, then
 *  the whole capability. A resource two of these produce is offered once, under the broader
 *  name. The subject and folder are sent back verbatim: the server maps them to the raw text KAS
 *  asked about, which these display copies may have cut or defused. */
function alwaysPatterns(consent: PermissionConsent): AlwaysPattern[] {
  const subject = consent.subject.trim();
  const covers = new Map<string, string>();
  if (subject !== "" && subject !== "*") {
    covers.set(subject, isPattern(subject) ? "anything this pattern matches" : "only this");
  }
  const shell = consent.capability === "shell";
  const words = subject.split(/\s+/);
  const first = words[0] ?? "";
  if (
    shell &&
    words.length > 1 &&
    !isPattern(first) &&
    !first.includes("=") &&
    !WRAPPER_COMMANDS.has(first)
  ) {
    covers.set(`${first} *`, `any ${first} command`);
  }
  const folder = consent.folder?.trim() ?? "";
  if (folder !== "") {
    covers.set(folder, "anything in this folder");
  }
  covers.set(
    "*",
    WILDCARD_COVERS.get(consent.capability) ?? `any ${consent.capability} request, by any tool`,
  );
  return [...covers].map(([resource, label]) => ({ resource, covers: label }));
}

/** Open or close the pattern chooser an always option leads to. Saving is the second click, on a
 *  pattern, so a rule is never saved for a scope the reader did not see. */
function toggleAlwaysChooser(
  slot: HTMLElement,
  trigger: HTMLElement,
  opt: PermissionOption,
  consent: PermissionConsent,
  onSelect: SelectFn,
): void {
  const openFor = slot.dataset["option"];
  for (const other of trigger.parentElement?.querySelectorAll("[aria-expanded]") ?? []) {
    other.setAttribute("aria-expanded", "false");
  }
  slot.replaceChildren();
  delete slot.dataset["option"];
  if (openFor === opt.option_id) {
    return;
  }
  trigger.setAttribute("aria-expanded", "true");
  slot.dataset["option"] = opt.option_id;
  const allow = opt.kind.startsWith("allow");
  const list = el("div", {
    className: "always-choice",
    role: "group",
    "aria-label": allow ? "Always allow" : "Always deny",
  });
  list.appendChild(
    el("div", { className: "always-choice-title" }, allow ? "Always allow:" : "Always deny:"),
  );
  for (const p of alwaysPatterns(consent)) {
    const choice = el(
      "button",
      { type: "button", className: "always-choice-option" },
      el("code", null, p.resource),
      el("span", { className: "always-choice-covers" }, p.covers),
    );
    choice.addEventListener("click", () => {
      onSelect({ optionID: opt.option_id, alwaysResource: p.resource });
    });
    list.appendChild(choice);
  }
  list.appendChild(
    el(
      "p",
      { className: "always-choice-hint" },
      "Saves the rule to your user permissions, which every kiro-cli session on this account reads, " +
        "not only these chats. Switches Permissions to Custom if another profile is selected.",
    ),
  );
  slot.appendChild(list);
}
