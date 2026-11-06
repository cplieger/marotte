import { describe, it, expect } from "vitest";
import { decorateCodeBlocks } from "./code-blocks.js";

// Its own file because the run callback is module state that no test can unset.
describe("Run before the shell panel wires its handler", () => {
  for (const text of ["echo hi", "cd /tmp\nls -la"]) {
    it(`disables Run on ${JSON.stringify(text)}`, () => {
      const root = document.createElement("div");
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      code.className = "language-bash";
      code.textContent = text;
      pre.appendChild(code);
      root.appendChild(pre);
      decorateCodeBlocks(root);
      const btn = [...root.querySelectorAll<HTMLButtonElement>(".code-act-btn")][1];
      expect(btn?.disabled).toBe(true);
      expect(btn?.getAttribute("data-tooltip")).toBe("Shell not available");
      expect(btn?.getAttribute("aria-label")).toBe("Shell not available");
    });
  }
});
