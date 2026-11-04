import { afterEach, describe, expect, it } from "vitest";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_RUN, workflowIcon } from "./icons.js";

const hosts: HTMLElement[] = [];

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function mount(...nodes: Element[]): HTMLElement {
  const host = document.createElement("div");
  host.append(...nodes);
  document.body.append(host);
  hosts.push(host);
  return host;
}

describe("iconEl and a badged glyph's mask", () => {
  it("gives every clone a mask id of its own, and points its group at it", () => {
    const a = iconEl(workflowIcon("run"));
    const b = iconEl(workflowIcon("run"));
    const ids = [a, b].map((svg) => svg.querySelector("mask")?.id ?? "");
    expect(ids[0]).not.toBe("");
    expect(ids[0]).not.toBe(ids[1]);
    for (const [i, svg] of [a, b].entries()) {
      expect(svg.querySelector("g[mask]")?.getAttribute("mask")).toBe(`url(#${ids[i] ?? ""})`);
    }
  });

  // `url(#id)` resolves to the first match in the document: a shared id inside a hidden subtree
  // would unmask every later badge.
  it("keeps a visible badge masked when an earlier copy sits in a hidden subtree", () => {
    const hidden = mount(iconEl(workflowIcon("message")));
    hidden.hidden = true;
    const visible = iconEl(workflowIcon("message"));
    mount(visible);
    const id = visible.querySelector("mask")?.id ?? "";
    expect(document.querySelectorAll(`[id="${id}"]`)).toHaveLength(1);
  });

  it("leaves an unbadged glyph untouched", () => {
    const svg = iconEl(ICON_TAB_RUN);
    expect(svg.querySelector("mask")).toBeNull();
    expect(svg.outerHTML).toBe(iconEl(ICON_TAB_RUN).outerHTML);
  });
});
