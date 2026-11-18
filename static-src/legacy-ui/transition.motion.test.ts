// Injects real stylesheets and uses real timers so an actual CSS transition can
// run, which a synthetic transitionend event cannot observe. `transitionend` is
// the assertion throughout, never elapsed time (flaky on a loaded box).
import { describe, it, expect, afterEach, beforeAll, afterAll, vi } from "vitest";

import { until } from "../__test-helpers__/until.js";

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
    vi.restoreAllMocks();
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

  it("closes a region created closed over laid-out content at once, with no height transition", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    // A tool group's body is painted open before its disclosure exists; one created closed then
    // must not play a collapse the reader never asked for.
    const region = document.createElement("div");
    region.appendChild(document.createElement("p")).textContent = "x".repeat(400);
    document.body.append(region);
    await nextFrame();
    expect(region.getBoundingClientRect().height, "the premise: laid out open").toBeGreaterThan(0);

    const ctl = createDisclosure(null, region, { open: false });

    expect(region.getAnimations().map((a) => (a as CSSTransition).transitionProperty)).toEqual([]);
    expect(region.getBoundingClientRect().height).toBe(0);
    await nextFrame();
    expect(region.getAnimations()).toEqual([]);

    // The tweened path still animates afterwards.
    const ended = ownTransitionEndWithin(region, 2000);
    ctl.open();
    expect(await ended).toBe("height");

    ctl.dispose();
  });

  it("keeps a reopen's tween running past the end of the close it replaced", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    const trigger = document.createElement("button");
    const region = document.createElement("div");
    region.appendChild(document.createElement("p")).textContent = "x".repeat(400);
    document.body.append(trigger, region);
    const ctl = createDisclosure(trigger, region, { open: true });
    await nextFrame();

    ctl.close();
    const closing = region.getAnimations();
    expect(closing.length, "the premise: the close tweens").toBeGreaterThan(0);
    // The reopen runs where an awaiter of the close's `finished` resumes: before the close's
    // own transitionend is dispatched.
    await Promise.allSettled(closing.map((a) => a.finished));
    ctl.open();
    // Record every own end from the reopen on: the close's, then the reopen's.
    const ends: string[] = [];
    let onSecond: (property: string | null) => void = () => undefined;
    const secondEnd = new Promise<string | null>((resolve) => {
      onSecond = resolve;
    });
    const timer = setTimeout(() => {
      onSecond(null);
    }, 2000);
    const onEnd = (e: TransitionEvent): void => {
      if (e.target !== region || e.pseudoElement !== "") {
        return;
      }
      ends.push(e.propertyName);
      if (ends.length === 2) {
        clearTimeout(timer);
        onSecond(e.propertyName);
      }
    };
    region.addEventListener("transitionend", onEnd);
    await settle();

    expect(ends, "the premise: the close's end reaches the region after the reopen").toEqual([
      "height",
    ]);
    expect(region.style.height).toBe("auto");
    expect(region.getAnimations().map((a) => (a as CSSTransition).transitionProperty)).toEqual([
      "height",
    ]);
    expect(await secondEnd).toBe("height");
    region.removeEventListener("transitionend", onEnd);
    await settle();
    expect(region.style.height).toBe("");

    ctl.dispose();
  });

  it("keeps a disclosure's open tween running when it outlasts the settle ceiling", async () => {
    expect(window.matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);

    // A tween still running at the ceiling is what a busy page's late frames make of a short
    // one; a long duration makes that deterministic.
    const trigger = document.createElement("button");
    const region = document.createElement("div");
    region.style.setProperty("--uip-disclosure-duration", "700ms");
    region.appendChild(document.createElement("p")).textContent = "x".repeat(400);
    document.body.append(trigger, region);
    const ctl = createDisclosure(trigger, region);

    ctl.open();
    const opening = region.getAnimations();
    expect(opening.length, "the premise: the open tweens").toBe(1);
    const [outcome] = await Promise.allSettled(opening.map((a) => a.finished));

    expect(outcome?.status, "the tween ran to its end, not cancelled at the ceiling").toBe(
      "fulfilled",
    );
    await settle();
    expect(region.style.height).toBe("");

    ctl.dispose();
  });

  /** A probe running an opacity transition through `runTransition` that outlasts the test, and
   *  whether its settle has run and ran while the fade still did. */
  function longFade(): { settled: () => boolean; whileRunning: () => boolean } {
    const probe = document.createElement("div");
    probe.style.opacity = "0";
    probe.style.transition = "opacity 60s linear";
    document.body.appendChild(probe);
    let done = false;
    let running = false;
    runTransition(probe, {
      change: () => {
        probe.style.opacity = "1";
      },
      settled: () => {
        done = true;
        running = probe.getAnimations().length > 0;
      },
    });
    expect(probe.getAnimations().length, "the premise: the fade runs").toBe(1);
    return { settled: () => done, whileRunning: () => running };
  }

  // A hidden page runs no rendering update, so a running transition's `finished` and
  // `transitionend` wait until it is shown; the mocked visibility stands in for that.
  it("settles at the ceiling on a hidden page instead of waiting for a transition's end", async () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    const { settled, whileRunning } = longFade();

    await until(settled, "the hidden page's settle");
    expect(whileRunning(), "settled while the fade still ran").toBe(true);
  });

  it("settles when the page hides while the ceiling waits on a running transition", async () => {
    const { settled, whileRunning } = longFade();
    // Armed after the ceiling's 400 ms timer, so it runs after it.
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 500);
    });
    expect(settled(), "the premise: the ceiling waits on the fade").toBe(false);

    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await settle();

    expect(settled(), "the hide settled it").toBe(true);
    expect(whileRunning(), "settled while the fade still ran").toBe(true);
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
