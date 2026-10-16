// The one agent engine's status (GET /api/engine and the engine_status event), its banner, the confirmed restart,
// and the restart hints on the engine-wide Settings rows.

import { signal } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { GLOBAL_BANNER, clearBannerCodes, showBanner } from "./banner-stack.js";
import { confirm } from "./confirm.js";
import { restartEngine } from "./actions/settings.js";
import { decodeEngineStatus } from "./wire/decoders.gen.js";
import type { EngineBusy, EngineStatus } from "./wire/types.gen.js";

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
  ["model_fallback_enabled", { label: "Model fallback", controlID: "flag-model-fallback" }],
  [
    "steering_reminders_enabled",
    { label: "Steering reminders", controlID: "flag-steering-reminders" },
  ],
  ["auto_routing", { label: "Auto model routing", controlID: "auto-routing" }],
  ["auto_delegation", { label: "Smart helper models", controlID: "auto-delegation" }],
  [
    "cloudformation_safety_check",
    { label: "AWS CloudFormation safety check", controlID: "flag-cloudformation-safety" },
  ],
  ["telemetry.enabled", { label: "kiro-cli telemetry", controlID: "flag-telemetry" }],
  ["engine_heap_mib", { label: "Agent engine memory limit", controlID: "engine-heap-mib" }],
]);

/** The Settings row a restart reason names, or null for a reason with no row (a kiro-cli version, an account). */
export function reasonRow(reason: string): EngineRow | null {
  return ENGINE_ROWS.get(reason) ?? null;
}

const LIST = new Intl.ListFormat("en-US", { type: "conjunction" });

/** The pending banner's sentence for `reasons`. */
export function restartPendingText(reasons: readonly string[]): string {
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
  sentences.push("It restarts by itself when nothing is running.");
  return sentences.join(" ");
}

function count(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** The confirmation's text: what a restart now interrupts, or that marotte could not read it (`busy` null). */
export function restartConfirmText(busy: EngineBusy | null): string {
  if (busy === null) {
    return "Restart the agent engine? marotte could not read what is running: any running reply stops, and running workflows pause and resume by themselves.";
  }
  const chats = busy.turns.map((t) => (t.name === "" ? "Untitled chat" : t.name));
  const runs = busy.runs.map((r) => (r.name === "" ? r.workflow_id : r.name));
  if (chats.length === 0 && runs.length === 0) {
    return "Restart the agent engine? Nothing is running, so nothing is interrupted.";
  }
  const parts: string[] = [];
  if (chats.length > 0) {
    parts.push(`stops the reply in ${count(chats.length, "chat", "chats")}: ${chats.join(", ")}`);
  }
  if (runs.length > 0) {
    parts.push(
      `pauses ${count(runs.length, "workflow", "workflows")}: ${runs.join(", ")}, which ${runs.length === 1 ? "resumes by itself" : "resume by themselves"}`,
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
  const ok = await confirm(
    restartConfirmText(busy),
    "Restart now",
    interrupts ? "destructive" : "normal",
  );
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
  showBanner(GLOBAL_BANNER, PENDING_CODE, restartPendingText(s.restart.reasons), "info", false, {
    label: "Restart now",
    onClick: () => {
      void requestRestart();
    },
  });
}

const HINT_CLASS = "engine-restart-hint";

/** Give each engine-wide row its restart hint once, and its inline Restart now while the row's key is pending. */
export function paintEngineRows(s: EngineStatus | null): void {
  const pending = s?.restart.pending === true && s.state === "ready" ? s.restart.reasons : [];
  for (const [key, row] of ENGINE_ROWS) {
    const hintID = `${row.controlID}-engine-hint`;
    let hint = document.getElementById(hintID);
    const control = document.getElementById(row.controlID);
    if (hint === null && control !== null) {
      const hints = [
        ...(control.closest(".section-option")?.querySelectorAll(".section-hint") ?? []),
      ];
      const anchor = hints.at(-1);
      if (anchor === undefined) {
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
      const described = control.getAttribute("aria-describedby");
      control.setAttribute(
        "aria-describedby",
        described === null ? hintID : `${described} ${hintID}`,
      );
    }
    hint?.querySelector(".engine-restart-now")?.classList.toggle("hidden", !pending.includes(key));
  }
}

/** The Settings section's Restart now is offered in every state but a restart already under way: in `down` it wakes
 *  the server's respawn backoff at once. */
function paintSettingsControl(s: EngineStatus | null): void {
  const btn = document.getElementById("engine-restart") as HTMLButtonElement | null;
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

/** Paint `s` unless the status shown is newer: the GET and the event arrive in any order. */
export function adoptEngineStatus(s: EngineStatus): void {
  if (!isNewerStatus(s, engineStatus.peek())) {
    return;
  }
  engineStatus.value = s;
  paint(s);
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
  document.getElementById("engine-restart")?.addEventListener("click", () => {
    void requestRestart();
  });
  void readEngineStatus();
}
