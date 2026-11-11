// The one agent engine's status (GET /api/engine and the engine_status event), its banner, the confirmed restart,
// and the restart hints on the engine-wide Settings rows.

import { signal } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { GLOBAL_BANNER, clearBannerCodes, showBanner } from "./banner-stack.js";
import { confirm } from "./confirm.js";
import { restartEngine } from "./actions/settings.js";
import { decodeEngineStatus } from "./wire/decoders.gen.js";
import type { EngineBusy, EngineStatus } from "./wire/types.gen.js";
import { maybeEl } from "./dom.js";

/** The last status the server reported; null until the first answer. */
const engineStatus = signal<EngineStatus | null>(null);

/** The reasons that name no Settings row (internal/marotte EngineReason*). */
const REASON_KIRO_CLI_VERSION = "kiro_cli_version";
const REASON_ACCOUNT_SWITCH = "account_switch";

interface EngineRow {
  readonly label: string;
  readonly controlID: string;
}

/** Each engine-wide Settings row, keyed by the setting key the server names as a restart reason. */
const ENGINE_ROWS: ReadonlyMap<string, EngineRow> = new Map([
  ["knowledge_enabled", { label: "Knowledge base search", controlID: "flag-knowledge" }],
  ["knowledge_index_type", { label: "Knowledge index type", controlID: "knowledge-index-type" }],
  [
    "knowledge_include_patterns",
    { label: "Knowledge files to include", controlID: "knowledge-include-patterns" },
  ],
  [
    "knowledge_exclude_patterns",
    { label: "Knowledge files to skip", controlID: "knowledge-exclude-patterns" },
  ],
  ["knowledge_max_files", { label: "Knowledge file limit", controlID: "knowledge-max-files" }],
  ["knowledge_chunking", { label: "Knowledge passage size", controlID: "knowledge-chunk-size" }],
  ["model_fallback_enabled", { label: "Model fallback", controlID: "flag-model-fallback" }],
  [
    "steering_reminders_enabled",
    { label: "Steering reminders", controlID: "flag-steering-reminders" },
  ],
  ["auto_routing", { label: "Auto model routing", controlID: "auto-routing" }],
  ["auto_delegation", { label: "Smart helper models", controlID: "auto-delegation" }],
  [
    "cloudformation_safety_check",
    { label: "AWS CloudFormation safety check", controlID: "cloudformation-safety" },
  ],
  [
    "chat_message_delivery_limit",
    { label: "Messages per exchange", controlID: "chat-message-delivery-limit" },
  ],
  ["telemetry.enabled", { label: "kiro-cli telemetry", controlID: "flag-telemetry" }],
  ["engine_heap_mib", { label: "Agent engine memory limit", controlID: "engine-heap-mib" }],
  ["debug_logs", { label: "Debug logs", controlID: "flag-debug-logs" }],
  [
    "high_throughput_optimizations",
    { label: "High throughput optimizations", controlID: "flag-high-throughput" },
  ],
]);

/** The Settings row a restart reason names, or null for a reason with no row (a kiro-cli version, an account). */
export function reasonRow(reason: string): EngineRow | null {
  return ENGINE_ROWS.get(reason) ?? null;
}

const LIST = new Intl.ListFormat("en-US", { type: "conjunction" });

/** The pending banner's sentence for `reasons`; `configUnreadable` says the restart waits for config.json. */
export function restartPendingText(reasons: readonly string[], configUnreadable = false): string {
  const version = reasons.includes(REASON_KIRO_CLI_VERSION);
  const account = reasons.includes(REASON_ACCOUNT_SWITCH);
  const settings = reasons.filter(
    (r) => r !== REASON_KIRO_CLI_VERSION && r !== REASON_ACCOUNT_SWITCH,
  );
  const sentences: string[] = [];
  if (settings.length > 0 || (!version && !account)) {
    const labels = settings.map((r) => reasonRow(r)?.label);
    sentences.push(
      labels.length > 0 && labels.every((l) => l !== undefined)
        ? `Some settings apply after the agent engine restarts: ${LIST.format(labels)}.`
        : "Some settings apply after the agent engine restarts.",
    );
  }
  if (version) {
    sentences.push("A new kiro-cli version applies after the agent engine restarts.");
  }
  if (account) {
    sentences.push("The new Kiro account applies after the agent engine restarts.");
  }
  sentences.push(
    configUnreadable
      ? "config.json cannot be read, so the restart waits. After you fix the file, choose Restart now, or send a " +
          "message or change a setting and it restarts by itself when nothing is running."
      : "It restarts by itself when nothing is running.",
  );
  return sentences.join(" ");
}

