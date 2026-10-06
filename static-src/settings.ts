// Settings panel UI. Workspace preferences (last_model, notifications, theme, the file browser's
// path) live in server-side /api/settings; the three fields that are genuinely this SCREEN's live
// in device-view.ts.

import { initAllModals } from "./modals.js";
import { toggleSettingsView, toggleGitView } from "./tabs.js";
import { initGitBadge } from "./git-badge.js";
import { getGitTab } from "./git-tabs.js";
import { noteDefaultBrowsePath } from "./files.js";
import { initTools, loadToolsList } from "./tools.js";
import { restoreNotifications } from "./notify.js";
import { loadSettings, patchSettings, initSettingsTracking } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { cacheTheme, cachedTheme } from "./device-view.js";
import type { ThemeChoice } from "./device-view.js";
import { applyThemeChoice, initThemeToggle } from "./theme.js";
import type { ThemeStorage } from "@cplieger/ui-primitives/theme";
import { initSettingsTabs } from "./settings-tabs.js";
import type { IdentityVerdict } from "./identity.js";
import { initPermissionsUI, initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { initMCP } from "./mcp-ui.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";
import { apiGet } from "./api-client.js";
import { loadVersions, getVersions } from "./versions.js";
import { $ } from "./dom.js";
import { el } from "@cplieger/reactive";
import { initNotificationToggles } from "./settings-notifications.js";

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { logout, setKiroSetting } from "./actions/settings.js";
import { runDiagnostics } from "./actions/tools.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { initSteeringEditor, loadSteeringDoc } from "./settings-steering.js";
import { DEFAULT_AUTO_COMPACT_PCT, compactionPolicy } from "./context-ring.js";
import { paintSettingLocks, writeSwitch } from "./governance.js";

// Per-key write generation for the kiro-cli settings endpoint; same rule as persist.ts's `keyGen`,
// which explains it. Separate because the key namespaces are (dotted kiro-cli keys against
// AppSettings keys).
let kiroSeq = 0;
const kiroGen = new Map<string, number>();

/** Encapsulates the generation guard + showSaving/showSaved/showError lifecycle for dispatching
 *  a kiro-cli setting change. */
function dispatchKiroSetting(key: string, value: string, input: HTMLInputElement): void {
  showSaving(key);
  const gen = ++kiroSeq;
  kiroGen.set(key, gen);
  void setKiroSetting.dispatch({ key, value, input }, { silent: true }).then((r) => {
    if (kiroGen.get(key) !== gen) {
      return;
    }
    if (r === null) {
      showError(key);
    } else {
      showSaved(key);
    }
  });
}

export type { EffectiveSettings } from "./persist.js";
export { loadSettings } from "./persist.js";

// The VALUE lives in config.json. The cache lives in this browser's localStorage, owned byte-wise
// by device-view.ts, and the POLICY is here — beside the value it mirrors, which is the only place
// both halves are visible at once.

/** The theme the server last reported, or the cache before it has. Module state rather than a
 *  read of EffectiveSettings, because the toggle is constructed before the settings fetch
 *  resolves (rule 3). */
let themeChoice: ThemeChoice | null = null;

/** Whether a settings payload has been folded in yet. Separates "the server says no theme is
 *  set" from "the server has not answered", which is the distinction the one-time adoption below
 *  turns on. */
let themeLoaded = false;

/** Set while a server value is being pushed into the live controller. The controller has one
 *  write verb and it means "the user chose this", so adopting a value the server just sent would
 *  go straight back out as a PATCH — and on the settings_updated path that PATCH re-broadcasts
 *  settings_updated. */
let adoptingTheme = false;

function asThemeChoice(v: string | undefined): ThemeChoice | null {
  return v === "dark" || v === "light" || v === "system" ? v : null;
}

/** Push a choice into the live controller so the page repaints, without writing it back. */
function repaintTheme(choice: ThemeChoice): void {
  adoptingTheme = true;
  try {
    applyThemeChoice(choice);
  } finally {
    adoptingTheme = false;
  }
}

/** Fold a loaded settings payload's theme in, and carry the cache across ONCE when the server
 *  has none. */
export function adoptThemeFromSettings(s: EffectiveSettings): void {
  const fromServer = asThemeChoice(s.theme);
  const first = !themeLoaded;
  themeLoaded = true;
  if (fromServer !== null) {
    if (themeChoice !== fromServer) {
      themeChoice = fromServer;
      cacheTheme(fromServer);
      repaintTheme(fromServer);
    }
    return;
  }
  if (!first) {
    // The server has no theme and this is not the first answer, so there is nothing to carry across
    // and nothing new to learn. Adopting the cache again would let a value the user has since
    // cleared come back.
    return;
  }
  const carried = cachedTheme();
  themeChoice = carried;
  if (carried !== null) {
    // Write it through so the value stops being cache-only and starts travelling to every other
    // device, which is what it could not do before.
    void patchSettings({ theme: carried });
  }
}

/** The theme choice in force: the server's once it has answered, the paint cache until then. */
function currentTheme(): ThemeChoice | null {
  if (!themeLoaded && themeChoice === null) {
    return cachedTheme();
  }
  return themeChoice;
}

/** Record a chosen theme: server first (the authority), cache second (the paint hint), in-memory
 *  third so a read before the PATCH lands is still right. */
function setTheme(choice: ThemeChoice): void {
  themeChoice = choice;
  themeLoaded = true;
  cacheTheme(choice);
  if (adoptingTheme) {
    return;
  }
  void patchSettings({ theme: choice });
}

/** The adapter @cplieger/ui-primitives' createTheme persists through. Built here and INJECTED
 *  into initThemeToggle rather than reached from theme.ts, because this module already imports
 *  that one — a reverse import would be a cycle, and the direction is the reason this is a
 *  parameter. */
export const themeStorage: ThemeStorage = {
  get: () => currentTheme(),
  set: (value) => {
    setTheme(value === "light" || value === "system" ? value : "dark");
  },
};

/** @internal Test seam: forget the loaded theme so one case's choice is not the
 *  answer in the next. */
export function _resetThemeForTest(): void {
  themeChoice = null;
  themeLoaded = false;
  adoptingTheme = false;
}

/** Fetch settings from server and apply notification state only. Used for lightweight re-sync
 *  (e.g. after login) without touching per-device UI state. Compare with restoreAll(), which
 *  also seeds the file browser path and the settings panels. */
export async function syncSettings(): Promise<EffectiveSettings | null> {
  const s = await loadSettings();
  // Null means the fetch failed: seeding the dedup tracker from nothing would clear it and re-arm
  // the very write-back it exists to suppress, and applying notification state from nothing would
  // silence or unsilence push on a network blip.
  if (s === null) {
    return null;
  }
  // Seed the dedup tracker BEFORE any code path can fire patchSettings().
  initSettingsTracking(s);
  restoreNotifications(s);
  return s;
}

/** Restore the workspace prefs the loaded settings payload carries. Called once at startup, and
 *  only when the read answered, so per-device state does not belong here: a failed read never
 *  calls this. (Theme is applied separately by initThemeToggle() during initUI.) */
export function restoreAll(s: EffectiveSettings): void {
  // UNCONDITIONAL: "" is a real value meaning "nothing recorded", and the recorder maps it to the
  // mounts listing, so a guard would leave the recorder uncalled on a fresh volume for no gain.
  noteDefaultBrowsePath(s.fb_path);
  // `ui-state.editor_files` existed only to recover a path from a synthetic `editor:<path>` id.

  restoreNotifications(s);
  // Theme is applied by initThemeToggle() (initUI), which constructs the createTheme controller —
  // it reads through the storage adapter above and applies the resolved theme on construction.
  initPermissionsUI(s);
  initNativePolicyUI();
  applyGeneralPanel(s);
}

/** Seed the General panel's controls from a settings payload. Called at boot AND from the
 *  `settings_updated` arm, so a value chosen on another device reaches this screen's controls
 *  rather than only its behaviour. */
export function applyGeneralPanel(s: EffectiveSettings): void {
  serverRetentionDays = s.chat_retention_days;
  applyChatRetention(s);
  applyAgentCapabilities(s);
  applyPanelSwitches(s);
}

/** Register the General panel's `change` listeners. Once per page, from `initUI`: every one of
 *  them reads its control's own state at fire time, so none needs a payload. */
export function initGeneralPanelControls(): void {
  initChatRetentionControls();
  initAgentCapabilityControls();
  initPanelSwitchControls();
}

// kiro-cli's cleanup.periodDays is pinned to 0/never — marotte owns retention end to end. The
// Days-kept number field carries 0 (off) .. N (keep N days); the Keep-forever checkbox overrides it
// to -1 (kept, never purged) and HIDES the Days-kept row.

/** The retention value the server last stated. Module state because the Keep-forever listener is
 *  registered once and still has to fall back to the SERVER's number for this key — not a
 *  constant restated here — when the day box holds empty or non-numeric text. */
let serverRetentionDays = 0;

function retentionEls(): {
  daysInput: HTMLInputElement;
  daysRow: HTMLElement | null;
  foreverInput: HTMLInputElement;
} | null {
  const daysInput = document.getElementById("chat-retention-days") as HTMLInputElement | null;
  const foreverInput = document.getElementById("chat-retention-forever") as HTMLInputElement | null;
  if (daysInput === null || foreverInput === null) {
    return null;
  }
  return { daysInput, daysRow: document.getElementById("chat-retention-days-row"), foreverInput };
}

function applyChatRetention(s: EffectiveSettings): void {
  const els = retentionEls();
  if (els === null) {
    return;
  }
  // No coalesce: the field is required on the payload and the server resolved its default.
  const current = s.chat_retention_days;
  els.foreverInput.checked = current === -1;
  els.daysRow?.classList.toggle("hidden", current === -1);
  // Not while the reader is in the box: a remote change to any other key re-seeds the whole panel,
  // and rewriting a half-typed number under the caret is the one way that costs them work.
  if (current >= 0 && document.activeElement !== els.daysInput) {
    els.daysInput.value = String(current);
  }
}

function initChatRetentionControls(): void {
  const els = retentionEls();
  if (els === null) {
    return;
  }
  const { daysInput, daysRow, foreverInput } = els;
  const persist = (value: number, input: HTMLInputElement): void => {
    void patchSettings({ chat_retention_days: value }, input);
  };

  daysInput.addEventListener("change", () => {
    if (foreverInput.checked) {
      return; // forever wins; the Days-kept row is hidden
    }
    const n = parseInt(daysInput.value, 10);
    persist(!isNaN(n) && n >= 0 ? n : 0, daysInput);
  });
  foreverInput.addEventListener("change", () => {
    daysRow?.classList.toggle("hidden", foreverInput.checked);
    if (foreverInput.checked) {
      persist(-1, foreverInput);
      return;
    }
    const n = parseInt(daysInput.value, 10);
    persist(!isNaN(n) && n >= 0 ? n : serverRetentionDays, foreverInput);
  });
}

/** What the Instructions tab reads: the global-instructions document and the workspace knowledge
 *  bases. Fired once on the tab's first activation via the settings-tabs loader map. TWO lists
 *  left this panel, both because a `.kiro` inventory belongs on the page that shows `.kiro`
 *  inventories: the steering/skills/agents list, then the hooks dashboard. */
function loadInstructionsPanel(): void {
  loadSteeringDoc();
  loadKnowledge();
}

/** Read what the General panel's controls display. Fired once, on the panel's first activation,
 *  via the settings-tabs loader map. */
function loadGeneralPanel(): void {
  initExperimentalToggles();
}

export function initUI(): void {
  initThemeToggle(themeStorage);

  // Settings gear opens the tabbed Settings panel. Default tab is General; deep-link URLs (e.g.
  // /settings/tools) override this via applyRoute.
  $.settingsBtn.addEventListener("click", () => {
    void toggleSettingsView("general");
  });

  // Per-tab lazy data loaders: fired by settings-tabs on the first ACTIVATION of each tab, never on
  // the subscribe-time paint that shows the default panel. Every tab has one now — General's is
  // what took its three kiro-cli spawns off the boot path.
  initSettingsTabs({
    general: loadGeneralPanel,
    tools: loadToolsList,
    permissions: loadNativePolicy,
    instructions: loadInstructionsPanel,
  });
  initSteeringEditor();
  initGeneralPanelControls();
  initLogoutButton();
  initNotificationToggles();
  initDiagnostics();

  initTools();
  initMCP();
  initKnowledge();
  initAllModals();

  $.gitBtn.addEventListener("click", () => {
    // Open to whichever sub-tab is currently active (defaults to "changes" on first open) so the
    // URL the tab pushes matches the visible panel.
    void toggleGitView(getGitTab());
  });
}

/** Post-auth UI init: the reads that must not fire on a login screen. */
export function initPostAuthUI(): void {
  void loadAbout();
  initGitBadge();
}

function initLogoutButton(): void {
  bindLoadingState("settings.logout", $.logoutBtn);
  $.logoutBtn.addEventListener("click", () => {
    // The callback is injected because settings.ts imports that action, so importing renderIdentity
    // there would close a cycle.
    void logout.dispatch({ render: renderIdentity, prev: currentIdentity() });
  });
}

/** Render the About grid from the shared version pair. It does not fetch: `versions.ts` owns
 *  GET /api/version, because the sidebar status card names both values too and a second request
 *  for the same two strings is a second thing that can disagree. */
async function loadAbout(): Promise<void> {
  await loadVersions();
  const v = getVersions();
  const marotteEl = document.getElementById("about-marotte");
  const kiroEl = document.getElementById("about-kirocli");
  if (marotteEl !== null) {
    marotteEl.textContent = v.marotte === "" ? "—" : v.marotte;
  }
  if (kiroEl !== null) {
    kiroEl.textContent = v.kiroCli === "" ? "—" : v.kiroCli;
  }
}

/** Pull a kiro-cli / KAS version out of a diagnostics report so the About panel can surface it
 *  as a dedicated row. The report is the raw `kiro-cli diagnostic --format json-pretty` output;
 *  the version lives under `q-details.version` in the Amazon-Q-derived schema, with a few
 *  fallbacks for forks that name it differently (or a server that folds it in top-level). */
export function extractDiagnosticVersion(report: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(report);
  } catch {
    return "";
  }
  const paths: readonly string[][] = [
    ["q-details", "version"],
    ["kiro_cli", "version"],
    ["kiro-cli", "version"],
    ["cli", "version"],
    ["version"],
    ["kiro_cli"],
    ["kas"],
  ];
  for (const path of paths) {
    const v = digPath(parsed, path);
    if (typeof v === "string" && v.trim() !== "") {
      return v.trim();
    }
  }
  return "";
}

