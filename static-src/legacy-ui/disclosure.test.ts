import { describe, it, expect, afterEach, vi } from "vitest";

import { createRoot } from "@cplieger/reactive";

import { createDisclosure } from "./disclosure.js";

afterEach(() => {
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

function mount(triggerTag = "button"): {
  trigger: HTMLElement;
  region: HTMLElement;
} {
  const trigger = document.createElement(triggerTag);
  const region = document.createElement("div");
  region.textContent = "panel body";
  document.body.append(trigger, region);
  return { trigger, region };
}

/** Mount a disclosure whose region sits inside `wrapper`, appended for real:
 *  `checkVisibility()` reads the live tree, so a detached node answers false. */
function mountInside(wrapper: HTMLElement): {
  trigger: HTMLElement;
  region: HTMLElement;
} {
  const trigger = document.createElement("button");
  const region = document.createElement("div");
  region.textContent = "panel body";
  wrapper.append(region);
  document.body.append(trigger, wrapper);
  return { trigger, region };
}

/** Two rendering updates, which is what it takes for the engine to have decided
 *  a `content-visibility: auto` subtree's relevancy. */
function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

/** Count `scrollHeight` reads on `el`, so a case can assert the measurement did not happen. */
function watchScrollHeight(el: HTMLElement): { reads: number } {
  const seen = { reads: 0 };
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    get() {
      seen.reads += 1;
      return 120;
    },
  });
  return seen;
}

/** Force `prefers-reduced-motion: reduce` to match. */
function forceReducedMotion(): void {
  vi.spyOn(window, "matchMedia").mockReturnValue({
    matches: true,
    media: "(prefers-reduced-motion: reduce)",
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  } as unknown as MediaQueryList);
}

describe("createDisclosure wiring", () => {
  it("links trigger to region and reflects the collapsed state by default", () => {
    const { trigger, region } = mount();
    createDisclosure(trigger, region);
    expect(region.classList.contains("uip-disclosure-region")).toBe(true);
    expect(region.id).not.toBe("");
    expect(trigger.getAttribute("aria-controls")).toBe(region.id);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(region.getAttribute("aria-hidden")).toBe("true");
  });

  it("preserves an existing region id", () => {
    const { trigger, region } = mount();
    region.id = "my-region";
    createDisclosure(trigger, region);
    expect(trigger.getAttribute("aria-controls")).toBe("my-region");
    expect(region.id).toBe("my-region");
  });

  it("honors an initial open state", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { open: true });
    expect(d.isOpen).toBe(true);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(region.getAttribute("aria-hidden")).toBe("false");
    expect(region.style.height).toBe("");
  });

  it("gives a non-button trigger button semantics", () => {
    const { trigger, region } = mount("div");
    createDisclosure(trigger, region);
    expect(trigger.getAttribute("role")).toBe("button");
    expect(trigger.getAttribute("tabindex")).toBe("0");
  });

  it("leaves a native <button> without an added role/tabindex", () => {
    const { trigger, region } = mount("button");
    createDisclosure(trigger, region);
    expect(trigger.getAttribute("role")).toBeNull();
    expect(trigger.getAttribute("tabindex")).toBeNull();
  });
});

describe("open / close / toggle", () => {
  it("toggle() flips the state and fires onToggle", () => {
    const onToggle = vi.fn();
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { onToggle, animate: false });

    d.toggle();
    expect(d.isOpen).toBe(true);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(region.getAttribute("aria-hidden")).toBe("false");
    expect(onToggle).toHaveBeenLastCalledWith(true, "api");

    d.toggle();
    expect(d.isOpen).toBe(false);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(region.getAttribute("aria-hidden")).toBe("true");
    expect(onToggle).toHaveBeenLastCalledWith(false, "api");
  });

  it("open()/close() are idempotent — no onToggle when the state is unchanged", () => {
    const onToggle = vi.fn();
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { onToggle, animate: false });
    d.close(); // already closed
    expect(onToggle).not.toHaveBeenCalled();
    d.open();
    d.open(); // already open
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("toggles on trigger click", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { animate: false });
    trigger.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(d.isOpen).toBe(true);
  });
});

