import { describe, it, expect, afterEach, vi } from "vitest";

import { mountLiveRegions, _resetForTest as resetAnnounce } from "../announce.js";

import { toast, createToaster, _resetForTest } from "./index.js";
import { createToastView } from "./view.js";

// announce() is used for real here (isolate:false makes vi.mock leak across
// files), so announcements are asserted through the actual live region.
afterEach(() => {
  _resetForTest();
  resetAnnounce();
  document.body.innerHTML = "";
});

function stack(): HTMLElement | null {
  return document.querySelector(".uip-toast-stack");
}

function toasts(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(".uip-toast")];
}

function liveRegion(politeness: "polite" | "assertive"): HTMLElement | null {
  return document.querySelector<HTMLElement>(`.uip-visually-hidden[aria-live="${politeness}"]`);
}

function endTransition(node: HTMLElement): void {
  node.dispatchEvent(new Event("transitionend"));
}

describe("toast", () => {
  it("renders a non-live visual stack and node (no nested live regions, no aria-label)", () => {
    toast.info("Saved");

    const s = stack();
    expect(s).not.toBeNull();
    expect(s!.hasAttribute("role")).toBe(false);
    expect(s!.hasAttribute("aria-live")).toBe(false);

    const nodes = toasts();
    expect(nodes).toHaveLength(1);
    const node = nodes[0]!;

    expect(node.hasAttribute("role")).toBe(false);
    expect(node.hasAttribute("aria-live")).toBe(false);
    expect(node.hasAttribute("aria-label")).toBe(false);

    // Dismiss hint stays visually-hidden but not aria-hidden, so the focusable
    // node is self-describing.
    expect(node.querySelector(".uip-toast-msg")!.textContent).toBe("Saved");
    const hint = node.querySelector<HTMLElement>(".uip-visually-hidden");
    expect(hint).not.toBeNull();
    expect(hint!.textContent).toBe("Click to dismiss.");
    expect(hint!.hasAttribute("aria-hidden")).toBe(false);

    expect(node.getAttribute("tabindex")).toBe("0");
    expect(node.classList.contains("uip-toast--info")).toBe(true);
  });

  it("announces an info message through the polite live region (not via the node)", () => {
    vi.useFakeTimers();
    try {
      mountLiveRegions(document.body);
      toast.info("Saved");
      const region = liveRegion("polite");
      expect(region).not.toBeNull();
      expect(region!.getAttribute("role")).toBe("status");
      vi.advanceTimersByTime(100);
      expect(region!.textContent).toBe("Saved");
      expect(liveRegion("assertive")?.textContent).toBe("");
    } finally {
      vi.useRealTimers();
    }
  });

  it("announces a success message politely", () => {
    vi.useFakeTimers();
    try {
      mountLiveRegions(document.body);
      toast.success("Profile updated");
      vi.advanceTimersByTime(100);
      expect(liveRegion("polite")?.textContent).toBe("Profile updated");
      expect(liveRegion("assertive")?.textContent).toBe("");
    } finally {
      vi.useRealTimers();
    }
  });

  it("announces an error assertively through a role=alert live region", () => {
    vi.useFakeTimers();
    try {
      mountLiveRegions(document.body);
      toast.error("boom");
      const region = liveRegion("assertive");
      expect(region).not.toBeNull();
      expect(region!.getAttribute("role")).toBe("alert");
      vi.advanceTimersByTime(100);
      expect(region!.textContent).toBe("boom");
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not append the stack at import time — createToastView() is side-effect-free until mount", () => {
    // Constructing the view must not touch the DOM; the stack appears only on mount.
    const view = createToastView();
    expect(stack()).toBeNull();
    expect(document.body.childElementCount).toBe(0);

    view.mount(
      { message: "hi", level: "info", duration: 0 },
      { dismiss: vi.fn(), pause: vi.fn(), resume: vi.fn() },
    );
    expect(stack()).not.toBeNull();
    view.dispose();
  });

  it("importing the toast module does not append anything to document.body", async () => {
    vi.resetModules();
    document.body.innerHTML = "";
    const fresh = await import("./index.js");
    expect(document.querySelector(".uip-toast-stack")).toBeNull();
    expect(document.body.childElementCount).toBe(0);
    fresh.toast.dispose();
  });

  it("sets --uip-toast-duration and renders a progress bar for timed toasts", () => {
    toast.info("timed");
    const node = toasts()[0]!;
    expect(node.style.getPropertyValue("--uip-toast-duration")).toBe("4000ms");
    expect(node.querySelector(".uip-toast-progress")).not.toBeNull();
  });

  it("makes error toasts sticky (no progress bar) and gives them no role", () => {
    toast.error("boom");
    const node = toasts()[0]!;
    expect(node.hasAttribute("role")).toBe(false);
    expect(node.style.getPropertyValue("--uip-toast-duration")).toBe("");
    expect(node.querySelector(".uip-toast-progress")).toBeNull();
  });

  it("dismisses via the returned function and removes the node after the leave transition", () => {
    const dismiss = toast.info("bye");
    expect(toasts()).toHaveLength(1);
    const node = toasts()[0]!;
    dismiss();
    expect(node.classList.contains("is-leaving")).toBe(true);
    endTransition(node);
    expect(toasts()).toHaveLength(0);
  });

  it("dismisses on click", () => {
    toast.info("clickme");
    const node = toasts()[0]!;
    node.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(true);
  });

  it("Escape dismisses the newest toast only", () => {
    toast.error("first");
    toast.error("second");
    const nodes = toasts();
    expect(nodes).toHaveLength(2);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(nodes[1]!.classList.contains("is-leaving")).toBe(true);
    expect(nodes[0]!.classList.contains("is-leaving")).toBe(false);
  });

  it("renders a retry button that runs its handler and guards async rejection", async () => {
    const onClick = vi.fn().mockRejectedValue(new Error("nope"));
    toast.error("failed", { label: "Try again", onClick });
    const btn = toasts()[0]!.querySelector<HTMLButtonElement>(".uip-toast-retry")!;
    expect(btn.textContent).toBe("Try again");
    expect(() => {
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    }).not.toThrow();
    expect(onClick).toHaveBeenCalledOnce();
    await Promise.resolve();
    await Promise.resolve();
  });

  it("guards a retry handler that throws synchronously", () => {
    const onClick = vi.fn(() => {
      throw new Error("sync boom");
    });
    toast.error("failed", { onClick });
    const btn = toasts()[0]!.querySelector<HTMLButtonElement>(".uip-toast-retry")!;
    expect(btn.textContent).toBe("Retry");
    expect(() => {
      btn.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    }).not.toThrow();
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("clear() removes all toasts immediately", () => {
    toast.info("a");
    toast.info("b");
    expect(toasts()).toHaveLength(2);
    toast.clear();
    expect(toasts()).toHaveLength(0);
  });

  it("pauses the progress animation on hover and resumes on leave", () => {
    toast.info("hover me");
    const node = toasts()[0]!;
    const progress = node.querySelector<HTMLElement>(".uip-toast-progress")!;
    node.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
    expect(progress.style.animationPlayState).toBe("paused");
    node.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
    expect(progress.style.animationPlayState).toBe("running");
  });

  it("ref-counts pause: un-hovering a still-focused toast does not restart its timer", () => {
    vi.useFakeTimers();
    try {
      toast.info("hover+focus");
      const node = toasts()[0]!;
      node.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
      node.dispatchEvent(new Event("focusin", { bubbles: true }));
      node.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
      vi.advanceTimersByTime(10000);
      expect(node.classList.contains("is-leaving")).toBe(false);
      expect(toasts()).toHaveLength(1);
      node.dispatchEvent(new Event("focusout", { bubbles: true }));
      vi.advanceTimersByTime(4000);
      expect(node.classList.contains("is-leaving")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels the pending enter frame on dismiss and settles into a leaving state", () => {
    const cancelSpy = vi.spyOn(globalThis, "cancelAnimationFrame");
    const dismiss = toast.info("quick");
    const node = toasts()[0]!;
    expect(node.classList.contains("is-entering")).toBe(true);
    dismiss();
    expect(cancelSpy).toHaveBeenCalled();
    expect(node.classList.contains("is-entering")).toBe(false);
    expect(node.classList.contains("is-leaving")).toBe(true);
    endTransition(node);
    expect(toasts()).toHaveLength(0);
  });

  it("createToaster() is disposable: dispose stops the ESC listener without leaking it", () => {
    const addSpy = vi.spyOn(document, "addEventListener");
    const removeSpy = vi.spyOn(document, "removeEventListener");

    const toaster = createToaster();
    toaster.show("sticky", { level: "error" });
    const node = document.querySelector<HTMLElement>(".uip-toast")!;
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(node.classList.contains("is-leaving")).toBe(true);

    toaster.dispose();
    expect(document.querySelector(".uip-toast-stack")).toBeNull();
    const adds = addSpy.mock.calls.filter((c) => c[0] === "keydown").length;
    const removes = removeSpy.mock.calls.filter((c) => c[0] === "keydown").length;
    expect(adds).toBe(1);
    expect(removes).toBe(1);
  });

  it("repeated createToaster()/dispose() cycles do not accumulate document listeners", () => {
    const addSpy = vi.spyOn(document, "addEventListener");
    const removeSpy = vi.spyOn(document, "removeEventListener");
    for (let i = 0; i < 5; i++) {
      createToaster().dispose();
    }
    const adds = addSpy.mock.calls.filter((c) => c[0] === "keydown").length;
    const removes = removeSpy.mock.calls.filter((c) => c[0] === "keydown").length;
    expect(adds).toBe(5);
    expect(removes).toBe(5);
  });

  it("dismisses a focused toast when Enter is pressed on the toast node", () => {
    toast.info("enter me");
    const node = toasts()[0]!;
    node.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(true);
  });

  it("dismisses a focused toast when Space is pressed on the toast node", () => {
    toast.info("space me");
    const node = toasts()[0]!;
    node.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(true);
  });

  it("ignores a keydown bubbling up from the retry button (does not dismiss the toast)", () => {
    toast.error("with retry", { onClick: vi.fn() });
    const node = toasts()[0]!;
    const btn = node.querySelector<HTMLButtonElement>(".uip-toast-retry")!;
    btn.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(false);
  });

  it("ignores a key that is neither Enter nor Space on the toast node", () => {
    toast.info("type near me");
    const node = toasts()[0]!;
    node.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(false);
  });

  it('dismisses on the legacy "Spacebar" key name (older engines)', () => {
    toast.info("spacebar me");
    const node = toasts()[0]!;
    node.dispatchEvent(new KeyboardEvent("keydown", { key: "Spacebar", bubbles: true }));
    expect(node.classList.contains("is-leaving")).toBe(true);
  });

  it("prevents the default of a dismissing key press (Space must not scroll the page)", () => {
    toast.info("space me");
    const node = toasts()[0]!;
    const evt = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
    node.dispatchEvent(evt);
    expect(evt.defaultPrevented).toBe(true);
  });

  it("dismisses the toast when its retry button is clicked, as well as running the handler", () => {
    const onClick = vi.fn();
    toast.error("failed", { onClick });
    const node = toasts()[0]!;
    node
      .querySelector<HTMLButtonElement>(".uip-toast-retry")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(onClick).toHaveBeenCalledOnce();
    expect(node.classList.contains("is-leaving")).toBe(true);
  });

  it("settles an entering toast into is-shown on the next frame", () => {
    vi.useFakeTimers();
    try {
      toast.info("entering");
      const node = toasts()[0]!;
      expect(node.classList.contains("is-entering")).toBe(true);
      vi.advanceTimersToNextFrame();
      expect(node.classList.contains("is-entering")).toBe(false);
      expect(node.classList.contains("is-shown")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops is-shown when the leave begins so the two motion states never overlap", () => {
    vi.useFakeTimers();
    try {
      const dismiss = toast.info("shown, then leaving");
      const node = toasts()[0]!;
      vi.advanceTimersToNextFrame();
      expect(node.classList.contains("is-shown")).toBe(true);
      dismiss();
      expect(node.classList.contains("is-shown")).toBe(false);
      expect(node.classList.contains("is-leaving")).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a focusout with no matching focusin does not corrupt the pause ref-count", () => {
    vi.useFakeTimers();
    try {
      toast.info("stray focusout");
      const node = toasts()[0]!;
      // A focusout with no matching focusin must not push the count below zero.
      node.dispatchEvent(new Event("focusout", { bubbles: true }));
      node.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
      vi.advanceTimersByTime(10000);
      expect(node.classList.contains("is-leaving")).toBe(false);
      expect(toasts()).toHaveLength(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves the stack where it is when a later toast finds it already hosted", () => {
    toast.info("first");
    const s = stack()!;
    const sibling = document.createElement("div");
    document.body.appendChild(sibling);
    toast.info("second");
    expect(document.body.lastElementChild).toBe(sibling);
    expect(s.parentElement).toBe(document.body);
  });
});

describe("createToaster: container option", () => {
  it("mounts the stack inside the given host instead of document.body", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const t = createToaster({ container: host });
    t.info("scoped");
    const scoped = host.querySelector(".uip-toast-stack");
    expect(scoped).not.toBeNull();
    expect(document.querySelectorAll(".uip-toast-stack")).toHaveLength(1);
    t.dispose();
    expect(host.querySelector(".uip-toast-stack")).toBeNull();
  });

  it("replace mode swaps the visible toast in place", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const t = createToaster({ container: host, mode: "replace" });
    t.info("first");
    t.info("second");
    const toasts = host.querySelectorAll(".uip-toast");
    expect(toasts).toHaveLength(1);
    expect(toasts[0]?.querySelector(".uip-toast-msg")?.textContent).toBe("second");
    t.dispose();
  });
});

describe("toast: modal <dialog> auto-hosting", () => {
  function openModal(): HTMLDialogElement {
    const dlg = document.createElement("dialog");
    document.body.appendChild(dlg);
    dlg.showModal();
    return dlg;
  }

  /** Close a dialog and await its `close` event (queued as a task, not synchronous). */
  async function closeAndSettle(dlg: HTMLDialogElement): Promise<void> {
    const delivered = new Promise<void>((resolve) => {
      dlg.addEventListener("close", () => resolve(), { once: true });
    });
    dlg.close();
    await delivered;
  }

  it("hosts the default stack inside an open modal so toasts stay interactive (not inert)", () => {
    const dlg = openModal();
    toast.info("over the modal");
    // Inertness is DOM-tree-scoped: only a stack inside the dialog subtree is usable.
    expect(stack()!.parentElement).toBe(dlg);
    dlg.close();
    dlg.remove();
  });

  it("moves a live stack in and back out, carrying visible toasts along", async () => {
    toast.error("sticky");
    expect(stack()!.parentElement).toBe(document.body);

    const dlg = openModal();
    toast.info("raised over modal");
    expect(stack()!.parentElement).toBe(dlg);
    expect(toasts()).toHaveLength(2);

    await closeAndSettle(dlg);
    expect(stack()!.parentElement).toBe(document.body);
    expect(toasts()).toHaveLength(2);
    dlg.remove();
  });

  it("a toast raised after the modal closed lands on document.body", () => {
    const dlg = openModal();
    toast.info("in dialog");
    expect(stack()!.parentElement).toBe(dlg);
    dlg.close();
    dlg.remove();
    toast.info("after close");
    expect(stack()!.parentElement).toBe(document.body);
  });

  it("nested modals: hosts into the most recent, steps back one per close", async () => {
    const outer = openModal();
    const inner = openModal();
    toast.info("nested");
    expect(stack()!.parentElement).toBe(inner);
    await closeAndSettle(inner);
    expect(stack()!.parentElement).toBe(outer);
    await closeAndSettle(outer);
    expect(stack()!.parentElement).toBe(document.body);
    inner.remove();
    outer.remove();
  });

  it("an explicit container pins the stack — no auto-hosting into modals", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const t = createToaster({ container: host });
    const dlg = openModal();
    t.info("scoped");
    expect(host.querySelector(".uip-toast-stack")).not.toBeNull();
    dlg.close();
    dlg.remove();
    t.dispose();
  });

  it("recovers a stack whose hosting dialog was removed without firing close", () => {
    const dlg = openModal();
    toast.info("inside");
    expect(stack()!.parentElement).toBe(dlg);
    dlg.remove();
    expect(stack()).toBeNull();
    toast.info("recovers");
    expect(stack()!.parentElement).toBe(document.body);
  });

  /** How many of the spied add/removeEventListener calls were for `close`. */
  function closeCalls(spy: { mock: { calls: unknown[][] } }): number {
    return spy.mock.calls.filter((call) => call[0] === "close").length;
  }

  it("releases the dialog it evacuates: no close listener is left behind on it", async () => {
    const dlg = openModal();
    const addSpy = vi.spyOn(dlg, "addEventListener");
    const removeSpy = vi.spyOn(dlg, "removeEventListener");

    toast.info("over the modal");
    expect(stack()!.parentElement).toBe(dlg);
    expect(closeCalls(addSpy)).toBe(1);

    dlg.close();
    // close() queues the close event, so it has not fired on the next statement.
    await new Promise<void>((resolve) => {
      dlg.addEventListener("close", () => resolve(), { once: true });
    });
    expect(stack()!.parentElement).toBe(document.body);
    // A retained listener keeps a closed dialog wired to a stack that left it.
    expect(closeCalls(removeSpy)).toBe(1);
    dlg.remove();
  });

  it("dispose() releases the dialog the stack was hosted in", () => {
    const dlg = openModal();
    const removeSpy = vi.spyOn(dlg, "removeEventListener");

    const t = createToaster();
    t.info("inside the modal");
    expect(dlg.querySelector(".uip-toast-stack")).not.toBeNull();

    t.dispose();
    // A disposed toaster leaves nothing of itself on the page.
    expect(closeCalls(removeSpy)).toBe(1);
    dlg.close();
    dlg.remove();
  });

  it("does not churn the close listener when a second toast re-resolves the same dialog", () => {
    const dlg = openModal();
    const addSpy = vi.spyOn(dlg, "addEventListener");
    const removeSpy = vi.spyOn(dlg, "removeEventListener");

    toast.info("first");
    toast.info("second");
    expect(stack()!.parentElement).toBe(dlg);

    // Re-registering per toast would open a window where a racing close is missed.
    expect(closeCalls(addSpy)).toBe(1);
    expect(closeCalls(removeSpy)).toBe(0);
    dlg.close();
    dlg.remove();
  });

  it("an explicit <dialog> container is pinned, never adopted", () => {
    const dlg = openModal();
    const addSpy = vi.spyOn(dlg, "addEventListener");

    const t = createToaster({ container: dlg });
    t.info("pinned");
    expect(dlg.querySelector(".uip-toast-stack")).not.toBeNull();

    // Pinned means exempt from auto-hosting: never watched for close.
    expect(closeCalls(addSpy)).toBe(0);
    t.dispose();
    dlg.close();
    dlg.remove();
  });
});

describe("toast: default toaster laziness", () => {
  it("importing attaches no document listener and creates no DOM; first use does", async () => {
    vi.resetModules();
    const spy = vi.spyOn(document, "addEventListener");
    const fresh = await import("./index.js");
    const keydownCalls = (): number => spy.mock.calls.filter(([type]) => type === "keydown").length;

    // Import-time: no Escape listener, no stack container.
    expect(keydownCalls()).toBe(0);
    expect(document.querySelector(".uip-toast-stack")).toBeNull();

    fresh.toast.info("first use");
    expect(keydownCalls()).toBe(1);
    expect(document.querySelector(".uip-toast-stack")).not.toBeNull();

    fresh._resetForTest();
    spy.mockRestore();
  });
});

describe("createToaster: option pass-through", () => {
  /** An isolated toaster in its own host, so the default singleton is untouched. */
  function scoped(opts: Parameters<typeof createToaster>[0]): {
    t: ReturnType<typeof createToaster>;
    host: HTMLElement;
  } {
    const host = document.createElement("div");
    document.body.appendChild(host);
    return { t: createToaster({ ...opts, container: host }), host };
  }

  function messages(host: HTMLElement): (string | null)[] {
    return [...host.querySelectorAll(".uip-toast-msg")].map((n) => n.textContent);
  }

  it("honors maxVisible, holding the overflow back", () => {
    const { t, host } = scoped({ maxVisible: 1 });
    t.info("one");
    t.info("two");
    expect(messages(host)).toEqual(["one"]);
    t.dispose();
  });

  it("honors maxQueue, dropping the oldest queued toast", () => {
    const { t, host } = scoped({ maxVisible: 1, maxQueue: 1 });
    const dismiss = t.info("one");
    t.info("two");
    t.info("three");

    dismiss();
    endTransition(host.querySelector<HTMLElement>(".uip-toast")!);
    expect(messages(host)).toEqual(["three"]);
    t.dispose();
  });

  it("honors defaultDuration for the auto-dismiss window", () => {
    const { t, host } = scoped({ defaultDuration: 100 });
    t.info("timed");
    const node = host.querySelector<HTMLElement>(".uip-toast")!;
    expect(node.style.getPropertyValue("--uip-toast-duration")).toBe("100ms");
    t.dispose();
  });

  it("raises success() at the success level, not the default", () => {
    const { t, host } = scoped({});
    t.success("Saved");
    const node = host.querySelector<HTMLElement>(".uip-toast")!;
    expect(node.classList.contains("uip-toast--success")).toBe(true);
    t.dispose();
  });

  it("clear() empties an isolated toaster's stack", () => {
    const { t, host } = scoped({});
    t.info("one");
    t.info("two");
    expect(messages(host)).toHaveLength(2);
    t.clear();
    expect(messages(host)).toHaveLength(0);
    t.dispose();
  });
});

describe("the default toaster's teardown seams", () => {
  it("toast.dispose() removes the stack it built", () => {
    toast.info("hi");
    expect(stack()).not.toBeNull();
    toast.dispose();
    expect(stack()).toBeNull();
  });

  it("_resetForTest() disposes the singleton so the next use rebuilds it", () => {
    toast.info("hi");
    expect(stack()).not.toBeNull();
    _resetForTest();
    expect(stack()).toBeNull();
    toast.info("again");
    expect(stack()).not.toBeNull();
  });
});

describe("toast: the engine port the view drives", () => {
  it("pauses the engine once for two overlapping pause sources, and resumes once", () => {
    const view = createToastView();
    const ctx = { dismiss: vi.fn(), pause: vi.fn(), resume: vi.fn() };
    const handle = view.mount({ message: "hover and focus", level: "info", duration: 4000 }, ctx);
    const node = handle.el;

    // Two DOM sources over one engine timer: ref-counted, resume fires once.
    node.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
    node.dispatchEvent(new Event("focusin", { bubbles: true }));
    expect(ctx.pause).toHaveBeenCalledTimes(1);

    node.dispatchEvent(new MouseEvent("mouseleave", { bubbles: true }));
    expect(ctx.resume).not.toHaveBeenCalled();
    node.dispatchEvent(new Event("focusout", { bubbles: true }));
    expect(ctx.resume).toHaveBeenCalledTimes(1);

    view.dispose();
  });
});

describe("toast: teardown cancels pending work", () => {
  /** A host element for an isolated toaster, so the singleton is untouched. */
  function makeHost(): HTMLElement {
    const host = document.createElement("div");
    document.body.appendChild(host);
    return host;
  }

  it("cancels a removed toast's pending enter frame so it cannot restyle the detached node", () => {
    vi.useFakeTimers();
    try {
      const t = createToaster({ container: makeHost(), mode: "replace" });
      t.info("first");
      const first = document.querySelector<HTMLElement>(".uip-toast")!;
      expect(first.classList.contains("is-entering")).toBe(true);

      t.info("second");
      vi.advanceTimersToNextFrame();
      expect(first.isConnected).toBe(false);
      expect(first.classList.contains("is-shown")).toBe(false);
      t.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a pending leave transition on clear(), leaving no fallback timer armed", () => {
    vi.useFakeTimers();
    try {
      const t = createToaster({ container: makeHost() });
      const dismiss = t.info("bye");
      vi.advanceTimersByTime(100);
      expect(vi.getTimerCount()).toBe(1);

      dismiss();
      expect(vi.getTimerCount()).toBe(1);

      t.clear();
      // The leave's fallback timer goes with the node it would have removed.
      expect(vi.getTimerCount()).toBe(0);
      t.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a live toast's countdown on dispose(), leaving no timer armed", () => {
    vi.useFakeTimers();
    try {
      const t = createToaster({ container: makeHost() });
      t.info("timed");
      vi.advanceTimersByTime(100);
      expect(vi.getTimerCount()).toBe(1);

      t.dispose();
      // A countdown outliving the view would dismiss into a torn-down stack.
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("toast: a failing retry handler", () => {
  function clickRetry(root: ParentNode): void {
    root
      .querySelector<HTMLButtonElement>(".uip-toast-retry")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
  }

  it("does not let the retry click escape the toast to the app's own root", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const onHostClick = vi.fn();
    host.addEventListener("click", onHostClick);

    const t = createToaster({ container: host });
    t.error("failed", { onClick: vi.fn() });
    clickRetry(host);

    // The retry button owns the click; a listener on the app's root must not see it.
    expect(onHostClick).not.toHaveBeenCalled();
    t.dispose();
  });

  it("reports a rejected retry handler instead of dropping the rejection", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const boom = new Error("nope");
    const onClick = vi.fn().mockRejectedValue(boom);

    toast.error("failed", { onClick });
    clickRetry(document);
    await Promise.resolve();
    await Promise.resolve();

    expect(logged).toHaveBeenCalledWith("[uip-toast] retry handler rejected", boom);
  });

  it("reports a retry handler that throws synchronously", () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const boom = new Error("sync boom");
    const onClick = vi.fn(() => {
      throw boom;
    });

    toast.error("failed", { onClick });
    clickRetry(document);

    expect(logged).toHaveBeenCalledWith("[uip-toast] retry handler threw", boom);
  });
});
