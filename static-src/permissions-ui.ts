// "Permissions" section in the Settings panel.

import { patchSettings } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { maybeEl } from "./dom.js";
import { apiGet } from "./api-client.js";
import { buildChip } from "./chip.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { editNativeRule, explainPolicy, setSecurityProfile } from "./actions/permissions.js";
import { reconcile } from "./reconcile.js";
import { paintIfChanged, sigChanged } from "./paint-sig.js";
import { join } from "@cplieger/keyenc";
import { onSSE } from "./bus.js";
import { confirm } from "./confirm.js";
import type { PolicyView, PolicyRule, SecurityProfile } from "./types.js";
// `types.js` re-exports the Policy* shapes the panel renders and not this one, which only an
// explain result carries.
import type { PolicyRuleCore } from "./wire/types.gen.js";

/** One row of the policy table: a scope's heading, or one rule under it. */
type PolicyEntry =
  | { readonly kind: "label"; readonly scope: string }
  | { readonly kind: "rule"; readonly scope: string; readonly rule: PolicyRule };

/** A row's stable IDENTITY — everything about a rule except its effect, which is the one field
 *  an edit moves and the one the row is repainted for. `keyenc` join because a capability, a
 *  glob and a source path are all free-form text, so a "|"-joined key would let one field's
 *  content impersonate a boundary and two distinct rules collide into one row. */
function policyEntryKey(e: PolicyEntry): string {
  if (e.kind === "label") {
    return join("label", e.scope);
  }
  return join(
    "rule",
    e.scope,
    e.rule.capability,
    e.rule.source,
    join(...(e.rule.match ?? [])),
    join(...(e.rule.exclude ?? [])),
  );
}
import { ICON_CLOSE } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { el } from "@cplieger/reactive";

// Agent ignore files: the floor, and the entry rule KAS enforces.

/** Sent to kiro-cli whatever the user's list holds, so a `.kiroignore` at the workspace root is
 *  always enforced. Mirrors `settings.AgentIgnoreFloor`; the panel renders it as a fixed row the
 *  user cannot remove. */
export const AGENT_IGNORE_FLOOR = ".kiroignore";

/** The reason this entry cannot be ADDED to the list, or null when it can. Every arm but one
 *  mirrors `settings.ValidAgentIgnoreEntry`, because an entry KAS refuses is one it SKIPS —
 *  offering to add one would claim a filter the agent never applies. */
export function agentIgnoreEntryError(entry: string): string | null {
  if (entry === "") {
    return "An entry cannot be empty.";
  }
  if (entry === AGENT_IGNORE_FLOOR) {
    return "Always enforced. It is already in the list.";
  }
  if (entry !== entry.trim()) {
    return "Leading or trailing whitespace.";
  }
  if (entry === ".") {
    return '"." is not a filename.';
  }
  if (/[/\\]/.test(entry) || entry.includes("..")) {
    return "Name a file at the workspace root, not a path.";
  }
  if (/[*?[\]{}]/.test(entry)) {
    return "Put a glob pattern inside an ignore file, not in this list.";
  }
  return null;
}

// PermissionsUIController — encapsulates all module-level state.

class PermissionsUIController {
  private ignoreFiles: string[] = [];

  initPermissions(initial: EffectiveSettings): void {
    // Supervised-mode default for new chats. (Tool-call approval itself is the native Cedar policy,
    // rendered by NativePolicyController below.)
    const supCheckbox = maybeEl<HTMLInputElement>("supervised-default-checkbox");
    if (supCheckbox !== null) {
      supCheckbox.checked = initial.supervised_default;
      supCheckbox.addEventListener("change", () => {
        void patchSettings({ supervised_default: supCheckbox.checked });
      });
    }
    // Whether a SCHEDULED run's tool request is approved or refused when nobody answers it. Its own
    // switch rather than a read of the policy above, because approving while watching is a
    // different consent from approving unattended.
    const schedCheckbox = maybeEl<HTMLInputElement>("scheduled-auto-approve-checkbox");
    if (schedCheckbox !== null) {
      schedCheckbox.checked = initial.scheduled_auto_approve;
      schedCheckbox.addEventListener("change", () => {
        void patchSettings({ scheduled_auto_approve: schedCheckbox.checked });
      });
    }
    this.initAgentIgnoreUI(initial);
  }

