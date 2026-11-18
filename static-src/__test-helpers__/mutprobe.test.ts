import { describe, it, expect, afterEach } from "vitest";

import { park, unpark } from "../dom.js";
import { installMutationProbe, type MutationProbe } from "./mutprobe.js";

let probe: MutationProbe | undefined;
const hosts: HTMLElement[] = [];

afterEach(() => {
  probe?.dispose();
  probe = undefined;
  for (const h of hosts.splice(0)) {
    unpark(h, () => undefined);
    h.remove();
  }
});

function host(attrs: Record<string, string> = {}): HTMLElement {
  const el = document.createElement("div");
  el.id = `mutprobe-${String(hosts.length)}`;
  for (const [k, v] of Object.entries(attrs)) {
    el.setAttribute(k, v);
  }
  const leaf = el.appendChild(document.createElement("span"));
  leaf.className = "on";
  leaf.dataset["state"] = "a";
  leaf.textContent = "text";
  document.body.appendChild(el);
  hosts.push(el);
  return el;
}

function leafOf(el: HTMLElement): HTMLSpanElement {
  const leaf = el.querySelector("span");
  if (leaf === null) {
    throw new Error("host has no leaf");
  }
  return leaf;
}

describe("mutation probe", () => {
  it("finds an attribute set to the value it held, and not one that changed", () => {
    const el = host();
    probe = installMutationProbe();
    const leaf = leafOf(el);
    leaf.dataset["state"] = "a";
    leaf.dataset["state"] = "b";
    leaf.classList.add("on");
    const { records, sameValue } = probe.take();
    expect(records).toBe(3);
    expect(sameValue).toEqual([
      { kind: "attribute", name: "data-state", where: `#${el.id}`, parked: false },
      { kind: "attribute", name: "class", where: `#${el.id}`, parked: false },
    ]);
  });

  it("counts a value changed and changed back in one batch as two changes", () => {
    const leaf = leafOf(host());
    probe = installMutationProbe();
    leaf.dataset["state"] = "b";
    leaf.dataset["state"] = "a";
    expect(probe.take()).toEqual({ records: 2, sameValue: [] });
  });

  it("finds text replaced by the same text, through textContent and through the text node", () => {
    const el = host();
    const leaf = leafOf(el);
    probe = installMutationProbe();
    leaf.textContent = "text";
    leaf.textContent = "other";
    const node = leaf.firstChild;
    if (node === null) {
      throw new Error("leaf has no text node");
    }
    node.nodeValue = "other";
    expect(probe.take().sameValue).toEqual([
      { kind: "text", name: "#text", where: `#${el.id}`, parked: false },
      { kind: "text", name: "#text", where: `#${el.id}`, parked: false },
    ]);
  });

  it("does not count a child element replaced by an equal one as a text write", () => {
    const el = host();
    probe = installMutationProbe();
    el.replaceChildren(leafOf(el).cloneNode(true));
    expect(probe.take()).toEqual({ records: 1, sameValue: [] });
  });

  it("counts a write in a surface parked before or after it starts, once", () => {
    const before = host({ "data-tab-view": "" });
    park(before);
    probe = installMutationProbe();
    const after = host({ "data-tab-view": "" });
    probe.reset();
    park(after);
    probe.reset();
    leafOf(before).dataset["state"] = "a";
    leafOf(after).dataset["state"] = "a";
    expect(probe.take()).toEqual({
      records: 2,
      sameValue: [
        { kind: "attribute", name: "data-state", where: `#${before.id}`, parked: true },
        { kind: "attribute", name: "data-state", where: `#${after.id}`, parked: true },
      ],
    });
  });

  it("discards the records before a reset, and stops at dispose", () => {
    const leaf = leafOf(host());
    probe = installMutationProbe();
    leaf.dataset["state"] = "a";
    probe.reset();
    expect(probe.take()).toEqual({ records: 0, sameValue: [] });
    probe.dispose();
    leaf.dataset["state"] = "a";
    expect(probe.take()).toEqual({ records: 0, sameValue: [] });
  });
});
