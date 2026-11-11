/**
 * toast/index.ts — Public toast surface: the `Toaster` factory and the default
 * module singleton.
 *
 * @module
 */

import { ToastEngine } from "./engine.js";
import type { ToastLevel, ToastOptions, ToastRetry } from "./engine.js";
import { createToastView } from "./view.js";
import type { ToastHandle } from "./view.js";

export type { ToastLevel, ToastRetry };

/** One toast stack: its own container, queue and Escape listener. Every show
 *  variant returns a function that dismisses the toast it created, so a caller
 *  can retract a message whose cause has gone away. */
interface Toaster {
  /** Show a toast; returns a function that dismisses it. */
  show(message: string, opts?: ToastOptions): () => void;
  info(message: string): () => void;
  success(message: string): () => void;
  error(message: string, retry?: ToastRetry): () => void;
  /** Dismiss all visible toasts and clear the queue. */
  clear(): void;
  /** Tear down this toaster: remove its document keydown (Escape) listener,
   *  clear all toasts, and remove its stack container. Call this for any
   *  per-component `createToaster()` so its listener doesn't outlive the
   *  component. (The module `toast` singleton lives for the app's lifetime, so
   *  it is never disposed.) */
  dispose(): void;
}

/** How one stack behaves: how many toasts show and queue, how long a
 *  non-error toast lives, where the stack mounts, and whether an arriving
 *  toast queues behind the visible ones or replaces them. All optional. */
interface ToasterOptions {
  /** Maximum simultaneously-visible toasts (`"stack"` mode). Default `3`. */
  maxVisible?: number;
  /** Overflow queue capacity (`"stack"` mode; oldest dropped). Default `20`. */
  maxQueue?: number;
  /** Auto-dismiss window (ms) for non-error toasts. Default `4000`. */
  defaultDuration?: number;
  /** Mount the toast stack inside this element instead of `document.body` —
   *  for an embeddable widget that must confine its chrome to its own root. */
  container?: HTMLElement;
  /** `"stack"` (default): up to `maxVisible` show, the rest queue.
   *  `"replace"`: single-slot latest-wins — a new toast instantly replaces the
   *  visible one; nothing queues. */
  mode?: "stack" | "replace";
}

function build(opts?: ToasterOptions): Toaster {
  const view = createToastView(opts?.container);
  const engine = new ToastEngine<ToastHandle>({
    view,
    ...(opts?.maxVisible !== undefined ? { maxVisible: opts.maxVisible } : {}),
    ...(opts?.maxQueue !== undefined ? { maxQueue: opts.maxQueue } : {}),
    ...(opts?.defaultDuration !== undefined ? { defaultDuration: opts.defaultDuration } : {}),
    ...(opts?.mode !== undefined ? { mode: opts.mode } : {}),
  });

  const onKeyDown = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      engine.dismissNewest();
    }
  };
  document.addEventListener("keydown", onKeyDown);

  return {
    show: (message, showOpts) => engine.show(message, showOpts),
    info: (message) => engine.show(message, { level: "info" }),
    success: (message) => engine.show(message, { level: "success" }),
    error: (message, retry) =>
      engine.show(message, retry !== undefined ? { level: "error", retry } : { level: "error" }),
    clear: () => {
      engine.clear();
    },
    dispose: () => {
      document.removeEventListener("keydown", onKeyDown);
      engine.clear();
      view.dispose();
    },
  };
}

/** Create an isolated toaster with its own stack container and queue. */
// deadset:ignore DS1102 -- test seam: an isolated stack per case, built as the singleton is
export function createToaster(opts?: ToasterOptions): Toaster {
  return build(opts);
}

// Built lazily, so a page that only calls createToaster never builds the
// default stack or its document listener.
let singleton: Toaster | null = null;

function getSingleton(): Toaster {
  singleton ??= build();
  return singleton;
}

/** Default shared toaster (built lazily on first use; lives for the app's
 *  lifetime — never dispose it). */
export const toast: Toaster = {
  show: (message, opts) => getSingleton().show(message, opts),
  info: (message) => getSingleton().info(message),
  success: (message) => getSingleton().success(message),
  error: (message, retry) => getSingleton().error(message, retry),
  clear: (): void => {
    getSingleton().clear();
  },
  dispose: (): void => {
    getSingleton().dispose();
  },
};

/** Test-only: tear down the default toaster entirely (listener included); the
 *  next use lazily rebuilds it fresh. */
export function _resetForTest(): void {
  if (singleton !== null) {
    singleton.dispose();
    singleton = null;
  }
}
