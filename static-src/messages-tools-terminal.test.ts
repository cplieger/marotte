// Live terminal chunks that arrive before a card claims their terminal id. Both defects are ordering defects, so
// only the real mount path (hold, build, flush in reconciler order) reaches them.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderOutput } from "./output-render.js";
import type { TextSpan, ToolCall } from "./types.js";
import type * as ToolSchema from "./tool-schema.js";

// The signal layer hands back the snapshot it was given, so mount's effect does not re-enter the update path.
vi.mock("./store-signals.js", () => ({
  // The module drops a card's signal by key on release and dispose, so an `undefined` `clear` throws in teardown.
  toolCallSigs: { clear: vi.fn() },
  // Real key composition: an `undefined` key would collapse every registry entry onto one.
  toolCallSigKey: vi.fn((chatID: string, toolID: string) => `${chatID}\u0000${toolID}`),
  ensureToolCallSig: vi.fn((_chat: string, _id: string, tc: unknown) => ({ value: tc })),
  // Present-but-undefined so real-ESM linking succeeds; no path under test calls these.
  peekToolCallSig: undefined,
  entryTextSigs: undefined,
  laneSigs: undefined,
  entryKey: undefined,
  laneKey: undefined,
  ensureEntryTextSig: undefined,
  entryTextSig: undefined,
  writeEntryText: undefined,
  clearEntryTextSig: undefined,
  laneSig: undefined,
  bumpLane: undefined,
  clearTurnSigs: undefined,
  clearAllEntrySigs: undefined,
}));
vi.mock("./tool-group.js", () => ({
  maybeCollapseGroup: vi.fn(),
}));
// Spread from the original: the module is pure and the title path reads its real functions.
vi.mock("./tool-schema.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ToolSchema>()),
  isToolDone: vi.fn(() => false),
}));

// A card double that renders `opts.output` at build time through `renderOutput`, which makes a later flush a
// duplicate rather than a first paint.
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn((opts: { output?: string; outputSpans?: TextSpan[] }) => {
    const card = document.createElement("div");
    card.className = "tool-call";
    card.dataset["depth1"] = "output";
    const out = document.createElement("div");
    out.className = "tool-output";
    card.appendChild(out);
    if (opts.output !== undefined && opts.output !== "") {
      const pre = document.createElement("pre");
      renderOutput(pre, opts.output, opts.outputSpans ?? []);
      out.appendChild(pre);
    }
    return card;
  }),
  insertDiffPreview: vi.fn(),
  expandToolDetails: vi.fn(),
  applyOutcome: vi.fn(),
  refreshToolDisclosure: vi.fn(),
  // Inert: the mount effect calls it per pass.
  syncSilenceMarker: vi.fn(),
  syncOffloadLink: vi.fn(),
  syncInteractionFact: vi.fn(),
}));

import {
  mountToolCallCard,
  updateToolCall,
  appendTerminalChunk,
  forgetTerminal,
  disposeAllToolEffects,
  drainParkedTerminals,
  initToolViewCallbacks,
} from "./messages-tools.js";

const parkedCards = new Set<HTMLElement>();
initToolViewCallbacks({ isCardParked: (card) => parkedCards.has(card) });

/** The mount every card here uses; the chat id is part of the per-tool signal key. */
const mount = (tc: ToolCall): HTMLDivElement => mountToolCallCard("c-terminal", tc);

/** A minimal ToolCall, cast at the boundary: only `terminal_id` and `output` matter here. */
function toolCall(over: Record<string, unknown>): ToolCall {
  return {
    id: "tc-1",
    title: "npm test",
    kind: "execute",
    status: "in_progress",
    ts: 0,
    ...over,
  } as ToolCall;
}

function pre(card: Element): string {
  return card.querySelector(".tool-output pre")?.textContent ?? "";
}

beforeEach(() => {
  disposeAllToolEffects();
  parkedCards.clear();
  document.body.replaceChildren();
});