/** Walk `obj` down `path`, returning undefined at the first non-object hop. */
function digPath(obj: unknown, path: readonly string[]): unknown {
  let cur: unknown = obj;
  for (const key of path) {
    if (typeof cur !== "object" || cur === null) {
      return undefined;
    }
    cur = (cur as Record<string, unknown>)[key];
  }
  return cur;
}

/** Copy `text` to the clipboard, resolving to whether it worked. The clipboard API rejects on a
 *  non-secure-context self-host (plain-http LAN IP) or in an iframe, so the caller falls back to
 *  the always-present textarea. */
async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** How long the Copy control holds the run button's slot before the button comes back. Long
 *  enough to paste the report into an issue and return for a second copy; past it a reader wants
 *  a fresh report rather than a stale one, and the textarea keeps this one either way. */
const COPY_SLOT_MS = 15 * 60 * 1000;

/** Wires the "Run diagnostics" button. Shows a spinner (keeping the label) while kiro-cli
 *  collects its report, then renders the FULL report into a readonly, selectable textarea — the
 *  report can be large and the clipboard is unreachable on non-HTTPS self-hosts, so a truncated
 *  ephemeral string is never the only surface. */
export function initDiagnostics(): void {
  const btn = document.getElementById("diagnostics-run") as HTMLButtonElement | null;
  const status = document.getElementById("diagnostics-status") as HTMLParagraphElement | null;
  if (btn === null || status === null) {
    return;
  }

  // Announce the transient status transitions (collecting / ready / error) to assistive tech.
  // Setting the live-region role here keeps announcements working regardless of the static markup.
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");

  // The run ROW is the `.section-option` holding the button and the status line. The report surface
  // stacks below it in the section's own flex column, whose gap spaces it — which is what leaves
  // the row free for the Copy control to take the button's place in.
  const row = status.parentElement ?? btn.parentElement;
  const versionRow = el("p", {
    className: "section-hint diagnostics-version",
  }) as HTMLParagraphElement;
  versionRow.hidden = true;

  const result = el("textarea", {
    className: "diagnostics-result",
    "aria-label": "Diagnostics report",
    spellcheck: "false",
  }) as HTMLTextAreaElement;
  result.readOnly = true;
  result.hidden = true;

  const copyBtn = el(
    "button",
    { type: "button", className: "btn-small diagnostics-copy" },
    "Copy report",
  ) as HTMLButtonElement;

  // `#diagnostics-run` is a deep-link target (runtime-health.ts) and cannot be removed from the
  // DOM, so the two controls SHARE the slot and hiding goes through the `.hidden` utility for both:
  // `.btn` and `.btn-small` each declare `display`, and an author-origin `display` beats the UA's
  // `[hidden]` rule at any specificity.
  const showCopy = (on: boolean): void => {
    copyBtn.classList.toggle("hidden", !on);
    btn.classList.toggle("hidden", on);
  };

  btn.after(copyBtn);
  showCopy(false);
  row?.after(versionRow, result);

  // One timer, cleared before it is re-armed, and released on unload — the only disposal hook there
  // is, since `initDiagnostics` runs once per page and the Settings tab has no per-view teardown.
  let slotTimer: ReturnType<typeof setTimeout> | undefined;
  registerCleanup(() => {
    clearTimeout(slotTimer);
  });

  copyBtn.addEventListener("click", () => {
    void copyToClipboard(result.value).then((ok) => {
      if (ok) {
        status.hidden = false;
        status.textContent = "Copied report to clipboard.";
      } else {
        // Select the text so the keyboard shortcut copies the whole report.
        result.focus();
        result.select();
        status.hidden = false;
        status.textContent = "Press Ctrl/Cmd+C to copy the selected report.";
      }
    });
  });

  bindLoadingState("tools.run_diagnostics", btn, { pendingClass: "btn-loading" });

  // eslint-disable-next-line @typescript-eslint/no-misused-promises
  btn.addEventListener("click", async () => {
    status.hidden = false;
    status.textContent = "Collecting diagnostics\u2026";
    result.hidden = true;
    versionRow.hidden = true;
    const out = await runDiagnostics.dispatch(undefined);
    if (out === null || out.error !== undefined) {
      status.textContent = out?.error ?? "Diagnostics failed. Check server logs.";
      return;
    }
    const report = out.report ?? "";
    // Full report in a selectable, newline-preserving textarea (the durable surface), plus a
    // version row when the payload carries one.
    result.value = report;
    result.hidden = false;
    const version = extractDiagnosticVersion(report);
    if (version !== "") {
      versionRow.textContent = `kiro-cli ${version}`;
      versionRow.hidden = false;
    }
    clearTimeout(slotTimer);
    showCopy(true);
    slotTimer = setTimeout(() => {
      showCopy(false);
    }, COPY_SLOT_MS);
    // Clipboard copy is a convenience; the textarea below works regardless.
    const copied = await copyToClipboard(report);
    status.textContent = copied
      ? `Report ready. Copied ${report.length.toLocaleString()} characters to your clipboard.`
      : "Report ready. Copy it from the box below.";
  });
}

