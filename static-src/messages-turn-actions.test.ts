// The turn footer's actions, and `turnMarkdown`'s per-prose-run paragraphs over the turn's entries.
import { describe, it, expect, vi, afterEach } from "vitest";
import type { Entry, EntryKind } from "./types.js";
import type { Turn } from "./turns.js";

const dispatch = vi.fn(
  (_text: string, opts?: { silent?: boolean; onSuccess?: () => void }): Promise<void> => {
    opts?.onSuccess?.();
    return Promise.resolve();
  },
);

let active = { id: "c1", name: "chat" };

vi.mock("./store.js", async () => ({
  // The canonical mock, so an unreached name cannot fail the file at link time.
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  getActive: () => active,
  getActiveId: () => active.id,
}));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: {
    dispatch: (text: string, opts?: { silent?: boolean; onSuccess?: () => void }) =>
      dispatch(text, opts),
  },
}));

const { mountTurnFooterActions, initTurnActionCallbacks, initTurnActionsBodyProbe, turnLink } =
  await import("./messages-turn-actions.js");

// A plain element stands in for messages.ts's <template> clone.
initTurnActionCallbacks({
  svgTemplate: (markup: string) => () => {
    const span = document.createElement("span");
    span.dataset["icon"] = String(markup.length);
    return span;
  },
});

// The body probe is module state `mockReset` cannot restore, so a failing armed case would leak it.
afterEach(() => {
  initTurnActionsBodyProbe(() => true);
});

/** `seq` is its position (read for the plan rule); `lane` is `""` for the turn's agent. */
function entry(seq: number, kind: EntryKind, payload: unknown = {}, lane = ""): Entry {
  return { id: `e${seq}`, turn: "t1", lane, kind, payload, seq, ts: seq };
}

/** A `text` entry: the only kind that contributes words to the copy. */
function text(seq: number, s: string): Entry {
  return entry(seq, "text", { text: s });
}

interface Fixture {
  card: HTMLElement;
  footer: HTMLElement;
  body: HTMLElement;
  row: HTMLElement;
  bubble: HTMLDivElement;
  tool: HTMLElement;
  turn: Turn;
}

/** The DOM shape buildTurn produces; the body's children are what `renderedRegions` returns. */
function fixture(body: Entry[], rendered = "rendered reply", outcome = "completed"): Fixture {
  document.body.innerHTML = "";
  const card = document.createElement("div");
  card.className = "turn";
  const bodyEl = document.createElement("div");
  bodyEl.className = "turn-body";
  const row = document.createElement("div");
  row.className = "msg-row";
  const bubble = document.createElement("div");
  bubble.className = "message assistant";
  bubble.textContent = rendered;
  row.appendChild(bubble);
  bodyEl.appendChild(row);
  const tool = document.createElement("div");
  tool.className = "tool-group";
  tool.textContent = "ran a command";
  bodyEl.appendChild(tool);
  card.appendChild(bodyEl);
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  card.appendChild(footer);
  document.body.appendChild(card);
  active = { id: "c1", name: "chat" };
  const turn: Turn = {
    id: "t1",
    n: 1,
    trigger: undefined,
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: outcome as Turn["outcome"],
    rewindTo: undefined,
  };
  return { card, footer, body: bodyEl, row, bubble, tool, turn };
}

/** Fold the fixture card and give it the face a folded turn renders. */
function fold(f: Fixture, faceText = "face prose"): void {
  f.card.setAttribute("data-folded", "");
  const face = document.createElement("div");
  face.className = "turn-face";
  const prose = document.createElement("div");
  prose.className = "message assistant turn-face-prose";
  prose.textContent = faceText;
  face.appendChild(prose);
  f.footer.before(face);
}

/** The default body: one prose entry. */
function prose(s = "# hi\n\nsource text"): Entry[] {
  return [text(1, s)];
}

function buttons(footer: HTMLElement): HTMLButtonElement[] {
  return [
    ...footer.querySelectorAll<HTMLButtonElement>(".turn-actions-buttons button.turn-action-btn"),
  ];
}

