import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { page, userEvent } from "vitest/browser";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";
import indexHtml from "../static/index.html?raw";
import { iconEl } from "./icon-el.js";
import {
  ICON_REFRESH,
  ICON_VIEWPORT_DESKTOP,
  ICON_VIEWPORT_FILL,
  ICON_VIEWPORT_PHONE,
  ICON_VIEWPORT_TABLET,
} from "./icons.js";

let style: HTMLStyleElement;
let host: HTMLElement;
let entry: { readonly width: number; readonly height: number } | null = null;

const MODES: readonly [string, string][] = [
  ["fill", ICON_VIEWPORT_FILL],
  ["phone", ICON_VIEWPORT_PHONE],
  ["tablet", ICON_VIEWPORT_TABLET],
  ["desktop", ICON_VIEWPORT_DESKTOP],
  ["page", ICON_VIEWPORT_DESKTOP],
];

function mount(): void {
  host = document.createElement("div");
  const view = new DOMParser().parseFromString(indexHtml, "text/html").getElementById("web-view")!;
  view.classList.remove("hidden");
  host.append(document.importNode(view, true));
  host
    .querySelector("#web-stage")!
    .insertAdjacentHTML(
      "beforeend",
      `<div class="web-frame" data-mode="phone"><iframe title="Preview"></iframe></div>`,
    );
  host.querySelector("#web-path")!.insertAdjacentHTML("beforeend", "<bdi>demo/index.html</bdi>");
  const viewport = host.querySelector<HTMLElement>("#web-viewport")!;
  for (const [mode, icon] of MODES) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = mode === "fill" ? "seg active" : "seg";
    btn.setAttribute("role", "radio");
    btn.setAttribute("aria-checked", String(mode === "fill"));
    btn.setAttribute("aria-label", mode);
    btn.tabIndex = mode === "fill" ? 0 : -1;
    const glyph = iconEl(icon);
    glyph.classList.add("seg-icon");
    btn.append(glyph);
    viewport.append(btn);
  }
  host.querySelector("#web-reload-btn")!.append(iconEl(ICON_REFRESH));
  document.body.append(host);
}

const q = <T extends Element = HTMLElement>(sel: string): T => host.querySelector<T>(sel)!;
const segs = (): HTMLElement[] => [...host.querySelectorAll<HTMLElement>(".web-viewport .seg")];
const px = (v: string): number => Number.parseFloat(v);
const hitFloor = (): number =>
  px(getComputedStyle(document.documentElement).getPropertyValue("--hit-floor"));

async function at(width: number, height: number, pointer: "fine" | "coarse"): Promise<void> {
  await page.viewport(width, height);
  expect(window.innerWidth).toBe(width);
  document.documentElement.dataset["pointer"] = pointer;
}

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  document.body.style.margin = "0";
  mount();
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