describe("keyboard activation", () => {
  it("Enter and Space toggle a non-button trigger (Space is prevented)", () => {
    const { trigger, region } = mount("div");
    const d = createDisclosure(trigger, region, { animate: false });

    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    trigger.dispatchEvent(enter);
    expect(d.isOpen).toBe(true);

    const space = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
    trigger.dispatchEvent(space);
    expect(d.isOpen).toBe(false);
    expect(space.defaultPrevented).toBe(true);
  });

  it("does not bind keydown on a native button (relies on native click)", () => {
    const { trigger, region } = mount("button");
    const d = createDisclosure(trigger, region, { animate: false });
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    trigger.dispatchEvent(enter);
    expect(d.isOpen).toBe(false);
  });
});

describe("height animation", () => {
  it("animates open then settles inline height to auto on transitionend", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    d.open();
    expect(d.isOpen).toBe(true);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(region.style.height).not.toBe("");
    region.dispatchEvent(new Event("transitionend"));
    expect(region.style.height).toBe("");
  });

  it("collapses to height 0 on close", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { open: true });
    d.close();
    expect(region.style.height).toBe("0px");
    expect(region.getAttribute("aria-hidden")).toBe("true");
  });

  it("skips the tween under reduced motion (height set directly)", () => {
    forceReducedMotion();
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    d.open();
    expect(region.style.height).toBe("");
    d.close();
    expect(region.style.height).toBe("0px");
  });
});

