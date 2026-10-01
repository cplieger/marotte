// Unit test for applyOutputUpdate: kiro-cli sends CUMULATIVE tool output on
// every tool_call_update, so the card's <pre> must be REPLACED, not appended.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ToolCall } from "./types.js";

// Mock messages-tools.ts's heavy DOM deps so the import resolves without pulling
// tool-card's builders or the group's collapse. store-signals, output-render and
// reactive stay real (applyOutputUpdate paints through renderOutput + el).
vi.mock("./tool-group.js", () => ({
  maybeCollapseGroup: vi.fn(),
}));
vi.mock("./tool-schema.js", () => ({
  isToolDone: vi.fn(() => false),
  // Present-but-undefined, same reason as the mocks above: another module in
  // this graph imports the name and no path under test calls it.
  isToolActive: undefined,
  isSubagentInvocation: undefined,
}));
vi.mock("./tool-card.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  applyOutcome: undefined,
  expandToolDetails: undefined,
  // Inert rather than undefined: `applyOutputUpdate` calls it. Nothing here
  // asserts on it — bare-ness is pinned against the real writer in
  // `tool-card.test.ts` and `messages-tools-status.test.ts`.
  refreshToolDisclosure: vi.fn(),
  buildToolCard: vi.fn(() => document.createElement("div")),
  insertDiffPreview: vi.fn(),
  // Inert: the mount effect calls it on every pass and this file's cards have no
  // `.tool-header`, so the real writer would be a no-op here anyway. Its own
  // behaviour is pinned in `tool-card-silence.test.ts`.
  syncSilenceMarker: vi.fn(),
}));

import { applyOutputUpdate, updateToolCall } from "./messages-tools.js";

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
    // Exactly one <pre>, not one per update.
    expect(card.querySelectorAll(".tool-output pre").length).toBe(1);
  });

  it("keeps a command's streaming output WINDOWED, and offers the rest", () => {
    // Streaming the middle of a 5,000-line build into the card would undo the
    // window on the first update.
    const card = commandCard();
    const lines = Array.from({ length: 200 }, (_, i) => `line${String(i)}`);
    applyOutputUpdate(card, lines.join("\n"));
    const pre = card.querySelector(".tool-output pre");
    expect(pre?.textContent).not.toContain("line100");
    const reveal = card.querySelector(".tool-output-reveal");
    expect(reveal?.textContent).toContain("160 more lines");

    // The reveal restores the full text and retires itself.
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
});
