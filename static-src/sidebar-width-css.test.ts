// Measured against the assembled stylesheet at real viewport sizes, because the clamp is resolved
// by the engine, not by script.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
});

afterAll(async () => {
  style.remove();
  await page.viewport(entry.width, entry.height);
});

afterEach(() => {
  document.body.replaceChildren();
  const root = document.documentElement;
  root.style.removeProperty("--sidebar-w-pref");
  root.style.removeProperty("font-size");
});

interface Shell {
  sidebar: HTMLElement;
  chat: HTMLElement;
  handle: HTMLElement;
  rail: HTMLElement;
}

/** The production shell, cut to what the readers and the rail's container query touch. The rail
 *  carries a marker because `.turn-rail:empty` hides it. */
function mountShell(): Shell {
  const app = document.createElement("div");
  app.id = "app";
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  const handle = document.createElement("div");
  handle.id = "sidebar-resize";
  handle.className = "sidebar-resize";
  sidebar.appendChild(handle);

  const chat = document.createElement("main");
  chat.id = "chat-area";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  const marker = document.createElement("button");
  marker.className = "rail-marker";
  marker.textContent = "1";
  rail.appendChild(marker);
  outer.appendChild(rail);
  chat.appendChild(outer);

  app.append(sidebar, chat);
  document.body.replaceChildren(app);
  return { sidebar, chat, handle, rail };
}

async function at(width: number, height = 900): Promise<void> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
}

function pref(value: string): void {
  document.documentElement.style.setProperty("--sidebar-w-pref", value);
}

function rootPx(): number {
  return parseFloat(getComputedStyle(document.documentElement).fontSize);
}

describe("the clamp renders the preference, bounded by the window", () => {
  it.each([
    [960, 260],
    [1100, 260],
    [1180, 260],
    [1181, 260],
    [1182, 261],
    [1280, 359],
    [1366, 445],
    [1440, 512],
    [1920, 512],
    [2560, 512],
  ])("at %ipx a maximal preference renders %ipx, and every reader agrees", async (vw, want) => {
    await at(vw);
    const s = mountShell();
    pref("9999px");
    expect(s.sidebar.getBoundingClientRect().width).toBeCloseTo(want, 0);
    expect(s.chat.getBoundingClientRect().left).toBeCloseTo(want, 0);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      document.documentElement.clientWidth,
    );
  });

  it("paints the body's two-tone stop at the rendered width", async () => {
    await at(1440);
    mountShell();
    pref("400px");
    const bg = getComputedStyle(document.body).backgroundImage;
    expect(bg).toContain("400px");
    expect(bg).not.toContain("260px");
  });

  it("keeps the user's width across a snapped window, with no script", async () => {
    const s = mountShell();
    pref("480px");
    const width = (): number => Math.round(s.sidebar.getBoundingClientRect().width);

    await at(1920);
    expect(width()).toBe(480);
    await at(960);
    expect(width()).toBe(260);
    await at(1920);
    expect(width()).toBe(480);

    await at(2560);
    expect(width()).toBe(480);
    await at(1280);
    expect(width()).toBe(359);
    await at(2560);
    expect(width()).toBe(480);
  });

  it("never hides the rail by widening", async () => {
    for (const vw of [1182, 1280, 1366, 1440, 1920, 2560]) {
      await at(vw);
      const s = mountShell();
      pref("9999px");
      expect(s.chat.getBoundingClientRect().width, `${String(vw)}px`).toBeGreaterThanOrEqual(
        57.5 * rootPx(),
      );
      expect(getComputedStyle(s.rail).display, `${String(vw)}px`).not.toBe("none");
    }
  });

  // A fractional root font size is what a non-default zoom or text size produces. 16.2px at 1280 is
  // a combination where the max expression without its `- 1px` leaves the chat area a layout unit
  // under 57.5rem and the rail hidden.
  it.each([
    ["16.2px", 1280],
    ["15.37px", 1310],
    ["16.71px", 1366],
    ["15.37px", 1440],
  ])("keeps the rail at a %s root font at %ipx", async (fontSize, vw) => {
    await at(vw);
    const s = mountShell();
    document.documentElement.style.fontSize = fontSize;
    pref("9999px");
    expect(s.sidebar.getBoundingClientRect().width, "premise: widened").toBeGreaterThan(
      16.25 * rootPx() + 1,
    );
    expect(s.chat.getBoundingClientRect().width).toBeGreaterThanOrEqual(57.5 * rootPx());
    expect(getComputedStyle(s.rail).display).not.toBe("none");
  });

  it("answers the registered limits in px, following the viewport", async () => {
    mountShell();
    const cs = (): CSSStyleDeclaration => getComputedStyle(document.documentElement);
    await at(1440);
    expect(cs().getPropertyValue("--sidebar-w-max")).toBe("512px");
    expect(cs().getPropertyValue("--sidebar-w-min")).toBe("260px");
    await at(1280);
    expect(cs().getPropertyValue("--sidebar-w-max")).toBe("359px");
  });

  it("ignores the preference in the drawer", async () => {
    await at(390, 844);
    const s = mountShell();
    pref("480px");
    expect(s.sidebar.getBoundingClientRect().width).toBe(390);
    expect(s.chat.getBoundingClientRect().left).toBe(0);
    expect(getComputedStyle(s.handle).display).toBe("none");
  });
});

describe("the handle exists exactly where it can widen", () => {
  it.each([
    [390, "none"],
    [768, "none"],
    [1180, "none"],
    [1181, "none"],
    [1182, "block"],
    [1440, "block"],
  ])("at %ipx its display is %s", async (vw, want) => {
    await at(vw);
    const s = mountShell();
    expect(getComputedStyle(s.handle).display).toBe(want);
  });
});

// `var()` is illegal in a query condition, so these literals cannot read a token.
describe("the query literals are the tokens' values", () => {
  const tokens = loadCSS("01-tokens.css");
  const rem = (name: string): number => {
    const m = new RegExp(`${name}:\\s*([\\d.]+)rem;`).exec(tokens);
    if (m?.[1] === undefined) {
      throw new Error(`no rem token ${name}`);
    }
    return Number(m[1]);
  };

  it("pins the rail's and the resume control's thresholds to --main-min-w", () => {
    const main = `${String(rem("--main-min-w"))}rem`;
    expect(loadCSS("29-turns.css")).toContain(`@container chat-area (width < ${main})`);
    expect(loadCSS("13-messages.css")).toContain(`@container chat-area (width >= ${main})`);
  });

  it("pins the handle's hide query to --sidebar-w-min + --main-min-w + the guard", () => {
    const sum = rem("--sidebar-w-min") + rem("--main-min-w");
    expect(loadCSS("10-shell-app.css")).toContain(
      `@media (width <= calc(${String(sum)}rem + 1px))`,
    );
    expect(tokens).toMatch(/--sidebar-w-max:\s*min\(32rem, 100vw - var\(--main-min-w\) - 1px\);/);
  });
});