describe("animation edge paths", () => {
  it("settles to auto via the fallback timeout when transitionend never fires", () => {
    vi.useFakeTimers();
    try {
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open();
      expect(region.style.height).not.toBe("");
      vi.advanceTimersByTime(400);
      expect(region.style.height).toBe("");
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a pending open settle when a close interrupts it", () => {
    vi.useFakeTimers();
    try {
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open(); // registers a pending settle
      d.close(); // interrupts it before transitionend/fallback
      expect(region.style.height).toBe("0px");
      // Cancelled settle must not fire and reopen the height.
      vi.advanceTimersByTime(400);
      expect(region.style.height).toBe("0px");
      expect(d.isOpen).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("a region the page is not rendering", () => {
  it("collapses without measuring inside content-visibility: hidden", () => {
    const wrap = document.createElement("div");
    wrap.style.contentVisibility = "hidden";
    const { trigger, region } = mountInside(wrap);
    const d = createDisclosure(trigger, region, { open: true });
    const seen = watchScrollHeight(region);

    d.close();
    expect(seen.reads).toBe(0);
    expect(region.style.height).toBe("0px");
    d.dispose();
  });

  it("collapses without measuring inside display: none", () => {
    const wrap = document.createElement("div");
    wrap.style.display = "none";
    const { trigger, region } = mountInside(wrap);
    const d = createDisclosure(trigger, region, { open: true });
    const seen = watchScrollHeight(region);

    d.close();
    expect(seen.reads).toBe(0);
    expect(region.style.height).toBe("0px");
    d.dispose();
  });

  it("still measures inside a SKIPPED content-visibility: auto subtree", async () => {
    const spacer = document.createElement("div");
    spacer.style.height = "4000px";
    document.body.append(spacer);
    const wrap = document.createElement("div");
    wrap.style.cssText = "content-visibility: auto; contain-intrinsic-size: auto 200px";
    const { trigger, region } = mountInside(wrap);
    await nextFrame();
    expect(
      region.checkVisibility({ contentVisibilityAuto: true }),
      "the subtree is genuinely skipped, or this case passes for the wrong reason",
    ).toBe(false);

    const d = createDisclosure(trigger, region, { open: true });
    const seen = watchScrollHeight(region);

    d.close();
    expect(seen.reads).toBeGreaterThan(0);
    expect(region.style.height).toBe("0px");
    d.dispose();
  });
});

describe("dispose", () => {
  it("removes listeners so the trigger no longer toggles", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { animate: false });
    d.dispose();
    trigger.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(d.isOpen).toBe(false);
  });

  it("removes the keydown listener from a non-button trigger on dispose", () => {
    const { trigger, region } = mount("div");
    const d = createDisclosure(trigger, region, { animate: false });
    d.dispose();
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    trigger.dispatchEvent(enter);
    expect(d.isOpen).toBe(false);
  });

  it("settles the height on dispose so a mid-animation dispose does not freeze an inline px height", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    d.open(); // animated open sets an inline height mid-transition
    expect(region.style.height).not.toBe("");
    d.dispose(); // dispose while the open tween is still pending
    expect(region.style.height).toBe("");
  });

  it("settles the height to 0 on dispose while collapsed", () => {
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region, { open: true });
    d.close(); // collapsing
    d.dispose();
    expect(region.style.height).toBe("0px");
  });
});

describe("createDisclosure: region-only mode (trigger: null)", () => {
  it("drives the region via the controller with no trigger wiring", () => {
    const region = document.createElement("div");
    document.body.appendChild(region);
    const d = createDisclosure(null, region, { animate: false });

    expect(region.getAttribute("aria-hidden")).toBe("true");
    expect(region.inert).toBe(true);
    d.open();
    expect(d.isOpen).toBe(true);
    expect(region.getAttribute("aria-hidden")).toBe("false");
    expect(region.inert).toBe(false);
    d.close();
    expect(region.getAttribute("aria-hidden")).toBe("true");
    d.dispose();
  });
});

describe("createDisclosure: onToggle source", () => {
  it("reports 'user' for trigger toggles and 'api' for controller toggles", () => {
    const trigger = document.createElement("button");
    const region = document.createElement("div");
    document.body.append(trigger, region);
    const onToggle = vi.fn();
    const d = createDisclosure(trigger, region, { animate: false, onToggle });

    trigger.click();
    expect(onToggle).toHaveBeenLastCalledWith(true, "user");
    d.close();
    expect(onToggle).toHaveBeenLastCalledWith(false, "api");
    d.toggle();
    expect(onToggle).toHaveBeenLastCalledWith(true, "api");
    d.dispose();
  });
});

describe("createDisclosure: the two height-animation engines", () => {
  // CSS.supports is stubbed per case: Chromium supports interpolate-size, so
  // without the stub the measured-px fallback would never run in a test.
  it("interpolates the auto keyword when the engine supports it", () => {
    vi.stubGlobal("CSS", { supports: () => true });
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    d.open();
    expect(region.style.height).toBe("auto");
    d.dispose();
  });

  it("animates to a measured pixel height when it does not", () => {
    vi.stubGlobal("CSS", { supports: () => false });
    const { trigger, region } = mount();
    Object.defineProperty(region, "scrollHeight", { value: 120, configurable: true });
    const d = createDisclosure(trigger, region);
    d.open();
    expect(region.style.height).toBe("120px");
    d.dispose();
  });
});

describe("createDisclosure: ARIA the app already set", () => {
  it("keeps an app-set role on a non-button trigger", () => {
    // Overwriting a role the app chose would silently re-label the control.
    const { trigger, region } = mount("div");
    trigger.setAttribute("role", "tab");
    const d = createDisclosure(trigger, region);
    expect(trigger.getAttribute("role")).toBe("tab");
    d.dispose();
  });

  it("keeps an app-set tabindex on a non-button trigger", () => {
    const { trigger, region } = mount("div");
    trigger.setAttribute("tabindex", "-1");
    const d = createDisclosure(trigger, region);
    expect(trigger.getAttribute("tabindex")).toBe("-1");
    d.dispose();
  });
});

describe("createDisclosure: which keys activate a non-button trigger", () => {
  it("ignores a key that is neither Enter nor Space", () => {
    const { trigger, region } = mount("div");
    const d = createDisclosure(trigger, region, { animate: false });
    const other = new KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true });
    trigger.dispatchEvent(other);
    expect(d.isOpen).toBe(false);
    expect(other.defaultPrevented).toBe(false);
    d.dispose();
  });

  it("activates on the legacy Spacebar key value", () => {
    // Older engines report Space as "Spacebar"; the trigger stays operable there.
    const { trigger, region } = mount("div");
    const d = createDisclosure(trigger, region, { animate: false });
    trigger.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Spacebar", bubbles: true, cancelable: true }),
    );
    expect(d.isOpen).toBe(true);
    d.dispose();
  });
});

describe("createDisclosure: engine capability guards", () => {
  // Each capability is stubbed away to exercise the degrade-instead-of-throw
  // guards; the real engine here provides all of them by default.

  it("treats an engine without matchMedia as expressing no motion preference", () => {
    vi.stubGlobal("CSS", { supports: () => true });
    vi.stubGlobal("matchMedia", undefined);
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    d.open();
    // No matchMedia to ask ⇒ no reduce claim ⇒ the tween runs.
    expect(region.style.height).toBe("auto");
    d.dispose();
  });

  it("falls back to a measured pixel height when CSS is absent entirely", () => {
    vi.stubGlobal("CSS", undefined);
    const { trigger, region } = mount();
    Object.defineProperty(region, "scrollHeight", { value: 90, configurable: true });
    const d = createDisclosure(trigger, region);
    d.open();
    expect(region.style.height).toBe("90px");
    d.dispose();
  });

  it("falls back to a measured pixel height when CSS exists without supports()", () => {
    vi.stubGlobal("CSS", {});
    const { trigger, region } = mount();
    Object.defineProperty(region, "scrollHeight", { value: 90, configurable: true });
    const d = createDisclosure(trigger, region);
    d.open();
    expect(region.style.height).toBe("90px");
    d.dispose();
  });
});

