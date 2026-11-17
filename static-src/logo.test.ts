// The marotte logo across the shipped artifacts: one mark in the sprite, drawn bare in the text
// colour where it brands the app, grey where it decorates, and white on the brand violet wherever
// it stands as the app's icon.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { userEvent } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import faviconSvg from "../static/favicon.svg?raw";
import manifestRaw from "../static/manifest.json?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const BRAND_VIOLET: [number, number, number] = [0x90, 0x46, 0xff];

const images = import.meta.glob<string>(["../static/*.png", "../static/*.svg"], {
  query: "?url",
  import: "default",
  eager: true,
});

const doc = new DOMParser().parseFromString(indexHtml, "text/html");

function sprite(): SVGSymbolElement[] {
  return [...doc.querySelectorAll("symbol")] as SVGSymbolElement[];
}

function logoPath(): string {
  const d = doc.querySelector("symbol#vk-logo path")?.getAttribute("d") ?? "";
  expect(d, "the sprite has no #vk-logo path").not.toBe("");
  return d;
}

async function pixels(src: string): Promise<ImageData> {
  const url = images[`../static${src}`];
  expect(url, `${src} is not in static/`).toBeDefined();
  const res = await fetch(url ?? "");
  expect(res.ok, `${src} answers ${res.status}`).toBe(true);
  const bitmap = await createImageBitmap(await res.blob());
  const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
  const ctx = canvas.getContext("2d");
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.drawImage(bitmap, 0, 0);
  return ctx.getImageData(0, 0, bitmap.width, bitmap.height);
}

function at(img: ImageData, x: number, y: number): [number, number, number, number] {
  const i = (y * img.width + x) * 4;
  const d = img.data;
  return [d[i] ?? 0, d[i + 1] ?? 0, d[i + 2] ?? 0, d[i + 3] ?? 0];
}

/** The sRGB bytes Chromium paints for a computed colour string. */
function rgb(colour: string): [number, number, number] {
  const canvas = new OffscreenCanvas(1, 1);
  const ctx = canvas.getContext("2d", { willReadFrequently: true });
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.fillStyle = colour;
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0];
}

describe("the logo sprite", () => {
  it("holds one symbol, the bare mark as a single currentColor path", () => {
    expect(sprite().map((s) => s.id)).toEqual(["vk-logo"]);
    const symbol = doc.getElementById("vk-logo");
    const shapes = [...(symbol?.querySelectorAll("path, circle, rect, ellipse, polygon") ?? [])];
    expect(shapes.map((s) => s.tagName.toLowerCase())).toEqual(["path"]);
    expect(shapes[0]?.getAttribute("fill")).toBe("currentColor");
  });

  it("carries the mark's path data once in the page", () => {
    const d = logoPath();
    expect(indexHtml.split(d).length - 1).toBe(1);
  });

  it("is referenced from the page, so no symbol ships unused", () => {
    for (const symbol of sprite()) {
      expect(
        doc.querySelector(`use[href="#${symbol.id}"]`),
        `#${symbol.id} is unused`,
      ).not.toBeNull();
    }
  });

  it("is the favicon's artwork, path for path", () => {
    const favicon = new DOMParser().parseFromString(faviconSvg, "image/svg+xml");
    expect(favicon.querySelector("g path")?.getAttribute("d")).toBe(logoPath());
  });
});

describe("the favicon", () => {
  it("is the white mark on the brand violet tile", () => {
    const favicon = new DOMParser().parseFromString(faviconSvg, "image/svg+xml");
    expect(favicon.querySelector("rect")?.getAttribute("fill")?.toLowerCase()).toBe("#9046ff");
    expect(favicon.querySelector("g path")?.getAttribute("fill")).toBe("#fff");
  });
});

describe("the brand lockups", () => {
  for (const selector of [".logo", ".modal-logo"]) {
    it(`draws the bare mark in ${selector}, with no tile and no colour of its own`, () => {
      const svgs = [...doc.querySelectorAll(`${selector} svg`)];
      expect(svgs).toHaveLength(1);
      const svg = svgs[0];
      expect(svg?.querySelector("rect")).toBeNull();
      const uses = [...(svg?.querySelectorAll("use") ?? [])];
      expect(uses.map((u) => u.getAttribute("href"))).toEqual(["#vk-logo"]);
      for (const node of [svg, ...uses]) {
        expect(node?.getAttribute("color")).toBeNull();
        expect(node?.getAttribute("fill")).toBeNull();
      }
      const symbol = doc.getElementById("vk-logo");
      expect(symbol?.querySelector("rect, use"), "the symbol draws a tile").toBeNull();
    });
  }
});

