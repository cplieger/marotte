// kiro-cli sends CUMULATIVE tool output on every tool_call_update, so the card's <pre> is replaced, not appended.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ToolCall } from "./types.js";
import type * as ToolSchema from "./tool-schema.js";

// Heavy DOM deps mocked; store-signals, output-render and reactive stay real (the paint goes through them).
vi.mock("./tool-group.js", () => ({
  maybeCollapseGroup: vi.fn(),
}));
// Spread from the original: the module is pure and the title cases exercise its real display-title functions.
vi.mock("./tool-schema.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ToolSchema>()),
  isToolDone: vi.fn(() => false),
}));
vi.mock("./tool-card.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds; the node runner gave `undefined` too.
  applyOutcome: undefined,
  expandToolDetails: undefined,
  // Inert: `applyOutputUpdate` calls it; bare-ness is pinned against the real writer elsewhere.
  refreshToolDisclosure: vi.fn(),
  buildToolCard: vi.fn(() => document.createElement("div")),
  insertDiffPreview: vi.fn(),
  // Inert: these cards have no `.tool-header`, so the real writer would be a no-op anyway.
  syncSilenceMarker: vi.fn(),
  syncOffloadLink: vi.fn(),
  syncInteractionFact: vi.fn(),
}));

import { applyOutputUpdate, updateToolCall } from "./messages-tools.js";
import { syncInteractionFact, syncOffloadLink } from "./tool-card.js";

/** A card whose depth 1 is a windowed output (execute / shell / command). */
function commandCard(): HTMLDivElement {
  const card = document.createElement("div");
  card.className = "tool-call";
  card.dataset["depth1"] = "output";
  const out = document.createElement("div");
  out.className = "tool-output";
  card.appendChild(out);
  return card;
}

/** A card with a plain (unwindowed) output region. */
function simpleCard(): HTMLDivElement {
  const card = document.createElement("div");
  card.className = "tool-call";
  const out = document.createElement("div");
  out.className = "tool-output";
  card.appendChild(out);
  return card;
}

describe("applyOutputUpdate (cumulative output → replace, not append)", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("replaces the <pre> rather than appending each cumulative snapshot", () => {
    const card = commandCard();
    applyOutputUpdate(card, "line one\n");
    applyOutputUpdate(card, "line one\nline two\n");
    const pre = card.querySelector(".tool-output pre");
    expect(pre).not.toBeNull();
    // Not appended ("line one\nline one\nline two\n") — replaced.
    expect(pre?.textContent).toBe("line one\nline two\n");
    expect(card.querySelectorAll(".tool-output pre").length).toBe(1);
  });

  it("keeps a command's streaming output WINDOWED, and offers the rest", () => {
    // Streaming the middle of a 5,000-line build into the card would undo the window on the first update.
    const card = commandCard();
    const lines = Array.from({ length: 200 }, (_, i) => `line${String(i)}`);
    applyOutputUpdate(card, lines.join("\n"));
    const pre = card.querySelector(".tool-output pre");
    expect(pre?.textContent).not.toContain("line100");
    const reveal = card.querySelector(".tool-output-reveal");
    expect(reveal?.textContent).toContain("160 more lines");

    (reveal as HTMLElement).click();
    expect(card.querySelector(".tool-output pre")?.textContent).toContain("line100");
    expect(card.querySelector(".tool-output-reveal")).toBeNull();
  });

  it("does not window a card whose depth 1 is not an output", () => {
    const card = simpleCard();
    applyOutputUpdate(card, "A");
    applyOutputUpdate(card, "AB");
    applyOutputUpdate(card, "ABC");
    const pre = card.querySelector(".tool-output pre");
    expect(pre?.textContent).toBe("ABC");
    expect(card.querySelectorAll(".tool-output pre").length).toBe(1);
  });
});

describe("updateToolCall title", () => {
  it("keeps a late title frame human-readable", () => {
    const card = document.createElement("div");
    card.className = "tool-call";
    card.dataset["title"] = "Running";
    const header = document.createElement("div");
    header.className = "tool-header";
    const title = document.createElement("span");
    title.className = "tool-title";
    header.appendChild(title);
    card.appendChild(header);

    updateToolCall(
      card,
      {
        id: "late-title",
        title: "Running: remote_web_search",
      } as ToolCall,
      "c1",
    );

    expect(title.textContent).toBe("remote web search");
    expect(header.title).toBe("remote web search");
    expect(card.dataset["title"]).toBe("remote web search");
  });

  function titledCard(text: string): {
    card: HTMLDivElement;
    header: HTMLDivElement;
    title: HTMLSpanElement;
  } {
    const card = document.createElement("div");
    card.className = "tool-call";
    card.dataset["title"] = text;
    const header = document.createElement("div");
    header.className = "tool-header";
    const title = document.createElement("span");
    title.className = "tool-title";
    title.textContent = text;
    header.appendChild(title);
    card.appendChild(header);
    return { card, header, title };
  }

  // KAS repeats its placeholder on routine frames; the update must not put "Run Command" back over the command title.
  it("keeps a shell card's command title through a placeholder update frame", () => {
    const { card, header, title } = titledCard("make lint");
    updateToolCall(
      card,
      {
        id: "shell-update",
        title: "Run Command",
        kind: "execute",
        input: { command: "make lint" },
      } as ToolCall,
      "c1",
    );
    expect(title.textContent).toBe("make lint");
    expect(header.title).toBe("make lint");
    expect(card.dataset["title"]).toBe("make lint");
  });

  it("keeps a described title verbatim on update", () => {
    const { card, header, title } = titledCard("Running");
    updateToolCall(
      card,
      {
        id: "prose-update",
        title: "Run tests in ./internal/foo_bar with --dry-run",
        kind: "execute",
        input: { command: "go test --dry-run ./internal/foo_bar" },
      } as ToolCall,
      "c1",
    );
    expect(title.textContent).toBe("Run tests in ./internal/foo_bar with --dry-run");
    expect(header.title).toBe("Run tests in ./internal/foo_bar with --dry-run");
    expect(card.dataset["title"]).toBe("Run tests in ./internal/foo_bar with --dry-run");
  });
});

describe("an offloaded output reaching a card already on screen", () => {
  it("hands the update frame's offload to the card's link writer", () => {
    const card = simpleCard();
    const offload = {
      path: "/config/.kiro/sessions/cli/sess_1/tool-outputs/shell-0a1b2c3d.txt",
      total_chars: 90000,
    };
    updateToolCall(card, { id: "off-update", offload } as ToolCall, "c1");
    expect(vi.mocked(syncOffloadLink)).toHaveBeenCalledWith(card, offload);
  });
});

describe("an answered ask reaching a card already on screen", () => {
  it("hands the update frame's interaction to the card's fact writer", () => {
    const card = simpleCard();
    const interaction = { type: "tool_approval", outcome: "selected", choice: "allow_once" };
    updateToolCall(card, { id: "ask-update", interaction } as ToolCall, "c1");
    expect(vi.mocked(syncInteractionFact)).toHaveBeenCalledWith(card, interaction);
  });
});
