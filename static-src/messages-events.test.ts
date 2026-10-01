// ---------------------------------------------------------------------------
// The boundary rows five ENTRY kinds draw inside a turn's body.
//
// `buildEvent` is total over `EventEntry` by TYPE, so exhaustiveness is the type
// check's job and not a case here (a sixth event kind leaves the function's end
// reachable and fails `typecheck`). What is left for a test is what each arm PUTS
// ON SCREEN: which class carries the row's meaning, which payload field becomes its
// words, and which of the five discloses a body rather than stating a line.
//
// No mocks: this module reaches `chevron.ts`, `markdown.ts` and the mode catalog
// (`roles.ts`), so the five the old suite stood in front of a `Message`-shaped graph
// are retired with it.
// ---------------------------------------------------------------------------

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

/** The row's WORDS, read apart from its glyph: every wording case below asserts the
 *  whole label, and folding the icon into the expectation would redden all of them
 *  for a glyph change that says nothing about what the row says. */
function labelOf(node: HTMLElement): string {
  return node.querySelector(".boundary-label")?.textContent ?? "";
}

describe("the row a kind draws", () => {
  // A face is what the stylesheet keys a row's meaning on. The two SWITCH kinds share
  // one deliberately — a mode switch and a model switch are one shape of event and a
  // reader learns one row — and every other kind carries its own.
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

  // `from === to` is the whole discriminator for the effort-only trigger, and
  // "Switched to opus-5" beside a model that did not move would read as a switch.
  it("names the tier rather than the model when only the effort changed", () => {
    const node = buildEvent(
      event("model_switched", { from: "opus-5", to: "opus-5", effort: "high" }),
    );
    expect(labelOf(node)).toBe("Reasoning effort: high");
  });

  // `effort` is optional on the wire so every chat file written before it existed
  // still decodes, and such an entry renders the row it always rendered.
  it("names the model alone when the entry carries no tier", () => {
    const node = buildEvent(event("model_switched", { from: "sonnet-5", to: "opus-5" }));
    expect(labelOf(node)).toBe("Switched to opus-5");
  });

  // Neither trigger produces this, so the row falls back to the fact it has rather
  // than naming a tier it was not given.
  it("names the model when neither the model nor a tier moved", () => {
    const node = buildEvent(event("model_switched", { from: "opus-5", to: "opus-5" }));
    expect(labelOf(node)).toBe("Switched to opus-5");
  });

  // Through `effortLabel`, so the row records the spelling the picker shows rather
  // than the wire id.
  it("spells the tier the way the effort vocabulary does", () => {
    const node = buildEvent(
      event("model_switched", { from: "sonnet-5", to: "opus-5", effort: "xhigh" }),
    );
    expect(labelOf(node)).toBe("Switched to opus-5 (x-high)");
  });

  // An empty `to` is not a switch that lost its target: KAS reports a context reset
  // through the same entry, and a row reading "Switched to " would be a sentence with
  // its subject missing. The tier a reopened session runs at is not what it is about.
  it("reads an empty target as a context reset rather than a switch, with no tier", () => {
    const node = buildEvent(event("model_switched", { from: "sonnet-5", to: "", effort: "high" }));
    expect(labelOf(node)).toBe("Context reset");
  });
});

describe("mode_switched", () => {
  const switched = (from: string, to: string, source: "user" | "agent"): HTMLElement =>
    buildEvent(event("mode_switched", { from, to, source }));

  // BOTH endpoints, because a mode id names a workflow rather than a version: "Mode:
  // Spec" would not say what changed. Through the catalog, so the row names what the
  // composer's mode pill names — `vibe` is "Default" in both places.
  it("names the mode the turn left and the one it continued in", () => {
    expect(labelOf(switched("spec", "vibe", "user"))).toBe("Mode: Spec \u2192 Default");
  });

  // KAS flips the mode itself at a turn's end (Plan to Execute), and a reader who did
  // not touch the pill is owed that.
  it("says so when the agent made the switch rather than the reader", () => {
    expect(labelOf(switched("plan", "vibe", "agent"))).toBe(
      "Mode: Plan \u2192 Default, switched by the agent",
    );
  });

  // An empty `from` is a chat whose mode was never recorded, not a switch that lost its
  // origin, and a row opening with an arrow would read as a sentence missing its subject.
  it("names the destination alone when the previous mode was never recorded", () => {
    expect(labelOf(switched("", "spec", "user"))).toBe("Mode: Spec");
  });

  // A mode the catalog does not offer keeps its id rather than rendering as nothing:
  // `labelForMode` ends its chain at the id, shaped by `displayModeName`.
  it("falls back to the id for a mode the catalog does not know", () => {
    expect(labelOf(switched("spec", "semantic_reviewer", "user"))).toBe(
      "Mode: Spec \u2192 Semantic Reviewer",
    );
  });
});

// Decision 3: a revert is VISIBLE. The row is the compaction divider's sibling — the
// simple form, because a revert has no summary to disclose — and its words come from the
// cause rather than from the arm, so a second cause cannot render the first one's wording.
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
    // The label is the whole row: the chevron says it opens, so a trailing
    // "summary" word restated it.
    expect(head?.textContent ?? "").not.toContain("summary");
  });

  // The row sets `list-style: none`, so this glyph is the only thing on screen saying
  // it opens — and it LEADS, because it discloses (chevron.ts).
  it("carries the app's disclosure chevron, leading its head", () => {
    const node = compacted("A summary.");
    expect(node.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    const head = node.querySelector("summary.compaction-head");
    expect(head?.firstElementChild?.classList.contains("disclosure-chevron")).toBe(true);
  });

  // A summary runs to 16 KB of markdown and `::details-content` skips layout and paint
  // but not CONSTRUCTION, so a body built at mount is paid for by every reader who
  // never opens it.
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
  // The reason is the COMPACTION's, not the turn's: `turnFailureText` owns the prose
  // account of a turn that ended badly, and this row is the only durable record of a
  // compaction that did not happen, so it states its own reason rather than dropping it.
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
  // KAS blocked the write upstream in enforce mode, so nothing ran and nothing was
  // written; the payload's properties are the only account of WHY, and the transient
  // banner that also reports it does not survive a reload.
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
