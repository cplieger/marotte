import { describe, it, expect } from "vitest";
import { renderMarkdownInto } from "./markdown.js";

// Its own file because the run callback is module state that no test can unset.
describe("Run before the shell panel wires its handler", () => {
  for (const text of ["echo hi", "cd /tmp\nls -la"]) {
    it(`disables Run on ${JSON.stringify(text)}`, () => {
      const root = document.createElement("div");
      renderMarkdownInto(root, `\`\`\`bash\n${text}\n\`\`\`\n`);
      const btn = root.querySelector<HTMLButtonElement>(
        '[data-part="actions"] > button:not(.cp-copy)',
      );
      expect(btn?.disabled).toBe(true);
      expect(btn?.getAttribute("data-tooltip")).toBe("Shell not available");
      expect(btn?.getAttribute("aria-label")).toBe("Shell not available");
    });
  }
});