describe("createDisclosure: the committed start height between the two writes", () => {
  // The layout read between the start-height and target-height writes must
  // land while the height still reads the START value, or both writes
  // collapse into one frame and no transition starts.
  function watchReflow(region: HTMLElement): string[] {
    const seen: string[] = [];
    const orig = region.getBoundingClientRect.bind(region);
    vi.spyOn(region, "getBoundingClientRect").mockImplementation(() => {
      seen.push(region.style.height);
      return orig();
    });
    return seen;
  }

  it("commits the collapsed start height before setting the open target", () => {
    vi.stubGlobal("CSS", { supports: () => true });
    const { trigger, region } = mount();
    const d = createDisclosure(trigger, region);
    const seen = watchReflow(region);
    d.open();
    expect(seen).toEqual(["0px"]);
    expect(region.style.height).toBe("auto");
    d.dispose();
  });

  it("commits the measured start height before collapsing to 0", () => {
    const { trigger, region } = mount();
    Object.defineProperty(region, "scrollHeight", { value: 90, configurable: true });
    const d = createDisclosure(trigger, region, { open: true });
    const seen = watchReflow(region);
    d.close();
    // auto is not an animatable start, so the collapse begins from a measured px height.
    expect(seen).toEqual(["90px"]);
    expect(region.style.height).toBe("0px");
    d.dispose();
  });
});

describe("createDisclosure: generated region ids", () => {
  it("names id-less regions from a rising positive counter", () => {
    const a = document.createElement("div");
    const b = document.createElement("div");
    document.body.append(a, b);
    const d1 = createDisclosure(null, a, { animate: false });
    const d2 = createDisclosure(null, b, { animate: false });
    expect(a.id).toMatch(/^uip-disclosure-\d+$/);
    expect(b.id).toMatch(/^uip-disclosure-\d+$/);
    const seq = (id: string): number => Number(id.slice("uip-disclosure-".length));
    expect(seq(b.id)).toBeGreaterThan(seq(a.id));
    d1.dispose();
    d2.dispose();
  });
});

