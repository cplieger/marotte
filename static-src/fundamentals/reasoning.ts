// ReasoningBlock: the collapsible "Thinking…" trace for a `thinking` block. Pure view, sealed when
// the trace ends or sibling text arrives. The summary shows a word count only: no reasoning
// content may leak into a collapsed summary.

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { CHROME_ATTR } from "../chrome-attr.js";

/** One whitespace character. No `g` flag, so `test` stays stateless. */
const SPACE = /\s/u;

/** Words `chunk` adds to a trace ending `openWord`, and whether it ends mid-word. Whitespace
 *  tokens only (a CJK trace reads as "1 word"). Summed PER DELTA: a trace reaches megabytes, so
 *  recounting the whole string is O(length²) per block. */
function foldWords(chunk: string, openWord: boolean): { added: number; openWord: boolean } {
  if (chunk === "") {
    return { added: 0, openWord };
  }
  let added = chunk.split(/\s+/u).filter((w) => w !== "").length;
  if (added > 0 && openWord && !SPACE.test(chunk.charAt(0))) {
    added -= 1;
  }
  return { added, openWord: !SPACE.test(chunk.charAt(chunk.length - 1)) };
}

/** A mounted reasoning block plus its imperative update handle. */
export interface ReasoningView {
  /** The `<details>` root to insert into the DOM. */
  readonly root: HTMLDetailsElement;
  /** Append a streamed delta to the body (text-node append + word recount). */
  append(delta: string): void;
  /** Replace-to-full: append only the tail beyond what's rendered. */
  setText(full: string): void;
  /** Settle the trace: flip the summary, drop the pulse. Says nothing about the
   *  disclosure — a trace that has finished thinking is not a trace something was
   *  posted after. Idempotent. */
  settle(): void;
  /** Settle, then COLLAPSE. What a successor arriving means: the trace is finished AND
   *  no longer the newest element in its lane. Idempotent. */
  seal(): void;
}

/** Build a reasoning block. `live` owns the pulse and label; `open` owns the disclosure and is
 *  the caller's (a settled trace nothing followed still renders expanded). */
export function buildReasoning(initial: string, live: boolean, open: boolean): ReasoningView {
  const root = el("details", {
    className: "reasoning-block msg-reasoning",
  }) as HTMLDetailsElement;
  // Own element so seal() can rewrite it without touching the chevron beside it.
  const label = el("span", { className: "reasoning-label" }, live ? "Thinking…" : "Reasoning");
  const count = el("span", { className: "reasoning-count" });
  // `<summary>`'s accessible name comes from its descendants: a count repainted per chunk would
  // rename a focusable control dozens of times. Hidden permanently.
  count.setAttribute("aria-hidden", "true");
  const summary = el(
    "summary",
    { className: "reasoning-summary", [CHROME_ATTR]: "" },
    chevronEl(),
    label,
    count,
  );
  const body = el("blockquote", { className: "reasoning-body" }, initial);
  root.append(summary, body);
  root.open = open;
  if (live) {
    root.classList.add("streaming");
  }

  // Also the watermark setText() slices against.
  let text = initial;
  let settled = false;
  let collapsed = false;
  let words = 0;
  let openWord = false;

  /** Fold one appended chunk into the running count. */
  function countIn(chunk: string): void {
    const r = foldWords(chunk, openWord);
    words += r.added;
    openWord = r.openWord;
  }

  /** Repaint the count. A count of zero renders NOTHING, not "0 words". */
  function showCount(): void {
    count.textContent =
      words === 0 ? "" : `${words.toLocaleString()} ${words === 1 ? "word" : "words"}`;
  }
  countIn(initial);
  showCount();

  /** The label-and-pulse half, shared by both exits so a detached `seal` reference
   *  cannot miss it. */
  function settleNow(): void {
    if (settled) {
      return;
    }
    settled = true;
    label.textContent = "Thinking completed";
    root.classList.remove("streaming");
    showCount();
  }

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      body.appendChild(document.createTextNode(delta));
      text += delta;
      countIn(delta);
      showCount();
    },
    setText(full: string): void {
      if (full.length <= text.length) {
        return;
      }
      const tail = full.slice(text.length);
      body.appendChild(document.createTextNode(tail));
      text = full;
      countIn(tail);
      showCount();
    },
    settle: settleNow,
    seal(): void {
      settleNow();
      if (collapsed) {
        return;
      }
      collapsed = true;
      root.open = false;
    },
  };
}