describe("the rendered brand", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    style = mountAppCSS();
    host = document.createElement("div");
    const defs = doc.querySelector("body > svg");
    const header = doc.querySelector(".sidebar-header");
    const modal = doc.querySelector(".modal-logo");
    expect(defs && header && modal).toBeTruthy();
    const sidebar = document.createElement("nav");
    sidebar.id = "sidebar";
    sidebar.append(document.importNode(header as Element, true));
    host.append(
      document.importNode(defs as Element, true),
      sidebar,
      document.importNode(modal as Element, true),
    );
    document.body.append(host);
  });

  afterAll(() => {
    style.remove();
    host.remove();
  });

  afterEach(async () => {
    document.documentElement.removeAttribute("data-theme");
    await userEvent.unhover(document.body);
  });

  async function settled(): Promise<void> {
    await Promise.all(document.getAnimations().map((a) => a.finished));
  }

  function setTheme(theme: "dark" | "light"): void {
    if (theme === "dark") {
      document.documentElement.removeAttribute("data-theme");
    } else {
      document.documentElement.dataset["theme"] = theme;
    }
  }

  function part(selector: string): HTMLElement | SVGElement {
    const node = host.querySelector<HTMLElement | SVGElement>(selector);
    expect(node, `${selector} is not mounted`).not.toBeNull();
    return node as HTMLElement | SVGElement;
  }

  for (const theme of ["light", "dark"] as const) {
    it(`paints the sidebar mark in the wordmark's colour, at rest and hovered (${theme})`, async () => {
      setTheme(theme);
      await settled();
      const icon = part(".logo .logo-icon");
      const name = part(".logo .logo-name");
      expect(getComputedStyle(icon).color).toBe(getComputedStyle(name).color);
      const rest = getComputedStyle(name).color;

      await userEvent.hover(part(".logo"));
      await settled();
      expect(part(".logo").matches(":hover")).toBe(true);
      expect(getComputedStyle(name).color, "the hover state changes the wordmark").not.toBe(rest);
      expect(getComputedStyle(icon).color).toBe(getComputedStyle(name).color);
    });

    it(`paints the sign-in mark in its wordmark's colour (${theme})`, async () => {
      setTheme(theme);
      await settled();
      const modal = part(".modal-logo");
      const icon = part(".modal-logo svg");
      expect(getComputedStyle(icon).color).toBe(getComputedStyle(modal).color);
    });
  }

  for (const selector of [".logo", ".modal-logo"]) {
    it(`sits the mark level with the wordmark in ${selector}`, () => {
      const icon = part(`${selector} svg`).getBoundingClientRect();
      const text = part(selector === ".logo" ? ".logo .logo-name" : ".modal-logo > span");
      const box = text.getBoundingClientRect();
      const em = Number.parseFloat(getComputedStyle(text).fontSize);
      // The symbol's viewBox is the mark's own 3:5 extent, so the box is the ink.
      expect(icon.width / icon.height).toBeCloseTo(3 / 5, 2);
      expect(Math.abs(icon.top + icon.height / 2 - (box.top + box.height / 2))).toBeLessThanOrEqual(
        1,
      );
      expect(icon.height / em).toBeGreaterThanOrEqual(1);
      expect(icon.height / em).toBeLessThanOrEqual(1.5);
    });
  }
});

describe("the empty-chat mark", () => {
  let style: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    style = mountAppCSS();
    host = document.createElement("div");
    host.style.cssText = "position:relative;inline-size:800px;block-size:600px";
    const mark = doc.getElementById("empty-chat-logo");
    expect(mark).not.toBeNull();
    const node = document.importNode(mark as Element, true);
    node.classList.remove("hidden");
    host.append(node);
    document.body.append(host);
  });

  afterAll(() => {
    style.remove();
    host.remove();
    document.documentElement.removeAttribute("data-theme");
  });

  for (const theme of ["light", "dark"] as const) {
    it(`is grey in the ${theme} theme`, () => {
      if (theme === "light") {
        document.documentElement.dataset["theme"] = "light";
      } else {
        document.documentElement.removeAttribute("data-theme");
      }
      const svg = host.querySelector("svg");
      expect(svg?.querySelector("use")?.getAttribute("href")).toBe("#vk-logo");
      const [r, g, b] = rgb(getComputedStyle(svg as SVGElement).color);
      expect(Math.max(r, g, b) - Math.min(r, g, b)).toBeLessThanOrEqual(1);
    });
  }
});

