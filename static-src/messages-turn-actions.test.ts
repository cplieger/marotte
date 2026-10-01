// The turn footer's action buttons: copy, export, and the raw-markdown toggle.
//
// The toggle's three mechanical constraints are what most of this file pins. It
// must ADD a sibling rather than replace the rendered children, because
// reconcile re-runs the body updater on every repaint; it must hide with
// `.hidden`, because find-in-chat's walker prunes `.hidden` subtrees and would
// otherwise count every match twice; and it must hide the block CONTAINER
// rather than its children, so a block arriving after the toggle cannot leak
// into the raw view.
//
// The other half is `turnMarkdown`, which is now read off the turn's ENTRIES:
// one paragraph per PROSE RUN, a run's `text` entries concatenated because the
// renderer feeds them to a single markdown parser, and `entryRenders` as the
// whole boundary test — so a `tool_result` folds away without splitting a run
// while a `steer_ack` ends one without contributing to it, and a delegate's
// lane never reaches the parent's copy.
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
  // The canonical mock, so a name this module's graph reaches but no case drives
  // cannot fail the whole file at link time.
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
const downloadChatExport = vi.fn();
vi.mock("./chat-export.js", () => ({ downloadChatExport }));

const {
  mountTurnFooterActions,
  resetTurnSourceView,
  initTurnActionCallbacks,
  initTurnActionsBodyProbe,
  syncSourceView,
  copyWithFeedback,
} = await import("./messages-turn-actions.js");

// The real injection point is messages.ts's CSP-safe <template> clone; a plain
// element is enough here and keeps the fixture DOM readable.
initTurnActionCallbacks({
  svgTemplate: (markup: string) => () => {
    const span = document.createElement("span");
    span.dataset["icon"] = String(markup.length);
    return span;
  },
});

// The body probe is MODULE state and a plain arrow, so `mockReset` cannot restore
// it: a case arming a windowed body must not leave every later case windowed, and a
// case that FAILS while armed would. Wired here, not in each case.
afterEach(() => {
  initTurnActionsBodyProbe(() => true);
});

/** One sealed entry of the fixture turn. `seq` is its position — `entryRenders`
 *  reads it for the plan rule — and `lane` is `""` for the agent that owns the
 *  turn, a uuid for a delegate. */
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

/** The DOM shape buildTurn produces: a card whose body holds one element per entry
 *  (a text row and a tool card) plus a ledger footer. The body's own children are
 *  what `renderedRegions` returns, so a shape change in production moves this. */
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

/** Fold the fixture card and give it the face a folded turn renders: the
 *  prose bubble plus the surface the raw toggle swaps against. */
function fold(f: Fixture, faceText = "face prose"): HTMLElement {
  f.card.setAttribute("data-folded", "");
  const face = document.createElement("div");
  face.className = "turn-face";
  const prose = document.createElement("div");
  prose.className = "message assistant turn-face-prose";
  prose.textContent = faceText;
  face.appendChild(prose);
  f.footer.before(face);
  return face;
}

/** The default body: one prose entry, whose text is both the markdown and the
 *  source the toggle shows. */
function prose(s = "# hi\n\nsource text"): Entry[] {
  return [text(1, s)];
}

function buttons(footer: HTMLElement): HTMLButtonElement[] {
  return [
    ...footer.querySelectorAll<HTMLButtonElement>(".turn-actions-buttons button.turn-action-btn"),
  ];
}

function sourceButton(footer: HTMLElement): HTMLButtonElement {
  const b = footer.querySelector<HTMLButtonElement>(".turn-action-btn[aria-pressed]");
  if (b === null) {
    throw new Error("no source toggle");
  }
  return b;
}

function raw(card: HTMLElement): HTMLElement | null {
  return card.querySelector(".turn-raw");
}