describe("mountTurnFooterActions", () => {
  it("mounts one slot with copy as text, copy as markdown and the turn link", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
    expect(buttons(f.footer)).toHaveLength(3);
  });

  it("groups every action under More, Copy included, so the phone row holds one trigger", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);

    const slot = f.footer.querySelector<HTMLElement>(".turn-actions-buttons")!;
    const more = slot.querySelector<HTMLDetailsElement>(".turn-actions-more")!;
    // Nothing sits beside the overflow.
    expect(slot.querySelectorAll(":scope > button.turn-action-btn")).toHaveLength(0);
    expect(more.querySelector(":scope > summary")?.getAttribute("aria-label")).toBe(
      "More turn actions",
    );
    expect(
      more.querySelectorAll(":scope > .turn-actions-group > button.turn-action-btn"),
    ).toHaveLength(3);
  });

  // The menu closes on the gesture and hides the `.copied` flash, so the toast is the only channel left.
  it("lets the copy toast through when the click came from the open overflow", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const more = f.footer.querySelector<HTMLDetailsElement>(".turn-actions-more")!;

    more.querySelector<HTMLElement>(":scope > summary")!.click();
    expect(more.open).toBe(true);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenLastCalledWith("bold", expect.objectContaining({ silent: false }));
  });

  it("suppresses the copy toast while the overflow is inline, where the flash is visible", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const more = f.footer.querySelector<HTMLDetailsElement>(".turn-actions-more")!;

    expect(more.open).toBe(false);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenLastCalledWith("bold", expect.objectContaining({ silent: true }));
  });

  it("closes the mobile overflow after an action", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);

    const more = f.footer.querySelector<HTMLDetailsElement>(".turn-actions-more")!;
    const summary = more.querySelector<HTMLElement>(":scope > summary")!;
    summary.click();
    expect(more.open).toBe(true);

    more.querySelector<HTMLButtonElement>('[aria-label="Copy as markdown"]')?.click();
    expect(more.open).toBe(false);
  });

  it("is idempotent across repeated paint passes", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
    expect(buttons(f.footer)).toHaveLength(3);
  });

  it("mounts nothing for a turn that wrote no prose at all", () => {
    // No markdown to copy and no source to show, so the row carries no actions.
    const f = fixture([entry(1, "tool_call")], "");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelector(".turn-actions-buttons")).toBeNull();
  });

  it("mounts nothing while the turn is still running", () => {
    const f = fixture(prose(), "rendered reply", "running");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelector(".turn-actions-buttons")).toBeNull();
  });

  it("mounts once the turn settles, on the pass after the running one", () => {
    const f = fixture(prose(), "rendered reply", "running");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, { ...f.turn, outcome: "completed" });
    expect(buttons(f.footer)).toHaveLength(3);
  });

  it("copies the stored markdown, not the rendered text", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });

  it("copies the rendered text for copy-as-text", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("bold", expect.anything());
  });

  it("concatenates a prose run's entries, because they are one markdown stream", () => {
    // A run feeds a single parser, so a construct may straddle two entries.
    const f = fixture([text(1, "**bo"), text(2, "ld**")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });

  it("spends a blank line at a run BOUNDARY, so two runs are two paragraphs", () => {
    // Something rendered between two text entries ends the run, as the renderer does.
    const f = fixture([text(1, "before"), entry(2, "tool_call"), text(3, "after")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("before\n\nafter", expect.anything());
  });

  it("folds a tool_result away without breaking the run it sits in", () => {
    // A `tool_result` folds into its call's card, so it does not split a run.
    const f = fixture([text(1, "first "), entry(2, "tool_result"), text(3, "second")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("first second", expect.anything());
  });

  it("ends a run on a steer_ack, which contributes no words of its own", () => {
    // It renders, so it is a boundary; it carries no text.
    const f = fixture([text(1, "before"), entry(2, "steer_ack"), text(3, "after")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("before\n\nafter", expect.anything());
  });

  it("leaves a delegate's prose out of the turn's copy", () => {
    // A delegate's entries are its own lane; the parent's copy is the parent's words.
    const f = fixture([text(1, "parent words"), entry(2, "text", { text: "delegate" }, "sub-1")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("parent words", expect.anything());
  });

  it("copies the face prose for copy-as-text when folded", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    f.body.remove(); // a folded stub keeps no body
    fold(f, "face words");
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("face words", expect.anything());
  });

  it("copies the FACE, not a partial body, when the window holds only part of the turn", () => {
    // A windowed body is a hole, so the copy comes from the face, the turn's final answer.
    const f = fixture(prose("**bold**"), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    fold(f, "face words");
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("face words", expect.anything());
  });

  it("falls back to the MARKDOWN for a partial body with no face at all", () => {
    // With a partial body and no face, the store is the only complete answer.
    const f = fixture(prose("**bold**"), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });
});

describe("the prompt in the copies", () => {
  function withPrompt(f: Fixture, prompt: string): Turn {
    return { ...f.turn, trigger: { id: "m1", text: prompt } as Turn["trigger"] };
  }

  it("puts the prompt first in copy-as-text", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, withPrompt(f, "  why?  "));
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenLastCalledWith("why?\n\nbold", expect.anything());
  });

  it("quotes the prompt in copy-as-markdown, every line of it", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, withPrompt(f, "first\n\nsecond"));
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenLastCalledWith(
      "> first\n>\n> second\n\n**bold**",
      expect.anything(),
    );
  });

  it("copies the reply alone on an agent-initiated turn", () => {
    const f = fixture(prose("**bold**"), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenLastCalledWith("**bold**", expect.anything());
  });
});

describe("the link to the turn", () => {
  it("copies the chat's full URL with the turn's fragment", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, { ...f.turn, n: 7 });
    f.footer.querySelector<HTMLButtonElement>('[aria-label="Copy link to this turn"]')?.click();
    expect(dispatch).toHaveBeenLastCalledWith(
      `${location.origin}/chat/c1#turn-7`,
      expect.anything(),
    );
  });

  it("encodes the chat id", () => {
    expect(turnLink("c a", 2)).toBe(`${location.origin}/chat/c%20a#turn-2`);
  });
});