describe("a completion snapshot supersedes the hold", () => {
  it("does not print the opening output twice when the first frame already carries it", () => {
    // A fast command's chunks land before any card owns the terminal, then its first frame arrives completed with
    // the whole output.
    appendTerminalChunk("term-1", "hello\n", [], 0);
    appendTerminalChunk("term-1", "world\n", [], 6);

    const card = mount(
      toolCall({ terminal_id: "term-1", status: "completed", output: "hello\nworld\n" }),
    );

    // Flushing the hold on top of the snapshot would duplicate the output.
    expect(pre(card)).toBe("hello\nworld\n");
  });

  it("still flushes the hold when the tool call carries no output of its own", () => {
    // Without the hold, a command's opening lines are missing from the live view until completion.
    appendTerminalChunk("term-2", "starting\n", [], 0);
    const card = mount(toolCall({ id: "tc-2", terminal_id: "term-2" }));
    expect(pre(card)).toBe("starting\n");
  });

  it("treats an empty-string output as no output", () => {
    // `output: ""` is what a pending tool call carries, and it is not a snapshot.
    appendTerminalChunk("term-3", "early\n", [], 0);
    const card = mount(toolCall({ id: "tc-3", terminal_id: "term-3", output: "" }));
    expect(pre(card)).toBe("early\n");
  });

  it("is idempotent — a later update carrying the same link re-flushes nothing", () => {
    appendTerminalChunk("term-4", "a\n", [], 0);
    const tc = toolCall({ id: "tc-4", terminal_id: "term-4" });
    const card = mount(tc);
    expect(pre(card)).toBe("a\n");
    // The link arrives on every later update, so the discard must be permanent.
    updateToolCall(card, tc, "c-terminal");
    expect(pre(card)).toBe("a\n");
  });

  it("keeps live chunks flowing to an in-flight card after the link", () => {
    const card = mount(toolCall({ id: "tc-5", terminal_id: "term-5" }));
    appendTerminalChunk("term-5", "after\n", [], 0);
    expect(pre(card)).toBe("after\n");
  });

  it("gives a settled card no live chunks: its snapshot is its record", () => {
    const card = mount(
      toolCall({ id: "tc-5s", terminal_id: "term-5s", status: "completed", output: "snap\n" }),
    );
    appendTerminalChunk("term-5s", "after\n", [], 5);
    expect(pre(card)).toBe("snap\n");
  });

  it("still gives a settled card with no snapshot the opening lines it was held", () => {
    appendTerminalChunk("term-5h", "opening\n", [], 0);
    const card = mount(toolCall({ id: "tc-5h", terminal_id: "term-5h", status: "completed" }));
    expect(pre(card)).toBe("opening\n");
  });
});

describe("a settled call's opening lines reach a card that was parked", () => {
  function twoSurfaces(): {
    view: HTMLElement;
    parkedCard: HTMLDivElement;
    liveCard: HTMLDivElement;
  } {
    const view = document.createElement("div");
    const page = document.createElement("div");
    document.body.append(view, page);
    const tc = toolCall({ id: "tc-p", status: "in_progress" });
    const parkedCard = mount(tc);
    const liveCard = mount(tc);
    view.appendChild(parkedCard);
    page.appendChild(liveCard);
    parkedCards.add(parkedCard);
    appendTerminalChunk("term-p", "opening\n", [], 0);
    return { view, parkedCard, liveCard };
  }

  it("writes the prefix into the parked card at unpark, and the live card once", () => {
    const { view, parkedCard, liveCard } = twoSurfaces();
    updateToolCall(
      liveCard,
      toolCall({ id: "tc-p", terminal_id: "term-p", status: "completed" }),
      "c-terminal",
    );
    expect(pre(liveCard)).toBe("opening\n");
    expect(pre(parkedCard)).toBe("");

    parkedCards.delete(parkedCard);
    drainParkedTerminals("c-terminal", view);
    expect(pre(parkedCard)).toBe("opening\n");
    expect(pre(liveCard)).toBe("opening\n");

    drainParkedTerminals("c-terminal", view);
    expect(pre(parkedCard)).toBe("opening\n");
  });

  it("leaves a parked card alone when a snapshot reached it meanwhile", () => {
    const { view, parkedCard, liveCard } = twoSurfaces();
    updateToolCall(
      liveCard,
      toolCall({ id: "tc-p", terminal_id: "term-p", status: "completed" }),
      "c-terminal",
    );
    parkedCards.delete(parkedCard);
    updateToolCall(
      parkedCard,
      toolCall({
        id: "tc-p",
        terminal_id: "term-p",
        status: "completed",
        output: "opening\nall\n",
      }),
      "c-terminal",
    );
    drainParkedTerminals("c-terminal", view);
    expect(pre(parkedCard)).toBe("opening\nall\n");
  });
});

