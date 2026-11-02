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
} from "./notify.js";
import { KEYED_PUSH_KINDS } from "./push-kinds.js";
import { newestSettings, patchSettings, settingsAsShown } from "./persist.js";
import type { EffectiveSettings, PatchAnswer, SettingsSwitch } from "./persist.js";
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
  settingsKey: (typeof KEYED_PUSH_KINDS)[string];
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
    // As shown, so a click not yet committed stays painted; the state applied is the server's.
    const shown = settingsAsShown();
    notifyToggle.checked = shown?.notifications_enabled ?? areNotificationsEnabled();
    syncRowInputs(rows, shown);
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

  // Clicks inside one debounce share one write's answer: only the newest click acts on it.
  let clicks = 0;
  notifyToggle.addEventListener("change", () => {
    const click = ++clicks;
    const isNewest = (): boolean => click === clicks;
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked) {
      void enableNotifications(rows, notifyToggle, notifyHint, updateSub, isNewest);
      return;
    }
    void patchSettings({ notifications_enabled: false }, masterSwitch(notifyToggle)).then(
      async (r) => {
        if ((await staysOff(r)) && isNewest()) {
          turnOff(updateSub);
        }
      },
    );
  });

  for (const row of rows) {
    row.input.addEventListener("change", () => {
      void applyKindChange(row);
    });
  }
}

/** Whether notifications stay off once a write that turned them off settles with r: the server holds
 *  them off (the newest settings, for a superseded write) and no later click of this page turns
 *  them back on. */
async function staysOff(r: PatchAnswer): Promise<boolean> {
  switch (r.outcome) {
    case "failed":
      return false; // persist has repainted the switch from the settings state
    case "unchanged":
      return false; // no write: the switch already showed off
    case "superseded": {
      const s = await newestSettings();
      if (s === null || s.notifications_enabled) {
        return false;
      }
      break;
    }
    case "committed":
      if (r.settings.notifications_enabled) {
        return false;
      }
      break;
  }
  return settingsAsShown()?.notifications_enabled !== true;
}

/** Apply notifications switched off by this page's click. */
function turnOff(updateSub: () => void): void {
  setNotificationsEnabled(false);
  // A refusal, and the only place the client can tell one from "never opted in": both reach it
  // as `notifications_enabled: false`. Spending the ask is what stops a later automatic grant
  // reversing this switch — see `notify-ask.ts`.
  spendNotifyAsk();
  unregisterPush();
  updateSub();
}

/** Paint each kind's input from the settings as shown, else from the per-kind state applied. */
function syncRowInputs(rows: readonly KindRow[], shown: EffectiveSettings | null = null): void {
  for (const row of rows) {
    row.input.checked = shown?.[row.settingsKey] ?? isKindEnabled(row.kind);
  }
}

/** Switch ON writes the switch alone: the kinds are their own choice, and a keyed kind's push needs both. */
async function enableNotifications(
  rows: readonly KindRow[],
  notifyToggle: HTMLInputElement,
  notifyHint: HTMLElement,
  updateSub: () => void,
  isNewest: () => boolean,
): Promise<void> {
  updateSub(); // show the sub-options optimistically
  const r = await patchSettings({ notifications_enabled: true }, masterSwitch(notifyToggle));
  // The newer settings decide whether notifications are on; the prompt is this click's alone.
  const newest = r.outcome === "superseded" ? await newestSettings() : null;
  updateSub();
  if (!isNewest()) {
    return;
  }
  switch (r.outcome) {
    case "superseded":
      if (!newest?.notifications_enabled) {
        return;
      }
      break;
    case "unchanged":
      // The switch is stored on, and this click retries a failed registration.
      setNotificationsEnabled(true);
      break;
    case "failed":
      // persist repaints the switch; tear down any push subscription requestPermission
      // may have started.
      setNotificationsEnabled(false);
      unregisterPush();
      return;
    case "committed":
      setNotificationsEnabled(r.settings.notifications_enabled);
      for (const row of rows) {
        setKindEnabled(row.kind, r.settings[row.settingsKey]);
      }
      if (!r.settings.notifications_enabled) {
        return; // a later click in the same write turned them off, and its own settle applies that
      }
      break;
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

/** A kind click writes its own key alone and leaves the switch as it is. */
async function applyKindChange(changed: KindRow): Promise<void> {
  const r = await patchSettings(
    { [changed.settingsKey]: changed.input.checked },
    kindSwitch(changed),
  );
  if (r.outcome === "committed") {
    setKindEnabled(changed.kind, r.settings[changed.settingsKey]);
  }
}
