// The boundary rows six entry kinds draw inside a turn's body. `messages.ts` hands this to the dispatcher as
// `makeEvent`, so the block renderer declares no event vocabulary.

import type { KindedEntry, ModelSwitchReason, TurnRevertCause } from "./types.js";
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

/**
 * The six entry kinds that render as a boundary row; `placeEntry` routes every other kind. Distributed over the
 * six: `KindedEntry` of a union kind is one object with a union payload, which no switch can narrow.
 */
export type EventEntry = { [K in EventEntryKind]: KindedEntry<K> }[EventEntryKind];

type BoundaryKind = "switched" | "compacted" | "failed" | "blocked" | "rewound";

/** Total over the cause by type, so a second `TurnRevertCause` cannot render another cause's sentence. */
const REVERT_LABELS: Record<TurnRevertCause, string> = { rewind: "Rewound to here" };

/** The row one event entry draws, total over `EventEntry` by type: a new kind fails the type check. */
export function buildEvent(e: EventEntry): HTMLElement {
  switch (e.kind) {
    case "model_switched": {
      return boundary("switched", "\u21bb", switchedLabel(e.payload));
    }
    case "mode_switched": {
      // One boundary row for both switches, so a reader learns one shape whichever thing moved.
      return boundary("switched", "\u21bb", modeSwitchedLabel(e.payload));
    }
    case "turn_revert": {
      // The simple form, never `compactionBreak`: a revert has no summary to disclose.
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
      // The compaction's own reason, never the turn's: `turnFailureText` owns that account and this row never repeats it.
      const reason = e.payload.reason;
      return boundary(
        "failed",
        "\u26a0",
        reason === "" ? "Compaction failed" : `Compaction failed: ${reason}`,
      );
    }
    case "safety_blocked": {
      // KAS blocked the write or shell call upstream in enforce mode, so nothing ran; this is the durable record of the
      // safety banner. Chat-scoped because KAS's toolId there is a tool name.
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

/**
 * `model_switched` has two triggers: a model switch (`from !== to`) and an effort-only change (`from === to`). An
 * empty `to` is a context reset and takes no tier.
 */
function switchedLabel(p: {
  readonly to: string;
  readonly from: string;
  readonly effort?: string;
  readonly reason?: ModelSwitchReason;
}): string {
  if (p.to === "") {
    return "Context reset";
  }
  const label = switchedBase(p);
  return p.reason === undefined ? label : `${label} · ${SWITCH_REASONS[p.reason]}`;
}

/** Why KAS moved the chat, TOTAL over the reason by type. */
const SWITCH_REASONS: Record<ModelSwitchReason, string> = {
  unavailable: "not available for this account",
};

function switchedBase(p: {
  readonly to: string;
  readonly from: string;
  readonly effort?: string;
}): string {
  // `effortLabel`, never the live catalog: the row records the tier picked then.
  const tier = p.effort === undefined || p.effort === "" ? "" : effortLabel({ id: p.effort });
  if (tier === "") {
    return `Switched to ${p.to}`;
  }
  // "Switched to opus-5" beside an unchanged model would read as a model switch.
  return p.from === p.to ? `Reasoning effort: ${tier}` : `Switched to ${p.to} (${tier})`;
}

/**
 * Names both endpoints, since a mode id names a workflow; an empty `from` names the destination alone. Through
 * the live catalog, because a workspace agent is named by front matter no payload carries.
 */
function modeSwitchedLabel(p: {
  readonly from: string;
  readonly to: string;
  readonly source: string;
}): string {
  const to = labelForMode(p.to);
  const row = p.from === "" ? `Mode: ${to}` : `Mode: ${labelForMode(p.from)} \u2192 ${to}`;
  // KAS flips the mode itself at a turn's end (Plan to Execute), and a reader who did not touch the pill is owed that.
  return p.source === "agent" ? `${row}, switched by the agent` : row;
}

function boundary(kind: BoundaryKind, icon: string, label: string): HTMLElement {
  const node = el("div", { className: `boundary boundary-${kind}` });
  node.appendChild(el("span", { className: "boundary-icon" }, icon));
  node.appendChild(el("span", { className: "boundary-label" }, label));
  return node;
}

/**
 * Two dashed rules with the marker and its collapsible summary between. The body renders on first open: a summary
 * runs to 16 KB and `::details-content` skips layout and paint but not construction.
 */
function compactionBreak(icon: string, label: string, summary: string): HTMLElement {
  const root = el("details", { className: "compaction" }) as HTMLDetailsElement;
  // `.message assistant` is the app's markdown-prose skin.
  const body = el("div", { className: "compaction-body message assistant" });
  root.append(
    el(
      "summary",
      { className: "compaction-head" },
      // Leads the row because it discloses: a disclosing chevron comes first and rotates, a navigating one trails.
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
