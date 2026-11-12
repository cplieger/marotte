// What each event arm puts on screen: the class carrying the row's meaning, the field that becomes its words, and
// which kind discloses a body. Exhaustiveness is the type check's job.

import { describe, it, expect, vi } from "vitest";
import { buildEvent, type EventEntry } from "./messages-events.js";
import type { EntryKind, KindedEntry } from "./types.js";
import { glyphMarkup, registryMarkup } from "./__test-helpers__/glyph.js";
import { ICON_ALERT, ICON_COMPACT, ICON_REWIND, ICON_SHIELD, ICON_TOOL_SWITCH } from "./icons.js";

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
      [event("model_routed", { message: "" }), "boundary-switched"],
      [event("model_fallback", { from: "a", to: "b", trigger: "refusal" }), "boundary-switched"],
      [event("compaction", { summary: "" }), "boundary-compacted"],
      [event("compaction_failed", { reason: "" }), "boundary-failed"],
      [event("safety_blocked", { properties: [] }), "boundary-blocked"],
      [event("context_reset", { cause: "load_failed" }), "boundary-reset"],
    ];
    for (const [entry, face] of faces) {
      expect(buildEvent(entry).className, entry.kind).toContain(face);
    }
  });

  it("draws each kind's registry glyph, hidden from assistive tech", () => {
    const glyphs: readonly (readonly [string, EventEntry, string])[] = [
      ["a model switch", event("model_switched", { from: "a", to: "b" }), ICON_TOOL_SWITCH],
      [
        "a mode switch",
        event("mode_switched", { from: "spec", to: "vibe", source: "user" }),
        ICON_TOOL_SWITCH,
      ],
      ["a routed model", event("model_routed", { message: "" }), ICON_TOOL_SWITCH],
      [
        "a fallback",
        event("model_fallback", { from: "a", to: "b", trigger: "refusal" }),
        ICON_TOOL_SWITCH,
      ],
      [
        "a rewind",
        event("turn_revert", {
          from: "t-2",
          from_n: 2,
          through: "t-2",
          kas_message_id: "kas-2",
          cause: "rewind",
        }),
        ICON_REWIND,
      ],
      ["a compaction", event("compaction", { summary: "" }), ICON_COMPACT],
      [
        "a compaction with a summary",
        event("compaction", { summary: "Kept the plan." }),
        ICON_COMPACT,
      ],
      ["a failed compaction", event("compaction_failed", { reason: "" }), ICON_ALERT],
      ["a safety block", event("safety_blocked", { properties: [] }), ICON_SHIELD],
    ];
    for (const [name, entry, glyph] of glyphs) {
      const icon = buildEvent(entry).querySelector(".boundary-icon, .compaction-icon");
      expect(icon?.getAttribute("aria-hidden"), name).toBe("true");
      expect(icon?.childElementCount, name).toBe(1);
      expect(glyphMarkup(icon?.firstElementChild), name).toBe(registryMarkup(glyph));
    }
  });
});

describe("model_routed", () => {
  it("shows KAS's notice as the row's words", () => {
    const node = buildEvent(
      event("model_routed", { message: "Found a better model for this task. Switched to it." }),
    );
    expect(labelOf(node)).toBe("Found a better model for this task. Switched to it.");
  });

  it("says what happened when KAS sent no notice text", () => {
    expect(labelOf(buildEvent(event("model_routed", { message: "" })))).toBe(
      "Auto moved to a stronger model",
    );
  });
});

describe("model_fallback", () => {
  const label = (payload: KindedEntry<"model_fallback">["payload"]): string =>
    labelOf(buildEvent(event("model_fallback", payload)));

  it.each([
    ["refusal", "model-a-5 refused this request, retried on model-b-5"],
    ["availability", "model-a-5 was unavailable, retried on model-b-5"],
    ["latency", "model-a-5 stopped for an unknown reason, retried on model-b-5"],
  ])("names the %s cause and the model the turn moved to", (trigger, want) => {
    expect(label({ from: "model-a-5", to: "model-b-5", scope: "turn", trigger })).toBe(want);
  });

  it("says a session-wide fallback's model now serves the session, in the same row", () => {
    const node = buildEvent(
      event("model_fallback", {
        from: "model-a-5",
        to: "model-b-5",
        scope: "session",
        trigger: "refusal",
      }),
    );
    expect(node.className).toContain("boundary-switched");
    expect(labelOf(node)).toBe(
      "model-a-5 refused this request, retried on model-b-5, which now serves this session",
    );
  });

  it("words KAS's same-model retry as a retry, naming the model once", () => {
    expect(label({ from: "auto", to: "auto", scope: "turn", trigger: "refusal" })).toBe(
      "auto refused this request, retried",
    );
  });

  it.each([
    ["noServedTarget", "none of its fallback models is offered to this account"],
    ["chainExhausted", "every fallback model was already tried"],
    ["unsafeToReissue", "the turn had already run tools, so a retry could repeat them"],
    ["raceLost", "the model changed while Kiro was switching"],
  ])("words the %s decline", (reason, why) => {
    expect(label({ from: "model-a-5", trigger: "availability", reason })).toBe(
      `model-a-5 was unavailable, no fallback model available \u00b7 ${why}`,
    );
  });

  it("names a decline's cause from its trigger", () => {
    expect(label({ from: "model-a-5", trigger: "refusal", reason: "chainExhausted" })).toBe(
      "model-a-5 refused this request, no fallback model available \u00b7 every fallback model was already tried",
    );
  });

  // KAS's decline log carries the model it would have tried; the notice decides on `reason` alone.
  it("reads a reason as a decline even when a model is named", () => {
    expect(
      label({
        from: "model-a-5",
        to: "model-b-5",
        scope: "session",
        trigger: "availability",
        reason: "raceLost",
      }),
    ).toBe(
      "model-a-5 was unavailable, no fallback model available \u00b7 the model changed while Kiro was switching",
    );
  });

  it("shows a decline code it does not know as sent, and an inherited name as a code", () => {
    expect(label({ from: "model-a-5", trigger: "availability", reason: "quotaGone" })).toBe(
      "model-a-5 was unavailable, no fallback model available \u00b7 quotaGone",
    );
    expect(label({ from: "model-a-5", trigger: "availability", reason: "constructor" })).toBe(
      "model-a-5 was unavailable, no fallback model available \u00b7 constructor",
    );
  });

  it("names the cause alone with no target", () => {
    expect(label({ from: "model-a-5", trigger: "refusal" })).toBe("model-a-5 refused this request");
  });
});

