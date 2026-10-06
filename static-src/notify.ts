// Notifications: the Notification API (foreground tab) and Web Push
// (background or closed). Preferences are global server-side settings.

import { isIOS, isStandalone } from "./platform.js";
import { openPushTarget } from "./notification-open.js";
import { pushTargetTag, settleableTag, type PushTarget } from "./push-subject.js";
import { registerPush, unsubscribePush } from "./actions/notify.js";
import { registerCleanup } from "./actions/index.js";
import { createNotifyAsk, type NotifyAsk } from "./notify-ask.js";
import { LS_NOTIFY_ASK_KEY } from "./ls-keys.js";
import { patchSettings } from "./persist.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

/** Application name used in browser Notification titles. */
export const NOTIFY_TITLE = "Marotte";

type PushState =
  | { kind: "idle" }
  | { kind: "registering" }
  | { kind: "registered"; registration: ServiceWorkerRegistration }
  | { kind: "failed"; error: string };

/** The push kinds the user can switch off, keyed by wire value (`marotte.PushKind`)
 *  and paired with each settings key. `permission` has none on purpose: an ask
 *  blocks the turn, so a channel that could go dark would stall every later turn
 *  with nothing on screen to say why. */
export const KEYED_PUSH_KINDS: Readonly<
  Record<string, "notify_agent_finished" | "notify_pr_status" | "notify_run_outcome">
> = {
  agent_finished: "notify_agent_finished",
  pr_status: "notify_pr_status",
  run_outcome: "notify_run_outcome",
};

/** Each keyed kind's default (`pr_status` is OFF). A mirror the payload cannot
 *  replace: `EffectiveSettings` carries values in force, so a switched-off kind
 *  looks like one defaulting off. push-kinds.test.ts keeps the two in step. */
export const KEYED_PUSH_DEFAULTS: Readonly<Record<keyof typeof KEYED_PUSH_KINDS, boolean>> = {
  agent_finished: true,
  pr_status: false,
  run_outcome: true,
};

let swRegistration: ServiceWorkerRegistration | null = null;
let enabled = false;
/** Seeded from KEYED_PUSH_DEFAULTS, so a config.json predating a kind behaves as
 *  the server does. */
const kindEnabled = new Map<string, boolean>(Object.entries(KEYED_PUSH_DEFAULTS));
let notifyUICallback: (() => void) | null = null;
let pushState: PushState = { kind: "idle" };

registerCleanup(() => {
  registerPush.cancel();
});

export function areNotificationsEnabled(): boolean {
  return enabled;
}
/** Whether a keyed kind is on; true for any other kind (the always-on permission floor). */
export function isKindEnabled(kind: string): boolean {
  return kindEnabled.get(kind) ?? true;
}

export function isAgentFinishedEnabled(): boolean {
  return isKindEnabled("agent_finished");
}

// No per-kind getter for the permission ask: the master switch, checked in
// notifyIfHidden, is its only gate (KEYED_PUSH_KINDS says why).

export function setNotificationsEnabled(v: boolean): void {
  enabled = v;
}

/** Set one keyed kind. Any other kind is ignored, so nothing can create an off
 *  switch for the permission floor. */
export function setKindEnabled(kind: string, v: boolean): void {
  if (!(kind in KEYED_PUSH_KINDS)) {
    return;
  }
  kindEnabled.set(kind, v);
}

export function setNotifyUICallback(fn: () => void): void {
  notifyUICallback = fn;
}

/** Apply the persisted preferences; per-kind values are read through KEYED_PUSH_KINDS. */
export function restoreNotifications(s: EffectiveSettings): void {
  const wasEnabled = enabled;
  enabled = s.notifications_enabled;
  for (const [kind, settingsKey] of Object.entries(KEYED_PUSH_KINDS)) {
    kindEnabled.set(kind, s[settingsKey]);
  }

  if (enabled) {
    autoSubscribe();
  } else if (wasEnabled) {
    unregisterPush();
  }
  notifyUICallback?.();
}

// Two doors onto one permission prompt: requestPermission (Settings, inside the
// user's click) and the arm/gesture pair (automatic). Both live here so they agree
// on what a grant leads to; the model is DOM-free in notify-ask.ts.

let ask: NotifyAsk | null = null;

/** The Notification constructor, or undefined. Tested for a FUNCTION, not with
 *  `"Notification" in window`: `in` is true for a non-constructor global, and
 *  reading `.permission` off that would throw out of the arm. */