function count(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** How many groups a confirmation list names before it counts the rest. */
const LISTED = 3;

/** `groups` as one list naming the first LISTED, each with its count past one, then "and N more". */
function listed(groups: readonly { text: string; n: number }[]): string {
  const shown = groups
    .slice(0, LISTED)
    .map((g) => (g.n > 1 ? `${g.text} ×${String(g.n)}` : g.text));
  const rest = groups.slice(LISTED).reduce((sum, g) => sum + g.n, 0);
  return LIST.format(rest > 0 ? [...shown, `${String(rest)} more`] : shown);
}

/** A run the server names with nothing yet. */
const UNNAMED_RUN = "a workflow";

/** Busy runs grouped by what the confirmation calls them, named runs first. */
function runGroups(runs: EngineBusy["runs"]): { text: string; n: number }[] {
  const groups = new Map<string, { text: string; n: number; named: boolean }>();
  for (const r of runs) {
    const name = r.name === "" ? UNNAMED_RUN : r.name;
    const chat = r.chat_name ?? "";
    const text = chat === "" ? name : `${name} (${chat})`;
    const g = groups.get(text);
    if (g === undefined) {
      groups.set(text, { text, n: 1, named: r.name !== "" });
    } else {
      g.n += 1;
    }
  }
  const all = [...groups.values()];
  return [...all.filter((g) => g.named), ...all.filter((g) => !g.named)];
}

/** The confirmation's text: what a restart now interrupts, or that marotte could not read it (`busy` null). */
export function restartConfirmText(busy: EngineBusy | null): string {
  if (busy === null) {
    return "marotte could not read what is running: any running reply stops, and running workflows pause and resume by themselves.";
  }
  const chats = busy.turns.map((t) => ({ text: t.name === "" ? "Untitled chat" : t.name, n: 1 }));
  const runs = busy.runs.length;
  if (chats.length === 0 && runs === 0) {
    return "Nothing is running, so nothing is interrupted.";
  }
  const parts: string[] = [];
  if (chats.length > 0) {
    parts.push(`stops the reply in ${count(chats.length, "chat", "chats")}: ${listed(chats)}`);
  }
  if (runs > 0) {
    parts.push(
      `pauses ${count(runs, "workflow run", "workflow runs")}: ${listed(runGroups(busy.runs))}, which ${runs === 1 ? "resumes by itself" : "resume by themselves"}`,
    );
  }
  return `Restarting ${parts.join(", and ")}.`;
}

const PENDING_CODE = "engine_restart_pending";
const RESTARTING_CODE = "engine_restarting";
const DOWN_CODE = "engine_down";
const RESTARTING_TEXT = "The agent engine is restarting.";

/** Ask what a restart interrupts, confirm it, then request it. The fresh read matters: busy is broadcast only while
 *  a restart is pending, so the last event may be stale. */
export async function requestRestart(): Promise<void> {
  const fresh = await readEngineStatus();
  const busy = fresh?.busy ?? null;
  const interrupts = busy === null || busy.turns.length > 0 || busy.runs.length > 0;
  const ok = await confirm({
    title: "Restart the agent engine?",
    body: restartConfirmText(busy),
    confirmLabel: "Restart now",
    variant: interrupts ? "destructive" : "normal",
  });
  if (ok) {
    await restartEngine.dispatch(undefined);
  }
}

function paintBanner(s: EngineStatus | null): void {
  if (s === null) {
    clearBannerCodes(GLOBAL_BANNER, [PENDING_CODE, RESTARTING_CODE, DOWN_CODE]);
    return;
  }
  if (s.state === "restarting" || s.state === "down") {
    const code = s.state === "down" ? DOWN_CODE : RESTARTING_CODE;
    clearBannerCodes(GLOBAL_BANNER, [
      PENDING_CODE,
      code === DOWN_CODE ? RESTARTING_CODE : DOWN_CODE,
    ]);
    showBanner(
      GLOBAL_BANNER,
      code,
      RESTARTING_TEXT,
      code === DOWN_CODE ? "warning" : "info",
      false,
    );
    return;
  }
  clearBannerCodes(GLOBAL_BANNER, [RESTARTING_CODE, DOWN_CODE]);
  if (!s.restart.pending) {
    clearBannerCodes(GLOBAL_BANNER, [PENDING_CODE]);
    return;
  }
  showBanner(
    GLOBAL_BANNER,
    PENDING_CODE,
    restartPendingText(s.restart.reasons, s.restart.config_unreadable),
    "info",
    false,
    {
      label: "Restart now",
      onClick: () => {
        void requestRestart();
      },
    },
  );
}

const HINT_CLASS = "engine-restart-hint";

/** Give each engine-wide row its restart hint once, and its inline Restart now while the row's key is pending. */
export function paintEngineRows(s: EngineStatus | null): void {
  const pending = s?.restart.pending === true && s.state === "ready" ? s.restart.reasons : [];
  for (const [key, row] of ENGINE_ROWS) {
    const hintID = `${row.controlID}-engine-hint`;
    let hint = maybeEl(hintID);
    const control = maybeEl(row.controlID);
    if (hint === null && control !== null) {
      const option = control.closest(".section-option");
      const anchor = [...(option?.querySelectorAll(".section-hint") ?? [])].at(-1);
      if (option === null || anchor === undefined) {
        continue;
      }
      hint = document.createElement("p");
      hint.id = hintID;
      hint.className = `section-hint ${HINT_CLASS}`;
      const text = document.createElement("span");
      text.textContent = "Applies after the agent engine restarts.";
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn-small engine-restart-now hidden";
      btn.textContent = "Restart now";
      btn.addEventListener("click", () => {
        void requestRestart();
      });
      hint.append(text, btn);
      anchor.after(hint);
      // Every box of the row, as the passage size row saves one setting from two.
      for (const box of option.querySelectorAll("input, select")) {
        const described = box.getAttribute("aria-describedby");
        box.setAttribute(
          "aria-describedby",
          described === null ? hintID : `${described} ${hintID}`,
        );
      }
    }
    hint?.querySelector(".engine-restart-now")?.classList.toggle("hidden", !pending.includes(key));
  }
}

/** The Settings section's Restart now is offered in every state but a restart already under way: in `down` it wakes
 *  the server's respawn backoff at once. */
function paintSettingsControl(s: EngineStatus | null): void {
  const btn = maybeEl<HTMLButtonElement>("engine-restart");
  if (btn !== null) {
    btn.disabled = s?.state === "restarting";
  }
}

/** Whether `next` is newer than `held` in `EngineStatusVersion`'s order. Across processes that order is the wall
 *  clock's, which can step back, so `reconcileEngineStatus` owns a replaced process. */
export function isNewerStatus(next: EngineStatus, held: EngineStatus | null): boolean {
  if (held === null) {
    return true;
  }
  if (next.version.boot_ms !== held.version.boot_ms) {
    return next.version.boot_ms > held.version.boot_ms;
  }
  return next.version.seq > held.version.seq;
}

function paint(s: EngineStatus | null): void {
  paintBanner(s);
  paintEngineRows(s);
  paintSettingsControl(s);
}

/** Paint `s` unless the status shown is newer: the GET and the event arrive in any order. True when it brings the
 *  engine back to ready from a status shown not ready: the reads the server refused meanwhile can be asked again. */
export function adoptEngineStatus(s: EngineStatus): boolean {
  const held = engineStatus.peek();
  if (!isNewerStatus(s, held)) {
    return false;
  }
  engineStatus.value = s;
  paint(s);
  return held !== null && held.state !== "ready" && s.state === "ready";
}

/** Counts the reconciles so far; a GET answer is adopted only in the count it was asked in. */
let connection = 0;

/** The newest status of the current connection once the read lands (null if it has none), or null when the read
 *  failed with no reconcile since: never an answer from a replaced process. A failed read keeps what is shown. */
async function readEngineStatus(signal?: AbortSignal): Promise<EngineStatus | null> {
  const askedIn = connection;
  const s = await apiGetTyped("/api/engine", decodeEngineStatus, signal);
  if (askedIn === connection) {
    if (s === null) {
      return null;
    }
    adoptEngineStatus(s);
  }
  return engineStatus.peek();
}

/** Re-read after the stream lost its claims. A replaced marotte process reaches every signed-in tab here, as a new
 *  hub epoch, so the held status and every answer still in flight are forgotten first: boot order then only has to
 *  hold inside one connection, and a clock that stepped back across the restart strands nothing. With no status
 *  read or received since, the paint is cleared: what is shown may be the replaced process's. */
export async function reconcileEngineStatus(signal: AbortSignal): Promise<void> {
  connection += 1;
  engineStatus.value = null;
  await readEngineStatus(signal);
  if (engineStatus.peek() === null) {
    paint(null);
  }
}

/** Paint the rows' hints, wire the Settings control and read the status. Once, after sign-in. */
export function initEngineStatus(): void {
  paintEngineRows(engineStatus.peek());
  maybeEl("engine-restart")?.addEventListener("click", () => {
    void requestRestart();
  });
  void readEngineStatus();
}
