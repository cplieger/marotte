import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { info, success, error, notice, showToast, _resetForTest } from "./toast.js";

// The marotte wrapper against ui-primitives' toast DOM contract (.uip-toast--<level>, announce())
// and behaviour. State is reset with _resetForTest(), not resetModules(), which would strand the
// module-level Escape listener.
beforeEach(() => {
  _resetForTest();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  _resetForTest();
});

function getStack(): HTMLElement | null {
  return document.querySelector(".uip-toast-stack");
}

function toasts(): NodeListOf<Element> {
  return document.querySelectorAll(".uip-toast");
}

// Force the entry-frame requestAnimationFrame to fire so is-entering → is-shown.
function flushRaf(): void {
  vi.advanceTimersByTime(20);
}

describe("toast — basic rendering", () => {
  it("info creates a toast with uip-toast--info modifier", () => {
    info("hello");
    const t = document.querySelector(".uip-toast");
    expect(t).not.toBeNull();
    expect(t?.classList.contains("uip-toast--info")).toBe(true);
    expect(t?.textContent).toContain("hello");
  });

  it("success creates a toast with uip-toast--success modifier", () => {
    success("ok");
    const t = document.querySelector(".uip-toast");
    expect(t?.classList.contains("uip-toast--success")).toBe(true);
  });

  it("error creates a toast with uip-toast--error modifier", () => {
    error("boom");
    const t = document.querySelector(".uip-toast");
    expect(t?.classList.contains("uip-toast--error")).toBe(true);
  });

  it("announces via a shared live region (assertive for errors)", () => {
    // The library decouples announcement from the visual stack: the stack
    // carries no role/aria-live, and errors announce through the assertive
    // live region created by announce().
    error("boom");
    expect(getStack()?.getAttribute("aria-live")).toBeNull();
    expect(getStack()?.getAttribute("role")).toBeNull();
    expect(document.querySelector('[aria-live="assertive"]')).not.toBeNull();
  });

  // Every level has a finite duration and so a countdown bar; a RETRYABLE error is the sticky case.
  it("all levels include a progress bar; a retryable error does not", () => {
    for (const show of [info, success, error]) {
      _resetForTest();
      show("a");
      expect(toasts()[0]?.querySelector(".uip-toast-progress")).not.toBeNull();
    }
    _resetForTest();
    error("needs a decision", { label: "Retry", onClick: vi.fn() });
    expect(toasts()[0]?.querySelector(".uip-toast-progress")).toBeNull();
  });

  it("toast text uses textContent, not HTML", () => {
    info("<script>alert(1)</script>");
    const msg = document.querySelector(".uip-toast-msg");
    expect(msg?.textContent).toBe("<script>alert(1)</script>");
    expect(document.querySelector(".uip-toast script")).toBeNull();
  });
});

