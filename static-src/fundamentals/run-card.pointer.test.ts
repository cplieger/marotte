// A run card's step name under the real mouse, which only this page may move (vitest.config.ts `browser-pointer`).
import { describe, it, expect } from "vitest";
import { userEvent } from "vitest/browser";

import { buildRunCard } from "./run-card.js";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { nodePathKey } from "../run-node-key.js";

// Real gestures, as a reader selects: neither Chromium nor WebKit selects a word inside a link. The app's CSS lays the
// row out, which decides where a word ends. A double-click's first click is a plain click, so only a drag selects
// without opening.
describe("a run card's step name under the real mouse", () => {
  it("selects a step's name by double-click or drag, and a drag opens no step", async () => {
    const style = mountAppCSS();
    const opened: string[] = [];
    const c = buildRunCard("wf_1", "Workflow run", (_id, _label, node) => {
      opened.push(node ?? "");
    });
    c.render({
      workflowId: "wf_1",
      status: "running",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "compile", type: "step", status: "running" }],
      },
    });
    document.body.append(c.root);
    const name = c.root.querySelector<HTMLElement>(".run-step-name")!;
    try {
      await userEvent.dblClick(name);
      expect(document.getSelection()?.toString()).toBe("compile");
      document.getSelection()?.removeAllRanges();
      opened.length = 0;
      const { width, height } = name.getBoundingClientRect();
      await userEvent.dragAndDrop(name, name, {
        sourcePosition: { x: 1, y: height / 2 },
        targetPosition: { x: width - 1, y: height / 2 },
      });
      expect(document.getSelection()?.toString()).toMatch(/^c?ompil/);
      expect(opened).toEqual([]);
      document.getSelection()?.removeAllRanges();
      c.root.querySelector<HTMLElement>(".run-step-head")?.click();
      expect(opened).toEqual([nodePathKey(["wf_1", "compile"])]);
    } finally {
      document.getSelection()?.removeAllRanges();
      c.root.remove();
      style.remove();
    }
  });
});
