// Status updates against the REAL tool card (`messages-tools.test.ts` stubs it). A terminal frame often carries
// status and output, and `applyStatusUpdate` reads the output region for the bare-disclosure predicate, so status is
// applied last.

import { describe, it, expect } from "vitest";
import type { ToolCall } from "./types.js";

// The real signal layer: `ensureToolCallSig` seeds the snapshot, so the mount effect does not re-enter the update path.

// The real chrome reaches `scroll.ts`, which throws on a missing id at module load.
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
const { applyToolCallUpdate, mountToolCallCard, appendTerminalChunk, disposeAllToolEffects } =
  await import("./messages-tools.js");

/** A live call with nothing disclosable yet; `other` gives depth 1 a details region. */
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
  it("stays collapsed like any other call, with the failure in its header and no button of its own", () => {
    const card = liveCard("st-shut");
    applyToolCallUpdate(
      card,
      frame("st-shut", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("false");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    expect(card.dataset["outcome"]).toBe("fail");
    expect(card.querySelector(".tool-icon.is-fail")).not.toBeNull();
    expect(card.querySelector(":scope > button")).toBeNull();
    card.remove();
  });

  it("keeps the chevron", () => {
    const card = liveCard("st-chevron");
    applyToolCallUpdate(
      card,
      frame("st-chevron", { status: "failed", output: "exit status 2\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });
});

describe("a live call that has produced nothing YET", () => {
  it("has no chevron while the region is empty", () => {
    // Both production call sites pass `live: true`, so a replayed in-progress call is indistinguishable from a live one.
    const card = liveCard("st-inflight-bare");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gains one on its first output frame, still in flight", () => {
    const card = liveCard("st-inflight-out");
    applyToolCallUpdate(
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
    // The predicate reads this region as empty, so a `<pre>` here would be unreachable DOM.
    const card = liveCard("st-blank-pre");
    applyToolCallUpdate(
      card,
      frame("st-blank-pre", { status: "completed", output: "   \n  \n" }),
      "c1",
    );
    expect(card.querySelector(".tool-output pre")).toBeNull();
    card.remove();
  });
});

describe("a call that failed having produced nothing", () => {
  it("is not force-opened", () => {
    const card = liveCard("st-bare-open");
    applyToolCallUpdate(card, frame("st-bare-open", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("loses its chevron", () => {
    const card = liveCard("st-bare-chevron");
    applyToolCallUpdate(card, frame("st-bare-chevron", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    card.remove();
  });

  it("gets the chevron back if output arrives on a later frame", () => {
    const card = liveCard("st-bare-late");
    applyToolCallUpdate(card, frame("st-bare-late", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    applyToolCallUpdate(card, frame("st-bare-late", { output: "late stderr\n" }), "c1");
    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    card.remove();
  });

  it("gets the chevron back if its terminal writes a chunk afterwards", () => {
    // `terminal_output` frames arrive until `terminal_exited`, after a failed status; without the refresh those bytes
    // sit in a region nothing can open.
    const tc = frame("st-bare-term", { status: "in_progress", terminal_id: "term-1" });
    const card = mountToolCallCard("c1", tc);
    document.body.appendChild(card);
    applyToolCallUpdate(card, frame("st-bare-term", { status: "failed" }), "c1");
    expect(card.querySelector(".tool-disclosure")).toBeNull();

    appendTerminalChunk("term-1", "late stderr\n", [], 0);

    expect(card.querySelector(".tool-disclosure")).not.toBeNull();
    disposeAllToolEffects();
    card.remove();
  });
});

describe("a group whose members ran LIVE and then settled", () => {
  it("folds once the next element supersedes it", () => {
    // The live path end to end: transcript fixtures build cards born `completed` and never carry the in-flight marker.
    const group = buildToolGroupShell();
    const a = liveCard("st-fold-a");
    const b = liveCard("st-fold-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    applyToolCallUpdate(a, frame("st-fold-a", { status: "completed", duration_ms: 1200 }), "c1");
    applyToolCallUpdate(b, frame("st-fold-b", { status: "completed", duration_ms: 3400 }), "c1");
    expect(a.dataset["startMs"]).toBeUndefined();
    expect(b.dataset["startMs"]).toBeUndefined();

    autoCollapseGroup(group);

    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
    group.remove();
  });
});
// A refusal arrives on a `completed` frame with `declined` on the delta, a one-way `omitempty` latch stamped on
// the dataset ahead of the status and read back by `applyOutcome`.

describe("a frame carrying a refusal", () => {
  it("takes the refusal as its outcome, not the completed it rides on", () => {
    const card = liveCard("st-dec-mark");
    applyToolCallUpdate(
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
    applyToolCallUpdate(
      card,
      frame("st-dec-open", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    expect(card.querySelector(".tool-disclosure")?.getAttribute("aria-expanded")).toBe("true");
    card.remove();
  });

  it("is not force-opened when the region is bare", () => {
    // `expandToolDetails` refuses a card with no chevron whatever the caller, so the refusal path needs no gate.
    const card = liveCard("st-dec-bare");
    applyToolCallUpdate(card, frame("st-dec-bare", { status: "completed", declined: true }), "c1");
    expect(card.querySelector(".tool-details")?.getAttribute("aria-hidden")).toBe("true");
    card.remove();
  });

  it("keeps the mark when a later frame carries no refusal", () => {
    // `declined` is `omitempty` and only ever true, so an absent one means unchanged; clearing it would repaint the
    // card as a plain success.
    const card = liveCard("st-dec-latch");
    applyToolCallUpdate(
      card,
      frame("st-dec-latch", { status: "completed", declined: true, output: "plan rejected\n" }),
      "c1",
    );
    applyToolCallUpdate(card, frame("st-dec-latch", { status: "completed" }), "c1");
    expect(card.dataset["declined"]).toBe("1");
    expect(card.dataset["outcome"]).toBe("declined");
    card.remove();
  });
});
