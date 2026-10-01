// ---------------------------------------------------------------------------
// Tests for fundamentals/todo.ts: the checklist paints its status through the
// shared work-status table, so a row carries a class hook and no character, and
// a status change on reconcile repaints the same row in place.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { buildTodoList, updateTodoList } from "./todo.js";
import { STATUS, STATUS_CLASS } from "./work-status.js";

function glyphs(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(".todo-glyph"));
}

describe("buildTodoList", () => {
  it("paints each row's glyph from the shared status table with no character", () => {
    const root = buildTodoList([
      { content: "read", status: "completed" },
      { content: "write", status: "in_progress" },
      { content: "test", status: "pending" },
    ]);
    const g = glyphs(root);
    expect(g).toHaveLength(3);
    expect(g[0]?.classList.contains(STATUS.completed.className)).toBe(true);
    expect(g[1]?.classList.contains(STATUS.in_progress.className)).toBe(true);
    expect(g[2]?.classList.contains(STATUS.pending.className)).toBe(true);
    for (const el of g) {
      expect(el.classList.contains(STATUS_CLASS)).toBe(true);
      expect(el.textContent).toBe("");
    }
    expect(root.querySelector(".todo-progress")?.textContent).toBe("1/3");
  });

  it("repaints a row in place when its status moves", () => {
    const root = buildTodoList([{ content: "read", status: "pending" }]);
    const before = glyphs(root)[0];
    updateTodoList(root, [{ content: "read", status: "completed" }]);
    const after = glyphs(root)[0];
    expect(after).toBe(before);
    expect(after?.classList.contains(STATUS.completed.className)).toBe(true);
    expect(after?.classList.contains(STATUS.pending.className)).toBe(false);
    expect(root.querySelector<HTMLElement>(".todo-row")?.dataset["status"]).toBe("completed");
  });
});
