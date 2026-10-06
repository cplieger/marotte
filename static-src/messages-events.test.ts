// What each event arm puts on screen: the class carrying the row's meaning, the field that becomes its words, and
// which kind discloses a body. Exhaustiveness is the type check's job.

import { describe, it, expect, vi } from "vitest";
import { buildEvent, type EventEntry } from "./messages-events.js";
import type { EntryKind, KindedEntry } from "./types.js";

/** One event entry of `kind`, sealed at `seq` 1 of its turn. */
function event<K extends EntryKind & EventEntry["kind"]>(
  kind: K,
  payload: KindedEntry<K>["payload"],
): EventEntry {
  return {
    id: `t1-e1-${kind}`,
    turn: "t1",
    kind,
    seq: 1,
    ts: 2,
    payload,
  } as EventEntry;
}

function textOf(node: HTMLElement): string {
  return node.textContent ?? "";
}

/** The row's words apart from its glyph, so a glyph change does not redden every wording case. */
function labelOf(node: HTMLElement): string {
  return node.querySelector(".boundary-label")?.textContent ?? "";
}

describe("the row a kind draws", () => {
  // The two switch kinds share one face deliberately; every other kind carries its own.
  it("gives each kind its boundary face, the two switches sharing one", () => {
    const faces: readonly (readonly [EventEntry, string])[] = [
      [event("model_switched", { from: "a", to: "b" }), "boundary-switched"],
      [event("mode_switched", { from: "spec", to: "vibe", source: "user" }), "boundary-switched"],
      [event("compaction", { summary: "" }), "boundary-compacted"],
      [event("compaction_failed", { reason: "" }), "boundary-failed"],
      [event("safety_blocked", { properties: [] }), "boundary-blocked"],
    ];
    for (const [entry, face] of faces) {
      expect(buildEvent(entry).className, entry.kind).toContain(face);
    }
  });
});

describe("model_switched", () => {
  it("names the model the turn continued on and the tier it continued at", () => {
    const node = buildEvent(
      event("model_switched", { from: "sonnet-5", to: "opus-5", effort: "high" }),
    );
    expect(labelOf(node)).toBe("Switched to opus-5 (high)");
  });

  // `from === to` is the effort-only discriminator.
  it("names the tier rather than the model when only the effort changed", () => {
    const node = buildEvent(
      event("model_switched", { from: "opus-5", to: "opus-5", effort: "high" }),
    );
    expect(labelOf(node)).toBe("Reasoning effort: high");
  });

  // `effort` is optional on the wire so older chat files still decode and render the row they always did.
  it("names the reason on a switch KAS made by itself", () => {
    const node = buildEvent(
      event("model_switched", { from: "opus-9", to: "sonnet-5", reason: "unavailable" }),
    );
    expect(labelOf(node)).toBe("Switched to sonnet-5 · not available for this account");
  });

  it("names the model alone when the entry carries no tier", () => {
    const node = buildEvent(event("model_switched", { from: "sonnet-5", to: "opus-5" }));
    expect(labelOf(node)).toBe("Switched to opus-5");
  });

  it("names the model when neither the model nor a tier moved", () => {
    const node = buildEvent(event("model_switched", { from: "opus-5", to: "opus-5" }));
    expect(labelOf(node)).toBe("Switched to opus-5");
  });

  it("spells the tier the way the effort vocabulary does", () => {
    const node = buildEvent(
      event("model_switched", { from: "sonnet-5", to: "opus-5", effort: "xhigh" }),
    );
    expect(labelOf(node)).toBe("Switched to opus-5 (x-high)");
  });

  // KAS reports a context reset through the same entry with an empty `to`.
  it("reads an empty target as a context reset rather than a switch, with no tier", () => {
    const node = buildEvent(event("model_switched", { from: "sonnet-5", to: "", effort: "high" }));
    expect(labelOf(node)).toBe("Context reset");
  });
});

