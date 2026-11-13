// view.ts — DOM implementation of the toast `ToastView` port. Enter/leave
// lifecycle via `is-entering` → `is-shown` → `is-leaving` state classes.
// Screen-reader announcement is delegated to `announce()`, so neither the
// stack nor a toast node is a live region and no live region nests inside
// another.

import { el } from "@cplieger/reactive";

import { announce } from "../announce.js";
import { topmostOpenDialog } from "../modal-host.js";
import { cancelTransition, runTransition } from "../transition.js";
import type { ToastCallbacks, ToastRenderData, ToastView } from "./engine.js";

/** What this view hands the engine for one mounted toast. The engine stores it
 *  and passes it back; only this module reads the fields. */
export interface ToastHandle {
  /** The node `mount` built and appended to the stack. Detaching it is this
   *  module's alone — from `scheduleLeave`'s settle, from `remove`, or from
   *  `dispose`, which removes the whole stack container and takes any node
   *  still mounted inside it — so the engine must not remove it itself, and a
   *  handle is spent once any of the three has run: the reference still reads,
   *  but the node is out of the document. */
  readonly el: HTMLElement;
  /** The progress bar, or `null` for a sticky toast (no duration to show). */
  readonly progressEl: HTMLElement | null;
  /** Handle of the pending enter `requestAnimationFrame`, or `null` once it has
   *  run (or been cancelled). Cancelled on leave/remove so a late enter frame
   *  can't re-apply `is-shown` mid-leave. */
  enterRaf: number | null;
}

/** Create a DOM-backed toast view whose stack is created on first `mount`.
 *  It mounts on `host` when given, else on `document.body`, moving into the
 *  topmost open modal `<dialog>` while one is open (`showModal()` inerts
 *  everything outside it) and back to the body after. */
