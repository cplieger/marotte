// A refusal auto-opens its tool card's details, and the toggle's aria-expanded follows. The three mocks are hoisted:
// Browser Mode mocks through an import map, so a module another test already imported cannot be mocked late.
import { describe, it, expect, vi } from "vitest";
import type * as Actions from "./actions/index.js";
import type * as EditorOpeners from "./editor-openers.js";
import type * as Scroll from "./scroll.js";

vi.mock("./scroll.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Scroll>()),
  setUserScrolledUp: (): void => {
    /* noop */
  },
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  openFile: (): void => {
    /* noop */
  },
  openFileDiff: (): void => {
    /* noop */
  },
  openFileGitDiff: undefined,
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Actions>()),
  // A transcript card registers its cleanup on this path; the test owns no registry.
  registerCleanup: (): void => {
    /* noop */
  },
}));

// The real scroll module resolves the app's root elements when it loads.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}

describe("a11y: declined tool aria-expanded", () => {
  it("a refusal's auto-open sets aria-expanded=true on the tool toggle", async () => {
    const noop = (): void => {
      /* noop */
    };
    const { buildToolCard } = await import("./tool-card.js");
    const { initToolCallbacks, applyToolCallUpdate } = await import("./messages-tools.js");

    initToolCallbacks({ refreshGroupHeader: noop });

    const card = buildToolCard({
      id: "tf1",
      title: "Running: grep",
      kind: "other",
      status: "in_progress",
      input: { pattern: "boom" },
      live: true,
    });
    document.body.appendChild(card);

    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    const details = card.querySelector<HTMLElement>(".tool-details")!;
    // Precondition: a live (in_progress) medium-tier card starts collapsed —
    // the disclosure controller marks the region aria-hidden + inert.
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(details.getAttribute("aria-hidden")).toBe("true");

    applyToolCallUpdate(
      card,
      {
        id: "tf1",
        title: "Running: grep",
        kind: "other",
        status: "completed",
        declined: true,
        ts: 0,
      },
      "c-a11y",
    );

    // The refusal branch auto-expands the details, which must keep aria-expanded and the
    // region's aria-hidden in sync.
    expect(details.getAttribute("aria-hidden")).toBe("false");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");

    document.body.removeChild(card);
  });
});
