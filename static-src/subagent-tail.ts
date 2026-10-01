// ---------------------------------------------------------------------------
// ONE delegate's last few output lines, derived from its LANE rather than harvested
// from the DOM: the transcript renders none of a delegate's own entries, because
// every lane predicate in `messages-blocks.ts` compares against its root lane.
// ---------------------------------------------------------------------------

import type { Entry } from "./types.js";
import { effect, touch } from "@cplieger/reactive";
import { get, messagesVersionOf } from "./store.js";
import { laneSig } from "./store-signals.js";
import { payloadOf } from "./turns.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";
import { readLane, type LaneRead } from "./subagent-slice.js";

/** How many trailing lines a card's tail shows. */
export const TAIL_LINES = 3;

/** The last `want` non-empty lines of `s`. Backwards from the end, so a 40KB report
 *  costs its last three lines rather than a `split` over all of it per delta. */
function tailOf(s: string, want: number): string[] {
  const out: string[] = [];
  let end = s.length;
  while (end > 0 && out.length < want) {
    const nl = s.lastIndexOf("\n", end - 1);
    const line = s
      .slice(nl + 1, end)
      .replace(/\s+/gu, " ")
      .trim();
    if (line !== "") {
      out.unshift(line);
    }
    end = nl;
  }
  return out;
}

/** The line a `tool_call` entry contributes: its title. A card's claim line is
 *  `tool-card.ts`'s presentation of the input, so deriving one here is a second owner. */
function toolLine(e: Entry): string[] {
  const call = payloadOf(e, "tool_call");
  if (
    call === undefined ||
    isSubagentInvocation(call) ||
    isInternalToolTitle(call.title) ||
    call.title === ""
  ) {
    return [];
  }
  return tailOf(call.title, 1);
}

function textOf(e: Entry): string | null {
  if (e.kind === "text") {
    return payloadOf(e, "text")?.text ?? "";
  }
  if (e.kind === "thinking") {
    return payloadOf(e, "thinking")?.text ?? "";
  }
  return null;
}

/** The last `want` lines of one lane's read.
 *
 *  Walks the lane's entries BACKWARDS over `text`, `thinking` and tool titles until it
 *  holds `want` lines, sealed or open: the open entry is the lane's tail and carries no
 *  `seq`, so it is read first and separately. */
function tailOfLane(lane: LaneRead, want: number): string[] {
  const lines: string[] = [];
  if (want <= 0) {
    return lines;
  }
  if (lane.open !== undefined) {
    lines.unshift(...tailOf(lane.open.text, want));
  }
  for (let i = lane.entries.length - 1; i >= 0 && lines.length < want; i--) {
    const e = lane.entries[i];
    if (e === undefined) {
      continue;
    }
    if (e.kind === "tool_call") {
      lines.unshift(...toolLine(e));
      continue;
    }
    const text = textOf(e);
    if (text !== null) {
      lines.unshift(...tailOf(text, want - lines.length));
    }
  }
  return lines.slice(-want);
}

/** One delegate's trailing output, from its LANE alone.
 *
 *  An empty subtask id is nobody's delegate and answers nothing, and so does a chat the
 *  store does not hold. */
export function subagentTail(
  chatID: string,
  subtaskID: string,
  want: number = TAIL_LINES,
): string[] {
  const session = get(chatID);
  if (session === undefined || subtaskID === "") {
    return [];
  }
  return tailOfLane(readLane(session, subtaskID), want);
}

/** Repaint ONE delegate's tail whenever its own output grows. Returns the disposer.
 *
 *  TWO dependencies. The chat's TRANSCRIPT version exists before the lane does, which is
 *  the state at a card's creation — the card is built from the invocation, and that call
 *  sits in the ISSUER's lane — so on the lane signal alone this effect records no source
 *  and never runs again, which is the live delegate it exists for; the store bumps that
 *  version for a laned entry too (`entryCause` answers `chunk`). The LANE signal is the
 *  narrow channel, bumped on every open, delta, seal and laned append. */
export function bindSubagentTail(
  chatID: string,
  subtaskID: string,
  paint: (lines: string[]) => void,
): () => void {
  let painted: string | undefined;
  return effect(() => {
    touch(messagesVersionOf(chatID));
    const session = get(chatID);
    const lane = session === undefined ? undefined : readLane(session, subtaskID);
    if (lane?.turnID !== undefined) {
      touch(laneSig(lane.turnID, subtaskID));
    }
    const lines = lane === undefined ? [] : tailOfLane(lane, TAIL_LINES);
    const next = lines.join("\n");
    if (next === painted) {
      return;
    }
    painted = next;
    paint(lines);
  });
}