describe("only an in-flight card is a terminal's live sink", () => {
  it("a completed read card does not steal the stream from an in-flight owner", () => {
    const live = mount(toolCall({ id: "tc-a", terminal_id: "term-x" }));
    const done = mount(
      toolCall({ id: "tc-b", terminal_id: "term-x", status: "completed", output: "snap\n" }),
    );
    appendTerminalChunk("term-x", "more\n", [], 0);
    expect(pre(live)).toBe("more\n");
    expect(pre(done)).toBe("snap\n");
  });

  it("a second in-flight card does not displace the first owner", () => {
    const first = mount(toolCall({ id: "tc-c", terminal_id: "term-y" }));
    const second = mount(toolCall({ id: "tc-d", terminal_id: "term-y" }));
    appendTerminalChunk("term-y", "x\n", [], 0);
    expect(pre(first)).toBe("x\n");
    expect(pre(second)).toBe("");
  });

  it("settling the owner through an update releases the stream", () => {
    const tc = toolCall({ id: "tc-e", terminal_id: "term-z" });
    const card = mount(tc);
    appendTerminalChunk("term-z", "one\n", [], 0);
    updateToolCall(card, { ...tc, status: "completed" }, "c-terminal");
    appendTerminalChunk("term-z", "two\n", [], 4);
    expect(pre(card)).toBe("one\n");
  });
});

describe("the hold is a contiguous prefix", () => {
  // PENDING_CHARS_CAP, deliberately not exported: a test reading it would pass whatever it was set to.
  const CAP = 64 * 1024;

  it("stops accepting after the first chunk that does not fit, leaving no hole", () => {
    // Every chunk is rebased by its own `base`, so accepting a smaller chunk after a dropped one would render the two
    // sides of the hole as adjacent.
    appendTerminalChunk("term-6", "head\n", [], 0);
    appendTerminalChunk("term-6", "M".repeat(CAP), [], 5);
    appendTerminalChunk("term-6", "tail\n", [], 5 + CAP);

    const card = mount(toolCall({ id: "tc-6", terminal_id: "term-6" }));
    // "head\ntail\n" would be the gap.
    expect(pre(card)).toBe("head\n");
  });

  it("takes every chunk that fits", () => {
    for (let i = 0; i < 5; i++) {
      appendTerminalChunk("term-7", `line${String(i)}\n`, [], i * 6);
    }
    const card = mount(toolCall({ id: "tc-7", terminal_id: "term-7" }));
    expect(pre(card)).toBe("line0\nline1\nline2\nline3\nline4\n");
  });

  it("releases an unclaimed hold on terminal_exited", () => {
    appendTerminalChunk("term-8", "orphan\n", [], 0);
    forgetTerminal("term-8");
    const card = mount(toolCall({ id: "tc-8", terminal_id: "term-8" }));
    expect(pre(card)).toBe("");
  });

  it("evicts the oldest hold once too many terminals are unclaimed", () => {
    // PENDING_TERMINALS_CAP is 16; the 17th evicts the first, since the newest most likely still finds its card.
    for (let i = 0; i < 17; i++) {
      appendTerminalChunk(`bulk-${String(i)}`, `t${String(i)}\n`, [], 0);
    }
    const first = mount(toolCall({ id: "tc-b0", terminal_id: "bulk-0" }));
    expect(pre(first)).toBe("");
    const last = mount(toolCall({ id: "tc-b16", terminal_id: "bulk-16" }));
    expect(pre(last)).toBe("t16\n");
  });
});

describe("a flushed chunk keeps its styling", () => {
  it("rebases held spans by each chunk's own base", () => {
    // `base` is stored per chunk so the second chunk's spans address the accumulated stream.
    appendTerminalChunk("term-9", "ok\n", [{ start: 0, end: 2, fg: 2, bg: -1, attrs: 0 }], 0);
    appendTerminalChunk("term-9", "bad\n", [{ start: 3, end: 6, fg: 1, bg: -1, attrs: 0 }], 3);
    const card = mount(toolCall({ id: "tc-9", terminal_id: "term-9" }));
    const spans = card.querySelectorAll(".tool-output pre span");
    expect([...spans].map((s) => [s.textContent, s.className])).toEqual([
      ["ok", "ansi-green-fg"],
      ["bad", "ansi-red-fg"],
    ]);
  });
});
