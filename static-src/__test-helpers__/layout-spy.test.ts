import { describe, it, expect, afterEach } from "vitest";

import { installLayoutSpy, type LayoutSpy } from "./layout-spy.js";

let spy: LayoutSpy | undefined;
const hosts: HTMLElement[] = [];

afterEach(() => {
  spy?.dispose();
  spy = undefined;
  for (const h of hosts.splice(0)) {
    h.remove();
  }
});

function box(): HTMLElement {
  const el = document.createElement("div");
  el.style.cssText = "height:100px;overflow:auto";
  el.appendChild(document.createElement("div")).style.height = "300px";
  document.body.appendChild(el);
  hosts.push(el);
  return el;
}

describe("layout spy", () => {
  it("counts a read after a DOM write as forced, and a second read as clean", () => {
    const el = box();
    spy = installLayoutSpy();
    spy.begin();
    el.style.height = "120px";
    void el.scrollHeight;
    void el.getBoundingClientRect();
    const { forced, log } = spy.end();
    expect(forced).toBe(1);
    expect(log.map((a) => [a.what, a.forced])).toEqual([
      ["scrollHeight", true],
      ["getBoundingClientRect", false],
    ]);
  });

  it("discards the writes made before begin", () => {
    const el = box();
    spy = installLayoutSpy();
    el.style.height = "140px";
    spy.begin();
    void el.clientHeight;
    expect(spy.end().forced).toBe(0);
  });

  it("logs a scrollTop write as a forcing write, in order", () => {
    const el = box();
    spy = installLayoutSpy();
    spy.begin();
    el.firstElementChild?.setAttribute("data-x", "1");
    el.scrollTop = 50;
    void el.scrollTop;
    const { log } = spy.end();
    expect(log.map((a) => [a.kind, a.what, a.forced])).toEqual([
      ["write", "scrollTop=", true],
      ["read", "scrollTop", false],
    ]);
  });

  it("restores the platform readers on dispose", () => {
    const before = Element.prototype.getBoundingClientRect;
    spy = installLayoutSpy();
    expect(Element.prototype.getBoundingClientRect).not.toBe(before);
    spy.dispose();
    spy = undefined;
    expect(Element.prototype.getBoundingClientRect).toBe(before);
  });
});
