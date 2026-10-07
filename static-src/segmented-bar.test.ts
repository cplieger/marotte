// The segmented switcher's controller, over real layout: it owns the ARIA wiring, the
// click-to-select seam, the keyboard model, the icon-only fit and the active projection, and it
// decides nothing about routes or panels.

import { afterEach, describe, expect, it, vi } from "vitest";

import { initSegmentedBar, setSegmentHidden } from "./segmented-bar.js";

type Tab = "one" | "two" | "three";
const TABS: readonly { id: Tab; label: string }[] = [
  { id: "one", label: "First section" },
  { id: "two", label: "Second section" },
  { id: "three", label: "Third section" },
];

/** A bar the way `static/index.html` authors one: buttons carrying the page's data attribute,
 *  each with a label span. The production rules that turn overflow into truncation are inlined,
 *  because the stylesheet is not loaded here and the fit reads `scrollWidth > clientWidth` off
 *  them. */
function buildBar(width: string, ids: readonly Tab[] = ["one", "two", "three"]): HTMLElement {
  const host = document.createElement("div");
  host.style.width = width;
  const bar = document.createElement("nav");
  bar.className = "seg-bar";
  bar.style.display = "flex";
  bar.setAttribute("aria-label", "Sections");
  for (const id of ids) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "seg";
    btn.dataset["testTab"] = id;
    btn.style.flex = "1";
    btn.style.minWidth = "0";
    btn.style.overflow = "hidden";
    btn.style.whiteSpace = "nowrap";
    const label = document.createElement("span");
    label.className = "seg-label";
    label.textContent = TABS.find((t) => t.id === id)?.label ?? id;
    btn.appendChild(label);
    bar.appendChild(btn);
  }
  host.appendChild(bar);
  document.body.appendChild(host);
  return bar;
}

const seg = (bar: HTMLElement, id: Tab): HTMLButtonElement => {
  const btn = bar.querySelector<HTMLButtonElement>(`[data-test-tab="${id}"]`);
  if (btn === null) {
    throw new Error(`no segment ${id}`);
  }
  return btn;
};

function wire(bar: HTMLElement): { paint: (t: Tab) => void; onSelect: ReturnType<typeof vi.fn> } {
  const onSelect = vi.fn();
  const paint = initSegmentedBar<Tab>(bar, {
    attr: "data-test-tab",
    idPrefix: "test",
    tabs: TABS,
    onSelect,
  });
  return { paint, onSelect };
}

afterEach(() => {
  document.body.replaceChildren();
});

describe("initSegmentedBar", () => {
  it("writes the tablist and tab roles, ids, names and controls, and leaves the bar's own name alone", () => {
    const bar = buildBar("600px");
    wire(bar);

    expect(bar.getAttribute("role")).toBe("tablist");
    expect(bar.getAttribute("aria-label")).toBe("Sections");
    for (const { id, label } of TABS) {
      const btn = seg(bar, id);
      expect(btn.getAttribute("role"), id).toBe("tab");
      expect(btn.id, id).toBe(`test-tab-${id}`);
      expect(btn.getAttribute("aria-label"), id).toBe(label);
      expect(btn.getAttribute("aria-controls"), id).toBe(`test-panel-${id}`);
    }
  });

  it("reports a click to the page and paints nothing of its own", () => {
    const bar = buildBar("600px");
    const { paint, onSelect } = wire(bar);
    paint("one");

    seg(bar, "two").click();
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith("two");
    // The page's store decides what is active; the click alone moves nothing.
    expect(seg(bar, "one").classList.contains("active")).toBe(true);
    expect(seg(bar, "two").classList.contains("active")).toBe(false);
  });

  it("projects the active segment onto class, aria-selected and the one tab stop", () => {
    const bar = buildBar("600px");
    const { paint } = wire(bar);

    paint("two");
    for (const { id } of TABS) {
      const on = id === "two";
      const btn = seg(bar, id);
      expect(btn.classList.contains("active"), id).toBe(on);
      expect(btn.getAttribute("aria-selected"), id).toBe(on ? "true" : "false");
      expect(btn.getAttribute("tabindex"), id).toBe(on ? "0" : "-1");
    }

    paint("three");
    expect(seg(bar, "two").classList.contains("active")).toBe(false);
    expect(seg(bar, "two").getAttribute("aria-selected")).toBe("false");
    expect(seg(bar, "two").getAttribute("tabindex")).toBe("-1");
    expect(seg(bar, "three").classList.contains("active")).toBe(true);
    expect(seg(bar, "three").getAttribute("tabindex")).toBe("0");
  });

  it("moves focus between segments with the horizontal arrows", () => {
    const bar = buildBar("600px");
    const { paint } = wire(bar);
    paint("one");

    const first = seg(bar, "one");
    first.focus();
    expect(document.activeElement).toBe(first);

    first.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(seg(bar, "two"));
    // The moved-to segment is now the bar's one Tab stop.
    expect(seg(bar, "two").getAttribute("tabindex")).toBe("0");
    expect(first.getAttribute("tabindex")).toBe("-1");

    seg(bar, "two").dispatchEvent(
      new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }),
    );
    expect(document.activeElement).toBe(first);
  });

  it("drops every label when one cannot fit, and keeps them when all do", () => {
    const wide = buildBar("600px");
    wire(wide);
    expect(wide.classList.contains("seg-bar-icons")).toBe(false);

    const narrow = buildBar("120px");
    wire(narrow);
    expect(narrow.classList.contains("seg-bar-icons")).toBe(true);
  });

  it("wires the segments the markup has and skips one it lacks", () => {
    const bar = buildBar("600px", ["one", "three"]);
    const { paint, onSelect } = wire(bar);

    paint("three");
    expect(seg(bar, "three").classList.contains("active")).toBe(true);
    expect(seg(bar, "one").getAttribute("aria-selected")).toBe("false");

    seg(bar, "one").click();
    expect(onSelect).toHaveBeenCalledWith("one");
  });
});

describe("setSegmentHidden", () => {
  /** The app's `.hidden` utility (40-a11y.css), since the stylesheet is not loaded here. */
  function hiddenUtility(): HTMLStyleElement {
    const style = document.createElement("style");
    style.textContent = ".hidden { display: none !important; }";
    document.head.appendChild(style);
    return style;
  }

  it("refits the labels to the segments that remain, both ways", () => {
    const style = hiddenUtility();
    try {
      const bar = buildBar("240px");
      wire(bar);
      expect(bar.classList.contains("seg-bar-icons"), "three labels in 240px").toBe(true);

      setSegmentHidden(bar, "data-test-tab", "three", true);
      expect(seg(bar, "three").classList.contains("hidden")).toBe(true);
      expect(bar.classList.contains("seg-bar-icons"), "two labels in 240px").toBe(false);

      setSegmentHidden(bar, "data-test-tab", "three", false);
      expect(bar.classList.contains("seg-bar-icons"), "three again").toBe(true);
    } finally {
      style.remove();
    }
  });

  it("takes a withdrawn segment out of the arrow-key order", () => {
    const style = hiddenUtility();
    try {
      const bar = buildBar("600px");
      const { paint } = wire(bar);
      paint("one");
      setSegmentHidden(bar, "data-test-tab", "two", true);

      seg(bar, "one").focus();
      seg(bar, "one").dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }),
      );
      expect(document.activeElement).toBe(seg(bar, "three"));
    } finally {
      style.remove();
    }
  });
});