function notificationCtor(): { permission?: unknown; requestPermission?: unknown } | undefined {
  const value: unknown = (globalThis as { Notification?: unknown }).Notification;
  return typeof value === "function"
    ? (value as { permission?: unknown; requestPermission?: unknown })
    : undefined;
}

/** Built on first use, never at module load: every member below reads a global, and
 *  this module is imported by handlers long before any of them is asked a question. */
function notifyAsk(): NotifyAsk {
  ask ??= createNotifyAsk({
    supported: (): boolean => notificationCtor() !== undefined,
    permission: (): string => {
      const value = notificationCtor()?.permission;
      // Anything unrecognised degrades to the value that asks for nothing.
      return typeof value === "string" ? value : "denied";
    },
    request: async (): Promise<string> => {
      const api = notificationCtor();
      const fn = api?.requestPermission;
      if (typeof fn !== "function") {
        return "denied";
      }
      // Modern browsers return a promise; older Safari takes a callback and leaves the
      // answer on `permission`.
      const answer: unknown = await (fn as () => unknown).call(api);
      if (typeof answer === "string") {
        return answer;
      }
      const settled = notificationCtor()?.permission;
      return typeof settled === "string" ? settled : "denied";
    },
    // Access throws when site data is blocked: an unreadable marker reads as NOT
    // spent, so the per-page flag is then the only bound.
    spent: (): boolean => {
      try {
        return localStorage.getItem(LS_NOTIFY_ASK_KEY) === "1";
      } catch {
        return false;
      }
    },
    markSpent: (): void => {
      try {
        localStorage.setItem(LS_NOTIFY_ASK_KEY, "1");
      } catch {
        /* nothing to remember it with */
      }
    },
    granted: (): void => {
      void adoptGrant();
    },
  });
  return ask;
}

/** Drop the ask's per-page flags. Exported for test isolation only — the browser
 *  module registry is URL-keyed, so a suite cannot re-evaluate this module. */
export function _resetNotifyAskForTest(): void {
  ask = null;
}

/** Private: notifyIfHidden is the one funnel every cue passes through, so nothing
 *  else may arm the ask. */
function armNotifyAsk(): void {
  notifyAsk().arm();
}

/** Raises the prompt if one is armed and still worth raising. */
function noteNotifyGesture(): void {
  notifyAsk().gesture();
}

/** Record that this device's ask is answered without raising anything — the door for
 *  the Settings toggle, in BOTH directions. */
export function spendNotifyAsk(): void {
  notifyAsk().spend();
}

/** ONE delegated capture-phase `click` listener: a click carries user activation
 *  to keyboard users too, `keydown` would prompt someone mid-sentence, and capture
 *  survives an app-level `stopPropagation`. */
export function installNotifyAskGesture(): () => void {
  const ac = new AbortController();
  document.addEventListener(
    "click",
    () => {
      noteNotifyGesture();
    },
    { capture: true, passive: true, signal: ac.signal },
  );
  return () => {
    ac.abort();
  };
}

/** A grant through the automatic door turns the MASTER switch on, or the next cue
 *  is refused by a switch the reader never chose. Per-kind switches stay as they
 *  are: the cue that armed the ask already had its kind on. Push subscribes only
 *  after the server confirms, so a refused write cannot deliver hidden notifications. */
async function adoptGrant(): Promise<void> {
  if (!enabled) {
    const saved = await patchSettings({ notifications_enabled: true });
    if (saved === null) {
      return;
    }
    enabled = true;
    notifyUICallback?.();
  }
  await registerPushViaAction();
}

export function requestPermission(): string | null {
  // The Settings door raises the prompt itself, so the automatic one has nothing
  // left to do on this device whichever way the answer goes.
  spendNotifyAsk();
  if (!("Notification" in window)) {
    if (isIOS && !isStandalone) {
      return "Add this app to your Home Screen first, then enable notifications.";
    }
    return "Notifications are not supported in this browser.";
  }
  if (Notification.permission === "granted") {
    void registerPushViaAction();
    return null;
  }
  if (Notification.permission === "denied") {
    return "Notifications were blocked. Allow them in your browser settings.";
  }
  Notification.requestPermission()
    .then((result) => {
      if (result === "granted") {
        void registerPushViaAction();
      }
    })
    .catch(() => {
      /* noop */
    });
  return null;
}