describe("mode_switched", () => {
  const switched = (from: string, to: string, source: "user" | "agent"): HTMLElement =>
    buildEvent(event("mode_switched", { from, to, source }));

  it("names the mode the turn left and the one it continued in", () => {
    expect(labelOf(switched("spec", "vibe", "user"))).toBe("Mode: Spec \u2192 Default");
  });

  it("says so when the agent made the switch rather than the reader", () => {
    expect(labelOf(switched("plan", "vibe", "agent"))).toBe(
      "Mode: Plan \u2192 Default, switched by the agent",
    );
  });

  it("names the destination alone when the previous mode was never recorded", () => {
    expect(labelOf(switched("", "spec", "user"))).toBe("Mode: Spec");
  });

  // `labelForMode` ends its chain at the id.
  it("falls back to the id for a mode the catalog does not know", () => {
    expect(labelOf(switched("spec", "semantic_reviewer", "user"))).toBe(
      "Mode: Spec \u2192 Semantic Reviewer",
    );
  });
});

// The words come from the cause, not the arm, so a second cause cannot render the first one's wording.
describe("turn_revert", () => {
  it("draws the rewind boundary and words it from the cause", () => {
    const node = buildEvent(
      event("turn_revert", {
        from: "t-2",
        from_n: 2,
        through: "t-2",
        kas_message_id: "kas-2",
        cause: "rewind",
      }),
    );

    expect(node.className).toContain("boundary-rewound");
    expect(labelOf(node)).toBe("Rewound to here");
    expect(node.querySelector("details")).toBeNull();
  });
});

describe("compaction", () => {
  const compacted = (summary: string): HTMLElement => buildEvent(event("compaction", { summary }));

  it("makes the marker itself the summary's trigger", () => {
    const node = compacted("The user asked to refactor auth; we split it into three files.");
    expect(node.tagName).toBe("DETAILS");
    expect(node.querySelector(".boundary")).toBeNull();
    const head = node.querySelector("summary.compaction-head");
    expect(head?.textContent ?? "").toContain("Conversation compacted");
    // The chevron says it opens, so a trailing "summary" word would restate it.
    expect(head?.textContent ?? "").not.toContain("summary");
  });

  // `list-style: none` leaves this glyph as the only sign the row opens.
  it("carries the app's disclosure chevron, leading its head", () => {
    const node = compacted("A summary.");
    expect(node.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    const head = node.querySelector("summary.compaction-head");
    expect(head?.firstElementChild?.classList.contains("disclosure-chevron")).toBe(true);
  });

  it("renders the summary as markdown on first open, and not before", async () => {
    const node = compacted("## Goal\n\nSplit `auth` into three files.\n");
    const body = node.querySelector(".compaction-body");
    expect(body).not.toBeNull();
    expect(body?.textContent).toBe("");

    (node as HTMLDetailsElement).open = true;
    // `toggle` is queued, not dispatched synchronously.
    await vi.waitFor(() => {
      expect(body?.querySelector("h2")?.textContent).toBe("Goal");
    });
    expect(body?.querySelector("code")?.textContent).toBe("auth");
    expect(body?.textContent ?? "").not.toContain("##");
    expect(body?.textContent ?? "").not.toContain("`");
  });

  it("renders just the marker, with no disclosure, when there is no summary", () => {
    const node = compacted("");
    expect(node.tagName).not.toBe("DETAILS");
    expect(node.className).toContain("boundary-compacted");
    expect(node.querySelector("details")).toBeNull();
    expect(textOf(node)).toContain("Conversation compacted");
  });
});

describe("compaction_failed", () => {
  it("states the compaction's own reason", () => {
    const node = buildEvent(event("compaction_failed", { reason: "the summary was refused" }));
    expect(textOf(node)).toContain("Compaction failed: the summary was refused");
  });

  it("falls back to naming the failure when no reason arrived", () => {
    const node = buildEvent(event("compaction_failed", { reason: "" }));
    expect(textOf(node)).toContain("Compaction failed");
    expect(textOf(node)).not.toContain(":");
  });
});

describe("safety_blocked", () => {
  // The payload's properties are the only account of why that survives a reload.
  it("carries the violated properties", () => {
    const node = buildEvent(
      event("safety_blocked", {
        properties: ["no public S3 buckets", "encrypt at rest"],
      }),
    );
    expect(textOf(node)).toContain("Infrastructure Safety blocked");
    expect(textOf(node)).toContain("no public S3 buckets, encrypt at rest");
  });

  it("falls back to a default label when the block names no property", () => {
    const node = buildEvent(event("safety_blocked", { properties: [] }));
    expect(textOf(node)).toContain("Infrastructure Safety blocked a change");
  });
});