/** The card's auth line, and it is only ever an ERROR now. */
const AUTH_LINE: Readonly<Record<IdentityVerdict["state"], string>> = {
  signed_in: "",
  signed_out: "not signed in",
  unavailable: "unknown",
};

/** The verdict this module last rendered, held so the logout action can restore it on a refusal. */
let lastVerdict: IdentityVerdict = { state: "unavailable", reason: "not resolved yet" };

/** Paint the sidebar identity row and the status card's auth line. Takes the VERDICT rather than
 *  an email because three answers reach it and only one carries an address. */
export function renderIdentity(v: IdentityVerdict): void {
  lastVerdict = v;
  $.userEmail.textContent = v.state === "signed_in" ? v.email : "";
  setAuthLine(AUTH_LINE[v.state]);
}

/** What `renderIdentity` last rendered. Exported for the logout action's rollback, which
 *  receives the VALUE rather than this accessor — the action takes `{ render, prev }`, so it
 *  never reads module state of its own. */
export function currentIdentity(): IdentityVerdict {
  return lastVerdict;
}

/** ONE writer of the auth row AND its separator. Two elements, one fact: a hidden row above a
 *  visible separator leaves the card with a rule pointing at nothing, and that is a defect a
 *  second writer reintroduces by forgetting one of them. */