describe("createDisclosure: a superseded settle must not fire late", () => {
  it("a settle armed by an earlier open does not clear a later open's height", () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal("CSS", { supports: () => true });
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open(); // settle #1 armed, deadline t=400
      vi.advanceTimersByTime(100);
      d.close(); // supersedes #1
      vi.advanceTimersByTime(100);
      d.open(); // settle #2 armed, deadline t=600
      expect(region.style.height).toBe("auto");

      vi.advanceTimersByTime(200); // t=400 — #1's old deadline
      // #1 must not fire and settle this second open's height mid-tween.
      expect(region.style.height).toBe("auto");

      vi.advanceTimersByTime(200); // t=600 — #2's deadline
      expect(region.style.height).toBe("");
      d.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a settle armed before the motion preference flipped does not reopen the region", () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal("CSS", { supports: () => true });
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open(); // animated: a settle is armed, deadline t=400
      expect(region.style.height).toBe("auto");

      // The reduced-motion close must still supersede the open's settle, or a
      // CLOSED region left at `height: auto` reads as reopened.
      forceReducedMotion();
      d.close();
      expect(region.style.height).toBe("0px");

      vi.advanceTimersByTime(400);
      expect(region.style.height).toBe("0px");
      d.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("a close leaves no settle armed against the region", () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal("CSS", { supports: () => true });
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open();
      expect(vi.getTimerCount()).toBe(1); // the open's settle fallback

      d.close();
      // A collapse has nothing to settle; no fallback should stay armed.
      expect(vi.getTimerCount()).toBe(0);
      d.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("dispose drops the pending settle so nothing touches the region afterwards", () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal("CSS", { supports: () => true });
      const { trigger, region } = mount();
      const d = createDisclosure(trigger, region);
      d.open(); // settle armed
      d.dispose(); // settles the height itself, and must drop the pending work
      expect(region.style.height).toBe("");

      region.style.height = "42px";
      vi.advanceTimersByTime(400);
      region.dispatchEvent(new Event("transitionend"));
      expect(region.style.height).toBe("42px");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("createDisclosure: ends with the reactive owner it was made under", () => {
  it("disposing the root unwires the trigger, and dispose() after that is a no-op", () => {
    const { trigger, region } = mount();
    const [disposeRoot, ctrl] = createRoot(
      (dispose) => [dispose, createDisclosure(trigger, region, { animate: false })] as const,
    );

    disposeRoot();
    trigger.click();
    expect(ctrl.isOpen).toBe(false);

    region.style.height = "5px";
    ctrl.dispose();
    expect(region.style.height).toBe("5px");
  });
});

/** Call `make` while a reactive owner is being disposed; return what it threw. */
function thrownWhileOwnerCloses(make: () => unknown): unknown {
  let thrown: unknown;
  createRoot((dispose, owner) => {
    owner.onDispose(() => {
      try {
        make();
      } catch (error) {
        thrown = error;
      }
    });
    dispose();
  });
  return thrown;
}

describe("createDisclosure: made while its owner is being disposed", () => {
  it("throws before touching the trigger or the region", () => {
    const { trigger, region } = mount("div");
    const add = vi.spyOn(trigger, "addEventListener");

    expect(String(thrownWhileOwnerCloses(() => createDisclosure(trigger, region)))).toMatch(
      /cannot create a root under an owner that is being disposed/,
    );
    expect(add).not.toHaveBeenCalled();
    expect(region.id).toBe("");
    expect(region.className).toBe("");
    expect(region.hasAttribute("aria-hidden")).toBe(false);
    expect(trigger.getAttributeNames()).toEqual([]);
  });
});

describe("createDisclosure: its owner disposed while the disclosure is being set up", () => {
  it("unwires once after setup, so the trigger no longer toggles it", () => {
    let disposeOwner = (): void => undefined;
    class OwnerClosingTrigger extends HTMLElement {
      static observedAttributes = ["aria-controls"];
      attributeChangedCallback(): void {
        disposeOwner();
      }
    }
    customElements.define("uip-test-owner-closing-trigger", OwnerClosingTrigger);
    const { trigger, region } = mount("uip-test-owner-closing-trigger");

    const ctrl = createRoot((dispose) => {
      disposeOwner = dispose;
      return createDisclosure(trigger, region, { animate: false });
    });
    trigger.click();
    expect(ctrl.isOpen).toBe(false);
    trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
    expect(ctrl.isOpen).toBe(false);

    region.style.height = "5px";
    ctrl.dispose();
    expect(region.style.height).toBe("5px");
  });
});

describe("createDisclosure: after dispose()", () => {
  it("open(), close() and toggle() do nothing", () => {
    const { trigger, region } = mount();
    const onToggle = vi.fn();
    const ctrl = createDisclosure(trigger, region, { animate: false, onToggle });
    ctrl.dispose();

    ctrl.open();
    ctrl.toggle();
    expect(ctrl.isOpen).toBe(false);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(region.inert).toBe(true);
    ctrl.close();
    expect(onToggle).not.toHaveBeenCalled();
  });
});

describe("createDisclosure: disposed by its trigger while a mutator runs", () => {
  let disposeOnExpanded: (() => void) | null = null;
  customElements.define(
    "uip-test-disposing-trigger",
    class extends HTMLElement {
      static observedAttributes = ["aria-expanded"];
      attributeChangedCallback(): void {
        const fn = disposeOnExpanded;
        disposeOnExpanded = null;
        fn?.();
      }
    },
  );

  it("an aria-expanded callback that disposes it leaves no transition timer and no onToggle", () => {
    vi.useFakeTimers();
    try {
      const trigger = document.createElement("uip-test-disposing-trigger");
      const region = document.createElement("div");
      region.textContent = "body";
      document.body.append(trigger, region);
      const onToggle = vi.fn();
      const ctrl = createDisclosure(trigger, region, { onToggle });
      disposeOnExpanded = ctrl.dispose;

      ctrl.open();

      expect(vi.getTimerCount()).toBe(0);
      expect(onToggle).not.toHaveBeenCalled();
      expect(region.style.height).toBe("");
    } finally {
      disposeOnExpanded = null;
      vi.useRealTimers();
    }
  });
});
