// ONE delegate's last output lines, derived from its LANE: the transcript renders none of a
// delegate's own entries.

import type { Entry } from "./types.js";
import { effect, touch } from "@cplieger/reactive";
import { get, messagesVersionOf } from "./store.js";
import { laneSig } from "./store-signals.js";
import { payloadOf } from "./turns.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";
import { readLane, type LaneRead } from "./subagent-slice.js";
import { callIDOfToolResult } from "./entry-ids.js";

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

/** The line a `tool_call` entry contributes: the title its card draws, its held result's
 *  (`settledTitle`) over its own. */
function toolLine(e: Entry, settledTitle: string | undefined): string[] {
  const call = payloadOf(e, "tool_call");
  if (call === undefined || isSubagentInvocation(call) || isInternalToolTitle(call.title)) {
    return [];
  }
  return tailOf(settledTitle ?? call.card_title ?? call.title, 1);
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

/** The last `want` lines of a lane, walking `text`, `thinking` and tool titles backwards; the
 *  open entry carries no `seq`, so it is read first. A result follows its call, so the walk meets
 *  it first. */
function tailOfLane(lane: LaneRead, want: number): string[] {
  const lines: string[] = [];
  if (want <= 0) {
    return lines;
  }
  if (lane.open !== undefined) {
    lines.unshift(...tailOf(lane.open.text, want));
  }
  const settled = new Map<string, string>();
  for (let i = lane.entries.length - 1; i >= 0 && lines.length < want; i--) {
    const e = lane.entries[i];
    if (e === undefined) {
      continue;
    }
    if (e.kind === "tool_result") {
      const callID = callIDOfToolResult(e.id);
      const title = payloadOf(e, "tool_result")?.card_title;
      if (callID !== null && title !== undefined) {
        settled.set(callID, title);
      }
      continue;
    }
    if (e.kind === "tool_call") {
      lines.unshift(...toolLine(e, settled.get(e.id)));
      continue;
    }
    const text = textOf(e);
    if (text !== null) {
      lines.unshift(...tailOf(text, want - lines.length));
    }
  }
  return lines.slice(-want);
}

/** Repaint ONE delegate's tail as its output grows; returns the disposer. TWO dependencies: the
 *  chat's TRANSCRIPT version (exists before the lane, and bumps for laned entries too) and the
 *  narrow LANE signal. */
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
