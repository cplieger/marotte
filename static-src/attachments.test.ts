// The composer's staged attachment row, bound once behind a latch, so a pill wired to the
// wrong chat or path is otherwise invisible until clicked.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { row, mockDispatch, mockFlush, mockPending } = vi.hoisted(() => ({
  row: document.createElement("ul"),
  mockDispatch: vi.fn(),
  mockFlush: vi.fn(),
  mockPending: vi.fn(() => false),
}));
vi.mock("./dom.js", () => ({ $: { attachmentRow: row } }));
// What reaches the persist action, and when a flush is forced. The 600ms itself
// is the framework's and is not re-tested here.
vi.mock("./actions/index.js", () => ({
  debouncedDispatch: () =>
    Object.assign(mockDispatch, { isPending: mockPending, flush: mockFlush, cancel: vi.fn() }),
}));
vi.mock("./actions/chat.js", () => ({ setAttachments: { name: "chat.set_attachments" } }));

import {
  addAttachment,
  addAttachmentTo,
  takeAttachments,
  stashAttachments,
  restoreAttachments,
  dropAttachments,
  seedAttachments,
  adoptRemoteAttachments,
  flushAttachments,
  _resetAttachmentsForTest,
} from "./attachments.js";
import { initAttachmentPillCallbacks } from "./attachment-pill.js";

const opened: string[] = [];

/** The path lists handed to the persist action, in order. */
function saved(): string[][] {
  return mockDispatch.mock.calls.map((c) => (c[0] as { paths: string[] }).paths);
}

beforeEach(() => {
  _resetAttachmentsForTest();
  mockDispatch.mockClear();
  mockFlush.mockClear();
  mockPending.mockReturnValue(false);
  opened.length = 0;
  initAttachmentPillCallbacks({
    open: (p) => {
      opened.push(p);
    },
  });
});

function pills(): HTMLElement[] {
  return [...row.querySelectorAll<HTMLElement>(".attachment-pill")];
}

function bodies(): HTMLButtonElement[] {
  return [...row.querySelectorAll<HTMLButtonElement>(".attachment-open")];
}

describe("the staged attachment row", () => {
  it("renders one pill per attached file, in the order they were added", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["src/a.ts", "docs/b.md"]);
    expect(pills().map((p) => p.querySelector(".attachment-name")?.textContent)).toEqual([
      "a.ts",
      "b.md",
    ]);
  });

  it("hides the row when nothing is attached and shows it once something is", () => {
    expect(row.classList.contains("hidden")).toBe(true);
    addAttachment("src/a.ts");
    expect(row.classList.contains("hidden")).toBe(false);
    takeAttachments();
    expect(row.classList.contains("hidden")).toBe(true);
  });

  // The tab id is `editor:<path>`, so each pill must ask for its own path. (tabs.test.ts
  // pins the id rule.)
  it("opens each attachment under its own path, so N attachments reach N tabs", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    addAttachment("out/shot.png");
    const targets = bodies();
    expect(targets).toHaveLength(3);
    for (const b of targets) {
      b.click();
    }
    expect(opened).toEqual(["src/a.ts", "docs/b.md", "out/shot.png"]);
    expect(new Set(opened).size).toBe(3);
  });

  // Two pills for one path would open one tab and re-activate it, which is the
  // right behaviour — but the row should not offer the same file twice either.
  it("deduplicates by path", () => {
    addAttachment("src/a.ts");
    addAttachment("src/a.ts");
    expect(pills()).toHaveLength(1);
  });

  it("removes the file when its × is clicked, and opens nothing", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    row.querySelector<HTMLButtonElement>(".attachment-pill .attachment-close")?.click();
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["docs/b.md"]);
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/b.md"]);
    expect(opened).toEqual([]);
  });

  // The staged pill is removable; the sent one is not. Both come from the same
  // builder, so the composer's half of that split is worth pinning here.
  it("gives every staged pill a remove control", () => {
    addAttachment("src/a.ts");
    expect(pills()[0]?.querySelector(".attachment-close")).not.toBeNull();
  });
});

