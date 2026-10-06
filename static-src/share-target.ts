// PWA share target + URL shortcuts: `?prompt=<text>` fills the composer, `?agent=planner` creates a
// planner session. Consumed once per launch.

import { $ } from "./dom.js";
import { setComposerValue } from "./composer-value.js";
import { createPlannerSession } from "./chat.js";

/** The one field of the Launch Handler API this app reads. Declared locally because the DOM lib
 *  has no `launchQueue`, and read off `window` as a capability rather than tested for. */
interface LaunchQueue {
  setConsumer: (consumer: (params: { readonly targetURL?: string }) => void) => void;
}

/** A launch the queue delivered before the boot took its turn. */
let queued: URLSearchParams | null = null;

/** Whether the boot has applied a launch yet. */
let bootApplied = false;

/** Apply this document's launch. */
export async function applyShareTarget(): Promise<void> {
  bootApplied = true;
  const params = queued ?? new URLSearchParams(location.search);
  queued = null;
  await applyLaunchParams(params);
}

/** Deliver a launch that FOCUSED this window instead of navigating it. */
export function initLaunchQueue(): void {
  const queue = (globalThis as { readonly launchQueue?: LaunchQueue }).launchQueue;
  if (queue === undefined) {
    return;
  }
  queue.setConsumer((params) => {
    const target = params.targetURL ?? "";
    if (target === "") {
      return;
    }
    const search = new URL(target, location.href).searchParams;
    if (!bootApplied) {
      // The cold launch's own params, arriving through the second door. HELD rather than applied:
      // the boot applies a launch between the strip's activation and the initial route, and
      // applying here would both mint a second planner chat and put it outside that ordering.
      queued = search;
      return;
    }
    void applyLaunchParams(search);
  });
}

async function applyLaunchParams(params: URLSearchParams): Promise<void> {
  // CONSUMED FIRST, before the await below. The query is what the document's own door reads, so
  // clearing it here is what makes the winner of the cold launch's two deliveries the only one that
  // acts — and it is also what keeps a reload from re-firing a share.
  if (location.search !== "") {
    history.replaceState(null, "", location.pathname);
  }

  const sharedText = params.get("prompt");
  if (sharedText !== null && sharedText !== "") {
    // Announced, not assigned: the per-chat draft layer is already wired by the time this runs
    // (setupInput precedes it), and a silent write leaves it holding nothing — so nothing schedules
    // a save and a user who opens a shared prompt and reloads before typing loses the text they
    // were handed.
    setComposerValue(sharedText);
    $.promptInput.focus();
  }

  if (params.get("agent") === "planner") {
    // AWAITED, which is why this function is async: the boot calls applyShareTarget() and then
    // applyInitialRoute(), which resolves the URL against the tab strip. The planner tab has to be
    // in it by then, and its id is the server's now, so the wait is real.
    await createPlannerSession();
  }
}
