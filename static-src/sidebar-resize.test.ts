import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { setSidebarWidth, sidebarWidth } from "./device-view.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import type * as SidebarResize from "./sidebar-resize.js";
import { applyStoredSidebarWidth } from "./sidebar-width.js";

let seq = 0;
let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

// Every test re-evaluates the module, and each evaluation keeps its window resize listener for the
// life of the page. So the viewport is only ever resized while a `#sidebar` is mounted for those
// listeners to measure.
afterAll(async () => {
  document.body.replaceChildren(shellFixture());
  await page.viewport(1280, 720);
  await afterResize();
  document.body.replaceChildren();
  style.remove();
});

afterEach(async () => {
  await page.viewport(1440, 900);
  await afterResize();
  document.body.replaceChildren();
  document.body.classList.remove("sidebar-resizing");
  document.documentElement.style.removeProperty("--sidebar-w-pref");
  localStorage.clear();
});

/** Back the pointer-capture methods with a Set: a synthetic event carries no real pointer for
 *  the browser to capture. */
function stubPointerCapture(el: HTMLElement): void {
  const captured = new Set<number>();
  el.setPointerCapture = (id: number): void => {
    captured.add(id);
  };
  el.releasePointerCapture = (id: number): void => {
    captured.delete(id);
  };
  el.hasPointerCapture = (id: number): boolean => captured.has(id);
}

function ptr(
  type: string,
  x: number,
  opts: { isPrimary?: boolean; pointerId?: number } = {},
): Event {
  return Object.assign(new Event(type), {
    pointerId: opts.pointerId ?? 1,
    isPrimary: opts.isPrimary ?? true,
    clientX: x,
    clientY: 300,
  });
}

function key(k: string): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: k, cancelable: true });
}