function setAuthLine(text: string): void {
  $.stAuth.textContent = text;
  $.stAuth.hidden = text === "";
  $.stAuthSep.hidden = text === "";
}

// kiro-cli experimental features gated by settings keys (see the experimentalFlags registry below
// for the full set). Marotte seeds them at container boot (entrypoint.sh); this UI lets the user
// flip each one.

/** What GET /api/kiro-settings answers: the requested keys and their values, as one document.
 *  ONE REQUEST FOR EVERY FLAG, because the server reads them all in one `kiro-cli settings list`
 *  subprocess. */
interface KiroSettingsPayload {
  settings?: Record<string, string>;
}

// experimentalFlags is the kiro-cli flags the UI exposes; a row here creates the toggle and its
// get/put wiring. A key belongs here only if it has a kiro-cli-SIDE role: KAS's ACP path reads no
// kiro-cli setting, so a write here reaches the TUI and the index builder, never a marotte chat.
const experimentalFlags: readonly {
  key: string;
  inputID: string;
  /** What the control shows when the endpoint answers "" for this key. Per row, because the
   *  three polarities are not uniform and the entrypoint's seed is best-effort — a boot whose
   *  seed spawn failed, and any read failure, both arrive as "". */
  defaultOn: boolean;
  inverted?: boolean;
}[] = [
  { key: "hooks.showStatus", inputID: "flag-hooks-status", defaultOn: true },
  { key: "telemetry.enabled", inputID: "flag-telemetry", defaultOn: false },
  // Checked = disable inheritance of default steering/skills/AGENTS.md by custom agents (kiro-cli
  // 2.10+). Not inverted: on = true = disabled.
  {
    key: "chat.disableInheritingDefaultResources",
    inputID: "flag-disable-inherit-resources",
    defaultOn: false,
  },
];

