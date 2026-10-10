import { describe, it, expect, afterEach, vi } from "vitest";
import { applyEdit } from "./text-edit.js";

let listeners = new AbortController();

afterEach(() => {
  listeners.abort();
  listeners = new AbortController();
  document.body.replaceChildren();
});

/** A focused box, so the native path is the one `applyEdit` would try first. */
function focusedBox(value: string): HTMLTextAreaElement {
  const el = document.createElement("textarea");
  document.body.append(el);
  el.value = value;
  el.focus();
  return el;
}

function declineExecCommand(): void {
  vi.spyOn(document, "execCommand").mockReturnValue(false);
}

describe("applyEdit when execCommand declines", () => {
  it("replaces the range and announces one input event", () => {
    declineExecCommand();
    const el = focusedBox("- one");
    const inputs: Event[] = [];
    // On the document, so the event is proven to bubble.
    document.addEventListener("input", (ev) => inputs.push(ev), { signal: listeners.signal });
    applyEdit(el, { start: 5, end: 5, text: "\n- ", caret: 8 });
    expect(el.value).toBe("- one\n- ");
    expect(inputs).toHaveLength(1);
  });

  it("puts the caret at the edit's caret, not the end of the replacement", () => {
    declineExecCommand();
    const el = focusedBox("draft");
    applyEdit(el, { start: 0, end: 5, text: "newest", caret: 3 });
    expect(el.value).toBe("newest");
    expect([el.selectionStart, el.selectionEnd]).toEqual([3, 3]);
  });

  it("stays unannounced when silent", () => {
    declineExecCommand();
    const el = focusedBox("draft");
    const inputs: Event[] = [];
    el.addEventListener("input", (ev) => inputs.push(ev), { signal: listeners.signal });
    applyEdit(el, { start: 0, end: 5, text: "newest", caret: 6 }, { silent: true });
    expect(el.value).toBe("newest");
    expect(inputs).toEqual([]);
  });
});