function nextFrame(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** A resize event is dispatched inside a frame's rendering steps, so the frame the handler
 *  requests is the one AFTER the frame a test awaited beside it. */
async function afterResize(): Promise<void> {
  await nextFrame();
  await nextFrame();
}

interface Rig {
  mod: typeof SidebarResize;
  sidebar: HTMLElement;
  handle: HTMLElement;
  rendered: () => number;
  pref: () => string;
}

/** The production shell, cut to the three elements the module and the clamp read. */
function shellFixture(): HTMLElement {
  const app = document.createElement("div");
  app.id = "app";
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  const handle = document.createElement("div");
  handle.id = "sidebar-resize";
  handle.className = "sidebar-resize";
  stubPointerCapture(handle);
  sidebar.appendChild(handle);
  const chat = document.createElement("main");
  chat.id = "chat-area";
  app.append(sidebar, chat);
  return app;
}

async function boot(viewport = 1440, opts: { prepaint?: boolean } = {}): Promise<Rig> {
  document.body.replaceChildren(shellFixture());
  await page.viewport(viewport, 900);
  const sidebar = document.getElementById("sidebar");
  const handle = document.getElementById("sidebar-resize");
  if (sidebar === null || handle === null) {
    throw new Error("fixture not mounted");
  }

  seq++;
  const mod = (await import(
    /* @vite-ignore */ `./sidebar-resize.ts?t=${String(seq)}`
  )) as typeof SidebarResize;
  if (opts.prepaint !== false) {
    applyStoredSidebarWidth();
  }
  mod.initSidebarResize();
  return {
    mod,
    sidebar,
    handle,
    rendered: () => Math.round(sidebar.getBoundingClientRect().width),
    pref: () => document.documentElement.style.getPropertyValue("--sidebar-w-pref"),
  };
}

describe("the separator", () => {
  it("carries a complete value", async () => {
    const r = await boot();
    const h = r.handle;
    expect(h.getAttribute("role")).toBe("separator");
    expect(h.getAttribute("aria-orientation")).toBe("vertical");
    expect(h.getAttribute("aria-label")).toBe("Resize sidebar");
    expect(h.getAttribute("aria-controls")).toBe("sidebar");
    expect(document.getElementById(h.getAttribute("aria-controls") ?? "")).toBe(r.sidebar);
    expect(h.tabIndex).toBe(0);
    expect(h.getAttribute("aria-valuenow")).toBe("260");
    expect(h.getAttribute("aria-valuemin")).toBe("260");
    expect(h.getAttribute("aria-valuemax")).toBe("512");
    expect(h.getAttribute("aria-valuetext")).toBe("260 pixels");
    expect(h.parentElement?.closest('button, a[href], [role="button"]')).toBeNull();
  });
});

describe("dragging", () => {
  it("writes once per frame and persists only on release", async () => {
    const r = await boot();
    r.handle.dispatchEvent(ptr("pointerdown", 260));
    r.handle.dispatchEvent(ptr("pointermove", 300));
    r.handle.dispatchEvent(ptr("pointermove", 330));
    r.handle.dispatchEvent(ptr("pointermove", 360));
    expect(r.pref()).toBe("");
    expect(localStorage.getItem(LS_UI_STATE_KEY)).toBeNull();
    expect(document.body.classList.contains("sidebar-resizing")).toBe(true);

    await nextFrame();
    expect(r.pref()).toBe("360px");
    expect(r.rendered()).toBe(360);
    expect(localStorage.getItem(LS_UI_STATE_KEY)).toBeNull();

    r.handle.dispatchEvent(ptr("pointerup", 360));
    expect(sidebarWidth()).toBe(360);
    expect(r.handle.getAttribute("aria-valuenow")).toBe("360");
    expect(document.body.classList.contains("sidebar-resizing")).toBe(false);
  });

  it("responds at once when dragged past the maximum and back, and stores what it rendered", async () => {
    const r = await boot();
    r.handle.dispatchEvent(ptr("pointerdown", 260));
    r.handle.dispatchEvent(ptr("pointermove", 2000));
    await nextFrame();
    expect(r.handle.getAttribute("aria-valuenow")).toBe("512");
    r.handle.dispatchEvent(ptr("pointermove", 400));
    await nextFrame();
    expect(r.rendered()).toBe(400);
    r.handle.dispatchEvent(ptr("pointerup", 400));
    expect(sidebarWidth()).toBe(400);

    r.handle.dispatchEvent(ptr("pointerdown", 400));
    r.handle.dispatchEvent(ptr("pointermove", 2000));
    r.handle.dispatchEvent(ptr("pointerup", 2000));
    expect(sidebarWidth()).toBe(512);
  });

  it("stores the default when released at the minimum", async () => {
    setSidebarWidth(400);
    const r = await boot();
    expect(r.rendered()).toBe(400);
    r.handle.dispatchEvent(ptr("pointerdown", 400));
    r.handle.dispatchEvent(ptr("pointermove", 50));
    r.handle.dispatchEvent(ptr("pointerup", 50));
    expect(r.rendered()).toBe(260);
    expect(sidebarWidth()).toBe(0);
    expect(r.pref()).toBe("");
  });

  it("commits nothing for a press that never moved", async () => {
    setSidebarWidth(480);
    const r = await boot(1280);
    expect(r.rendered()).toBe(359);
    r.handle.dispatchEvent(ptr("pointerdown", 359));
    r.handle.dispatchEvent(ptr("pointerup", 359));
    expect(sidebarWidth()).toBe(480);
    expect(r.pref()).toBe("480px");
  });

  it("commits nothing when the window drops below the widening threshold mid-drag", async () => {
    setSidebarWidth(400);
    const r = await boot();
    r.handle.dispatchEvent(ptr("pointerdown", 400));
    r.handle.dispatchEvent(ptr("pointermove", 450));
    await nextFrame();
    expect(r.pref()).toBe("450px");
    await page.viewport(1000, 900);
    r.handle.dispatchEvent(ptr("pointermove", 460));
    await nextFrame();
    r.handle.dispatchEvent(ptr("pointerup", 460));
    expect(sidebarWidth()).toBe(400);
    await page.viewport(1440, 900);
    expect(r.rendered()).toBe(400);
  });

  it("keeps the preference when a shrink to a still-resizable window forces the endpoint", async () => {
    setSidebarWidth(480);
    const r = await boot(1920);
    r.handle.dispatchEvent(ptr("pointerdown", 480));
    r.handle.dispatchEvent(ptr("pointermove", 500));
    await nextFrame();
    await page.viewport(1280, 900);
    r.handle.dispatchEvent(ptr("pointermove", 520));
    await nextFrame();
    expect(r.handle.checkVisibility()).toBe(true);
    expect(r.rendered()).toBe(359);
    r.handle.dispatchEvent(ptr("pointerup", 520));
    expect(sidebarWidth()).toBe(480);
    await page.viewport(1920, 900);
    expect(r.rendered()).toBe(480);
  });

  it("after a snap, commits an inward drag and nothing for an outward one", async () => {
    setSidebarWidth(480);
    const r = await boot(1280);
    expect(r.rendered()).toBe(359);
    r.handle.dispatchEvent(ptr("pointerdown", 359));
    r.handle.dispatchEvent(ptr("pointermove", 420));
    r.handle.dispatchEvent(ptr("pointerup", 420));
    expect(sidebarWidth()).toBe(480);

    r.handle.dispatchEvent(ptr("pointerdown", 359));
    r.handle.dispatchEvent(ptr("pointermove", 300));
    r.handle.dispatchEvent(ptr("pointerup", 300));
    expect(sidebarWidth()).toBe(300);
    await page.viewport(1920, 900);
    expect(r.rendered()).toBe(300);
  });

  it("ignores a non-primary press and a move it did not capture", async () => {
    const r = await boot();
    r.handle.dispatchEvent(ptr("pointerdown", 260, { isPrimary: false }));
    r.handle.dispatchEvent(ptr("pointermove", 400));
    await nextFrame();
    r.handle.dispatchEvent(ptr("pointerup", 400));
    expect(r.pref()).toBe("");
    expect(localStorage.getItem(LS_UI_STATE_KEY)).toBeNull();
  });
});

describe("the keyboard and the reset", () => {
  it("steps by 1rem, jumps with Home and End, and persists each", async () => {
    const r = await boot();
    r.handle.dispatchEvent(key("ArrowRight"));
    expect(r.rendered()).toBe(276);
    expect(sidebarWidth()).toBe(276);
    r.handle.dispatchEvent(key("ArrowLeft"));
    r.handle.dispatchEvent(key("ArrowLeft"));
    expect(r.rendered()).toBe(260);
    expect(sidebarWidth()).toBe(0);
    r.handle.dispatchEvent(key("End"));
    expect(r.rendered()).toBe(512);
    expect(sidebarWidth()).toBe(512);
    expect(r.handle.getAttribute("aria-valuetext")).toBe("512 pixels");
    r.handle.dispatchEvent(key("Home"));
    expect(r.rendered()).toBe(260);
    expect(sidebarWidth()).toBe(0);
  });

  it("resets to the default on a double-click", async () => {
    const r = await boot();
    r.handle.dispatchEvent(key("End"));
    expect(sidebarWidth()).toBe(512);
    r.handle.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    expect(sidebarWidth()).toBe(0);
    expect(r.pref()).toBe("");
    expect(r.rendered()).toBe(260);
    expect(r.handle.getAttribute("aria-valuenow")).toBe("260");
  });
});

describe("a window resize", () => {
  it("never rewrites the preference, and refreshes the announced bounds", async () => {
    setSidebarWidth(480);
    const r = await boot();
    expect(r.rendered()).toBe(480);
    await page.viewport(960, 900);
    expect(r.rendered()).toBe(260);
    await afterResize();
    expect(r.handle.getAttribute("aria-valuemax")).toBe("260");
    expect(r.handle.getAttribute("aria-valuenow")).toBe("260");
    expect(sidebarWidth()).toBe(480);
    await page.viewport(1920, 900);
    expect(r.rendered()).toBe(480);
    await afterResize();
    expect(r.handle.getAttribute("aria-valuemax")).toBe("512");
    expect(r.handle.getAttribute("aria-valuenow")).toBe("480");
  });
});

describe("boot", () => {
  it("applies a stored width and ignores an invalid one", async () => {
    setSidebarWidth(400);
    let r = await boot();
    expect(r.pref()).toBe("400px");

    document.documentElement.style.removeProperty("--sidebar-w-pref");
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_w: "400" }));
    r = await boot();
    expect(r.pref()).toBe("");
    expect(r.rendered()).toBe(260);
  });

  it("applies the stored width itself when nothing applied it first", async () => {
    setSidebarWidth(480);
    const r = await boot(1440, { prepaint: false });
    expect(r.pref()).toBe("480px");
    expect(r.rendered()).toBe(480);
  });
});
