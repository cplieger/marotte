// The inline name field the file browser and the editor share: it opens on the original with the
// stem selected, commits a changed trimmed name once, and restores on anything else.
import { describe, it, expect, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";

import { startInlineRename } from "./inline-rename.js";

let host: HTMLElement | null = null;

afterEach(() => {
  host?.remove();
  host = null;
});

function open(original: string) {
  host = document.createElement("div");
  const target = document.createElement("span");
  target.textContent = original;
  host.append(target);
  document.body.append(host);
  const commit = vi.fn((_name: string, field: HTMLInputElement) => {
    field.replaceWith(document.createTextNode("pending"));
  });
  const restore = vi.fn((field: HTMLInputElement) => {
    field.replaceWith(target);
  });
  const field = startInlineRename({ target, original, className: "name-edit", commit, restore });
  return { target, field, commit, restore };
}

describe("startInlineRename", () => {
  it.each([
    ["a.txt", 0, 1],
    ["archive.tar.gz", 0, 11],
    [".env", 0, 4],
    ["Makefile", 0, 8],
  ])("opens on %s with the stem selected", (name, start, end) => {
    const { field, target } = open(name);
    expect(field.value).toBe(name);
    expect(field.isConnected).toBe(true);
    expect(target.isConnected).toBe(false);
    expect(document.activeElement).toBe(field);
    expect([field.selectionStart, field.selectionEnd]).toEqual([start, end]);
  });

  it("is labelled with the name it renames", () => {
    const { field } = open("a.txt");
    expect(field.getAttribute("aria-label")).toBe("New name for a.txt");
    expect(field.className).toBe("name-edit");
  });

  it("commits a changed, trimmed name once on Enter", async () => {
    const { field, commit, restore } = open("a.txt");
    await userEvent.clear(field);
    await userEvent.type(field, "  b.txt  ");
    await userEvent.keyboard("{Enter}");
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
    field.dispatchEvent(new FocusEvent("blur"));
    expect(commit).toHaveBeenCalledTimes(1);
    expect(commit.mock.calls[0]?.[0]).toBe("b.txt");
    expect(restore).not.toHaveBeenCalled();
  });

  it("commits on blur", async () => {
    const { field, commit } = open("a.txt");
    await userEvent.clear(field);
    await userEvent.type(field, "c.txt");
    field.blur();
    expect(commit).toHaveBeenCalledTimes(1);
    expect(commit.mock.calls[0]?.[0]).toBe("c.txt");
  });

  it("restores on Escape and commits nothing", async () => {
    const { field, target, commit, restore } = open("a.txt");
    await userEvent.clear(field);
    await userEvent.type(field, "c.txt");
    await userEvent.keyboard("{Escape}");
    expect(restore).toHaveBeenCalledTimes(1);
    expect(commit).not.toHaveBeenCalled();
    expect(target.isConnected).toBe(true);
  });

  it.each([
    ["an empty", ""],
    ["a blank", "   "],
    ["an unchanged", "a.txt"],
  ])("restores on %s name and commits nothing", async (_label, typed) => {
    const { field, commit, restore } = open("a.txt");
    await userEvent.clear(field);
    if (typed !== "") {
      await userEvent.type(field, typed);
    }
    await userEvent.keyboard("{Enter}");
    expect(commit).not.toHaveBeenCalled();
    expect(restore).toHaveBeenCalledTimes(1);
  });
});
