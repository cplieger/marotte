// The overlay's failure arm: the inline drawing already succeeded, so pele failing here is thrown, and the view it
// opened is torn down rather than left as an empty modal.

import { describe, it, expect, vi, afterEach } from "vitest";
import type { Mounted } from "pele/lazy";
import { openMermaidOverlay } from "./mermaid-overlay.js";
import type { Pele } from "./mermaid-svg.js";

afterEach(() => {
  document.body.replaceChildren();
});

describe("openMermaidOverlay when pele cannot draw", () => {
  it("throws pele's error, destroys the mount once and leaves no dialog open", () => {
    const error = new Error("x");
    const destroy = vi.fn();
    const failed: Mounted = {
      result: undefined,
      source: "graph TD",
      error,
      zoom: undefined,
      update: vi.fn(),
      refresh: vi.fn(),
      destroy,
    };
    const pele = { mount: vi.fn(() => failed) } as unknown as Pele;
    const returnFocus = document.createElement("button");
    document.body.append(returnFocus);

    expect(() => {
      openMermaidOverlay(pele, "graph TD", { title: "Diagram", returnFocus });
    }).toThrow(error);
    expect(destroy).toHaveBeenCalledOnce();
    expect(document.querySelector("dialog[open]")).toBeNull();
  });
});
