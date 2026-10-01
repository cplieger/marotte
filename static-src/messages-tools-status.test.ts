// ---------------------------------------------------------------------------
// The status half of the update path, against the REAL tool card.
//
// Its own file rather than a case in `messages-tools.test.ts`, because that file
// replaces `./tool-card.js` wholesale — and every observable here belongs to that
// module: whether the region opened, whether the chevron survived. Asserted
// against a stub they could not fail.
//
// Two behaviours meet on one frame, which is why they are pinned together. A
// terminal `tool_call_update` commonly carries the status AND the output, so
// `applyStatusUpdate` runs against whatever the output region holds at that
// moment — and it reads that region twice, for the Explain-this-error gate and for
// the bare-disclosure predicate. Status is therefore applied LAST.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import type { ToolCall } from "./types.js";

// The REAL signal layer. It was mocked to keep the chat store out of the graph, and
// that premise is gone: `store.ts` is linked here through `messages-tools.js`, so the
// factory had to name every symbol the whole graph imports — it named fourteen the
// entry model retired and omitted twelve it added, which is why it failed at
// COLLECTION. `ensureToolCallSig` seeds its signal with the snapshot it is handed, so
// the mount's effect reads `next === lastApplied` and does not re-enter the update
// path, which is the one behaviour the factory existed to fake.

// The real chrome reaches `scroll.ts`, which resolves the transcript scroller at
// module load and throws on a missing id.
for (const id of ["messages", "messages-wrap", "messages-wrap-outer", "chat-view"]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}
const scrollBottom = document.createElement("div");
scrollBottom.id = "scroll-bottom";
scrollBottom.appendChild(document.createElement("span"));
document.body.appendChild(scrollBottom);

const { buildToolCard } = await import("./tool-card.js");
const { buildToolGroupShell, groupBody, refreshGroupHeader, autoCollapseGroup } =
  await import("./tool-group.js");
const { updateToolCall, mountToolCallCard, appendTerminalChunk, disposeAllToolEffects } =
  await import("./messages-tools.js");

/** A live, in-flight call with nothing disclosable yet: no input, no output, and
 *  `other` for the depth 1 that gives it a details region at all. */
function liveCard(id: string): HTMLDivElement {
  const card = buildToolCard({
    id,
    title: "invoke_sub_agent",
    kind: "other",
    status: "in_progress",
    live: true,
  });
  document.body.appendChild(card);
  return card;
}

function frame(id: string, tc: Partial<ToolCall>): ToolCall {
  return { id, title: "invoke_sub_agent", kind: "other", ts: 0, ...tc } as ToolCall;
}

