// Permission card: the agent is asking to do something, rendered in the interaction dock
// (decision-dock.ts owns the queue, the host and the settle -once guard; this file only builds DOM
// and reports the choice).

import type {
  AlwaysAllowBlock,
  ApprovalFile,
  PermissionConsent,
  PermissionNeededPayload,
  PermissionOption,
  ToolCall,
} from "./types.js";
import type { PermissionCommand, RejectionNote } from "./wire/types.gen.js";
import { decodeToolCallBulk } from "./wire/decoders.gen.js";
import { apiGetTyped } from "./api-client.js";
import { el } from "@cplieger/ui";
import { formatMCPToolName } from "./tool-schema.js";
import { openChange } from "./navigate.js";
import { openSetting } from "./settings-highlight.js";
import { get } from "./store.js";
import { ensureToolCallSig, peekToolCallSig } from "./store-signals.js";
import { payloadOf } from "./turns.js";
import { ICON_DIFF } from "./icons.js";
import { iconEl } from "./icon-el.js";

/** One answer to a permission ask. `fileDecisions` rides only a turn approval; `rejectionReason`
 *  only a deny of an ask that accepts a note; `alwaysResource` only an always answer, as the
 *  pattern its saved rule matches, and `alwaysScope` only an Allow for this chat answer. */
export interface PermissionAnswer {
  readonly optionID: string;
  readonly fileDecisions?: Record<string, boolean>;
  readonly rejectionReason?: string;
  readonly alwaysResource?: string;
  readonly alwaysScope?: "session";
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
  return buildToolPermissionCard(chatID, payload, onSelect);
}

/** Each mounted tool card's refill of what it shows from the store's tool call. */
const inputRefills = new WeakMap<HTMLElement, () => void>();

/** Re-read the store's tool call behind a card built by `buildPermissionCard` and refill, in
 *  place, the nodes showing its input; a card with none is left alone. The dock calls it while
 *  the card's chat changes, since the input can land after the card was built. */
export function refreshPermissionCard(card: HTMLElement): void {
  inputRefills.get(card)?.();
}

/** The store's tool call an ask is about, whose input is what the user is actually approving: the
 *  folded call, since KAS sends the full arguments only on an update. Walks back from the newest
 *  turn because the ask is about the turn in flight, and mints the call's signal so a later
 *  update's fold is kept for a card that is not mounted. */
function lookupToolCall(chatID: string, toolCallID: string): ToolCall | undefined {
  if (toolCallID === "") {
    return undefined;
  }
  const folded = peekToolCallSig(chatID, toolCallID);
  if (folded !== undefined) {
    return folded;
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
        return ensureToolCallSig(chatID, toolCallID, call).peek();
      }
    }
  }
  return undefined;
}

