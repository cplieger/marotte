// The stack's placement contract: five custom properties read with fallbacks
// and never declared in :root, so a host in cascade layers can set them.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";

async function loadShippedCss(): Promise<HTMLLinkElement> {
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = "/legacy-ui/ui-primitives.css";
  const loaded = new Promise<void>((resolve, reject) => {
    link.addEventListener("load", () => {
      resolve();
    });
    link.addEventListener("error", () => {
      reject(new Error("could not load /legacy-ui/ui-primitives.css"));
    });
  });
  document.head.appendChild(link);
  await loaded;
  return link;
}

let link: HTMLLinkElement;
const styles: HTMLStyleElement[] = [];

function mount(): { stack: HTMLElement; toast: HTMLElement } {
  const stack = document.createElement("div");
  stack.className = "uip-toast-stack";
  const toast = document.createElement("div");
  toast.className = "uip-toast";
  toast.style.transition = "none";
  stack.append(toast);
  document.body.append(stack);
  return { stack, toast };
}

function hostCss(css: string, layer = true): void {
  const style = document.createElement("style");
  style.textContent = layer ? `@layer app { ${css} }` : css;
  document.head.append(style);
  styles.push(style);
}

const PROPS = [
  "--uip-toast-inset-block-start",
  "--uip-toast-inset-block-end",
  "--uip-toast-inset-inline-start",
  "--uip-toast-inset-inline-end",
  "--uip-toast-shift",
];

beforeAll(async () => {
  link = await loadShippedCss();
});

afterAll(() => {
  link.remove();
});

afterEach(() => {
  for (const s of styles.splice(0)) {
    s.remove();
  }
  document.body.replaceChildren();
});

describe("toast placement", () => {
  // A fixed box's computed insets resolve to pixels, so the box's own edges are read.
  const edges = (el: HTMLElement) => {
    const r = el.getBoundingClientRect();
    return { top: r.top, end: innerWidth - r.right, bottom: innerHeight - r.bottom, start: r.left };
  };

  it("sits at the top end, 1rem in, and drops from 8px above, with nothing set", () => {
    const { stack, toast } = mount();
    const e = edges(stack);
    expect([e.top, e.end]).toEqual([16, 16]);
    expect(e.start).toBeGreaterThan(16);
    expect(new DOMMatrix(getComputedStyle(toast).transform).f).toBe(-8);
  });

  it("declares none of the placement properties in :root", () => {
    const root = getComputedStyle(document.documentElement);
    expect(PROPS.map((p) => root.getPropertyValue(p))).toEqual(PROPS.map(() => ""));
  });

  it("moves to the bottom start for a host that sets the properties from a layer", () => {
    hostCss(`:root {
      --uip-toast-inset-block-start: auto;
      --uip-toast-inset-block-end: 24px;
      --uip-toast-inset-inline-start: 12px;
      --uip-toast-inset-inline-end: auto;
      --uip-toast-shift: 8px;
    }`);
    const { stack, toast } = mount();
    const e = edges(stack);
    expect([e.bottom, e.start]).toEqual([24, 12]);
    expect(e.top).toBeGreaterThan(24);
    expect(new DOMMatrix(getComputedStyle(toast).transform).f).toBe(8);
  });

  it("keeps --uip-toast-offset working for both default edges", () => {
    hostCss(":root { --uip-toast-offset: 2rem; }", false);
    const e = edges(mount().stack);
    expect([e.top, e.end]).toEqual([32, 32]);
  });
});