afterAll(async () => {
  host.remove();
  style.remove();
  await resetA11yMedia();
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

describe.each([
  ["desktop", 1440, 900, "fine"],
  ["tablet", 1024, 768, "coarse"],
] as const)("the toolbar at %s", (_name, width, height, pointer) => {
  it("keeps one control height across the switcher and the buttons", async () => {
    await at(width, height, pointer);
    const bar = q(".web-viewport").getBoundingClientRect();
    const reload = q("#web-reload-btn").getBoundingClientRect();
    expect(bar.height).toBeCloseTo(reload.height, 0);
    expect(reload.height).toBeGreaterThanOrEqual(hitFloor());
    expect(reload.width).toBeGreaterThanOrEqual(hitFloor());
    const scale = q("#web-scale-btn").getBoundingClientRect();
    expect(scale.height).toBeGreaterThanOrEqual(hitFloor());
    for (const seg of segs()) {
      expect(seg.getBoundingClientRect().width).toBeGreaterThanOrEqual(hitFloor());
    }
  });

  it("grows each segment's target to the bar's edges and no further sideways", async () => {
    await at(width, height, pointer);
    const bar = q(".web-viewport").getBoundingClientRect();
    const [first, second] = segs();
    const a = first!.getBoundingClientRect();
    const b = second!.getBoundingClientRect();
    const cx = a.left + a.width / 2;
    expect(document.elementFromPoint(cx, bar.top + 0.5)?.closest(".seg")).toBe(first);
    expect(document.elementFromPoint(cx, bar.bottom - 0.5)?.closest(".seg")).toBe(first);
    const gapX = (a.right + b.left) / 2;
    expect(document.elementFromPoint(gapX, a.top + a.height / 2)?.closest(".seg")).toBeNull();
  });
});

describe("the toolbar's shell", () => {
  it("is the editor's bottom toolbar, so its row layout is that rule's", async () => {
    await at(1440, 900, "fine");
    const bar = q("#web-view .web-page > .bottom-bar");
    expect(bar.classList.contains("editor-toolbar")).toBe(true);
    const cs = getComputedStyle(bar);
    expect(cs.display).toBe("flex");
    expect(cs.alignItems).toBe("center");
  });
});

describe("the toolbar on a phone-shaped screen", () => {
  it("hides the switcher and the scale toggle and keeps the path and Reload", async () => {
    await at(390, 844, "coarse");
    expect(getComputedStyle(q(".web-viewport")).display).toBe("none");
    expect(getComputedStyle(q("#web-scale-btn")).display).toBe("none");
    expect(getComputedStyle(q("#web-path")).display).not.toBe("none");
    const reload = q("#web-reload-btn").getBoundingClientRect();
    expect(reload.width).toBeGreaterThanOrEqual(hitFloor());
  });
});

describe("surfaces and motion", () => {
  it("paints no shadow on the frame or any control", async () => {
    await at(1440, 900, "fine");
    const els = [
      q(".web-frame"),
      q(".web-frame > iframe"),
      q("#web-view .editor-toolbar"),
      q("#web-scale-btn"),
      q("#web-reload-btn"),
      ...segs(),
    ];
    for (const e of els) {
      expect(getComputedStyle(e).boxShadow).toBe("none");
    }
  });

  it("moves the frame instantly", () => {
    for (const e of [q(".web-frame"), q(".web-frame > iframe")]) {
      const cs = getComputedStyle(e);
      expect(cs.transitionDuration).toBe("0s");
      expect(cs.animationName).toBe("none");
    }
  });

  it("collapses the segments' micro-transition under reduced motion", async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
    const dur = px(getComputedStyle(segs()[1]!).transitionDuration);
    expect(dur).toBeLessThanOrEqual(0.00001);
    await resetA11yMedia();
  });

  it("rings a keyboard-focused radio and button", async () => {
    await at(1440, 900, "fine");
    q("#web-path").setAttribute("tabindex", "-1");
    q("#web-path").focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(segs()[0]);
    expect(getComputedStyle(segs()[0]!).outlineStyle).not.toBe("none");
    await userEvent.tab();
    expect(document.activeElement).toBe(q("#web-scale-btn"));
    expect(getComputedStyle(q("#web-scale-btn")).outlineStyle).not.toBe("none");
    q("#web-path").removeAttribute("tabindex");
  });
});

describe("the stylesheet's own vocabulary", () => {
  const css = loadCSS("20-web-preview.css").replace(/\/\*[\s\S]*?\*\//g, "");

  it("declares no shadow, motion or literal colour", () => {
    expect(css).not.toMatch(/box-shadow|drop-shadow|transition|animation/);
    expect(css).not.toMatch(/#[0-9a-f]{3,8}\b|rgb\(|oklch\(|hsl\(/i);
  });

  it("reads only tokens that exist, plus the three the view writes", () => {
    const declared = new Set(
      [loadCSS("01-tokens.css"), loadCSS("11-page-lists.css")].flatMap((s) =>
        [...s.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]),
      ),
    );
    const written = new Set(["--web-frame-w", "--web-frame-h", "--web-scale"]);
    const reads = [...css.matchAll(/var\(\s*(--[\w-]+)/g)].map((m) => m[1]!);
    expect(reads.filter((r) => !declared.has(r) && !written.has(r))).toEqual([]);
  });
});
