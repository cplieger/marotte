// The pre-paint panel state is a root attribute the stylesheet turns into "the
// closed panel, painted at its open height". Its whole value is the hand-over:
// shell.ts takes the panel from that rule to the base rule in one task, and the
// two must paint the same height or the panel jumps (or animates) at boot.
//
// Asserted against the assembled bundle in real Chromium, at a phone width so the
// composer's padding rule (50-mobile.css) differs from the shell-open one and the
// padding assertion can fail.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { dropStoredShellPanel, releaseStoredShellPanel } from "./shell-height.js";

const root = document.documentElement;
let style: HTMLStyleElement;

interface Rig {
  panel: HTMLElement;
  bar: HTMLElement;
}

function mount(): Rig {
  const app = document.createElement("div");
  app.id = "app";
  const chat = document.createElement("main");
  chat.id = "chat-area";
  const bar = document.createElement("form");
  bar.id = "prompt-form";
  bar.className = "bottom-bar";
  bar.textContent = "composer";
  const panel = document.createElement("div");
  panel.id = "shell-panel";
  panel.className = "shell-panel shell-closed";
  const resize = document.createElement("div");
  resize.id = "shell-resize";
  resize.className = "shell-resize";
  const header = document.createElement("div");
  header.className = "shell-header";
  const term = document.createElement("div");
  term.id = "shell-terminal";
  term.className = "shell-terminal";
  panel.append(resize, header, term);
  chat.append(bar, panel);
  app.append(chat);
  document.body.replaceChildren(app);
  return { panel, bar };
}

function prepaint(px: number): void {
  root.setAttribute("data-shell-open", "");
  root.style.setProperty("--shell-h", `${String(px)}px`);
}

function handOver(panel: HTMLElement, px: number): void {
  panel.style.setProperty("--shell-h", `${String(px)}px`);
  panel.classList.remove("shell-closed");
  root.removeAttribute("data-shell-open");
  root.style.removeProperty("--shell-h");
}

const heightOf = (el: HTMLElement): number => el.getBoundingClientRect().height;
const paddingBottom = (el: HTMLElement): string => getComputedStyle(el).paddingBottom;

beforeAll(async () => {
  style = mountAppCSS();
  await page.viewport(390, 844);
});

afterAll(async () => {
  style.remove();
  await page.viewport(1280, 720);
});

afterEach(() => {
  root.removeAttribute("data-shell-open");
  root.style.removeProperty("--shell-h");
  document.body.replaceChildren();
});

describe("the pre-paint panel rule", () => {
  it("paints the closed panel open at the stored height, with no transition", () => {
    const { panel } = mount();
    prepaint(300);
    const cs = getComputedStyle(panel);
    expect(heightOf(panel)).toBe(300);
    expect(cs.visibility).toBe("visible");
    expect(cs.opacity).toBe("1");
    expect(cs.transitionDuration).toBe("0s");
    expect(panel.getAnimations()).toHaveLength(0);
  });

  it("gives the composer the padding an open panel gives it", () => {
    const closed = paddingBottom(mount().bar);
    const { panel, bar } = mount();
    prepaint(300);
    const prePainted = paddingBottom(bar);
    handOver(panel, 300);
    const open = paddingBottom(bar);
    expect(prePainted).toBe(open);
    expect(prePainted).not.toBe(closed);
  });

  it("hands over to the open panel without moving or animating", () => {
    const { panel, bar } = mount();
    prepaint(300);
    const before = heightOf(panel);
    const barBefore = heightOf(bar);
    handOver(panel, 300);
    expect(Math.abs(heightOf(panel) - before)).toBeLessThanOrEqual(0.5);
    expect(panel.getAnimations()).toHaveLength(0);
    expect(heightOf(bar)).toBe(barBefore);
  });

  it("falls back to the base rule's default height when none was stored", () => {
    const { panel } = mount();
    root.setAttribute("data-shell-open", "");
    const prePainted = heightOf(panel);
    panel.classList.remove("shell-closed");
    root.removeAttribute("data-shell-open");
    expect(prePainted).toBeGreaterThan(0);
    // A different default would start a height transition, which reads its start value.
    expect(panel.getAnimations()).toHaveLength(0);
    expect(heightOf(panel)).toBe(prePainted);
  });

  it("leaves a panel without the attribute closed", () => {
    const { panel } = mount();
    expect(heightOf(panel)).toBe(0);
    expect(getComputedStyle(panel).visibility).toBe("hidden");
  });

  it("restores the ordinary close animation once handed over", () => {
    const { panel } = mount();
    prepaint(300);
    handOver(panel, 300);
    // A transition starts from the last computed style, so the open one is read first.
    expect(heightOf(panel)).toBe(300);
    panel.classList.add("shell-closed");
    expect(panel.getAnimations().length).toBeGreaterThan(0);
  });
});

describe("ending the pre-paint state on a boot that restores nothing", () => {
  it("animates the panel shut when merely released", () => {
    const { panel } = mount();
    prepaint(300);
    expect(heightOf(panel)).toBe(300);
    releaseStoredShellPanel();
    expect(panel.getAnimations().length).toBeGreaterThan(0);
  });

  it("shuts the panel at once when dropped, and leaves no override behind", () => {
    const { panel } = mount();
    prepaint(300);
    expect(heightOf(panel)).toBe(300);
    dropStoredShellPanel(panel);
    expect(panel.getAnimations()).toHaveLength(0);
    expect(heightOf(panel)).toBe(0);
    expect(getComputedStyle(panel).visibility).toBe("hidden");
    expect(root.hasAttribute("data-shell-open")).toBe(false);
    expect(panel.style.getPropertyValue("transition")).toBe("");
  });
});