/** Which read of the experimental flags is the newest. The panel's loader is reached on every
 *  settings activation, and the read behind it is a `kiro-cli settings` SPAWN with no signal, no
 *  dedupe and no coalescing of its own, so a superseded answer must be discarded rather than
 *  painted over a newer one. */
let togglesGen = 0;

/** The signal this generation of flag listeners is attached with, aborted and replaced by the
 *  next call. The loader runs on every General-tab activation, so without it each activation
 *  stacked another `change` listener on every checkbox — and one click then cost N identical
 *  PUTs, each a `kiro-cli settings` spawn. */
let togglesAC: AbortController | null = null;

export function initExperimentalToggles(): void {
  const inputs = experimentalFlags.map(
    (flag) => document.getElementById(flag.inputID) as HTMLInputElement | null,
  );
  const wanted = experimentalFlags.map((flag) => flag.key).join(",");
  const gen = ++togglesGen;
  togglesAC?.abort();
  togglesAC = new AbortController();
  const { signal } = togglesAC;
  void apiGet<KiroSettingsPayload>(`/api/kiro-settings?keys=${encodeURIComponent(wanted)}`).then(
    (payload) => {
      if (gen !== togglesGen) {
        return;
      }
      const values = payload?.settings ?? {};
      for (let i = 0; i < experimentalFlags.length; i++) {
        const input = inputs[i] ?? null;
        if (input === null) {
          continue;
        }
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        const flag = experimentalFlags[i]!;
        const v = values[flag.key] ?? "";
        const isOn = v === "" ? flag.defaultOn : v === "true";
        writeSwitch(input, flag.inverted ? !isOn : isOn);
      }
      paintSettingLocks();
    },
  );
  for (let i = 0; i < experimentalFlags.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const flag = experimentalFlags[i]!;
    const input = inputs[i] ?? null;
    if (input === null) {
      continue;
    }
    input.addEventListener(
      "change",
      () => {
        const wireValue = flag.inverted
          ? input.checked
            ? "false"
            : "true"
          : input.checked
            ? "true"
            : "false";
        dispatchKiroSetting(flag.key, wireValue, input);
      },
      { signal },
    );
  }
}

