// The drop zone's overlay follows the drag: it shows on the first `dragenter`,
// survives the enter/leave pair a browser fires when the pointer crosses into a
// nested element, hides on the final `dragleave` or on a drop, and a drop hands
// over its files only when it carries some. Real DragEvents with a real
// DataTransfer, so the event shape is the browser's own.
import { describe, it, expect, beforeEach, vi } from "vitest";
import { installDropZone } from "./drop-zone.js";

let container: HTMLDivElement;
let child: HTMLDivElement;
let overlay: HTMLDivElement;
let onDrop: ReturnType<typeof vi.fn<(files: FileList) => void>>;
let onDragLeave: ReturnType<typeof vi.fn<() => void>>;

function drag(type: string, target: HTMLElement, files: readonly File[] = []): void {
  const dataTransfer = new DataTransfer();
  for (const f of files) {
    dataTransfer.items.add(f);
  }
  target.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer }));
}

function shown(): boolean {
  return !overlay.classList.contains("hidden");
}

beforeEach(() => {
  container = document.createElement("div");
  child = document.createElement("div");
  overlay = document.createElement("div");
  overlay.className = "hidden";
  container.append(child, overlay);
  document.body.replaceChildren(container);
  onDrop = vi.fn<(files: FileList) => void>();
  onDragLeave = vi.fn<() => void>();
  installDropZone({ container, overlay, onDrop, onDragLeave });
});

describe("installDropZone", () => {
  it("shows the overlay on the first dragenter", () => {
    expect.assertions(1);
    drag("dragenter", container);
    expect(shown()).toBe(true);
  });

  it("keeps the overlay while the pointer crosses into a nested element", () => {
    expect.assertions(2);
    drag("dragenter", container);
    drag("dragenter", child);
    drag("dragleave", container);
    expect(shown()).toBe(true);
    expect(onDragLeave).not.toHaveBeenCalled();
  });

  it("hides the overlay and reports the leave once the drag fully exits", () => {
    expect.assertions(2);
    drag("dragenter", container);
    drag("dragenter", child);
    drag("dragleave", container);
    drag("dragleave", child);
    expect(shown()).toBe(false);
    expect(onDragLeave).toHaveBeenCalledTimes(1);
  });

  it("hides the overlay on a drop and hands over the dropped files", () => {
    expect.assertions(3);
    const file = new File(["x"], "a.txt", { type: "text/plain" });
    drag("dragenter", container);
    drag("drop", child, [file]);
    expect(shown()).toBe(false);
    expect(onDrop).toHaveBeenCalledTimes(1);
    expect(onDrop.mock.calls[0]?.[0][0]?.name).toBe("a.txt");
  });

  it("does not call onDrop for a drop that carries no files", () => {
    expect.assertions(2);
    drag("dragenter", container);
    drag("drop", container);
    expect(shown()).toBe(false);
    expect(onDrop).not.toHaveBeenCalled();
  });
});