  private initAgentIgnoreUI(initial: EffectiveSettings): void {
    this.renderIgnoreFloor();
    // No `?? []`. The row is AUTHORITATIVE on write — an add or a remove sends whatever it holds —
    // so a fallback silently persists an empty list whenever the field is missing, discarding
    // entries the server was enforcing.
    this.ignoreFiles = [...initial.agent_ignore_files];
    this.renderIgnoreChips();

    const input = maybeEl<HTMLInputElement>("agent-ignore-input");
    const addBtn = maybeEl<HTMLButtonElement>("agent-ignore-add");
    if (input === null || addBtn === null) {
      return;
    }
    // A REJECTED value disables Add and says which rule refused it; an EMPTY one leaves the button
    // alone, so it is never disabled at rest.
    const syncAddState = (): void => {
      const val = input.value.trim();
      const reason = val === "" ? null : agentIgnoreEntryError(val);
      addBtn.disabled = reason !== null;
      if (reason === null) {
        addBtn.removeAttribute("data-tooltip");
      } else {
        addBtn.setAttribute("data-tooltip", reason);
      }
    };
    const submit = (): void => {
      const val = input.value.trim();
      if (val === "" || agentIgnoreEntryError(val) !== null) {
        return;
      }
      if (this.ignoreFiles.includes(val)) {
        input.value = "";
        syncAddState();
        return;
      }
      this.ignoreFiles.push(val);
      void patchSettings({ agent_ignore_files: this.ignoreFiles });
      input.value = "";
      // Refocus for repeat entry (adding several files in a row).
      input.focus();
      this.renderIgnoreChips();
      syncAddState();
    };
    input.addEventListener("input", syncAddState);
    addBtn.addEventListener("click", submit);
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submit();
      }
    });
  }

  /** The always-enforced floor, above the user's own chips. A fixed row rather than a chip:
   *  `buildChip` requires an `onRemove` and this entry has none. */
  private renderIgnoreFloor(): void {
    const host = maybeEl("agent-ignore-floor");
    if (host === null) {
      return;
    }
    host.replaceChildren(
      el(
        "span",
        { class: "chip mono chip-fixed" },
        el("code", { class: "chip-label" }, AGENT_IGNORE_FLOOR),
        el("span", { class: "chip-note" }, "always enforced"),
      ),
    );
  }

  private renderIgnoreChips(): void {
    const container = maybeEl("agent-ignore-chips");
    const hint = maybeEl("agent-ignore-empty-hint");
    if (hint !== null) {
      hint.classList.toggle("hidden", this.ignoreFiles.length > 0);
    }
    if (container === null) {
      return;
    }
    // Ignore entries are immutable strings, so a key-by-entry, mount-only reconcile is sufficient
    // (no update fn needed): add/remove touches only the changed chip and preserves the rest. The
    // empty state is a sibling element (agent-ignore-empty-hint), toggled above.
    reconcile(container, this.ignoreFiles, {
      key: (entry) => entry,
      mount: (entry) =>
        buildChip({
          label: entry,
          code: true,
          chipClass: "chip mono",
          onRemove: () => {
            this.ignoreFiles = this.ignoreFiles.filter((f) => f !== entry);
            void patchSettings({ agent_ignore_files: this.ignoreFiles });
            this.renderIgnoreChips();
          },
        }),
    });
  }
}

// Singleton instance — internal to the module.
const controller = new PermissionsUIController();

// NativePolicyController — the native (Cedar) policy VIEW + conservative file-writing editor.