describe("a terminal frame carrying the failure AND its output", () => {
  it("auto-expands the card", () => {
    const card = liveCard("st-open");
    updateToolCall(card, frame("st-open", { status: "failed", output: "exit status 2\n" }), "c1");
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("true");
    card.remove();
  });

  it("keeps the chevron", () => {
    const card = liveCard("st-chevron");
    updateToolCall(
      card,
      frame("st-chevron", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });

  it("offers Explain this error", () => {
    // The ordering pin: on the pre-fix order this frame reached
    // `applyStatusUpdate` with an unpainted region, so the gate saw no output and
    // no button was ever offered unless a later update happened to arrive.
    const card = liveCard("st-explain");
    updateToolCall(
      card,
      frame("st-explain", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    const btn = card.querySelector(".tool-explain-btn");
    expect(btn).not.toBeNull();
    // The trigger is ICON-ONLY, so its accessible name is the whole of what a
    // reader has to identify it by — which makes a locator by NAME the honest one
    // here, and a class-only assertion something a nameless button would satisfy.
    // `tool-explain-btn.test.ts` owns the rest of that contract and its geometry.
    expect(card.querySelector('.tool-explain-btn[aria-label="Explain this error"]')).toBe(btn);
    card.remove();
  });
});

describe("a live call that has produced nothing YET", () => {
  it("has no chevron while the region is empty", () => {
    // The wire status is not evidence about the region: both production call sites
    // pass `live: true`, so a REPLAYED in-progress call is indistinguishable from
    // one still filling.
    const card = liveCard("st-inflight-bare");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gains one on its first output frame, still in flight", () => {
    const card = liveCard("st-inflight-out");
    updateToolCall(
      card,
      frame("st-inflight-out", { status: "in_progress", output: "step 1 of 3\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });
});

describe("a terminal frame whose output is blank", () => {
  it("paints nothing into the region", () => {
    // One value, three consumers, and the two downstream ones already trimmed: the
    // predicate and the Explain gate both read this region as empty, so a `<pre>`
    // built here was DOM nothing could ever reach.
    const card = liveCard("st-blank-pre");
    updateToolCall(card, frame("st-blank-pre", { status: "completed", output: "   \n  \n" }), "c1");
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });

  it("offers no Explain button on a failure", () => {
    const card = liveCard("st-blank-explain");
    updateToolCall(card, frame("st-blank-explain", { status: "failed", output: "  \n" }), "c1");
    expect(card.querySelector(".tool-explain-btn")).toBeNull();
    // Also by NAME, so the gate stays defended in a spelling the trigger's own
    // class cannot carry: an icon-only control that lost its `aria-label` and kept
    // its class would satisfy the line above.
    expect(card.querySelector('[aria-label="Explain this error"]')).toBeNull();
    card.remove();
  });
});

describe("a call that failed having produced nothing", () => {
  it("is not force-opened", () => {
    const card = liveCard("st-bare-open");
    updateToolCall(card, frame("st-bare-open", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("loses its chevron", () => {
    const card = liveCard("st-bare-chevron");
    updateToolCall(card, frame("st-bare-chevron", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gets the chevron back if output arrives on a later frame", () => {
    const card = liveCard("st-bare-late");
    updateToolCall(card, frame("st-bare-late", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    updateToolCall(card, frame("st-bare-late", { output: "late stderr\n" }), "c1");
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });

  it("gets the chevron back if its terminal writes a chunk afterwards", () => {
    // A terminal's lifecycle is its own: `terminal_output` frames keep arriving
    // until `terminal_exited`, so one can land after the call has already reported
    // failed. Without the refresh those bytes sit in a region nothing can open.
    const tc = frame("st-bare-term", { status: "in_progress", terminal_id: "term-1" });
    const card = mountToolCallCard("c1", tc);
    document.body.appendChild(card);
    updateToolCall(card, frame("st-bare-term", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    appendTerminalChunk("term-1", "late stderr\n", [], 0);

    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    disposeAllToolEffects();
    card.remove();
  });
});

describe("a group whose members ran LIVE and then settled", () => {
  it("folds once the next element supersedes it", () => {
    // The live path end to end, which is the population the transcript-level
    // fixtures miss: they build cards born `completed`, so nothing there ever
    // carries the in-flight marker. Two cards mounted in flight, settled by a
    // terminal frame carrying the SERVER's duration, then superseded.
    const group = buildToolGroupShell();
    const a = liveCard("st-fold-a");
    const b = liveCard("st-fold-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    updateToolCall(a, frame("st-fold-a", { status: "completed", duration_ms: 1200 }), "c1");
    updateToolCall(b, frame("st-fold-b", { status: "completed", duration_ms: 3400 }), "c1");
    expect(a.dataset["startMs"]).toBeUndefined();
    expect(b.dataset["startMs"]).toBeUndefined();

    autoCollapseGroup(group);

    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
    group.remove();
  });
});
// ---------------------------------------------------------------------------
// A REFUSAL is a fifth outcome, and it arrives on a `completed` frame.
//
// A workflow update the run rejected reports `completed` with the refusal in its
// own output, so `completed` alone paints a green check over it and `failed` would
// send the reader to debug a tool that behaved correctly. `declined` is carried on
// the DELTA as a one-way latch (`omitempty`, only ever `true`), stamped on the
// dataset ahead of the status, and read back from there by `applyOutcome` — which
// is the half only a real card can assert.
// ---------------------------------------------------------------------------

describe("a frame carrying a refusal", () => {
  it("takes the refusal as its outcome, not the completed it rides on", () => {
    const card = liveCard("st-dec-mark");
    updateToolCall(
      card,
      frame("st-dec-mark", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    expect(card.dataset["declined"]).toBe("1");
    expect(card.dataset["outcome"]).toBe("declined");
    expect(card.querySelector(".tool-icon.is-declined")).not.toBeNull();
    card.remove();
  });

  it("opens the region without a click, because the reason IS the output", () => {
    const card = liveCard("st-dec-open");
    updateToolCall(
      card,
      frame("st-dec-open", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("true");
    card.remove();
  });

  it("offers no Explain this error", () => {
    // The obvious implementation of the auto-expand above is to widen the failure
    // branch (`status === "failed" || declined`), which would also offer this button.
    // Nothing broke, so there is no error to explain and the button would send the
    // reader to debug a tool that did exactly what it was asked.
    const card = liveCard("st-dec-explain");
    updateToolCall(
      card,
      frame("st-dec-explain", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-explain-btn")).toBeNull();
    // By NAME too, for the reason the blank-output case states: the class alone is
    // not what identifies an icon-only control.
    expect(card.querySelector('[aria-label="Explain this error"]')).toBeNull();
    card.remove();
  });

  it("is not force-opened when the region is bare", () => {
    // One enforcement point: `expandToolDetails` refuses a card the refresh left
    // with no chevron, whatever the caller, so the refusal path needs no gate of
    // its own — and a region opened with nothing in it has no control to close it.
    const card = liveCard("st-dec-bare");
    updateToolCall(card, frame("st-dec-bare", { status: "completed", declined: true }), "c1");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("keeps the mark when a later frame carries no refusal", () => {
    // The wire field is `omitempty` on a bool that is only ever sent true, so an
    // absent one means UNCHANGED. A frame that cleared the stamp would repaint the
    // card as a plain success once the run's next status update landed.
    const card = liveCard("st-dec-latch");
    updateToolCall(
      card,
      frame("st-dec-latch", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    updateToolCall(card, frame("st-dec-latch", { status: "completed" }), "c1");
    expect(card.dataset["declined"]).toBe("1");
    expect(card.dataset["outcome"]).toBe("declined");
    card.remove();
  });
});