function buildToolPermissionCard(
  chatID: string,
  payload: PermissionNeededPayload,
  onSelect: SelectFn,
): HTMLElement {
  const title = payload.title ?? "Tool";
  const kind = payload.kind ?? "";
  const isModeSwitch = kind === "switch_mode";

  const body = el("div", { className: "approval-body" });
  const mcp = payload.mcp_tool;
  const command = isModeSwitch || mcp !== undefined ? undefined : payload.command;
  // The box below is the one statement of a command, whole; the title repeats it cut.
  const heading = isModeSwitch
    ? "Switch session mode"
    : mcp !== undefined
      ? formatMCPToolName(mcp.tool_name)
      : command !== undefined
        ? "Run this command"
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

  // Kiro's safety check did not clear the call; its reason says why a person is asked.
  const classifierNote = payload.classifier_note ?? "";
  if (classifierNote !== "") {
    body.appendChild(
      el("div", { className: "approval-origin approval-classifier" }, classifierNote),
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

  const inputSlot = el("div", { className: "approval-tool-input" });
  body.appendChild(inputSlot);
  if (command !== undefined) {
    appendCommandBox(body, command);
  }
  const refill = bindToolInput(inputSlot, chatID, toolCallID, command !== undefined);

  const noteKind = payload.rejection_note;
  const note = noteKind === undefined ? null : buildRejectionNote(noteKind);
  const actions = el("div", { className: "approval-actions" });
  const chooserSlot = el("div", { className: "always-choice-slot" });
  const consent = payload.consent;
  // Beside Allow for this chat and Always allow, KAS's bare "Allow" does not say it lasts once.
  const threeAllows =
    consent !== undefined && payload.options.some((o) => o.kind === "allow_always");
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
      threeAllows && opt.kind === "allow_once" ? "Allow once" : opt.name,
    );
    if (always && consent !== undefined) {
      if (opt.kind === "allow_always") {
        // KAS keeps a session-scope rule for the chat's live session only, so the third allow
        // answer is the always option with a narrower reach rather than an option of its own.
        const chat = el(
          "button",
          { type: "button", className: "btn-small confirm-allow" },
          "Allow for this chat",
        );
        chat.setAttribute("aria-expanded", "false");
        chat.addEventListener("click", () => {
          toggleAlwaysChooser(chooserSlot, chat, opt, consent, "session", onSelect);
        });
        actions.appendChild(chat);
      }
      btn.setAttribute("aria-expanded", "false");
      btn.addEventListener("click", () => {
        toggleAlwaysChooser(chooserSlot, btn, opt, consent, "user", onSelect);
      });
    } else {
      if (opt.kind === "reject_once" && noteKind === "plan_edits" && note !== null) {
        // The note turns this deny into a request for edits, so the label says so while it has text.
        note.addEventListener("input", () => {
          btn.textContent = note.value.trim() === "" ? opt.name : "Request edits";
        });
      }
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
  inputRefills.set(card, refill);
  return card;
}

/** Fill `slot` with what the store's tool call says about the ask's input, and return the refill
 *  that redoes it only when that changed: the cwd line beside a command, or else the input
 *  preview and a statement when part of it is not shown. A `has_full` call is a preview whose cut
 *  may be the input, so its persisted whole is fetched once and replaces it on landing. */
function bindToolInput(
  slot: HTMLElement,
  chatID: string,
  toolCallID: string,
  withCommand: boolean,
): () => void {
  let whole: { input: unknown } | null = null;
  let fetched = false;
  let shown: { input: unknown; gap: boolean } | null = null;
  const refill = (): void => {
    const call = lookupToolCall(chatID, toolCallID);
    const input = whole === null ? call?.input : whole.input;
    const gap =
      !withCommand &&
      (call?.truncated?.input_bytes !== undefined || (call?.has_full === true && whole === null));
    if (shown !== null && shown.input === input && shown.gap === gap) {
      return;
    }
    shown = { input, gap };
    slot.replaceChildren(...toolInputNodes(input, gap, withCommand));
    if (call?.has_full === true && !withCommand && !fetched) {
      fetched = true;
      void apiGetTyped(
        `/api/chats/${encodeURIComponent(chatID)}/tools/${encodeURIComponent(toolCallID)}`,
        decodeToolCallBulk,
      ).then((bulk) => {
        if (bulk !== null) {
          whole = { input: bulk.input };
          refill();
        }
      });
    }
  };
  refill();
  return refill;
}

function toolInputNodes(input: unknown, gap: boolean, withCommand: boolean): HTMLElement[] {
  if (withCommand) {
    const cwd = commandCwd(input);
    return cwd === ""
      ? []
      : [el("div", { className: "approval-origin approval-cwd" }, `in ${cwd}`)];
  }
  const nodes: HTMLElement[] = [];
  const preview = formatInputPreview(input);
  if (preview !== "") {
    nodes.push(el("pre", { className: "approval-input" }, preview));
  }
  if (gap) {
    nodes.push(
      el(
        "p",
        { className: "approval-hint approval-input-gap" },
        "Part of this tool's input is too large to show. Deny it unless you can vouch for all of it.",
      ),
    );
  }
  return nodes;
}

const REJECTION_NOTE_COPY: Record<RejectionNote, { placeholder: string; ariaLabel: string }> = {
  reason: {
    placeholder: "Optional: tell the agent why you're denying",
    ariaLabel: "Reason for denying (optional)",
  },
  plan_edits: {
    placeholder: "Optional: type the edits you want",
    ariaLabel: "Edits you want (optional)",
  },
};

function buildRejectionNote(kind: RejectionNote): HTMLTextAreaElement {
  const copy = REJECTION_NOTE_COPY[kind];
  const note = el("textarea", {
    className: "approval-reason",
    rows: 1,
    maxLength: REJECTION_REASON_MAX,
    placeholder: copy.placeholder,
  }) as HTMLTextAreaElement;
  note.setAttribute("aria-label", copy.ariaLabel);
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

/** The command an ask would run, whole; past the server's cap, its head and tail around a count of
 *  what is left out, so a suffix is always on screen. A count with no text is a command the connect
 *  snapshot dropped to fit its frame. */
function appendCommandBox(body: HTMLElement, command: PermissionCommand): void {
  const box = el("pre", { className: "approval-input" }, command.text);
  const omitted = command.omitted_bytes ?? 0;
  if (omitted <= 0) {
    body.appendChild(box);
    return;
  }
  box.append(
    el(
      "span",
      { className: "approval-omitted" },
      `… ${omitted.toLocaleString()} bytes not shown …`,
    ),
    command.tail ?? "",
  );
  body.append(
    box,
    el(
      "p",
      { className: "approval-hint" },
      command.text === "" && (command.tail ?? "") === ""
        ? "This command is not shown because too many approvals are waiting. Deny it unless you can vouch for it."
        : "The middle of this command is too long to show. Deny it unless you can vouch for all of it.",
    ),
  );
}

/** The directory the store's tool call runs its command in, "" when it names none. */
function commandCwd(input: unknown): string {
  if (typeof input !== "object" || input === null) {
    return "";
  }
  const cwd: unknown = (input as Record<string, unknown>)["cwd"];
  return typeof cwd === "string" ? displayLines(cwd) : "";
}

// C0 but LF, DEL, C1, the Bidi_Control runes and U+2028/9: textsafe's IsUnsafeSingleLine set minus LF.
const UNSAFE_BUT_LF =
  // eslint-disable-next-line no-control-regex
  /[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069\u2028\u2029]/g;

/** The server's displayLines for text the ask does not carry: KAS's ask holds no tool input, so
 *  the store's copy reaches this card untreated. CRLF becomes LF and every other unsafe rune a
 *  space, so a reversed span shows in reading order. */
function displayLines(s: string): string {
  return s.replaceAll("\r\n", "\n").replace(UNSAFE_BUT_LF, " ");
}

/** Turn a tool input (usually an object, sometimes raw JSON string or undefined) into a
 *  user-readable preview string, whole and defused: the box bounds its height and the server
 *  bounds the input. Returns "" if there is nothing meaningful to show — caller skips the
 *  preview block. */
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
  return displayLines(text);
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

/** Words a pattern must not generalise from: they run another command named later in the line, so
 *  `<wrapper> *` would allow every command, not one tool. */
const WRAPPER_COMMANDS = new Set([
  "sudo",
  "doas",
  "env",
  "time",
  "command",
  "nohup",
  "npx",
  "bunx",
  "uvx",
  "timeout",
  "nice",
  "xargs",
]);

/** `<first> <second>` pairs that run any program named after them, so `<first> <second> *` would
 *  allow every command; `<first> *` stays offered, named as what it is. */
const RUNNER_SUBCOMMANDS = new Set([
  "npm exec",
  "npm x",
  "pnpm exec",
  "pnpm dlx",
  "yarn dlx",
  "yarn exec",
  "bun x",
  "uv run",
  "uv tool",
  "pipx run",
]);

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

/** A second word a `<first> <second> *` row may name: the kiro-cli TUI's subcommand class. It can
 *  never hold the subject's `...` cut marker or a defused character, so the row names the raw words. */
const SUBCOMMAND_WORD = /^[A-Za-z][A-Za-z0-9_-]*$/;

/** The patterns an always answer can save for one ask, narrowest first: the exact subject, the
 *  kiro-cli TUI's `<first word> <subcommand> *` and `<first word> *` for a shell command or the
 *  server's folder for a path, then the whole capability. A resource two of these produce is
 *  offered once, under the broader name. The subject and folder are sent back verbatim: the
 *  server maps them to the raw text KAS asked about, which these display copies may have cut or
 *  defused. */
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
    const second = words[1] ?? "";
    if (
      SUBCOMMAND_WORD.test(second) &&
      !WRAPPER_COMMANDS.has(second) &&
      !RUNNER_SUBCOMMANDS.has(`${first} ${second}`)
    ) {
      covers.set(`${first} ${second} *`, `any ${first} ${second} command`);
    }
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

/** Where a chooser's rule is kept: the user permissions file, or the chat's live session. */
type AlwaysReach = "user" | "session";

const USER_RULE_HINT =
  "Saves the rule to your user permissions, which every kiro-cli session on this account reads, " +
  "not only these chats. Switches Permissions to Custom if another profile is selected.";

const SESSION_RULE_HINT =
  "kiro-cli keeps this rule for this chat's session only. Nothing is saved and Permissions stay " +
  "as they are. It may ask again after the chat closes or reloads.";

/** Open or close the pattern chooser an always option leads to. Saving is the second click, on a
 *  pattern, so a rule is never saved for a scope the reader did not see. */
function toggleAlwaysChooser(
  slot: HTMLElement,
  trigger: HTMLElement,
  opt: PermissionOption,
  consent: PermissionConsent,
  reach: AlwaysReach,
  onSelect: SelectFn,
): void {
  const open = slot.dataset["option"] === opt.option_id && slot.dataset["reach"] === reach;
  for (const other of trigger.parentElement?.querySelectorAll("[aria-expanded]") ?? []) {
    other.setAttribute("aria-expanded", "false");
  }
  slot.replaceChildren();
  delete slot.dataset["option"];
  delete slot.dataset["reach"];
  if (open) {
    return;
  }
  trigger.setAttribute("aria-expanded", "true");
  slot.dataset["option"] = opt.option_id;
  slot.dataset["reach"] = reach;
  const allow = opt.kind.startsWith("allow");
  const name = reach === "session" ? "Allow for this chat" : allow ? "Always allow" : "Always deny";
  const list = el("div", {
    className: "always-choice",
    role: "group",
    "aria-label": name,
  });
  list.appendChild(el("div", { className: "always-choice-title" }, `${name}:`));
  for (const p of alwaysPatterns(consent)) {
    const choice = el(
      "button",
      { type: "button", className: "always-choice-option" },
      el("code", null, p.resource),
      el("span", { className: "always-choice-covers" }, p.covers),
    );
    choice.addEventListener("click", () => {
      onSelect(
        reach === "session"
          ? { optionID: opt.option_id, alwaysResource: p.resource, alwaysScope: "session" }
          : { optionID: opt.option_id, alwaysResource: p.resource },
      );
    });
    list.appendChild(choice);
  }
  list.appendChild(
    el(
      "p",
      { className: "always-choice-hint" },
      reach === "session" ? SESSION_RULE_HINT : USER_RULE_HINT,
    ),
  );
  slot.appendChild(list);
}