// Toggles that look like the kiro-cli flags above and are a different mechanism: they write MAROTTE
// settings through /api/settings, and internal/agent resolves each at spawn time into the lever KAS
// reads: `_meta.kiro.settings` for knowledge, the child environment for tool search.
const agentCapabilities: readonly {
  key:
    | "knowledge_enabled"
    | "tool_search_enabled"
    | "spec_planning_ask_first"
    | "inline_agents_enabled"
    | "steering_reminders_enabled"
    | "workflows_enabled"
    | "content_collection_enabled";
  inputID: string;
}[] = [
  { key: "knowledge_enabled", inputID: "flag-knowledge" },
  { key: "tool_search_enabled", inputID: "flag-tool-search" },
  { key: "spec_planning_ask_first", inputID: "flag-spec-ask-first" },
  { key: "inline_agents_enabled", inputID: "flag-inline-agents" },
  { key: "steering_reminders_enabled", inputID: "flag-steering-reminders" },
  { key: "workflows_enabled", inputID: "flag-workflows" },
  { key: "content_collection_enabled", inputID: "flag-content-collection" },
];

/** The select-shaped agent capabilities, each with the values the server accepts
 *  (settings.ValidSpecPlanning and its siblings). A value outside the list, from a newer build,
 *  leaves the control as it is. */
