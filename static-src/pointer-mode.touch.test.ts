import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { initPointerModeToggle } from "./pointer-mode.js";
import { currentTier, initPointerTier, touchCapable } from "./pointer-tier.js";
import { setPointerModeChoice, type PointerTier } from "./device-view.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

function mountButton(): HTMLButtonElement {
  const at = indexHtml.indexOf('id="pointer-mode-btn"');
  expect(at, "static/index.html has no pointer-mode-btn").toBeGreaterThan(-1);
  const open = indexHtml.lastIndexOf("<button", at);
  const end = indexHtml.indexOf("</button>", at) + "</button>".length;
  document.body.innerHTML = indexHtml.slice(open, end);
  return document.getElementById("pointer-mode-btn") as HTMLButtonElement;
}

let entry: { readonly width: number; readonly height: number } | null = null;
let styleEl: HTMLStyleElement | null = null;

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  styleEl = mountAppCSS();
});

afterAll(async () => {
  styleEl?.remove();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
  document.body.innerHTML = "";
});

async function displayAt(width: number, height: number, mode: PointerTier): Promise<string> {
  await page.viewport(width, height);
  expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
    width,
    height,
  ]);
  expect(matchMedia("(any-pointer: fine)").matches, "this project's page has no mouse").toBe(false);
  setPointerModeChoice(mode);
  const btn = mountButton();
  initPointerTier();
  initPointerModeToggle();
  expect(currentTier()).toBe(mode);
  expect(touchCapable(), "the touch gate passes, so CSS decides").toBe(true);
  expect(btn.classList.contains("hidden")).toBe(false);
  return getComputedStyle(btn).display;
}

describe("the toggle on a touch-only device", () => {
  it("is hidden in touch mode on a phone in portrait", async () => {
    expect(await displayAt(360, 800, "coarse")).toBe("none");
  });

  it("is hidden in touch mode on the same phone rotated", async () => {
    expect(await displayAt(900, 400, "coarse")).toBe("none");
  });

  it("stays in mouse mode on a phone, as the way back to touch", async () => {
    expect(await displayAt(360, 800, "fine")).not.toBe("none");
  });

  it("is offered on a tablet in landscape", async () => {
    expect(await displayAt(1024, 768, "coarse")).not.toBe("none");
  });
});