const NATIVE_SCOPE_ORDER = ["kiro", "administration", "user", "workspace", "agent", "session"];
const NATIVE_SCOPE_LABEL: Record<string, string> = {
  kiro: "Kiro built-in, read-only",
  administration: "Administration, read-only",
  user: "User, global",
  workspace: "Workspace",
  agent: "Agent, current mode, read-only",
  session: "Session, runtime, read-only",
};

function splitGlobs(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** The Custom profile's id, and the one id the client must know by name: it is the state the
 *  table becomes editable in, which is a UI fact rather than a policy one. Every other profile
 *  is rendered from whatever the server sent. */
const CUSTOM_PROFILE = "custom";

/** The loosest profile's id, needed only to decide which selection earns the extra confirm. The
 *  ladder's ORDER is the server's, so this is a hint the picker checks rather than a policy it
 *  enforces — the server would grant the same set either way, and a rename upstream costs the
 *  extra confirm rather than correctness. */
const LOOSEST_PROFILE_HINT = "unrestricted";

/** Human labels, keyed by profile id. Prose for a person, so it lives on the client rather than
 *  travelling with the ladder; an id the client has no label for falls back to the id, which is
 *  ugly but true. */
function profileLabel(id: string): string {
  switch (id) {
    case "guarded":
      return "Guarded";
    case "read-only":
      return "Read-only";
    case "trusted":
      return "Trusted";
    case "unrestricted":
      return "Unrestricted";
    case CUSTOM_PROFILE:
      return "Custom";
    default:
      return id;
  }
}

/** What each profile grants, in the terms a reader decides on. Read-only names that it reads
 *  OUTSIDE the workspace. */
function profileDescription(id: string): string {
  switch (id) {
    case "guarded":
      return "Reads files in this workspace. Everything else asks.";
    case "read-only":
      return "Also reads any file on this machine, runs read-only commands, and reaches the web. That includes files outside this workspace, so an SSH key or a sibling project is readable with no prompt. Every change asks.";
    case "trusted":
      return "Also edits files in this workspace and runs everyday development commands. Destructive and irreversible ones still ask, including git push, reset and clean.";
    case "unrestricted":
      return "Never asks, including before installing a power. Kiro still protects its own settings and still asks before writing .git, .kiro/agents and .kiro/hooks.";
    case CUSTOM_PROFILE:
      return "Your own rules, edited in the table below. Nothing is granted that you do not add.";
    default:
      return "";
  }
}

/** A rule's globs in one vocabulary, shared by the table's rows and the explain box so a rule
 *  reads the same in both: a match is prefixed, an exclusion negated. */
function globLabels(r: { readonly match?: string[]; readonly exclude?: string[] }): string[] {
  return [...(r.match ?? []).map((m) => "+" + m), ...(r.exclude ?? []).map((e) => "\u2212" + e)];
}

/** One line naming a rule: what it grants, and over which globs. */
function ruleSummary(r: PolicyRuleCore): string {
  return [`${r.capability} ${r.effect}`, ...globLabels(r)].join(" ");
}

function shortSource(src: string): string {
  if (src === "") {
    return "";
  }
  const parts = src.split("/");
  return parts.length <= 2 ? src : ".../" + parts.slice(-2).join("/");
}

class NativePolicyController {
  private writable = new Set<string>();
  private ctrl: AbortController | null = null;
  /** The profile ladder and the id in force, both straight from the policy view. Never local
   *  constants: the ladder decides what one click grants, and policyfile owns it. */
  private profiles: SecurityProfile[] = [];
  private activeProfile = "";
  /** A transient line under the picker: the outcome of a selection, or a note that Custom is
   *  empty. Carried across the refetch a selection ends in, because that refetch repaints this
   *  same line. */
  private profileNote = "";
  private profileNoteIsError = false;
  /** The rules the last completed read-back reported, kept so the picker's own line can be
   *  repainted without spending another request on the bridge. */
  private lastRules: PolicyRule[] = [];

  init(): void {
    const addBtn = maybeEl<HTMLButtonElement>("native-rule-add");
    if (addBtn === null) {
      return; // permissions panel not present in this build
    }
    addBtn.addEventListener("click", () => {
      void this.addRule();
    });
    maybeEl<HTMLButtonElement>("security-profile-customize")?.addEventListener("click", () => {
      void this.customize();
    });
    maybeEl<HTMLButtonElement>("native-explain-run")?.addEventListener("click", () => {
      void this.runExplain();
    });
    registerCleanup(bindLoadingState("permissions.edit_native_rule", addBtn));
    registerCleanup(
      onSSE("permissions_changed", () => {
        void this.load();
      }),
    );
    registerCleanup(
      onSSE("policy_error", (_chatID, payload) => {
        const msgs = (payload.errors ?? []).map((e) => e.message).filter((m) => m !== "");
        this.showStatus(
          msgs.length > 0 ? "Policy error: " + msgs.join("; ") : "Policy error.",
          true,
        );
      }),
    );
    registerCleanup(() => {
      this.cancel();
    });
  }

  /** Fetch + render the policy view. Public for the lazy tab loader. */
  refresh(): void {
    void this.load();
  }

  cancel(): void {
    this.ctrl?.abort();
    this.ctrl = null;
  }

  private async load(): Promise<void> {
    this.ctrl?.abort();
    this.ctrl = new AbortController();
    const { signal } = this.ctrl;
    const data = await apiGet<PolicyView>("/api/permissions", signal);
    if (signal.aborted || data === null) {
      return;
    }
    // A completed read-back supersedes any note a past selection left. Without this, a failure
    // message outlived the thing it described and got repainted by every later refetch, including
    // ones triggered by another device.
    this.profileNote = "";
    this.profileNoteIsError = false;
    this.writable = new Set(data.writable_scopes);
    this.profiles = data.profiles;
    this.activeProfile = data.profile;
    this.lastRules = data.rules;
    this.populateCapabilities(data.capabilities);
    if (data.available) {
      this.showStatus("", false);
    } else {
      this.showStatus(
        "Live policy unavailable because no session is active. Showing your saved user and workspace rules only.",
        false,
      );
    }
    this.render(data.rules);
    this.renderProfiles();
    // AFTER render(), which rebuilds the rows this locks: locking first would disable controls that
    // are about to be replaced by fresh, enabled ones.
    this.renderProfileState(data.rules);
  }

  // A selection writes no rule, so the user's own rules survive it. Outside Custom the table is
  // read-only, or a hand-edit would be a second posture beside the picker's. Customize copies the
  // presets in force into the table; picking Custom from the list copies nothing.

  /** Render the picker. One radio per profile, its own description under the label, because what
   *  separates two of them is a sentence rather than a word. */
  private renderProfiles(): void {
    const host = maybeEl("security-profile-list");
    if (host === null) {
      return;
    }
    // The loosest rung is last in the ladder, and the ladder's ORDER is the server's (Custom sits
    // after it). Deriving "loosest" from the position rather than from the id is what keeps this
    // from hardcoding a profile name the server owns.
    const loosest = this.profiles[this.profiles.length - 2]?.id ?? "";
    const paint = (row: HTMLElement, p: SecurityProfile): void => {
      row.classList.toggle("profile-row-loosest", p.id === loosest);
      const input = row.querySelector<HTMLInputElement>(":scope > input");
      if (input !== null) {
        input.checked = p.id === this.activeProfile;
      }
    };
    reconcile(host, this.profiles, {
      key: (p: SecurityProfile) => p.id,
      mount: (p: SecurityProfile) => {
        const input = el("input", { type: "radio", className: "" }) as HTMLInputElement;
        input.type = "radio";
        input.name = "security-profile";
        input.value = p.id;
        input.addEventListener("change", () => {
          if (input.checked) {
            void this.selectProfile(p.id);
          }
        });
        const row = el("label", { className: "perm-mode profile-row" }, input);
        row.append(el("span", {}, profileLabel(p.id)));
        row.append(el("p", { className: "section-hint profile-desc" }, profileDescription(p.id)));
        paint(row, p);
        return row;
      },
      update: paint,
    });
  }

  /** Paint the Customize button and the status line, and lock the table outside Custom. The
   *  button is present only on a named profile, because on Custom you are already there and a
   *  control that does nothing teaches a reader to distrust every other one. */
  private renderProfileState(rules: PolicyRule[]): void {
    const custom = this.activeProfile === CUSTOM_PROFILE;
    maybeEl("security-profile-customize")?.classList.toggle("hidden", custom);
    this.lockPolicyTable(!custom);
    const status = maybeEl("security-profile-status");
    if (status === null) {
      return;
    }
    let text = this.profileNote;
    if (text === "" && custom && rules.filter((r) => this.writable.has(r.scope)).length === 0) {
      // An empty Custom policy is not a neutral state and the picker has to say so: Custom sends no
      // presets, so with no rules of its own the agent asks before it may even read a file. Saying
      // nothing here would leave that to be discovered one prompt at a time.
      text =
        "Custom, with no rules. Every capability asks, including reading a file. " +
        "Add rules below, or pick a profile to start from one.";
    }
    status.textContent = text;
    status.classList.toggle("hidden", text === "");
    status.classList.toggle("native-policy-status-error", this.profileNoteIsError);
  }

  /** Disable every editing affordance in the Active policy table. */
  private lockPolicyTable(locked: boolean): void {
    maybeEl("native-policy-section")?.classList.toggle("native-policy-locked", locked);
    const scope = maybeEl("native-policy-section");
    if (scope === null) {
      return;
    }
    for (const control of scope.querySelectorAll<
      HTMLInputElement | HTMLButtonElement | HTMLSelectElement
    >(
      "#native-policy-list select, #native-policy-list button, [data-rule-form='add'] input, [data-rule-form='add'] select, [data-rule-form='add'] button",
    )) {
      control.disabled = locked;
    }
  }

  /** Select a profile. The leaving-Custom confirm says the user's own rules SURVIVE, because a
   *  grant outliving a narrowing is the surprise this screen can produce. */
  private async selectProfile(id: string): Promise<void> {
    if (id === this.activeProfile) {
      return;
    }
    this.profileNote = "";
    this.profileNoteIsError = false;
    const leavingCustom = this.activeProfile === CUSTOM_PROFILE;
    const editable = this.lastRules.filter((r) => this.writable.has(r.scope)).length;
    if (leavingCustom && editable > 0) {
      const ok = await confirm(
        `Switch to ${profileLabel(id)}? Your ${String(editable)} custom ` +
          `${editable === 1 ? "rule STAYS" : "rules STAY"} on disk and ${editable === 1 ? "keeps" : "keep"} ` +
          `applying alongside that profile, so the agent can end up with more than its name suggests. ` +
          `Remove them in the table first if that is not what you want.`,
        "Switch anyway",
        "destructive",
      );
      if (!ok) {
        void this.load();
        return;
      }
    }
    if (id === LOOSEST_PROFILE_HINT && !(await this.confirmLoosest())) {
      void this.load();
      return;
    }
    await this.applyProfile(id, false);
  }

  /** The extra confirm the loosest profile earns. It is the one that grants `power`, so a power
   *  installed afterwards runs its author's code at this privilege with nothing asking, and it
   *  is also the one whose name invites a click. */
  private confirmLoosest(): Promise<boolean> {
    return confirm(
      `Allow every capability without asking? This includes "power", so a power you ` +
        `install runs its author's code at your privilege with no prompt. It does NOT ` +
        `silence every prompt: Kiro still refuses writes to its own settings ` +
        `directories and still asks before writing .git, .kiro/agents, .kiro/hooks ` +
        `and .vscode.`,
      "Allow everything",
      "destructive",
    );
  }

  /** Copy the profile in force into the editable table and switch to Custom. The starting-point
   *  door, as opposed to the blank one. */
  private async customize(): Promise<void> {
    this.profileNote = "";
    this.profileNoteIsError = false;
    await this.applyProfile(CUSTOM_PROFILE, true);
  }

  /** One writer for both doors. */
  private async applyProfile(id: string, seed: boolean): Promise<void> {
    this.setPickerInert(true);
    try {
      const res = await setSecurityProfile.dispatch({ profile: id, seed });
      // Repaint from the server FIRST, so the picker shows what is actually in force, then write
      // the failure over it. The other order loses the message: load() clears the note by design,
      // so a note set before it never survives.
      await this.load();
      if (res === null || res.error !== undefined) {
        this.profileNote = res?.error ?? "The profile was not changed.";
        this.profileNoteIsError = true;
        this.renderProfileState(this.lastRules);
      }
    } finally {
      this.setPickerInert(false);
    }
  }

  /** Make every control that can START a selection inert for one write: Customize snapshots the
   *  user file first, so an overlapping one could restore the wrong rules. Re-queried on
   *  release, so a radio load() mounted meanwhile is enabled. */
  private setPickerInert(inert: boolean): void {
    const btn = maybeEl<HTMLButtonElement>("security-profile-customize");
    if (btn !== null) {
      btn.disabled = inert;
    }
    const host = maybeEl("security-profile-list");
    if (host === null) {
      return;
    }
    for (const radio of host.querySelectorAll<HTMLInputElement>("input[name='security-profile']")) {
      radio.disabled = inert;
    }
  }

  /** Fill both capability pickers, rebuilding only when the SET moved. */
  private populateCapabilities(caps: string[]): void {
    for (const id of ["native-rule-capability", "native-explain-capability"]) {
      const sel = maybeEl<HTMLSelectElement>(id);
      if (sel === null || caps.length === 0) {
        continue;
      }
      const chosen = sel.value;
      const painted = paintIfChanged(sel, caps, () =>
        caps.map((c) => {
          const opt = el("option", { value: c }, c) as HTMLOptionElement;
          opt.value = c; // set the property explicitly so the value is usable everywhere
          return opt;
        }),
      );
      if (painted && caps.includes(chosen)) {
        sel.value = chosen;
      }
    }
  }

  private render(rules: PolicyRule[]): void {
    const emptyHint = maybeEl("native-policy-empty-hint");
    if (emptyHint !== null) {
      emptyHint.classList.toggle("hidden", rules.length > 0);
    }
    const list = maybeEl("native-policy-list");
    if (list === null) {
      return;
    }
    const groups = new Map<string, PolicyRule[]>();
    for (const r of rules) {
      const g = groups.get(r.scope) ?? [];
      g.push(r);
      groups.set(r.scope, g);
    }
    const order = [
      ...NATIVE_SCOPE_ORDER,
      ...[...groups.keys()].filter((s) => !NATIVE_SCOPE_ORDER.includes(s)),
    ];
    // ONE flat keyed list over both element kinds — a scope's label and the rules under it — so a
    // row is preserved without each group boundary needing a container. A writable row holds a
    // `<select>`, and this runs on every `permissions_changed` frame plus after every edit.
    const rows: PolicyEntry[] = [];
    for (const scope of order) {
      const grp = groups.get(scope);
      if (grp === undefined || grp.length === 0) {
        continue;
      }
      rows.push({ kind: "label", scope });
      for (const r of grp) {
        rows.push({ kind: "rule", scope, rule: r });
      }
    }
    reconcile(list, rows, {
      key: (e: PolicyEntry) => policyEntryKey(e),
      mount: (e: PolicyEntry) => {
        if (e.kind === "label") {
          return el(
            "div",
            { className: "native-policy-scope-label" },
            NATIVE_SCOPE_LABEL[e.scope] ?? e.scope,
          );
        }
        const row = this.ruleRow(e.rule);
        // Recorded at mount, or the first update repaints a row that has not moved.
        sigChanged(row, [e.rule.effect]);
        return row;
      },
      // The EFFECT is the only field not in the key, so it is the only one a kept row can be stale
      // about. It decides the row's class and its select's value, so the repaint is the row's
      // children plus that class.
      update: (row: HTMLElement, e: PolicyEntry) => {
        if (e.kind !== "rule" || !sigChanged(row, [e.rule.effect])) {
          return;
        }
        row.className = `native-rule native-rule-${e.rule.effect}`;
        row.replaceChildren(...Array.from(this.ruleRow(e.rule).childNodes));
      },
    });
  }

  private ruleRow(r: PolicyRule): HTMLElement {
    const row = el("div", { className: `native-rule native-rule-${r.effect}` });
    if (this.writable.has(r.scope)) {
      row.append(this.effectSelect(r));
    } else {
      row.append(el("span", { className: `native-rule-effect eff-${r.effect}` }, r.effect));
    }
    row.append(el("span", { className: "native-rule-cap mono" }, r.capability));
    const globs = globLabels(r);
    if (globs.length > 0) {
      row.append(el("span", { className: "native-rule-globs mono" }, globs.join("  ")));
    }
    const src = el("span", { className: "native-rule-src" }, shortSource(r.source));
    if (r.source !== "") {
      // `data-tooltip` rather than a native `title`: the delegated controller styles it like every
      // other hover in the app and republishes it as an accessible description.
      src.setAttribute("data-tooltip", r.source);
      src.setAttribute("aria-label", `Defined in ${r.source}`);
    }
    row.append(src);
    if (this.writable.has(r.scope)) {
      const rm = el(
        "button",
        { type: "button", className: "icon-btn native-rule-remove" },
        iconEl(ICON_CLOSE),
      );
      rm.setAttribute("aria-label", "Remove rule");
      rm.setAttribute("data-tooltip", "Remove rule");
      rm.addEventListener("click", () => {
        void this.removeRule(r);
      });
      row.append(rm);
    }
    return row;
  }

  /** Build the in-place effect editor for a writable rule: a select styled */
  private effectSelect(r: PolicyRule): HTMLSelectElement {
    const sel = el("select", {
      className: `native-rule-effect eff-${r.effect}`,
    }) as HTMLSelectElement;
    for (const eff of ["allow", "ask", "deny"]) {
      const opt = el("option", { value: eff }, eff) as HTMLOptionElement;
      opt.value = eff;
      opt.selected = eff === r.effect;
      sel.append(opt);
    }
    sel.setAttribute("aria-label", `Effect for the ${r.capability} rule`);
    sel.setAttribute("data-tooltip", "Change this rule's effect");
    sel.addEventListener("change", () => {
      void this.updateEffect(r, sel);
    });
    return sel;
  }

  private async updateEffect(r: PolicyRule, sel: HTMLSelectElement): Promise<void> {
    const next = sel.value;
    if (next === r.effect) {
      return;
    }
    const RANK: Record<string, number> = { deny: 2, ask: 1, allow: 0 };
    let confirmFlag = false;
    if ((RANK[next] ?? 0) < (RANK[r.effect] ?? 0)) {
      const ok = await confirm(
        `Change this "${r.capability}" rule from ${r.effect} to ${next}? That WIDENS what the agent is allowed to do.`,
        "Widen rule",
      );
      if (!ok) {
        sel.value = r.effect;
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "update",
      scope: r.scope as "user" | "workspace",
      capability: r.capability,
      effect: r.effect,
      new_effect: next,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    } else {
      sel.value = r.effect;
    }
  }

  private async addRule(): Promise<void> {
    const scope = (maybeEl<HTMLSelectElement>("native-rule-scope")?.value ?? "workspace") as
      "user" | "workspace";
    const capability = maybeEl<HTMLSelectElement>("native-rule-capability")?.value ?? "";
    const effect = maybeEl<HTMLSelectElement>("native-rule-effect")?.value ?? "ask";
    const match = splitGlobs(maybeEl<HTMLInputElement>("native-rule-match")?.value ?? "");
    const exclude = splitGlobs(maybeEl<HTMLInputElement>("native-rule-exclude")?.value ?? "");
    if (capability === "") {
      return;
    }
    const res = await editNativeRule.dispatch({
      op: "add",
      scope,
      capability,
      effect,
      match,
      exclude,
    });
    if (res !== null && res.error === undefined) {
      const mi = maybeEl<HTMLInputElement>("native-rule-match");
      const xi = maybeEl<HTMLInputElement>("native-rule-exclude");
      if (xi !== null) {
        xi.value = "";
      }
      if (mi !== null) {
        mi.value = "";
        // Refocus for repeat entry (adding several rules in a row).
        mi.focus();
      }
      void this.load();
    }
  }

  private async removeRule(r: PolicyRule): Promise<void> {
    const scope = r.scope as "user" | "workspace";
    let confirmFlag = false;
    if (r.effect === "deny") {
      const ok = await confirm(
        `Remove this deny rule for "${r.capability}"? That WIDENS what the agent is allowed to do.`,
        "Remove deny rule",
      );
      if (!ok) {
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "remove",
      scope,
      capability: r.capability,
      effect: r.effect,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    }
  }

  private async runExplain(): Promise<void> {
    const capability = maybeEl<HTMLSelectElement>("native-explain-capability")?.value ?? "";
    const resource = (maybeEl<HTMLInputElement>("native-explain-resource")?.value ?? "").trim();
    const out = maybeEl("native-explain-result");
    if (capability === "" || out === null) {
      return;
    }
    // Shell decisions are always resource-scoped (there is no command-independent shell decision to
    // simulate).
    if (capability === "shell" && resource === "") {
      out.textContent = "Enter a command to test the shell capability.";
      return;
    }
    const res = await explainPolicy.dispatch({ capability, resource });
    if (res === null) {
      out.textContent = "Could not evaluate. Check that a chat session is active.";
      return;
    }
    // The control is labelled "why?", so the matched RULE is the answer: a scope names only the
    // layer that decided, and the kiro layer holds dozens of globs the reader would then have to
    // find by eye.
    const parts = [`Effect: ${res.effect}${res.is_explicit_ask ? " (explicit ask)" : ""}`];
    if (res.matched_rule !== undefined) {
      parts.push(`rule: ${ruleSummary(res.matched_rule)}`);
    }
    if (res.scope !== undefined && res.scope !== "") {
      parts.push(`scope: ${res.scope}`);
    }
    if (res.source !== undefined && res.source !== "") {
      parts.push(`source: ${shortSource(res.source)}`);
    }
    out.textContent = parts.join(" \u00b7 ");
  }

  private showStatus(text: string, isError: boolean): void {
    const s = maybeEl("native-policy-status");
    if (s === null) {
      return;
    }
    s.textContent = text;
    s.classList.toggle("hidden", text === "");
    s.classList.toggle("native-policy-status-error", isError);
  }
}

const nativePolicy = new NativePolicyController();

// Public delegate functions preserving the existing module API.
export function initPermissionsUI(initial: EffectiveSettings): void {
  controller.initPermissions(initial);
}

/** Initialise the native Cedar policy view + editor: wires the add-rule / explain controls and
 *  the permissions_changed SSE refetch. Does NOT fetch GET /api/permissions — the initial load
 *  is lazy (loadNativePolicy). */
export function initNativePolicyUI(): void {
  nativePolicy.init();
}

/** Load the native policy view. Wired to the Permissions tab's first activation (settings-tabs
 *  loader map) instead of boot, so the bridge-backed /api/permissions endpoint isn't hit for an
 *  invisible panel. Safe to call repeatedly (in-flight loads are aborted). */
export function loadNativePolicy(): void {
  nativePolicy.refresh();
}
