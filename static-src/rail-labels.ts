import { formatElapsed } from "./strings.js";
import { OUTCOME_LABEL } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

export interface TurnLabel {
  /** `data-tooltip` text, where `\n` breaks the line. */
  preview: string;
  ariaLabel: string;
}

export interface MarkerSubject {
  n: number;
  outcome: TurnOutcome;
  first_line?: string;
  agent_initiated?: boolean;
  elapsed_ms?: number;
}

export interface MarkerState {
  pending: boolean;
  hit: boolean;
}

/** The app's own separator, matching `turn-footer.ts`'s ledger line. */
const SEP = " \u00b7 ";

/** The prompt's cut in code points: about two tooltip lines, so the tooltip's own clamp rarely has
 *  to fire mid-word. */
const PREVIEW_PROMPT_MAX = 90;

function promptLine(s: MarkerSubject): string {
  const runes = Array.from((s.first_line ?? "").trim());
  return runes.length > PREVIEW_PROMPT_MAX
    ? runes.slice(0, PREVIEW_PROMPT_MAX).join("") + "\u2026"
    : runes.join("");
}

/** The transient facts, on the first line: the tooltip holds `--tooltip-lines` (2) lines, so a
 *  third line would be clipped. Never in the name, which is read on every focus. */
function stateParts(state: MarkerState): string[] {
  const out: string[] = [];
  if (state.pending) {
    out.push("Loading\u2026");
  }
  if (state.hit) {
    out.push("Search match");
  }
  return out;
}

/** One turn's preview and accessible name, from one composer so the two cannot disagree. The
 *  preview names every outcome, `Completed` included, because it is read on request; the name
 *  omits `completed`, because a word on every row communicates nothing. */
export function markerLabel(s: MarkerSubject, state: MarkerState): TurnLabel {
  const head = [`#${String(s.n)}`, OUTCOME_LABEL[s.outcome]];
  if (s.elapsed_ms !== undefined && s.elapsed_ms > 0) {
    head.push(formatElapsed(s.elapsed_ms));
  }
  head.push(...stateParts(state));
  const ask = promptLine(s);
  // An agent-initiated turn has no request: the server sets `first_line` only for a user trigger.
  const second = ask !== "" ? ask : s.agent_initiated === true ? "Agent-initiated turn" : "";
  const preview = second === "" ? head.join(SEP) : `${head.join(SEP)}\n${second}`;

  const name: string[] = [`Turn ${String(s.n)}`];
  if (s.outcome !== "completed") {
    name.push(OUTCOME_LABEL[s.outcome].toLowerCase());
  }
  if (s.agent_initiated === true) {
    name.push("agent-initiated");
  }
  const ariaLabel = ask === "" ? name.join(", ") : `${name.join(", ")}: ${ask}`;
  return { preview, ariaLabel };
}

export function binLabel(
  bin: { readonly first: number; readonly last: number; readonly worst: TurnOutcome },
  state: MarkerState,
): TurnLabel {
  const head = [
    `Turns ${String(bin.first)}-${String(bin.last)}`,
    OUTCOME_LABEL[bin.worst],
    ...stateParts(state),
  ];
  const range = `Turns ${String(bin.first)} to ${String(bin.last)}`;
  const ariaLabel =
    bin.worst === "completed" ? range : `${range}, worst ${OUTCOME_LABEL[bin.worst].toLowerCase()}`;
  return { preview: head.join(SEP), ariaLabel };
}
