// Notification toggles: iOS detection, permission requests, sub-option visibility, push/unregister
// lifecycle. Extracted from settings.ts.

import {
  requestPermission,
  setNotificationsEnabled,
  areNotificationsEnabled,
  unregisterPush,
  isKindEnabled,
  setKindEnabled,
  setNotifyUICallback,
  spendNotifyAsk,
  KEYED_PUSH_KINDS,
  KEYED_PUSH_DEFAULTS,
} from "./notify.js";
import { newestSettings, patchSettings } from "./persist.js";
import type { SettingsSwitch } from "./persist.js";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { $, byId } from "./dom.js";
import { isIOS, isStandalone } from "./platform.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

/** The DOM input holding each keyed kind's toggle. Separate from KEYED_PUSH_KINDS (which pairs a
 *  kind with its SETTINGS key) because the two answer different questions and only one of them
 *  is the server's. */
export const KIND_INPUT_IDS: Readonly<Record<string, string>> = {
  agent_finished: "notify-finished-toggle",
  pr_status: "notify-pr-status-toggle",
  run_outcome: "notify-run-outcome-toggle",
};

/** One rendered sub-toggle: the kind it governs, its settings key, its input. */
interface KindRow {
  kind: string;
  settingsKey: string;
  input: HTMLInputElement;
}

function masterSwitch(el: HTMLInputElement): SettingsSwitch {
  return { el, key: "notifications_enabled", on: true };
}

function kindSwitch(row: KindRow): SettingsSwitch {
  return { el: row.input, key: row.settingsKey, on: true };
}

/** A kind with no markup yet is skipped rather than throwing: the server's registry is the source of
 *  truth for which kinds exist, and a kind that has not grown its row is a missing row, not a broken
 *  settings page. */
function kindRows(): KindRow[] {
  const rows: KindRow[] = [];
  for (const [kind, settingsKey] of Object.entries(KEYED_PUSH_KINDS)) {
    const id = KIND_INPUT_IDS[kind];
    if (id === undefined || document.getElementById(id) === null) {
      continue;
    }
    rows.push({ kind, settingsKey, input: byId<HTMLInputElement>(id) });
  }
  return rows;
}

export function initNotificationToggles(): void {
  const notifyToggle = $.notifyToggle;
  const notifyHint = $.notifyHint;
  const notifySubOptions = $.notifySubOptions;
  const rows = kindRows();

  // iOS Safari (non-PWA): Web Push is only available when the app is added to the home screen.
  // Disable the toggle and show a permanent hint so users don't wonder why notifications don't
  // work.
  if (isIOS && !isStandalone) {
    notifyToggle.checked = false;
    notifyToggle.disabled = true;
    notifyHint.textContent =
      'Push notifications require adding this app to your home screen. Tap the share button, then "Add to Home Screen".';
    notifyHint.classList.remove("hidden");
    return;
  }

  notifyToggle.checked = areNotificationsEnabled();
  syncRowInputs(rows);
  bindLoadingState("notify.register_push", notifyToggle);
  const unbindPatch = bindLoadingState("settings.patch", notifyToggle, { preserveDisabled: true });
  registerCleanup(unbindPatch);

  // Listen for registration-failed events from the action layer to roll back the toggle without
  // coupling the action to this DOM element.
  const notifyAC = new AbortController();
  document.addEventListener(
    "notify:registration-failed",
    () => {
      notifyToggle.checked = false;
    },
    { signal: notifyAC.signal },
  );
  registerCleanup(() => {
    notifyAC.abort();
  });

  // Region-only disclosure (trigger: null) — the documented shape for a checkbox-revealed section:
  // the checkbox's checked state conveys the collapse (no aria-expanded belongs on it), the
  // primitive drives the animated height + aria-hidden/inert. Normalize the authored hidden class.
  notifySubOptions.classList.remove("hidden");
  const subCtl = createDisclosure(null, notifySubOptions, { open: notifyToggle.checked });
  const updateSub = (): void => {
    if (notifyToggle.checked) {
      subCtl.open();
    } else {
      subCtl.close();
    }
  };

  setNotifyUICallback(() => {
    notifyToggle.checked = areNotificationsEnabled();
    syncRowInputs(rows);
    updateSub();
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked && "Notification" in window && Notification.permission === "denied") {
      notifyHint.textContent =
        "Notifications were blocked on this device. Allow them in your browser settings.";
      notifyHint.classList.remove("hidden");
    }
  });

  // Set initial sub-option visibility based on current toggle state.
  updateSub();

  notifyToggle.addEventListener("change", () => {
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked) {
      void enableEverything(rows, notifyToggle, notifyHint, updateSub);
      return;
    }
    void patchSettings({ notifications_enabled: false }, masterSwitch(notifyToggle)).then(
      async (r) => {
        // On null persist has repainted the toggle from the settings state.
        if (r === null || (r === "superseded" && !(await stillOff()))) {
          return;
        }
        setNotificationsEnabled(false);
        // A refusal, and the only place the client can tell one from "never opted in": both reach it
        // as `notifications_enabled: false`. Spending the ask is what stops a later automatic grant
        // reversing this switch — see `notify-ask.ts`.
        spendNotifyAsk();
        unregisterPush();
        updateSub();
      },
    );
  });

  for (const row of rows) {
    row.input.addEventListener("change", () => {
      void applyKindChange(rows, row, notifyToggle, updateSub);
    });
  }
}