interface ManifestIcon {
  src: string;
  sizes: string;
}

describe("the app icons", () => {
  const manifest = JSON.parse(manifestRaw) as {
    icons: ManifestIcon[];
    shortcuts: { icons: ManifestIcon[] }[];
  };
  const touch = [...doc.querySelectorAll('link[rel="apple-touch-icon"]')].map((l) => ({
    src: l.getAttribute("href") ?? "",
    // Apple's default when a link names no size.
    sizes: l.getAttribute("sizes") ?? "180x180",
  }));
  const pngs = [...manifest.icons, ...manifest.shortcuts.flatMap((s) => s.icons), ...touch].filter(
    (i) => i.src.endsWith(".png"),
  );

  it("names every icon it declares in static/", () => {
    for (const icon of [...manifest.icons, ...touch]) {
      expect(images[`../static${icon.src}`], `${icon.src} is missing`).toBeDefined();
    }
    expect(pngs.length).toBeGreaterThan(0);
  });

  for (const icon of pngs) {
    it(`draws ${icon.src} at its declared ${icon.sizes}, white on the brand violet`, async () => {
      const img = await pixels(icon.src);
      expect(`${img.width}x${img.height}`).toBe(icon.sizes);
      expect(at(img, 0, 0).slice(0, 3)).toEqual(BRAND_VIOLET);
      expect(at(img, img.width - 1, img.height - 1).slice(0, 3)).toEqual(BRAND_VIOLET);
      // icons.py places the mark's 100-unit box in the square 7..41 of the 48-unit tile.
      const lo = Math.floor((7 / 48) * img.width);
      const hi = Math.ceil((41 / 48) * img.width);
      let white = 0;
      let outside = 0;
      for (let y = 0; y < img.height; y++) {
        for (let x = 0; x < img.width; x++) {
          const [r, g, b] = at(img, x, y);
          if (r > 250 && g > 250 && b > 250) {
            white++;
            if (x < lo || x > hi || y < lo || y > hi) {
              outside++;
            }
          }
        }
      }
      expect(white / (img.width * img.height)).toBeGreaterThan(0.02);
      expect(outside, "white ink outside the mark's square").toBe(0);
    });
  }
});

describe("the notification images", () => {
  // sw-push.node.test.ts and notify-show.test.ts pin that the notifications name these files.
  it("has a PNG app icon, because Chromium draws no SVG notification icon", async () => {
    // https://issues.chromium.org/issues/41169211
    const img = await pixels("/icon-192.png");
    expect([img.width, img.height]).toEqual([192, 192]);
  });

  it("badges with the white mark on transparency, the only channel Android reads", async () => {
    const img = await pixels("/badge-96.png");
    expect([img.width, img.height]).toEqual([96, 96]);
    let opaque = 0;
    let x0 = img.width;
    let x1 = -1;
    let y0 = img.height;
    let y1 = -1;
    for (let y = 0; y < img.height; y++) {
      for (let x = 0; x < img.width; x++) {
        const [r, g, b, a] = at(img, x, y);
        if (a === 0) {
          continue;
        }
        if (a >= 128) {
          expect([r, g, b], `badge pixel ${x},${y}`).toEqual([255, 255, 255]);
          opaque++;
        }
        x0 = Math.min(x0, x);
        x1 = Math.max(x1, x);
        y0 = Math.min(y0, y);
        y1 = Math.max(y1, y);
      }
    }
    expect(at(img, 0, 0)[3]).toBe(0);
    expect(opaque / (img.width * img.height)).toBeGreaterThan(0.02);
    // Android's 22 dp live area inside the 24 dp status-bar icon: 4 px clear at 96 px.
    expect(Math.min(x0, y0)).toBeGreaterThanOrEqual(4);
    expect(Math.max(x1, y1)).toBeLessThanOrEqual(91);
  });
});
