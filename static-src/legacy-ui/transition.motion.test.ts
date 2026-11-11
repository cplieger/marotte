// Injects real stylesheets and uses real timers so an actual CSS transition can
// run, which a synthetic transitionend event cannot observe. `transitionend` is
// the assertion throughout, never elapsed time (flaky on a loaded box).
import { describe, it, expect, afterEach, beforeAll, afterAll } from "vitest";

import { createDialog } from "./dialog.js";
import { createDisclosure } from "./disclosure.js";
import { createPopup } from "./popup.js";
import { runTransition } from "./transition.js";

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

/** Let a whole event dispatch finish before asserting. A microtask checkpoint
 *  runs BETWEEN listeners on one event, so a promise resolved by the first
 *  listener can resume before the library's own listener has been invoked. */
function settle(): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}

/** Resolve the `propertyName` of the element's OWN first `transitionend` (not a
 *  descendant's, not a pseudo-element's), or `null` if none arrives in `ms`. */
function ownTransitionEndWithin(node: HTMLElement, ms: number): Promise<string | null> {
  return new Promise<string | null>((resolve) => {
    const onEnd = (e: TransitionEvent): void => {
      // A dialog's ::backdrop transitions opacity too and targets the dialog;
      // require the element's own transition.
      if (e.target !== node || e.pseudoElement !== "") {
        return;
      }
      clearTimeout(timer);
      node.removeEventListener("transitionend", onEnd);
      resolve(e.propertyName);
    };
    const timer = setTimeout(() => {
      node.removeEventListener("transitionend", onEnd);
      resolve(null);
    }, ms);
    node.addEventListener("transitionend", onEnd);
  });
}

/** Yield until after the next frame, so a state written this task is resolved. */
function nextFrame(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      setTimeout(resolve, 0);
    });
  });
}

describe("the committed start state, against real CSS", () => {
  let link: HTMLLinkElement;

  beforeAll(async () => {
    link = await loadShippedCss();
  });

  afterAll(() => {
    link.remove();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("animates a disclosure open from the collapsed height, not straight to auto", async () => {
    // Without motion allowed this test would pass vacuously; state the precondition.
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const trigger = document.createElement("button");
    const region = document.createElement("div");
    region.appendChild(document.createElement("p")).textContent = "x".repeat(400);
    document.body.append(trigger, region);
    const ctl = createDisclosure(trigger, region);

    // The open path writes height:0px then height:auto; the 0 must be committed
    // in between or the engine coalesces both writes and skips the transition.
    const ended = ownTransitionEndWithin(region, 2000);
    ctl.open();

    expect(await ended).toBe("height");
    await settle();
    expect(region.style.height).toBe("");

    ctl.dispose();
  });

  it("animates a disclosure close from its measured height, not straight to 0", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const trigger = document.createElement("button");
    const region = document.createElement("div");
    region.appendChild(document.createElement("p")).textContent = "x".repeat(400);
    document.body.append(trigger, region);
    const ctl = createDisclosure(trigger, region, { open: true });
    await nextFrame();

    const ended = ownTransitionEndWithin(region, 2000);
    ctl.close();

    expect(await ended).toBe("height");
    expect(region.style.height).toBe("0px");

    ctl.dispose();
  });

  it("fades a popup on hide, finishing on its own transitionend rather than the ceiling", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    // The resting state is also the leave target, so one declaration covers both
    // directions and is-leaving needs no rule of its own.
    const style = document.createElement("style");
    style.textContent = `
      .probe { opacity: 0; transition: opacity 60ms linear; }
      .probe.is-open { opacity: 1; }
    `;
    document.head.appendChild(style);

    const panel = document.createElement("div");
    panel.className = "probe";
    panel.textContent = "panel";
    document.body.appendChild(panel);
    const ctl = createPopup(panel);

    // The enter's own committed start state is the resting opacity: 0.
    const entered = ownTransitionEndWithin(panel, 2000);
    ctl.show();
    expect(await entered).toBe("opacity");

    const left = ownTransitionEndWithin(panel, 2000);
    ctl.hide();
    expect(await left).toBe("opacity");
    await settle();
    expect(panel.hidden).toBe(true);

    ctl.dispose();
    style.remove();
  });

  it("settles once when one change ends two transitions", async () => {
    // No "already ran" latch: detaching both sources before invoking settled is
    // what makes it run once, pinned against two real transitionend events.
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const style = document.createElement("style");
    style.textContent = `
      .two { opacity: 1; translate: 0 0; transition: opacity 60ms linear, translate 60ms linear; }
      .two.off { opacity: 0; translate: 0 8px; }
    `;
    document.head.appendChild(style);

    const el = document.createElement("div");
    el.className = "two";
    el.textContent = "x";
    document.body.appendChild(el);

    const ended: string[] = [];
    el.addEventListener("transitionend", (e) => {
      ended.push(e.propertyName);
    });
    let settles = 0;
    runTransition(el, {
      change: () => {
        el.classList.add("off");
      },
      settled: () => {
        settles++;
      },
    });

    await new Promise<void>((resolve) => {
      setTimeout(resolve, 300);
    });
    expect(ended.length).toBeGreaterThan(1);
    expect(settles).toBe(1);

    style.remove();
  });

  it("still fades a dialog closed in the same task it opened", async () => {
    // showModal() moves focus, which needs layout and resolves opacity-1, so the
    // dialog family never depended on the commit.
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const dialog = document.createElement("dialog");
    dialog.textContent = "hello";
    document.body.appendChild(dialog);
    let closed = false;
    const ctl = createDialog(dialog, {
      onClose: () => {
        closed = true;
      },
    });

    const ended = ownTransitionEndWithin(dialog, 2000);
    ctl.open();
    ctl.close();

    expect(await ended).toBe("opacity");
    await settle();
    expect(closed).toBe(true);
    expect(dialog.open).toBe(false);

    ctl.dispose();
  });

  it("documents the one case the commit cannot rescue: a popup hidden in the show's own task", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const style = document.createElement("style");
    style.textContent = `
      .probe { opacity: 0; transition: opacity 60ms linear; }
      .probe.is-open { opacity: 1; }
    `;
    document.head.appendChild(style);

    const panel = document.createElement("div");
    panel.className = "probe";
    panel.textContent = "panel";
    document.body.appendChild(panel);
    const ctl = createPopup(panel);

    // A hide in the show's own task reverses a transition at zero progress whose
    // target equals its current value, so CSS starts no transition at all; the
    // panel snaps to resting and [hidden] waits for the ceiling.
    const ended = ownTransitionEndWithin(panel, 250);
    ctl.show();
    ctl.hide();

    expect(await ended).toBe(null);
    expect(panel.hidden).toBe(false);

    ctl.dispose();
    style.remove();
  });
});