export function createToastView(host?: HTMLElement): ToastView<ToastHandle> {
  let container: HTMLElement | null = null;
  // The modal <dialog> currently auto-hosting the stack, if any. Its `close`
  // event re-runs syncHost so a sticky toast is evacuated before it hides.
  let adoptedDialog: HTMLDialogElement | null = null;

  const onAdoptedClose = (): void => {
    syncHost();
  };

  const releaseAdopted = (): void => {
    if (adoptedDialog !== null) {
      adoptedDialog.removeEventListener("close", onAdoptedClose);
      adoptedDialog = null;
    }
  };

  /** Move the stack to `host`, else the topmost open modal dialog, else
   *  `document.body`. `appendChild` MOVES the stack, so live toasts (timers,
   *  listeners, progress state) ride along untouched. */
  const syncHost = (): void => {
    if (container === null) {
      return;
    }
    const desired: HTMLElement = host ?? topmostOpenDialog() ?? document.body;
    if (container.parentElement !== desired) {
      desired.appendChild(container);
    }
    const dialog = host === undefined && desired instanceof HTMLDialogElement ? desired : null;
    if (dialog !== adoptedDialog) {
      releaseAdopted();
      if (dialog !== null) {
        adoptedDialog = dialog;
        dialog.addEventListener("close", onAdoptedClose);
      }
    }
  };

  const ensureContainer = (): HTMLElement => {
    container ??= el("div", { className: "uip-toast-stack" });
    // Re-resolve on every mount: a modal may have opened/closed, or its host
    // dialog may have been removed, since the last toast.
    syncHost();
    return container;
  };

  return {
    mount(data: ToastRenderData, ctx: ToastCallbacks): ToastHandle {
      const stack = ensureContainer();

      // The node itself stays non-live, so the message is announced once.
      announce(data.message, data.level === "error" ? "assertive" : "polite");

      const node = el("div", {
        className: `uip-toast uip-toast--${data.level} is-entering`,
        tabindex: "0",
      });

      // Visually-hidden (not aria-hidden) dismiss hint: the node is
      // tabindex=0 but not a live region, so a tabbed-to toast needs its own
      // description of how to dismiss it.
      node.appendChild(el("span", { className: "uip-toast-msg" }, data.message));
      node.appendChild(el("span", { className: "uip-visually-hidden" }, "Click to dismiss."));

      const retry = data.retry;
      if (retry !== undefined) {
        const btn = el(
          "button",
          { type: "button", className: "uip-toast-retry" },
          retry.label ?? "Retry",
        );
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          ctx.dismiss();
          runRetry(retry.onClick);
        });
        node.appendChild(btn);
      }

      let progressEl: HTMLElement | null = null;
      if (data.duration > 0) {
        // The CSS animates the progress bar off this property (no inline transition-duration).
        node.style.setProperty("--uip-toast-duration", `${data.duration}ms`);
        progressEl = el("span", { className: "uip-toast-progress", "aria-hidden": "true" });
        node.appendChild(progressEl);
      }

      node.addEventListener("click", () => {
        ctx.dismiss();
      });
      // Escape only targets the newest toast, so make the focused one
      // dismissable by keyboard too; guard to the node so it doesn't swallow
      // the retry button's Enter.
      node.addEventListener("keydown", (e) => {
        if (e.target !== node) {
          return;
        }
        if (e.key === "Enter" || e.key === " " || e.key === "Spacebar") {
          e.preventDefault();
          ctx.dismiss();
        }
      });

      // Ref-count hover + focus (two pause sources, one engine timer): resume
      // only when both release, or un-hovering a still-focused toast would
      // resume the countdown while focused.
      let pauseCount = 0;
      const addPause = (): void => {
        pauseCount++;
        if (pauseCount === 1) {
          ctx.pause();
        }
      };
      const removePause = (): void => {
        if (pauseCount === 0) {
          return;
        }
        pauseCount--;
        if (pauseCount === 0) {
          ctx.resume();
        }
      };
      node.addEventListener("mouseenter", addPause);
      node.addEventListener("mouseleave", removePause);
      node.addEventListener("focusin", addPause);
      node.addEventListener("focusout", removePause);

      const handle: ToastHandle = { el: node, progressEl, enterRaf: null };
      stack.appendChild(node);
      // Deferred a frame so the resting is-entering state resolves before
      // is-shown is applied — the node's first style resolution has nothing
      // to transition from otherwise.
      handle.enterRaf = requestAnimationFrame(() => {
        handle.enterRaf = null;
        node.classList.remove("is-entering");
        node.classList.add("is-shown");
      });

      return handle;
    },

    scheduleLeave(handle: ToastHandle, done: () => void): void {
      const node = handle.el;
      // A dismiss can land before the enter frame runs. Force is-shown now so
      // the leave has a defined start state — is-entering has no stylesheet
      // rule (opacity 0, same as is-leaving), so that pairing is no change and
      // starts no transition.
      if (handle.enterRaf !== null) {
        cancelAnimationFrame(handle.enterRaf);
        handle.enterRaf = null;
        node.classList.remove("is-entering");
        node.classList.add("is-shown");
      }
      runTransition(node, {
        change: () => {
          node.classList.remove("is-shown");
          node.classList.add("is-leaving");
        },
        settled: () => {
          node.remove();
          done();
        },
      });
    },

    remove(handle: ToastHandle): void {
      if (handle.enterRaf !== null) {
        cancelAnimationFrame(handle.enterRaf);
        handle.enterRaf = null;
      }
      cancelTransition(handle.el);
      handle.el.remove();
    },

    pauseProgress(handle: ToastHandle): void {
      if (handle.progressEl !== null) {
        handle.progressEl.style.animationPlayState = "paused";
      }
    },

    resumeProgress(handle: ToastHandle): void {
      if (handle.progressEl !== null) {
        handle.progressEl.style.animationPlayState = "running";
      }
    },

    dispose(): void {
      releaseAdopted();
      if (container !== null) {
        container.remove();
        container = null;
      }
    },
  };
}

/** Invoke a retry handler, guarding both sync throws and async rejections. */
function runRetry(onClick: () => void | Promise<void>): void {
  try {
    const result = onClick();
    if (result instanceof Promise) {
      result.catch((err: unknown) => {
        console.error("[uip-toast] retry handler rejected", err);
      });
    }
  } catch (err) {
    console.error("[uip-toast] retry handler threw", err);
  }
}
