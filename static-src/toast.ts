// Ephemeral toasts: a thin wrapper over ui-primitives' default `toast` singleton keeping marotte's
// surface (`info` / `success` / `error` / `showToast`). Announcement goes through the shared
// announce() live region, not a role on the stack. Visuals: the .uip-toast skin (04-uip-skin.css).

import { toast, _resetForTest as uipResetToast } from "@cplieger/ui-primitives/toast";
import type { ToastLevel, ToastRetry } from "@cplieger/ui-primitives/toast";
import type { NoticeLevel } from "./wire/types.gen.js";

export type { ToastRetry };

/** Show an info-level toast. Auto-dismisses after 4s (paused on hover/focus). */
export function info(message: string): () => void {
  return track(null, () => toast.info(message));
}

/** Show a success-level toast. Auto-dismisses after 4s (paused on hover/focus). */
export function success(message: string): () => void {
  return track(null, () => toast.success(message));
}

/** How long an error toast stays up; the library's default (sticky) would stack unread errors for
 *  the session. The stack pauses on hover/focus. A RETRYABLE error stays sticky: its button is the
 *  only way to take the action. MAX_STICKY bounds those. */
const ERROR_DURATION_MS = 12_000;

/** How many sticky toasts may be live at once. The stack shows 3 (ui-primitives'
 *  DEFAULT_MAX_VISIBLE, unexported, restated) and promotes only on dismiss or expiry, so a third
 *  sticky holds the last slot and queues everything else unseen. One fault is reported per chat, so
 *  three chats raise three copies. */
const MAX_STICKY = 2;

/** An entry leaves only by being dismissed, which is the whole bound; a hand-dismissed toast stays
 *  and costs a no-op eviction. */
const sticky: (() => void)[] = [];

/** The library has no warning level, so `info` and `warning` notices ride its info
 *  level and take a marotte class. */
const NOTICE_CLASS: Record<Exclude<NoticeLevel, "error">, string> = {
  info: "uip-toast--notice-info",
  warning: "uip-toast--notice-warning",
};

/** The library's DEFAULT_MAX_QUEUE, restated because it is not exported: past it
 *  the stack drops its oldest queued toast, which then never mounts. */
const MAX_QUEUE = 20;

interface Owed {
  readonly cls: string | null;
  mounted: boolean;
}

/** One entry per toast shown and not yet mounted, in show order. The stack's queue is FIFO, so the
 *  Nth unseen node is the Nth entry; show returns no node and text matching would mis-tag. */
const owed: Owed[] = [];
const observed = new WeakSet<Element>();
const SEEN_ATTR = "data-toast-seen";

function tagMounted(): void {
  for (const stack of document.querySelectorAll(".uip-toast-stack")) {
    if (!observed.has(stack)) {
      observed.add(stack);
      new MutationObserver(tagMounted).observe(stack, { childList: true });
    }
    for (const node of stack.querySelectorAll(`.uip-toast:not([${SEEN_ATTR}])`)) {
      node.setAttribute(SEEN_ATTR, "");
      const entry = owed.shift();
      if (entry === undefined) {
        continue;
      }
      entry.mounted = true;
      if (entry.cls !== null) {
        node.classList.add(entry.cls);
      }
    }
  }
}

function track(cls: string | null, show: () => () => void): () => void {
  const entry: Owed = { cls, mounted: false };
  owed.push(entry);
  const dismiss = show();
  tagMounted();
  while (owed.length > MAX_QUEUE) {
    owed.shift();
  }
  return () => {
    tagMounted();
    if (!entry.mounted) {
      const at = owed.indexOf(entry);
      if (at >= 0) {
        owed.splice(at, 1);
      }
    }
    dismiss();
  };
}

/** Raise a notice that never expires, dropping the OLDEST one past the cap: the
 *  reader has had longest to act on that, and the newest says what is wrong now. */
function showSticky(message: string, retry: ToastRetry): () => void {
  while (sticky.length >= MAX_STICKY) {
    sticky.shift()?.();
  }
  const dismiss = track(null, () => toast.show(message, { level: "error", retry }));
  sticky.push(dismiss);
  return dismiss;
}

/** Show an error-level toast. Auto-dismisses after 12s (paused on hover/focus);
 *  click or press Escape to dismiss sooner. Optionally accepts a retry config;
 *  the toast renders a button that invokes onClick + dismisses, and stays put
 *  until answered or until MAX_STICKY newer ones displace it. */
export function error(message: string, retry?: ToastRetry): () => void {
  if (retry !== undefined) {
    return showSticky(message, retry);
  }
  return track(null, () => toast.show(message, { level: "error", duration: ERROR_DURATION_MS }));
}

/** An error toast whose ACTION button is a convenience, so it keeps the 12s timeout. Sticky is for
 *  an action offered nowhere else; failure-notice.ts's jump target is one click away anyway. */
export function errorWithAction(message: string, action: ToastRetry): () => void {
  return track(null, () =>
    toast.show(message, { level: "error", duration: ERROR_DURATION_MS, retry: action }),
  );
}

/** Show a notice at its own level: info blue, warning amber, error red, success
 *  green. An `action` keeps it up as long as an error, because its button is the
 *  point. */
export function notice(
  message: string,
  level: NoticeLevel | "success",
  action?: ToastRetry,
): () => void {
  if (level === "error") {
    return action === undefined ? error(message) : errorWithAction(message, action);
  }
  const long = level === "warning" || action !== undefined;
  return track(level === "success" ? null : NOTICE_CLASS[level], () =>
    toast.show(message, {
      level: level === "success" ? "success" : "info",
      ...(long ? { duration: ERROR_DURATION_MS } : {}),
      ...(action !== undefined ? { retry: action } : {}),
    }),
  );
}

/** Show a toast with explicit level + duration. Use durationMs=0 for a sticky
 *  toast that requires manual dismissal. Pass undefined to use marotte's level
 *  default (4s for info/success, 12s for error — see error() above). */
export function showToast(
  message: string,
  level: ToastLevel = "info",
  durationMs?: number,
): () => void {
  if (durationMs !== undefined) {
    return track(null, () => toast.show(message, { level, duration: durationMs }));
  }
  return level === "error" ? error(message) : track(null, () => toast.show(message, { level }));
}

/** Test-only: clear all visible + queued toasts and remove the stack. */
// deadset:ignore DS1004 -- test seam: resets the sticky and owed toasts and the stack
export function _resetForTest(): void {
  sticky.length = 0;
  owed.length = 0;
  uipResetToast();
}