// The row persists on the chat record through set_attachments on the draft's 600ms
// debounce.
describe("persisting the staged row", () => {
  it("saves the whole list under the live chat on every change", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    expect(saved()).toEqual([["src/a.ts"], ["src/a.ts", "docs/b.md"]]);
    expect(mockDispatch.mock.calls.map((c) => (c[0] as { chatID: string }).chatID)).toEqual([
      "c1",
      "c1",
    ]);
  });

  it("saves the shorter list when a pill is removed", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    mockDispatch.mockClear();
    row.querySelector<HTMLButtonElement>(".attachment-close")?.click();
    expect(saved()).toEqual([["docs/b.md"]]);
  });

  // A dedupe is not a change, so it must not spend a POST.
  it("saves nothing when the same path is added twice", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    mockDispatch.mockClear();
    addAttachment("src/a.ts");
    expect(saved()).toEqual([]);
  });

  // No chat is live between a stash and its restore, as for the draft.
  it("saves nothing while no chat is live", () => {
    addAttachment("src/a.ts");
    expect(saved()).toEqual([]);
  });

  it("saves a parked chat's list without touching the live row", () => {
    restoreAttachments("c1");
    addAttachmentTo("c2", "src/b.ts");
    expect(saved()).toEqual([["src/b.ts"]]);
    expect(mockDispatch.mock.calls[0]?.[0]).toMatchObject({ chatID: "c2" });
    expect(pills()).toHaveLength(0);
  });

  // The send's clear goes out IMMEDIATELY: a STEER takes the row too and clears nothing
  // server-side, and a debounced clear would race the send's response.
  it("flushes an empty list the moment a send takes the row", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    mockDispatch.mockClear();
    mockFlush.mockClear();

    takeAttachments();

    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", paths: [] });
  });

  it("flushes nothing when the row was already empty", () => {
    restoreAttachments("c1");
    takeAttachments();
    expect(mockFlush).not.toHaveBeenCalled();
  });

  // After the stash the id is unrecoverable, so a pending save goes out first.
  it("gets a pending save out under the OUTGOING chat on a switch", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    mockPending.mockReturnValue(true);

    stashAttachments();

    expect(mockFlush).toHaveBeenCalledWith({ chatID: "c1", paths: ["src/a.ts"] });
  });

  it("flushes nothing when no save is pending", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    mockFlush.mockClear();
    flushAttachments();
    expect(mockFlush).not.toHaveBeenCalled();
  });
});

// The server's list only ever SEEDS the local one, which is what makes a reload
// restore the row while a list the user is building still wins.
describe("seeding from the chat record", () => {
  it("puts the server's list on an empty live row", () => {
    restoreAttachments("c1");
    seedAttachments("c1", ["docs/spec.pdf", "out/shot.png"]);
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["docs/spec.pdf", "out/shot.png"]);
  });

  it("loses to a row the user has already staged into", () => {
    restoreAttachments("c1");
    addAttachment("mine.ts");
    seedAttachments("c1", ["theirs.pdf"]);
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["mine.ts"]);
  });

  // A fetch can land twice — the boot activation and a later re-activation — and
  // the second must not undo a row the reader has emptied in between.
  it("adopts once per chat and then stays out of the way", () => {
    restoreAttachments("c1");
    seedAttachments("c1", ["docs/spec.pdf"]);
    takeAttachments();
    seedAttachments("c1", ["docs/spec.pdf"]);
    expect(pills()).toHaveLength(0);
  });

  it("parks a non-live chat's list in the stash", () => {
    restoreAttachments("c1");
    seedAttachments("c2", ["docs/spec.pdf"]);
    expect(pills()).toHaveLength(0);

    stashAttachments();
    restoreAttachments("c2");
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["docs/spec.pdf"]);
  });

  // Seeding is an adoption, not a write: the value came FROM the server.
  it("persists nothing", () => {
    restoreAttachments("c1");
    seedAttachments("c1", ["docs/spec.pdf"]);
    expect(saved()).toEqual([]);
    expect(mockFlush).not.toHaveBeenCalled();
  });
});

// A draft_changed frame converges a chat NOT on screen; for the visible row it would
// delete a pill mid-gesture.
describe("adopting a remote change", () => {
  it("updates a parked chat's list", () => {
    restoreAttachments("c1");
    adoptRemoteAttachments("c2", ["from-the-desktop.pdf"]);

    stashAttachments();
    restoreAttachments("c2");
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["from-the-desktop.pdf"]);
  });

  it("ignores a frame for the live chat", () => {
    restoreAttachments("c1");
    addAttachment("mine.ts");
    adoptRemoteAttachments("c1", ["theirs.pdf"]);
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["mine.ts"]);
  });

  // Unlike the seed it beats a local copy: the frame is a write the server accepted.
  it("replaces a parked list rather than deferring to it", () => {
    restoreAttachments("c1");
    addAttachmentTo("c2", "stale.ts");
    adoptRemoteAttachments("c2", ["fresh.pdf"]);

    stashAttachments();
    restoreAttachments("c2");
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["fresh.pdf"]);
  });

  it("clears a parked list on an empty frame", () => {
    restoreAttachments("c1");
    addAttachmentTo("c2", "stale.ts");
    adoptRemoteAttachments("c2", []);

    stashAttachments();
    restoreAttachments("c2");
    expect(pills()).toHaveLength(0);
  });

  it("persists nothing", () => {
    restoreAttachments("c1");
    mockDispatch.mockClear();
    adoptRemoteAttachments("c2", ["fresh.pdf"]);
    expect(saved()).toEqual([]);
  });
});

// A close keeps the record, so persisting an empty list would delete what reopening seeds.
describe("dropping a chat", () => {
  it("persists nothing", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    mockDispatch.mockClear();
    mockFlush.mockClear();
    dropAttachments("c1");
    expect(saved()).toEqual([]);
    expect(mockFlush).not.toHaveBeenCalled();
  });

  // The seed latch goes with it, so reopening the chat adopts the server's list
  // again rather than coming back empty.
  it("lets the server's list seed again on the next open", () => {
    restoreAttachments("c1");
    addAttachment("src/a.ts");
    dropAttachments("c1");

    restoreAttachments("c1");
    seedAttachments("c1", ["docs/spec.pdf"]);
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["docs/spec.pdf"]);
  });
});