const agentChoices: readonly {
  key: "spec_planning" | "output_style";
  selectID: string;
  values: readonly string[];
}[] = [
  { key: "spec_planning", selectID: "spec-planning", values: ["off", "quick", "full"] },
  { key: "output_style", selectID: "output-style", values: ["default", "concise"] },
];

/** Switches over a stored "" / "on" / "off" key. Unset ("") sends nothing, so kiro-cli's own
 *  rollout decides; it renders as off, kiro-cli's shipped default. A flip always stores "on" or
 *  "off", never "". */
const agentFeatureSwitches: readonly {
  key: "work_validation" | "cloudformation_safety_check";
  inputID: string;
}[] = [
  { key: "work_validation", inputID: "flag-work-validation" },
  { key: "cloudformation_safety_check", inputID: "flag-cloudformation-safety" },
];

/** KAS's bounds for the shell timeout, in seconds (the stored value is ms). */
const SHELL_TIMEOUT_MAX_S = 1800;

/** The stored milliseconds for a typed number of seconds: 0 (unset) for an empty or non-positive
 *  entry, else clamped into KAS's 1..1800 s. */
export function shellTimeoutMs(raw: string): number {
  const s = Math.round(Number(raw.trim()));
  if (raw.trim() === "" || !Number.isFinite(s) || s <= 0) {
    return 0;
  }
  return Math.min(s, SHELL_TIMEOUT_MAX_S) * 1000;
}

/** The ask-first switch only means something while spec planning is on. */
function paintSpecPlanning(mode: string): void {
  document.getElementById("spec-planning-ask-row")?.classList.toggle("hidden", mode === "off");
}

/** The Memory dropdown's values, mirroring settings.MemoryOff and its siblings. */
const MEMORY_MODES = ["off", "read_only", "read_write", "learn"] as const;

function memorySelect(): HTMLSelectElement | null {
  return document.getElementById("memory-mode") as HTMLSelectElement | null;
}

function applyAgentCapabilities(s: EffectiveSettings): void {
  for (const cap of agentCapabilities) {
    const input = document.getElementById(cap.inputID) as HTMLInputElement | null;
    if (input !== null) {
      writeSwitch(input, s[cap.key]);
    }
  }
  for (const sw of agentFeatureSwitches) {
    const input = document.getElementById(sw.inputID) as HTMLInputElement | null;
    if (input !== null) {
      input.checked = s[sw.key] === "on";
    }
  }
  const memory = memorySelect();
  // The server validates the value; an unknown one from a newer build leaves the control as it is
  // rather than selecting nothing.
  if (memory !== null && (MEMORY_MODES as readonly string[]).includes(s.memory_mode)) {
    memory.value = s.memory_mode;
  }
  for (const choice of agentChoices) {
    const select = document.getElementById(choice.selectID) as HTMLSelectElement | null;
    if (select !== null && choice.values.includes(s[choice.key])) {
      select.value = s[choice.key];
    }
  }
  paintSpecPlanning(s.spec_planning);
  const timeout = document.getElementById("shell-command-timeout") as HTMLInputElement | null;
  // Not while the reader is in the box, for retention's reason.
  if (timeout !== null && document.activeElement !== timeout) {
    timeout.value =
      s.terminal_command_timeout_ms > 0 ? String(s.terminal_command_timeout_ms / 1000) : "";
  }
  applyAutoCompaction(s);
  paintSettingLocks();
}

function initAgentCapabilityControls(): void {
  for (const cap of agentCapabilities) {
    const input = document.getElementById(cap.inputID) as HTMLInputElement | null;
    if (input === null) {
      continue;
    }
    input.addEventListener("change", () => {
      void patchSettings({ [cap.key]: input.checked }, input);
    });
  }
  for (const sw of agentFeatureSwitches) {
    const input = document.getElementById(sw.inputID) as HTMLInputElement | null;
    input?.addEventListener("change", () => {
      void patchSettings({ [sw.key]: input.checked ? "on" : "off" }, input);
    });
  }
  const memory = memorySelect();
  memory?.addEventListener("change", () => {
    void patchSettings({ memory_mode: memory.value });
  });
  for (const choice of agentChoices) {
    const select = document.getElementById(choice.selectID) as HTMLSelectElement | null;
    select?.addEventListener("change", () => {
      if (choice.key === "spec_planning") {
        paintSpecPlanning(select.value);
      }
      void patchSettings({ [choice.key]: select.value });
    });
  }
  const timeout = document.getElementById("shell-command-timeout") as HTMLInputElement | null;
  timeout?.addEventListener("change", () => {
    const ms = shellTimeoutMs(timeout.value);
    timeout.value = ms > 0 ? String(ms / 1000) : "";
    void patchSettings({ terminal_command_timeout_ms: ms }, timeout);
  });
  initAutoCompactionControls();
}

