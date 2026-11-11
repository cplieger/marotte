// Separate from toast.test.ts: needs the real shipped CSS and real timers to
// observe an actual transition, which a synthetic transitionend event cannot.
import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { _resetForTest as resetAnnounce } from "../announce.js";

import type { ToastCallbacks, ToastRenderData } from "./engine.js";
import { createToastView } from "./view.js";

/** Load the shipped stylesheet the way a consumer does, and wait for it. */
async function loadShippedCss(): Promise<HTMLLinkElement> {
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = "/legacy-ui/ui-primitives.css";
  const loaded = new Promise<void>((resolve, reject) => {
    link.addEventListener("load", () => {
      resolve();
    });
    link.addEventListener("error", () => {
      reject(new Error("could not load /legacy-ui/ui-primitives.css"));
    });
  });
  document.head.appendChild(link);
  await loaded;
  return link;
}

/** Resolve `true` on the node's own `transitionend`, `false` if none arrives. */
function transitionEndWithin(node: HTMLElement, ms: number): Promise<boolean> {
  return new Promise<boolean>((resolve) => {
    const onEnd = (e: TransitionEvent): void => {
      if (e.target !== node) {
        return;
      }
      clearTimeout(timer);
      node.removeEventListener("transitionend", onEnd);
      resolve(true);
    };
    const timer = setTimeout(() => {
      node.removeEventListener("transitionend", onEnd);
      resolve(false);
    }, ms);
    node.addEventListener("transitionend", onEnd);
  });
}

/** Let a whole event dispatch (every listener) finish before asserting. */
function settle(): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}

const noop = (): void => {
  /* no-op */
};

describe("toast view: a leave that starts before the enter frame has run", () => {
  let link: HTMLLinkElement;

  beforeAll(async () => {
    link = await loadShippedCss();
  });

  afterAll(() => {
    link.remove();
  });

  afterEach(() => {
    resetAnnounce();
    document.body.innerHTML = "";
  });

  it("settles into is-shown first, so the leave transition really runs", async () => {
    // If the runner reports prefers-reduced-motion, there is no transition to
    // observe and this test would be vacuous; state the precondition.
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const host = document.createElement("div");
    document.body.appendChild(host);
    const view = createToastView(host);
    const data: ToastRenderData = { message: "bye", level: "info", duration: 4000 };
    const ctx: ToastCallbacks = { dismiss: noop, pause: noop, resume: noop };
    const handle = view.mount(data, ctx);
    const node = handle.el;

    // `is-entering` has no stylesheet rule (opacity 0), and `is-leaving` is also
    // opacity 0, so scheduleLeave must settle the node into `is-shown` first or
    // the leave transition is a no-op.
    expect(node.classList.contains("is-entering")).toBe(true);

    let doneCalls = 0;
    const ended = transitionEndWithin(node, 1000);
    view.scheduleLeave(handle, () => {
      doneCalls++;
    });
    // Read before the transition progresses: the value is the settled `is-shown`
    // one, not `is-leaving`.
    const startOpacity = getComputedStyle(node).opacity;

    // Without the settle, no transition starts, so transitionend never arrives
    // and the node waits out LEAVE_FALLBACK_MS instead of the real leave.
    expect(await ended).toBe(true);
    await settle();
    expect(doneCalls).toBe(1);
    expect(node.isConnected).toBe(false);
    expect(startOpacity).toBe("1");

    view.dispose();
  });
});