describe("toast — auto-dismiss", () => {
  it("auto-dismisses info after 4s", () => {
    info("hi");
    flushRaf();
    expect(toasts().length).toBe(1);
    vi.advanceTimersByTime(4000);
    // Trigger the leave fallback (no stylesheet is loaded, so no transition runs).
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  // A sticky error stacks with the next ones for the rest of the session. 12s is past a
  // comfortable read.
  it("auto-dismisses a plain error after 12s", () => {
    error("fail");
    flushRaf();
    vi.advanceTimersByTime(11_000);
    expect(toasts().length).toBe(1);
    vi.advanceTimersByTime(2000);
    expect(toasts().length).toBe(0);
  });

  // The exception: a retry button is the only route to the offered action, so
  // timing it out would silently discard it.
  it("a retryable error stays until answered", () => {
    error("fail", { label: "Retry", onClick: vi.fn() });
    flushRaf();
    vi.advanceTimersByTime(60_000);
    expect(toasts().length).toBe(1);
  });

  it("showToast accepts an explicit duration", () => {
    showToast("slow", "info", 10000);
    flushRaf();
    vi.advanceTimersByTime(4000);
    expect(toasts().length).toBe(1); // still there past info default
    vi.advanceTimersByTime(7000);
    expect(toasts().length).toBe(0);
  });

  it("showToast(msg, 'error') with no duration uses the 12s error default", () => {
    showToast("explicit", "error");
    flushRaf();
    vi.advanceTimersByTime(11_000);
    expect(toasts().length).toBe(1);
    vi.advanceTimersByTime(2000);
    expect(toasts().length).toBe(0);
  });
});

describe("toast — dismissal", () => {
  it("click dismisses the toast", () => {
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    t.click();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("Escape dismisses the most recent toast (LIFO)", () => {
    info("a");
    info("b");
    flushRaf();
    expect(toasts().length).toBe(2);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(1);
    expect(toasts()[0]?.textContent).toContain("a");
  });

  it("returns a manual dismiss function", () => {
    const close = info("hi");
    flushRaf();
    expect(toasts().length).toBe(1);
    close();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("returned dismiss is idempotent", () => {
    const close = info("hi");
    flushRaf();
    close();
    close(); // safe to call again
    close();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });
});

describe("toast — pause on hover/focus", () => {
  it("mouseenter pauses the timer; mouseleave resumes", () => {
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    vi.advanceTimersByTime(2000); // half the duration elapsed
    t.dispatchEvent(new MouseEvent("mouseenter"));
    vi.advanceTimersByTime(60000); // long pause
    expect(toasts().length).toBe(1); // still visible
    t.dispatchEvent(new MouseEvent("mouseleave"));
    vi.advanceTimersByTime(2100); // remaining ~2s
    vi.advanceTimersByTime(500); // leave fallback
    expect(toasts().length).toBe(0);
  });

  it("focusin pauses; focusout resumes", () => {
    info("hi");
    flushRaf();
    const t = toasts()[0] as HTMLElement;
    vi.advanceTimersByTime(2000);
    t.dispatchEvent(new FocusEvent("focusin"));
    vi.advanceTimersByTime(60000);
    expect(toasts().length).toBe(1);
    t.dispatchEvent(new FocusEvent("focusout"));
    vi.advanceTimersByTime(2100);
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });
});

describe("toast — queue + max-visible", () => {
  it("queues 4th toast when 3 are visible", () => {
    info("1");
    info("2");
    info("3");
    info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    const texts = [...toasts()].map((t) => t.textContent);
    expect(texts.some((x) => x?.includes("1"))).toBe(true);
    expect(texts.some((x) => x?.includes("2"))).toBe(true);
    expect(texts.some((x) => x?.includes("3"))).toBe(true);
    expect(texts.some((x) => x?.includes("4"))).toBe(false);
  });

  it("promotes queued toast on dismiss", () => {
    info("1");
    info("2");
    info("3");
    info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    (toasts()[0] as HTMLElement).click();
    vi.advanceTimersByTime(500);
    flushRaf();
    expect(toasts().length).toBe(3);
    const texts = [...toasts()].map((t) => t.textContent);
    expect(texts.some((x) => x?.includes("4"))).toBe(true);
  });

  it("dismissing a queued toast (before mount) removes it from queue", () => {
    info("1");
    info("2");
    info("3");
    const close4 = info("4");
    flushRaf();
    expect(toasts().length).toBe(3);
    // Dismiss the queued one before any visible one drops.
    close4();
    // Now dismiss a visible one — nothing should promote (queue empty).
    (toasts()[0] as HTMLElement).click();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(2);
  });
});

// The stack promotes only on a dismiss or expiry, so an unbounded sticky set stops delivering. See
// MAX_STICKY.
describe("toast — the sticky set is bounded", () => {
  const retry = { label: "Sign in", onClick: vi.fn() };

  function texts(): string[] {
    return [...toasts()].map((t) => t.textContent ?? "");
  }

  // Two remedies are two problems offering two different actions, so both stand.
  it("keeps two sticky toasts", () => {
    error("remedy one", retry);
    error("remedy two", retry);
    vi.advanceTimersByTime(500);
    flushRaf();
    expect(toasts().length).toBe(2);
  });

  it("dismisses the oldest sticky when a third arrives", () => {
    error("remedy one", retry);
    error("remedy two", retry);
    error("remedy three", retry);
    vi.advanceTimersByTime(500); // the evicted toast's leave
    flushRaf();
    expect(toasts().length).toBe(2);
    expect(texts().some((x) => x.includes("remedy one"))).toBe(false);
    expect(texts().some((x) => x.includes("remedy three"))).toBe(true);
  });

  // THE CASE THE CAP EXISTS FOR: three sticky notices would hold every slot and queue this error
  // unseen for the page's life.
  it("lets an ordinary error reach the screen after a third remedy", () => {
    error("remedy one", retry);
    error("remedy two", retry);
    error("remedy three", retry);
    vi.advanceTimersByTime(500);
    flushRaf();
    error("something else broke");
    flushRaf();
    expect(texts().some((x) => x.includes("something else broke"))).toBe(true);
  });

  // A 12s error expires on its own, so it is not part of the population being
  // bounded and a remedy must not evict one.
  it("does not count a plain error against the cap", () => {
    error("plain one");
    error("plain two");
    error("remedy", retry);
    vi.advanceTimersByTime(500);
    flushRaf();
    expect(texts().some((x) => x.includes("plain one"))).toBe(true);
    expect(texts().some((x) => x.includes("remedy"))).toBe(true);
  });
});

describe("toast — accessibility attributes", () => {
  it("toast is keyboard-focusable (tabindex=0)", () => {
    info("hi");
    const t = document.querySelector(".uip-toast");
    expect(t?.getAttribute("tabindex")).toBe("0");
  });

  it("progress bar is aria-hidden", () => {
    info("hi");
    const bar = document.querySelector(".uip-toast-progress");
    expect(bar?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("toast — retry button", () => {
  it("error() with retry renders a button + attaches handler", () => {
    const onClick = vi.fn();
    error("Failed", { onClick });
    flushRaf();
    const btn = document.querySelector(".uip-toast-retry");
    expect(btn).not.toBeNull();
    expect(btn?.textContent).toBe("Retry");
  });

  it("custom retry label is rendered", () => {
    error("Failed", { label: "Try again", onClick: vi.fn() });
    flushRaf();
    expect(document.querySelector(".uip-toast-retry")?.textContent).toBe("Try again");
  });

  it("clicking retry invokes onClick and dismisses the toast", () => {
    const onClick = vi.fn();
    error("Failed", { onClick });
    flushRaf();
    const btn = document.querySelector(".uip-toast-retry") as HTMLButtonElement;
    btn.click();
    expect(onClick).toHaveBeenCalledOnce();
    vi.advanceTimersByTime(500);
    expect(toasts().length).toBe(0);
  });

  it("retry click does not also trigger toast click-to-dismiss handler", () => {
    const onClick = vi.fn();
    error("Failed", { onClick });
    flushRaf();
    const btn = document.querySelector(".uip-toast-retry") as HTMLButtonElement;
    btn.click();
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("error without retry config does not render the button", () => {
    error("No retry");
    flushRaf();
    expect(document.querySelector(".uip-toast-retry")).toBeNull();
  });

  it("a throwing onClick handler is logged but does not crash", () => {
    const consoleErr = vi.spyOn(console, "error").mockImplementation(() => undefined);
    error("Failed", {
      onClick: () => {
        throw new Error("oops");
      },
    });
    flushRaf();
    const btn = document.querySelector(".uip-toast-retry") as HTMLButtonElement;
    btn.click();
    expect(consoleErr).toHaveBeenCalled();
    consoleErr.mockRestore();
  });
});

describe("toast — a server notice carries its own level", () => {
  it("tints an info notice blue rather than leaving it the neutral base", () => {
    notice("Switched model", "info");
    const t = document.querySelector(".uip-toast");
    expect(t?.classList.contains("uip-toast--notice-info")).toBe(true);
    info("Plain fact");
    expect(toasts()[1]?.classList.contains("uip-toast--notice-info")).toBe(false);
  });

  it("tints a warning amber and keeps it up as long as an error", () => {
    notice("Rate limited", "warning");
    const t = document.querySelector(".uip-toast");
    expect(t?.classList.contains("uip-toast--notice-warning")).toBe(true);
    flushRaf();
    vi.advanceTimersByTime(11_000);
    expect(toasts().length).toBe(1);
    vi.advanceTimersByTime(1_500);
    expect(toasts().length).toBe(0);
  });

  it("gives an error notice the error face", () => {
    notice("Engine failed", "error");
    expect(document.querySelector(".uip-toast")?.classList.contains("uip-toast--error")).toBe(true);
  });

  // The library mounts a queued toast only when a slot frees, so the class cannot be
  // set at show time.
  it("tags a notice that mounts later from the queue", async () => {
    const dismissFirst = info("one");
    info("two");
    info("three");
    notice("Queued warning", "warning");
    expect(toasts().length).toBe(3);
    dismissFirst();
    vi.advanceTimersByTime(1_000);
    await Promise.resolve();
    const queued = [...toasts()].find((t) => t.textContent.includes("Queued warning"));
    expect(queued?.classList.contains("uip-toast--notice-warning")).toBe(true);
  });

  it("tints the notice and not a plain toast that shares its words", () => {
    info("Same words");
    notice("Same words", "warning");
    const [plain, warned] = [...toasts()];
    expect(plain?.classList.contains("uip-toast--notice-warning")).toBe(false);
    expect(warned?.classList.contains("uip-toast--notice-warning")).toBe(true);
  });

  it("gives a notice dismissed before it mounted no claim on the next toast", async () => {
    const dismissFirst = info("one");
    info("two");
    info("three");
    const dropQueued = notice("Same words", "warning");
    info("Same words");
    dropQueued();
    dismissFirst();
    vi.advanceTimersByTime(1_000);
    await Promise.resolve();
    const promoted = [...toasts()].find((t) => t.textContent.includes("Same words"));
    expect(promoted).toBeDefined();
    expect(promoted?.classList.contains("uip-toast--notice-warning")).toBe(false);
  });

  it("gives a notice the full queue dropped no claim on the next toast", async () => {
    const dismissFirst = info("one");
    info("two");
    info("three");
    notice("Dropped warning", "warning");
    for (let i = 0; i < 20; i++) {
      info(`queued ${String(i)}`);
    }
    dismissFirst();
    vi.advanceTimersByTime(1_000);
    await Promise.resolve();
    const promoted = [...toasts()].find((t) => t.textContent.includes("queued 0"));
    expect(promoted).toBeDefined();
    expect(promoted?.classList.contains("uip-toast--notice-warning")).toBe(false);
  });

  it("renders the action on the notice", () => {
    const onClick = vi.fn();
    notice("Elsewhere: retrying", "info", { label: "Open", onClick });
    const btn = document.querySelector(".uip-toast-retry") as HTMLButtonElement;
    expect(btn.textContent).toBe("Open");
    btn.click();
    expect(onClick).toHaveBeenCalledOnce();
  });
});