describe("model_switched", () => {
  it("names the model the turn continued on and the tier it continued at", () => {
    const node = buildEvent(
      event("model_switched", { from: "model-b-5", to: "model-a-5", effort: "high" }),
    );
    expect(labelOf(node)).toBe("Switched to model-a-5 (high)");
  });

  // `from === to` is the effort-only discriminator.
  it("names the tier rather than the model when only the effort changed", () => {
    const node = buildEvent(
      event("model_switched", { from: "model-a-5", to: "model-a-5", effort: "high" }),
    );
    expect(labelOf(node)).toBe("Reasoning effort: high");
  });

  it("names the unavailable model, the one now running and why", () => {
    const node = buildEvent(
      event("model_switched", {
        from: "model-a-9",
        to: "model-b-5",
        effort: "high",
        reason: "unavailable",
      }),
    );
    expect(labelOf(node)).toBe(
      "Switched from model-a-9 to model-b-5 · not available for this account",
    );
  });

  it("says so when the new model does not offer the tier the chat wanted", () => {
    const node = buildEvent(
      event("model_switched", {
        from: "model-a-5",
        to: "model-e-5.6",
        effort: "high",
        unoffered: "max",
      }),
    );
    expect(labelOf(node)).toBe("Switched to model-e-5.6 (high; max is not offered)");
  });

  it("names the unoffered tier alone when the new model runs at none", () => {
    const node = buildEvent(
      event("model_switched", { from: "model-a-5", to: "model-e-5.6", unoffered: "xhigh" }),
    );
    expect(labelOf(node)).toBe("Switched to model-e-5.6 (x-high is not offered)");
  });

  it("names the model alone when the entry carries no tier", () => {
    const node = buildEvent(event("model_switched", { from: "model-b-5", to: "model-a-5" }));
    expect(labelOf(node)).toBe("Switched to model-a-5");
  });

  it("names the model when neither the model nor a tier moved", () => {
    const node = buildEvent(event("model_switched", { from: "model-a-5", to: "model-a-5" }));
    expect(labelOf(node)).toBe("Switched to model-a-5");
  });

  it("spells the tier the way the effort vocabulary does", () => {
    const node = buildEvent(
      event("model_switched", { from: "model-b-5", to: "model-a-5", effort: "xhigh" }),
    );
    expect(labelOf(node)).toBe("Switched to model-a-5 (x-high)");
  });

  // A reset has its own kind, so an empty target is never read as one.
  it("never words an empty target as a context reset", () => {
    const node = buildEvent(event("model_switched", { from: "model-b-5", to: "" }));
    expect(labelOf(node)).not.toContain("Context reset");
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

describe("context_reset", () => {
  it("draws one reset boundary per cause, each in its own words", () => {
    const causes = ["session_missing", "load_failed", "empty_retry"] as const;
    const labels = causes.map((cause) => {
      const node = buildEvent(event("context_reset", { cause }));
      expect(node.className, cause).toContain("boundary-reset");
      expect(labelOf(node), cause).toMatch(/^Context reset · /);
      return labelOf(node);
    });
    expect(new Set(labels).size).toBe(causes.length);
  });

  it("says the agent had lost the chat when its session record was gone", () => {
    expect(labelOf(buildEvent(event("context_reset", { cause: "session_missing" })))).toBe(
      "Context reset · the agent had lost this chat's earlier turns, so a new session started",
    );
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
    // The summary sits under the chat's title, so `#` is an h2 and `##` an h3.
    await vi.waitFor(() => {
      expect(body?.querySelector("h3")?.textContent).toBe("Goal");
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
