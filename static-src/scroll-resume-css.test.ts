// The resume control is one component for the chat and the run tab: the same square glyph button,
// docked at its reading area's inline end, never centred over the run's content.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { page } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement | undefined;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(async () => {
  host?.remove();
  host = undefined;
  await page.viewport(1280, 720);
});

function resumeButton(id: string): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.id = id;
  btn.className = "scroll-resume";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  const label = document.createElement("span");
  label.textContent = "Latest";
  btn.append(svg, label);
  return btn;
}

/** The run tab's nesting: the view, its scroller and the measure inside it. */
function runTab(): { scroller: HTMLElement; btn: HTMLButtonElement } {
  host = document.createElement("div");
  host.id = "run-view";
  host.style.cssText = "position:fixed;inset:0;";
  const scroller = document.createElement("div");
  scroller.className = "page-content";
  const body = document.createElement("div");
  body.id = "run-body";
  body.style.blockSize = "3000px";
  const btn = resumeButton("run-scroll-bottom");
  scroller.append(body, btn);
  host.append(scroller);
  document.body.append(host);
  return { scroller, btn };
}

/** The chat's nesting the control is positioned in, narrower than the turn map's threshold, whose
 *  foot form `scroll-bottom-parity.test.ts` owns. */
function chat(): HTMLButtonElement {
  host = document.createElement("div");
  host.id = "chat-area";
  host.style.cssText =
    "position:fixed;top:0;left:0;inline-size:min(100vw, 800px);block-size:100vh;";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  outer.style.cssText = "position:relative;block-size:600px;";
  const btn = resumeButton("scroll-bottom");
  outer.append(btn);
  host.append(outer);
  document.body.append(host);
  return btn;
}

describe("the run tab's resume control", () => {
  it("sits at the scroller's inline end, a square glyph with its label withheld", () => {
    const { scroller, btn } = runTab();
    const s = scroller.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    const pad = parseFloat(getComputedStyle(scroller).paddingInlineEnd);
    const gutter = (scroller.offsetWidth - scroller.clientWidth) / 2;
    expect(Math.abs(s.right - b.right - pad - gutter)).toBeLessThanOrEqual(1);
    expect(b.left - s.left).toBeGreaterThan((s.right - b.right) * 4);
    expect(Math.abs(b.width - b.height)).toBeLessThanOrEqual(0.5);
    expect(getComputedStyle(btn.querySelector("span")!).display).toBe("none");
  });

  it("is the chat's own phone control, box for box", async () => {
    await page.viewport(390, 844);
    const chatBtn = chat().getBoundingClientRect();
    host?.remove();
    const { btn } = runTab();
    const runBtn = btn.getBoundingClientRect();
    expect([runBtn.width, runBtn.height]).toEqual([chatBtn.width, chatBtn.height]);
  });
});

describe("the chat's resume control", () => {
  it("docks in the corner on a phone", async () => {
    await page.viewport(390, 844);
    const btn = chat();
    const outer = btn.parentElement!.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    expect(Math.abs(outer.right - b.right - 16)).toBeLessThanOrEqual(1);
    expect(getComputedStyle(btn.querySelector("span")!).display).toBe("none");
  });

  it("is the centred labelled pill on a wider window", () => {
    const btn = chat();
    const outer = btn.parentElement!.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    expect(Math.abs(b.left + b.width / 2 - (outer.left + outer.width / 2))).toBeLessThanOrEqual(1);
    expect(getComputedStyle(btn.querySelector("span")!).display).not.toBe("none");
  });
});