describe("mountTurnFooterActions", () => {
  it("mounts one slot with copy, markdown, source, chat id and export", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
    expect(buttons(f.footer)).toHaveLength(5);
  });

  it("groups every action under More, Copy included, so the phone row holds one trigger", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);

    const slot = f.footer.querySelector<HTMLElement>(".turn-actions-buttons")!;
    const more = slot.querySelector<HTMLDetailsElement>(".turn-actions-more")!;
    // Nothing sits beside the overflow. Copy used to, which left a phone row
    // carrying two targets where the point of the overflow is one.
    expect(slot.querySelectorAll(":scope > button.turn-action-btn")).toHaveLength(0);
    expect(more.querySelector(":scope > summary")?.getAttribute("aria-label")).toBe(
      "More turn actions",
    );
    expect(
      more.querySelectorAll(":scope > .turn-actions-group > button.turn-action-btn"),
    ).toHaveLength(5);
  });

  // The menu closes on the gesture, so the button carrying the 1.5s `.copied`
  // flash is off screen before it can be read and the toast is the only channel
  // left. Everywhere else the flash is the feedback and a toast beside it would
  // be a second rendering of one fact.
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
    expect(buttons(f.footer)).toHaveLength(5);
  });

  it("mounts nothing for a turn that wrote no prose at all", () => {
    // A turn that ran a tool and said nothing: there is no markdown to copy and
    // no source to show, so the row carries no actions.
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
    expect(buttons(f.footer)).toHaveLength(5);
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
    // The renderer feeds a run to a SINGLE parser, so a construct may straddle two
    // entries: a separator between them would break the very word the seal split.
    const f = fixture([text(1, "**bo"), text(2, "ld**")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });

  it("spends a blank line at a run BOUNDARY, so two runs are two paragraphs", () => {
    // Something that RENDERS between two text entries ends the first run. It is
    // the renderer's own boundary, which is why the copy reads as the reader saw it.
    const f = fixture([text(1, "before"), entry(2, "tool_call"), text(3, "after")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("before\n\nafter", expect.anything());
  });

  it("folds a tool_result away without breaking the run it sits in", () => {
    // A `tool_result` renders NOTHING of its own — it folds into its call's card —
    // so a stream that straddles one is still one paragraph. The mirror of the
    // boundary case above, and the pair is what pins `entryRenders` as the test.
    const f = fixture([text(1, "first "), entry(2, "tool_result"), text(3, "second")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("first second", expect.anything());
  });

  it("ends a run on a steer_ack, which contributes no words of its own", () => {
    // It renders, so it is a boundary; it carries no text, so the paragraph it
    // ends is the last thing the agent wrote before the read.
    const f = fixture([text(1, "before"), entry(2, "steer_ack"), text(3, "after")]);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("before\n\nafter", expect.anything());
  });

  it("leaves a delegate's prose out of the turn's copy", () => {
    // A delegate's entries are its own lane and render on its own page; the parent's
    // copy is the parent's words. (Carried forward from the block model, where the
    // same oracle read as "skip a block carrying an agent_subtask_id".)
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
    // The body is mounted but WINDOWED, so its bubbles are a hole rather than the
    // turn's answer. The face is what the turn's final answer is, and the fold already
    // relies on that; copying the body here would hand over whichever entries the
    // reader's scroll position happened to leave mounted.
    const f = fixture(prose("**bold**"), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    fold(f, "face words");
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("face words", expect.anything());
  });

  it("falls back to the MARKDOWN for a partial body with no face at all", () => {
    // A partial body and no face is the shape a running or faceless turn has: the store
    // is then the only complete answer, and it is the one a folded stub already gives.
    const f = fixture(prose("**bold**"), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });
});

describe("the raw-markdown toggle", () => {
  it("starts closed and reports so on the button", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(raw(f.card)).toBeNull();
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("false");
  });

  it("shows the source and hides the whole rendered body with .hidden", () => {
    const f = fixture(prose("# hi"));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(raw(f.card)?.textContent).toBe("# hi");
    // Every row hides, so prose and evidence go together. `.hidden` specifically:
    // find-in-chat prunes it, so exactly one of the two renderings is searchable
    // and matches are never double-counted.
    expect(f.row.classList.contains("hidden")).toBe(true);
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("true");
  });

  it("takes the tool cards with it, so the raw view is source and nothing else", () => {
    // The source is one document: every word the model wrote lands at the top
    // of it, while a tool card left behind keeps the position it had between
    // two paragraphs. Half a swap shows one turn in two orders at once.
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(f.tool.classList.contains("hidden")).toBe(true);
  });

  it("toggles back to the rendering", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    btn.click();
    btn.click();
    expect(raw(f.card)?.classList.contains("hidden")).toBe(true);
    expect(f.row.classList.contains("hidden")).toBe(false);
    expect(f.tool.closest(".hidden")).toBeNull();
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });

  it("ADDS the source beside the rendering rather than replacing it", () => {
    // The replacement shape is what a repaint undoes; this is the structural
    // guarantee that makes the toggle survive one.
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(f.bubble.isConnected).toBe(true);
    expect(f.row.isConnected).toBe(true);
    // Inside the body, above the rows it stands in for.
    expect(raw(f.card)?.parentElement).toBe(f.body);
  });

  it("keeps one raw view and one action row across a repaint", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    // What the body updater does on every repaint: append newly-arrived entries as
    // rows of the body, then re-mount the footer. Neither may disturb the raw view
    // or double the footer's controls; hiding the arrival is `syncSourceView`'s,
    // pinned by the case below.
    const later = document.createElement("div");
    later.className = "tool-call";
    f.body.appendChild(later);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(raw(f.card)).not.toBeNull();
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
    expect(f.card.querySelectorAll(".turn-raw")).toHaveLength(1);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
  });

  it("swallows a ROW that arrives after the toggle", () => {
    // A window move mounts a new row into the body, unhidden — rendered output
    // beside the raw text until something re-hides it.
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    const region = document.createElement("div");
    region.className = "msg-row";
    region.textContent = "the rest of the turn";
    f.body.appendChild(region);
    expect(region.classList.contains("hidden")).toBe(false);

    syncSourceView(f.body);

    expect(region.classList.contains("hidden")).toBe(true);
    // And the raw view is untouched: this only ever hides, and only while raw shows.
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
  });

  it("leaves a new row alone while the rendering is what is showing", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    syncSourceView(f.body);
    expect(f.row.classList.contains("hidden")).toBe(false);
  });

  it("re-reads the source at click time, so a later refreshed turn wins", () => {
    // The slot mounts once and is idempotent, so a source captured at mount
    // time would go stale the moment the range read replaced the entry.
    const f = fixture(prose("streamed"));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const freshened: Turn = { ...f.turn, body: prose("server sanitized") };
    mountTurnFooterActions(f.footer, f.card, freshened);
    sourceButton(f.footer).click();
    expect(raw(f.card)?.textContent).toBe("server sanitized");
  });

  it("renames itself so the button says what the next press does", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View rendered reply");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
  });

  it("swaps the FACE prose when the turn is folded", () => {
    const f = fixture(prose("# hi"));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const face = fold(f);
    const faceProse = face.querySelector(".turn-face-prose");
    sourceButton(f.footer).click();
    const pre = raw(f.card);
    expect(pre?.parentElement).toBe(face);
    expect(pre?.textContent).toBe("# hi");
    expect(faceProse?.classList.contains("hidden")).toBe(true);
    // The body's rows hide with it: one rendering at a time, everywhere.
    expect(f.row.classList.contains("hidden")).toBe(true);
  });

  it("resets when the fold state changes, so the button never lies", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    btn.click();
    expect(f.row.classList.contains("hidden")).toBe(true);
    // What setCardFolded does on any fold flip.
    resetTurnSourceView(f.card);
    expect(raw(f.card)).toBeNull();
    expect(f.row.classList.contains("hidden")).toBe(false);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
  });

  it("reset is a no-op when no source view is open", () => {
    const f = fixture(prose());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    resetTurnSourceView(f.card);
    expect(f.row.classList.contains("hidden")).toBe(false);
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("false");
  });
});

describe("copyWithFeedback", () => {
  it("dispatches the copy silently and flashes the button", () => {
    const btn = document.createElement("button");
    copyWithFeedback(btn, "hello");
    expect(dispatch).toHaveBeenCalledWith("hello", expect.objectContaining({ silent: true }));
    expect(btn.classList.contains("copied")).toBe(true);
  });

  it("does nothing for empty text", () => {
    const btn = document.createElement("button");
    copyWithFeedback(btn, "");
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("clears the flash after the confirmation window", () => {
    vi.useFakeTimers();
    try {
      const btn = document.createElement("button");
      copyWithFeedback(btn, "hello");
      vi.advanceTimersByTime(1600);
      expect(btn.classList.contains("copied")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