// A switch and, while it is on, a slider. Both write /api/settings; the server resolves them per
// spawn into the session door, so a change reaches an open chat when it next opens.
// `compactionPolicy` is updated at once, so the ring follows the setting without a reload.

function autoCompactionEls(): {
  toggle: HTMLInputElement;
  row: HTMLElement;
  range: HTMLInputElement;
  value: HTMLOutputElement;
  warning: HTMLElement;
} | null {
  const toggle = document.getElementById("flag-auto-compaction") as HTMLInputElement | null;
  const row = document.getElementById("auto-compact-row");
  const range = document.getElementById("auto-compact-pct") as HTMLInputElement | null;
  const value = document.getElementById("auto-compact-pct-value") as HTMLOutputElement | null;
  const warning = document.getElementById("auto-compact-warning");
  if (toggle === null || row === null || range === null || value === null || warning === null) {
    return null;
  }
  return { toggle, row, range, value, warning };
}

/** Show the slider row only while the switch is on, the value beside the range, and the warning
 *  only above the default. `.hidden` rather than `hidden`: `.section-option` declares `display:
 *  flex`. */
function paintAutoCompaction(enabled: boolean, pct: number): void {
  const els = autoCompactionEls();
  if (els !== null) {
    els.row.classList.toggle("hidden", !enabled);
    els.value.textContent = `${String(pct)}%`;
    els.warning.classList.toggle("hidden", pct <= DEFAULT_AUTO_COMPACT_PCT);
  }
  compactionPolicy.value = { enabled, pct };
}

function applyAutoCompaction(s: EffectiveSettings): void {
  const els = autoCompactionEls();
  if (els !== null) {
    els.toggle.checked = s.auto_compaction_enabled;
    // Not while the reader is dragging it: a remote change to another key re-seeds the whole panel.
    if (document.activeElement !== els.range) {
      els.range.value = String(s.auto_compact_pct);
    }
  }
  paintAutoCompaction(s.auto_compaction_enabled, s.auto_compact_pct);
}

function initAutoCompactionControls(): void {
  const els = autoCompactionEls();
  if (els === null) {
    return;
  }
  const { toggle, range } = els;
  toggle.addEventListener("change", () => {
    paintAutoCompaction(toggle.checked, Number(range.value));
    void patchSettings({ auto_compaction_enabled: toggle.checked }, toggle);
  });
  // `input` repaints the readout while dragging; only `change`, the released gesture, writes.
  range.addEventListener("input", () => {
    paintAutoCompaction(toggle.checked, Number(range.value));
  });
  range.addEventListener("change", () => {
    void patchSettings({ auto_compact_pct: Number(range.value) }, range);
  });
}

// REMOVED: a Settings-level *default*-agent picker. Role selection now lives on the prompt-bar role
// pill (role-picker.ts, #role-pill): it picks the agent per chat (built-in or a workspace custom
// agent from .kiro/agents/), which fits marotte's per-chat model better than a persistent default.

// Each writes one /api/settings boolean, never the kiro-cli settings endpoint. The MCP wait switch
// lives on Settings > Tools; the other two on General.
const panelSwitches: readonly {
  key: "debug_logs" | "guard_payload_links" | "mcp_wait_for_ready";
  inputID: string;
}[] = [
  { key: "guard_payload_links", inputID: "flag-guard-payload-links" },
  { key: "debug_logs", inputID: "flag-debug-logs" },
  { key: "mcp_wait_for_ready", inputID: "mcp-wait-for-ready" },
];

function applyPanelSwitches(s: EffectiveSettings): void {
  for (const sw of panelSwitches) {
    const input = document.getElementById(sw.inputID) as HTMLInputElement | null;
    if (input !== null) {
      input.checked = s[sw.key];
    }
  }
}

function initPanelSwitchControls(): void {
  for (const sw of panelSwitches) {
    const input = document.getElementById(sw.inputID) as HTMLInputElement | null;
    if (input === null) {
      continue;
    }
    input.addEventListener("change", () => {
      void patchSettings({ [sw.key]: input.checked }, input);
    });
  }
}
