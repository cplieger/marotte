// Event rendering: the boundary rows six entry kinds draw inside a turn's body.
// `messages.ts` owns this markup and hands it to the dispatcher as `makeEvent`,
// so the block renderer declares no event vocabulary of its own.

import type { KindedEntry, TurnRevertCause } from "./types.js";
import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { effortLabel } from "./effort.js";
import { renderMarkdownInto } from "./markdown.js";
import { labelForMode } from "./roles.js";

type EventEntryKind =
  | "compaction"
  | "compaction_failed"
  | "safety_blocked"
  | "model_switched"
  | "mode_switched"
  | "turn_revert";

/** The six entry kinds that render as a boundary row. Every other kind renders as
 *  itself, outside the body, or not at all — `placeEntry` is what routes them, and this
 *  union is the one it hands over.
 *
 *  DISTRIBUTED over the six, like `AnyEntry` over the seventeen: `KindedEntry` of a union
 *  kind is ONE object whose payload is the union of the six payloads, which no switch can
 *  narrow — the correlated-pair loss, and the arms read a field off the wrong payload. */
export type EventEntry = { [K in EventEntryKind]: KindedEntry<K> }[EventEntryKind];

type BoundaryKind = "switched" | "compacted" | "failed" | "blocked" | "rewound";

/** The words the revert's boundary says, TOTAL over the cause by type: a second
 *  `TurnRevertCause` cannot compile without wording of its own, which is what stops one
 *  cause silently rendering another's sentence. */
const REVERT_LABELS: Record<TurnRevertCause, string> = { rewind: "Rewound to here" };

/** The row one event entry draws. TOTAL over `EventEntry` by type: a seventh event kind
 *  leaves the function's end reachable and fails the type check, which is what the
 *  exhaustive strategy map this replaced was for. */
export function buildEvent(e: EventEntry): HTMLElement {
  switch (e.kind) {
    case "model_switched": {
      return boundary("switched", "\u21bb", switchedLabel(e.payload));
    }
    case "mode_switched": {
      // The model banner's own component and CSS: one boundary row for both switches,
      // so a reader learns one shape whichever thing moved.
      return boundary("switched", "\u21bb", modeSwitchedLabel(e.payload));
    }
    case "turn_revert": {
      // The simple form, never `compactionBreak`: a revert has no summary to disclose,
      // and the record's own fields say WHICH turns went rather than anything a reader
      // asked to see. The cut itself is the whole row.
      return boundary("rewound", "\u21ba", REVERT_LABELS[e.payload.cause]);
    }
    case "compaction": {
      const summary = e.payload.summary;
      const label = "Conversation compacted";
      // The one kind whose payload is a body rather than a reason.
      return summary === ""
        ? boundary("compacted", "\u273b", label)
        : compactionBreak("\u273b", label, summary);
    }
    case "compaction_failed": {
      // The reason is the COMPACTION's, not the turn's: `turnFailureText` owns the prose
      // account of a turn that ended badly and this row never repeats it.
      const reason = e.payload.reason;
      return boundary(
        "failed",
        "\u26a0",
        reason === "" ? "Compaction failed" : `Compaction failed: ${reason}`,
      );
    }
    case "safety_blocked": {
      // KAS blocked an infra-as-code write or shell call upstream in enforce mode: the tool
      // never ran and nothing was written. The durable record of the transient status the
      // `handlers/safety.ts` banner shows; the payload names the properties violated, and
      // the block is chat-scoped because KAS's toolId there is a tool NAME.
      const props = e.payload.properties.join(", ");
      return boundary(
        "blocked",
        "\u{1F6E1}",
        props === ""
          ? "Infrastructure Safety blocked a change"
          : `Infrastructure Safety blocked: ${props}`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

/** The label a `model_switched` entry draws. ONE entry kind, TWO triggers: a model
 *  switch carries `from !== to`, and an effort-only change carries `from === to`,
 *  which is the whole discriminator — see EntryModelSwitched.
 *
 *  An empty `to` is the context reset and takes no tier: the effort a session
 *  reopens on is not what that row is about. */
function switchedLabel(p: {
  readonly to: string;
  readonly from: string;
  readonly effort?: string;
}): string {
  if (p.to === "") {
    return "Context reset";
  }
  // `effortLabel` and never the live catalog: a transcript row records the tier
  // that was picked then, and the vocabulary may have moved since.
  const tier = p.effort === undefined || p.effort === "" ? "" : effortLabel({ id: p.effort });
  if (tier === "") {
    return `Switched to ${p.to}`;
  }
  // "Switched to opus-5" beside a model name that did not move would read as a
  // model switch, so the effort-only row names the tier instead.
  return p.from === p.to ? `Reasoning effort: ${tier}` : `Switched to ${p.to} (${tier})`;
}

/** The label a `mode_switched` entry draws. BOTH endpoints, because a mode id names a
 *  workflow rather than a version and "Mode: Spec" would not say what changed. An empty
 *  `from` is a chat whose mode was never recorded, and names the destination alone.
 *
 *  `labelForMode` and so the live catalog, per ADDENDUM 16 rule 1: a workspace agent is
 *  named by its front matter, which no payload carries, and the chain ends at the id
 *  itself for a mode the catalog does not know. That makes the row name what the
 *  composer's mode pill named — `vibe` reads "Default" in both places, never "Vibe". */
function modeSwitchedLabel(p: {
  readonly from: string;
  readonly to: string;
  readonly source: string;
}): string {
  const to = labelForMode(p.to);
  const row = p.from === "" ? `Mode: ${to}` : `Mode: ${labelForMode(p.from)} \u2192 ${to}`;
  // KAS flips the mode itself at a turn's end (Plan to Execute), and a reader who did
  // not touch the pill is owed that.
  return p.source === "agent" ? `${row}, switched by the agent` : row;
}

function boundary(kind: BoundaryKind, icon: string, label: string): HTMLElement {
  const node = el("div", { className: `boundary boundary-${kind}` });
  node.appendChild(el("span", { className: "boundary-icon" }, icon));
  node.appendChild(el("span", { className: "boundary-label" }, label));
  return node;
}

/** The compaction break: two dashed rules with the marker and its collapsible
 *  summary between them.
 *
 *  The body renders on FIRST OPEN — a summary runs to 16 KB of markdown, and
 *  `::details-content` skips layout and paint but not CONSTRUCTION. */
function compactionBreak(icon: string, label: string, summary: string): HTMLElement {
  const root = el("details", { className: "compaction" }) as HTMLDetailsElement;
  // `.message assistant` is the app's markdown-prose skin (editor-markdown.ts).
  const body = el("div", { className: "compaction-body message assistant" });
  root.append(
    el(
      "summary",
      { className: "compaction-head" },
      // LEADS the row, because it DISCLOSES. One rule across the transcript: a
      // chevron that opens a region below it comes first and rotates, a chevron
      // that navigates sits at the trailing edge and does not — see chevron.ts.
      chevronEl(),
      el("span", { className: "compaction-icon" }, icon),
      el("span", { className: "compaction-label" }, label),
    ),
    body,
  );
  let rendered = false;
  root.addEventListener("toggle", () => {
    if (!root.open || rendered) {
      return;
    }
    rendered = true;
    renderMarkdownInto(body, summary);
  });
  return root;
}
