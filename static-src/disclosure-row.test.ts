// The four ways a row click must not reach the control, the two it must, and the forwarded click that would loop.
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { wireRowToggle } from "./disclosure-row.js";

interface Harness {
  row: HTMLElement;
  control: HTMLButtonElement;
  plain: HTMLElement;
  nested: HTMLButtonElement;
  link: HTMLAnchorElement;
  prose: HTMLElement;
  hits: () => number;
}

let built: HTMLElement[] = [];

function build(): Harness {
  const row = document.createElement("div");
  row.className = "row";

  const control = document.createElement("button");
  control.type = "button";
  control.className = "chevron";
  let n = 0;
  control.addEventListener("click", () => {
    n++;
  });

  const plain = document.createElement("span");
  plain.textContent = "title";

  const nested = document.createElement("button");
  nested.type = "button";
  nested.className = "action";
  nested.textContent = "act";

  const link = document.createElement("a");
  link.href = "#somewhere";
  link.textContent = "src/main.ts";

  const prose = document.createElement("div");
  prose.className = "prose";
  prose.textContent = "the request the user typed";

  row.append(control, plain, nested, link, prose);
  document.body.appendChild(row);
  built.push(row);
  return { row, control, plain, nested, link, prose, hits: () => n };
}

/** `HTMLElement.click()` sets `target` to its element, so clicking a descendant bubbles like a real click. */
function clickOn(el: HTMLElement): void {
  el.click();
}

function clearSelection(): void {
  document.getSelection()?.removeAllRanges();
}

beforeEach(() => {
  built = [];
  clearSelection();
});

afterEach(() => {
  for (const el of built) {
    el.remove();
  }
  clearSelection();
});

describe("wireRowToggle", () => {
  it("a click on inert row content activates the control", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.plain);
    expect(h.hits()).toBe(1);
  });

  it("a click on the row itself activates the control", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.row);
    expect(h.hits()).toBe(1);
  });

  it("activates exactly ONCE — the forwarded click does not re-enter", () => {
    // The forward calls control.click(), which bubbles back into the row; it stops because the control is a <button>.
    // A count of 2 (or a stack overflow) is the failure.
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.plain);
    expect(h.hits()).toBe(1);

    clickOn(h.plain);
    expect(h.hits()).toBe(2);
  });

  it("a click on the control itself activates it once, not twice", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.control);
    expect(h.hits()).toBe(1);
  });

  it("a nested button keeps its own click", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.nested);
    expect(h.hits()).toBe(0);
  });

  it("a nested link keeps its own click", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    clickOn(h.link);
    expect(h.hits()).toBe(0);
  });

  it("a control taken out of the document is not activated", () => {
    // A tool card detaches its chevron when it has nothing to reveal, and the detached control keeps its listeners.
    const h = build();
    wireRowToggle(h.row, h.control);
    h.control.remove();

    clickOn(h.plain);
    expect(h.hits()).toBe(0);
  });

  it("a click inside a live selection does not activate", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    document.getSelection()?.selectAllChildren(h.prose);
    expect(document.getSelection()?.isCollapsed).toBe(false);

    clickOn(h.prose);
    expect(h.hits()).toBe(0);
  });

  it("a collapsed selection does not block activation", () => {
    const h = build();
    wireRowToggle(h.row, h.control);

    document.getSelection()?.selectAllChildren(h.prose);
    document.getSelection()?.collapseToStart();

    clickOn(h.plain);
    expect(h.hits()).toBe(1);
  });
});