export function unregisterPush(): void {
  registerPush.cancel();
  pushState = { kind: "idle" };
  if (swRegistration === null) {
    return;
  }
  const reg = swRegistration;
  swRegistration = null;
  reg.pushManager
    .getSubscription()
    .then((sub) => {
      if (sub === null) {
        return;
      }
      const endpoint = sub.endpoint;
      sub.unsubscribe().catch(() => {
        /* noop */
      });
      void unsubscribePush.dispatch({ endpoint });
    })
    .catch(() => {
      /* noop */
    });
  reg.unregister().catch(() => {
    /* noop */
  });
}

/** The page-created notifications still on screen, by tag. `getNotifications` does
 *  not see a page-created Notification, so this map is what lets a retraction reach
 *  one when the ask it announced is answered elsewhere. */
const shown = new Map<string, Notification>();

/** Show a foreground notification when the page is hidden. `target` tags it with the
 *  same tag the service worker gives the push for that target, so the retraction on
 *  the settled ask reaches both. */
export function notifyIfHidden(title: string, body: string, target: PushTarget): boolean {
  // The arm leads every gate: each is a way this call wants to notify and cannot.
  // One funnel, so no new notify site has to remember to arm.
  armNotifyAsk();
  if (!enabled) {
    return false;
  }
  if (document.visibilityState !== "hidden") {
    return false;
  }
  if (!("Notification" in window) || Notification.permission !== "granted") {
    return false;
  }
  const tag = pushTargetTag(target);
  try {
    const n = new Notification(title, {
      body,
      icon: "/favicon.svg",
      tag,
    });
    n.addEventListener("click", () => {
      window.focus();
      n.close();
      openPushTarget(target);
    });
    n.addEventListener("close", () => {
      if (shown.get(tag) === n) {
        shown.delete(tag);
      }
    });
    shown.set(tag, n);
    return true;
  } catch {
    return false;
  }
}

/** The registration whose notifications the page can read; null where there is none
 *  (no service worker, or a test that stubs the API away). */
export type NotificationRegistration = Pick<ServiceWorkerRegistration, "getNotifications">;

let registrationFor: () => Promise<NotificationRegistration | null> = defaultRegistration;

// getRegistration, not `ready`: `ready` never settles where registration failed or
// was refused, and a retraction awaiting it would hang for the page's life.
async function defaultRegistration(): Promise<NotificationRegistration | null> {
  if (!("serviceWorker" in navigator)) {
    return null;
  }
  return (await navigator.serviceWorker.getRegistration()) ?? null;
}

/** Point the retraction at another registration; tests only. */
export function _setRegistrationForTest(
  fn: (() => Promise<NotificationRegistration | null>) | null,
): void {
  registrationFor = fn ?? defaultRegistration;
}

/** Retract every notification about `target`: the service worker's banner (by tag)
 *  and this page's foreground one. A push in flight at reconnect can still land
 *  after this; the server's per-subject debounce bounds that to one. */
export async function closeNotificationsFor(target: PushTarget): Promise<void> {
  const tag = pushTargetTag(target);
  const page = shown.get(tag);
  if (page !== undefined) {
    shown.delete(tag);
    page.close();
  }
  const reg = await registrationFor().catch(() => null);
  if (reg === null) {
    return;
  }
  for (const n of await reg.getNotifications({ tag })) {
    n.close();
  }
}

/** Retract every chat and run banner whose tag is not in `live` (a fresh hello's
 *  pending asks): the set is the whole truth, so anything else in that slot is stale. */
export async function closeNotificationsExcept(live: ReadonlySet<string>): Promise<void> {
  for (const [tag, n] of shown) {
    if (settleableTag(tag) && !live.has(tag)) {
      shown.delete(tag);
      n.close();
    }
  }
  const reg = await registrationFor().catch(() => null);
  if (reg === null) {
    return;
  }
  for (const n of await reg.getNotifications()) {
    if (settleableTag(n.tag) && !live.has(n.tag)) {
      n.close();
    }
  }
}

function autoSubscribe(): void {
  if (
    pushState.kind === "registered" ||
    pushState.kind === "failed" ||
    pushState.kind === "registering"
  ) {
    return;
  }
  if (!("Notification" in window)) {
    return;
  }
  if (Notification.permission === "granted") {
    void registerPushViaAction(true);
    return;
  }
}

async function registerPushViaAction(silent = false): Promise<void> {
  if (pushState.kind === "registered" || pushState.kind === "registering") {
    return;
  }
  pushState = { kind: "registering" };
  const reg = await registerPush.dispatch(undefined, silent ? { silent: true } : undefined);
  if (reg !== null) {
    swRegistration = reg;
    pushState = { kind: "registered", registration: reg };
  } else {
    pushState = { kind: "failed", error: "action failed" };
  }
}
