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

    expect(group.classList.contains("tool-group-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
    group.remove();
  });

  it("folds with a failed member like any other, the failure staying in its header", () => {
    const group = buildToolGroupShell();
    const a = liveCard("st-fold-fail-a");
    const b = liveCard("st-fold-fail-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    applyToolCallUpdate(a, frame("st-fold-fail-a", { status: "completed" }), "c1");
    applyToolCallUpdate(
      b,
      frame("st-fold-fail-b", { status: "failed", output: "exit status 1\n" }),
      "c1",
    );
    autoCollapseGroup(group);

    expect(group.classList.contains("tool-group-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
    expect(group.querySelector(".tool-group-count")?.textContent).toContain("1 failed");
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

describe("a call the user rejected at its prompt", () => {
  const rejected = { type: "tool_approval", outcome: "selected", choice: "reject_once" } as const;

  it("settles declined, not failed", () => {
    const card = liveCard("st-rej-paint");
    applyToolCallUpdate(card, frame("st-rej-paint", { interaction: rejected }), "c1");
    expect(card.dataset["outcome"]).toBe("running");
    applyToolCallUpdate(
      card,
      frame("st-rej-paint", { status: "failed", interaction: rejected, output: "rejected\n" }),
      "c1",
    );
    expect(card.dataset["outcome"]).toBe("declined");
    expect(card.querySelector(".tool-icon.is-declined")).not.toBeNull();
    expect(card.getAttribute("aria-label")).toMatch(/declined$/);
    card.remove();
  });

  it("is not counted failed in its group's header", () => {
    const group = buildToolGroupShell();
    const a = liveCard("st-rej-group-a");
    const b = liveCard("st-rej-group-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    applyToolCallUpdate(a, frame("st-rej-group-a", { status: "completed" }), "c1");
    applyToolCallUpdate(
      b,
      frame("st-rej-group-b", { status: "failed", interaction: rejected }),
      "c1",
    );
    autoCollapseGroup(group);

    const count = group.querySelector(".tool-group-count")?.textContent ?? "";
    expect(count).not.toContain("failed");
    expect(count).toContain("1 declined");
    group.remove();
  });
});

// A shell rule's block rides the terminal frame, never the create, so the mounted card adopts it there.
describe("a denial on the terminal frame", () => {
  const ruleDenial = {
    capability: "shell",
    resource: "rm -rf x",
    scope: "user",
    source: "permissions.yaml",
    rule: { capability: "shell", effect: "deny", match: ["rm *"] },
  };

  it("paints the card denied", () => {
    const card = liveCard("st-deny-paint");
    applyToolCallUpdate(
      card,
      frame("st-deny-paint", { status: "failed", denial: ruleDenial, output: "Blocked\n" }),
      "c1",
    );
    expect(card.dataset["outcome"]).toBe("denied");
    expect(card.querySelector(".tool-icon.is-denied")).not.toBeNull();
    expect(card.getAttribute("aria-label")).toMatch(/blocked by security policy$/);
    expect(card.dataset["disclosable"]).toBe("1");
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    expect(card.querySelectorAll(".tool-denial")).toHaveLength(1);
    card.remove();
  });

  it("prepends the denial to a region the reader already opened", () => {
    const card = liveCard("st-deny-open");
    applyToolCallUpdate(card, frame("st-deny-open", { output: "working\n" }), "c1");
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    applyToolCallUpdate(
      card,
      frame("st-deny-open", { status: "failed", denial: ruleDenial }),
      "c1",
    );
    const details = card.querySelector(".tool-details");
    expect(details?.querySelectorAll(".tool-denial")).toHaveLength(1);
    expect(details?.firstElementChild?.classList.contains("tool-denial")).toBe(true);
    card.remove();
  });

  it("adds the denial block once when the same settled value arrives twice", () => {
    const card = liveCard("st-deny-twice");
    applyToolCallUpdate(card, frame("st-deny-twice", { output: "working\n" }), "c1");
    card.querySelector<HTMLElement>(".tool-disclosure")?.click();
    const settled = frame("st-deny-twice", { status: "failed", denial: ruleDenial, output: "x\n" });
    applyToolCallUpdate(card, settled, "c1");
    applyToolCallUpdate(card, settled, "c1");
    expect(card.querySelectorAll(".tool-denial")).toHaveLength(1);
    card.remove();
  });

  it("is not counted failed in its group's header", () => {
    const group = buildToolGroupShell();
    const a = liveCard("st-deny-group-a");
    const b = liveCard("st-deny-group-b");
    groupBody(group).append(a, b);
    document.body.appendChild(group);
    refreshGroupHeader(group);

    applyToolCallUpdate(a, frame("st-deny-group-a", { status: "completed" }), "c1");
    applyToolCallUpdate(
      b,
      frame("st-deny-group-b", { status: "failed", denial: ruleDenial }),
      "c1",
    );
    autoCollapseGroup(group);

    expect(group.querySelector(".tool-group-count")?.textContent).not.toContain("failed");
    group.remove();
  });
});

describe("an append whose text lands after the create", () => {
  it("draws its diff once an update brings the text", () => {
    const create: ToolCall = {
      id: "append-1",
      title: "Append to notes.txt",
      kind: "edit",
      tool_name: "fs_append",
      status: "in_progress",
      input: { path: "notes.txt" },
      ts: 0,
    };
    const card = buildToolCard({ ...create, live: true, input: { path: "notes.txt" } });
    document.body.appendChild(card);
    expect(card.querySelector(".tool-diff-preview")).toBeNull();

    applyToolCallUpdate(
      card,
      { ...create, status: "pending", input: { path: "notes.txt", text: "one more line\n" } },
      "c1",
    );

    expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    card.remove();
  });
});
