import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";

import indexHtml from "../static/index.html?raw";
import type * as Router from "./router.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
// The page's module never arrives, as on a first visit offline.
vi.mock("./history.js", () => {
  throw new Error("Failed to fetch dynamically imported module");
});

import { showHistoryView, _resetForTest } from "./tabs.js";
import { maybeEl } from "./dom.js";

let shell: HTMLElement;

beforeAll(() => {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const s of doc.querySelectorAll("script")) {
    s.remove();
  }
  shell = document.createElement("div");
  for (const n of [...doc.body.childNodes]) {
    shell.appendChild(document.importNode(n, true));
  }
  document.body.appendChild(shell);
});

afterAll(() => {
  _resetForTest();
  shell.remove();
});

const PANES = ["history-table", "history-runs"] as const;

function noteOf(id: string): HTMLElement {
  const pane = maybeEl(id);
  const notes = pane === null ? [] : [...pane.children];
  expect(notes, `#${id} holds one note`).toHaveLength(1);
  return notes[0] as HTMLElement;
}

async function failed(): Promise<void> {
  await vi.waitFor(() => {
    expect(maybeEl("history-table")?.querySelector(".history-error")).not.toBeNull();
  });
}

describe("History when its page cannot load", () => {
  it("states the failure in both panes, never the empty list, and tries again from Retry", async () => {
    showHistoryView();
    await failed();

    for (const id of PANES) {
      const note = noteOf(id);
      expect(note.getAttribute("role")).toBe("listitem");
      expect(note.textContent).toContain(
        "Could not load previous sessions. Check your connection and try again.",
      );
      expect(note.textContent).not.toContain("No previous");
      expect(note.querySelector("button")?.textContent).toBe("Retry");
    }

    const before = PANES.map(noteOf);
    noteOf("history-table").querySelector("button")?.click();

    await vi.waitFor(() => {
      expect(noteOf("history-table")).not.toBe(before[0]);
    });
    expect(noteOf("history-runs"), "both panes repainted").not.toBe(before[1]);
    expect(noteOf("history-runs").textContent).toContain("Could not load previous sessions.");
  });
});