/** Whether the newest settings, which a superseded write gives way to, still have notifications
 *  off. Their read applies the state (restoreNotifications), not this click's refusal of the ask. */
async function stillOff(): Promise<boolean> {
  const s = await newestSettings();
  return s !== null && !s.notifications_enabled;
}

/** Mirror the in-memory per-kind state onto the inputs. */
function syncRowInputs(rows: readonly KindRow[]): void {
  for (const row of rows) {
    row.input.checked = isKindEnabled(row.kind);
  }
}

/** Master ON: enable the DEFAULT-ON kinds, not just one and not all of them. */
async function enableEverything(
  rows: readonly KindRow[],
  notifyToggle: HTMLInputElement,
  notifyHint: HTMLElement,
  updateSub: () => void,
): Promise<void> {
  const mutated: SettingsSwitch[] = [masterSwitch(notifyToggle)];
  const patch: Record<string, boolean> = { notifications_enabled: true };
  for (const row of rows) {
    const want = KEYED_PUSH_DEFAULTS[row.kind] ?? true;
    patch[row.settingsKey] = want;
    if (row.input.checked !== want) {
      row.input.checked = want;
      mutated.push(kindSwitch(row));
    }
  }
  updateSub(); // show the sub-options optimistically
  const r = await patchSettings(patch, ...mutated);
  if (r === "superseded") {
    // The newer settings decide whether notifications are on; the prompt is this click's alone.
    const s = await newestSettings();
    updateSub();
    if (!s?.notifications_enabled) {
      return;
    }
  } else if (r === null) {
    // persist repaints the inputs; tear down any push subscription requestPermission
    // may have started.
    setNotificationsEnabled(false);
    for (const row of rows) {
      setKindEnabled(row.kind, false);
    }
    unregisterPush();
    updateSub();
    return;
  } else {
    setNotificationsEnabled(true);
    for (const row of rows) {
      setKindEnabled(row.kind, KEYED_PUSH_DEFAULTS[row.kind] ?? true);
    }
    updateSub();
  }
  // Only prompt for browser permission after the server confirms the enable.
  const hint = requestPermission();
  if (hint !== null) {
    notifyHint.textContent = hint;
    notifyHint.classList.remove("hidden");
    return;
  }
  if ("Notification" in window && Notification.permission !== "granted") {
    notifyHint.textContent =
      "Browser permission is pending. You may need to allow notifications when prompted.";
    notifyHint.classList.remove("hidden");
  }
}

async function applyKindChange(
  rows: readonly KindRow[],
  changed: KindRow,
  notifyToggle: HTMLInputElement,
  updateSub: () => void,
): Promise<void> {
  if (changed.input.checked === isKindEnabled(changed.kind)) {
    return; // nothing actually changed — skip the server round trip
  }
  const allOff = rows.every((row) => !row.input.checked);
  const mutated: SettingsSwitch[] = [kindSwitch(changed)];
  const patch: Record<string, boolean> = { [changed.settingsKey]: changed.input.checked };
  if (allOff) {
    notifyToggle.checked = false;
    mutated.push(masterSwitch(notifyToggle));
    patch["notifications_enabled"] = false;
  }
  const r = await patchSettings(patch, ...mutated);
  if (r === "superseded") {
    if (allOff && (await stillOff())) {
      spendNotifyAsk();
      unregisterPush();
    }
    return;
  }
  if (r === null) {
    return; // persist repaints the toggle inputs
  }
  setKindEnabled(changed.kind, changed.input.checked);
  if (allOff) {
    setNotificationsEnabled(false);
    spendNotifyAsk(); // switching every channel off is the same refusal as the master
    unregisterPush();
    updateSub();
  }
}
