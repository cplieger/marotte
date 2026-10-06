// The assistant markdown bubble. A LIVE bubble owns a reveal cursor (`reveal.ts`) that spreads
// lumpy deltas across frames at a fixed lag behind the live edge; mounted history paints at once.
// `.message.assistant` is the prose container (~40rem measure).

import { el } from "@cplieger/reactive";
import { createMarkdownStream, renderMarkdownInto, type MarkdownStream } from "../markdown.js";
import { createReveal, type Reveal } from "../reveal.js";

/** A mounted assistant text bubble plus its streaming handle. */
export interface AssistantBubble {
  /** The `.message.assistant` element to insert into the DOM. */
  readonly root: HTMLDivElement;
  /** Feed a streamed markdown delta through the incremental parser. */
  append(delta: string): void;
  /** Replace-to-full: render only the tail beyond what's rendered — the
   *  twin of ReasoningView.setText, no-op when nothing grew. */
  setText(full: string): void;
  /** Stop the text: no more growth is coming. On a live bubble the reveal
   *  keeps running until it catches up, so the caret and streaming wash
   *  outlive `turn_closed` by the reveal's lag. */
  end(): void;
  /** end(), minus the wait: reveal the remainder in one write and finalize
   *  now — for a bubble being discarded, or any caller needing settled DOM
   *  synchronously. */
  finishNow(): void;
}

/** `live` primes an incremental stream plus a reveal cursor and marks the bubble `.streaming`
 *  (css/13-messages.css); replay renders one-shot. */
export interface AssistantBubbleOpts {
  /** Fires once when the bubble seals — the moment `.streaming` drops. The
   *  live-anchor registry hangs off this, cleared at whichever path sealed
   *  it (tail moved, turn finalized, unmount). */
  onSeal?: (root: HTMLElement) => void;
  /** Reports BLANK (nothing to show, not streaming) initially and on each transition; the row
   *  wrapper hides on it (`.msg-row.is-empty`). */
  onBlankChange?: (blank: boolean) => void;
}

export function buildAssistantBubble(
  initial: string,
  live: boolean,
  opts?: AssistantBubbleOpts,
): AssistantBubble {
  const root = el("div", { className: "message assistant" }) as HTMLDivElement;
  let stream: MarkdownStream | null = null;
  let text = ""; // what the parser has been given
  let targetLen = 0; // length of the full text known, revealed or not
  let ending = false;

  /** Blank = nothing to show and not streaming. Initialized after the initial
   *  render below; write/seal fire transitions through onBlankChange. */
  let blank = false;
  const setBlank = (b: boolean): void => {
    if (b !== blank) {
      blank = b;
      opts?.onBlankChange?.(b);
    }
  };

  /** Append `delta`, opening the incremental renderer if needed. Reached without a stream by a live
   *  bubble's first delta or a misjudged REPLAY bubble re-rendering through a fresh stream
   *  (`wasRendered` flushes so the swap is invisible). */
  const write = (delta: string): void => {
    if (delta === "") {
      return;
    }
    text += delta;
    setBlank(false);
    if (stream !== null) {
      stream.writeDelta(delta);
      return;
    }
    const wasRendered = root.firstChild !== null;
    root.replaceChildren();
    // Flush interval 0 on the live path: the reveal already regulates
    // cadence, so the stream's own buffering would re-lump what it spread.
    stream = createMarkdownStream(root, live ? { flushIntervalMs: 0 } : {});
    stream.writeDelta(text);
    if (wasRendered) {
      stream.flush();
    }
  };

  /** Finalize the markdown stream and drop `.streaming`. Idempotent; `onSeal`
   *  fires on the first call only. */
  let sealed = false;
  const seal = (): void => {
    stream?.end();
    stream = null;
    root.classList.remove("streaming");
    setBlank(root.firstChild === null);
    if (!sealed) {
      sealed = true;
      opts?.onSeal?.(root);
    }
  };

  const reveal: Reveal | null = live
    ? createReveal(initial, {
        onWrite: write,
        onIdle: () => {
          if (ending) {
            seal();
          }
        },
      })
    : null;

  if (live) {
    root.classList.add("streaming");
    targetLen = initial.length;
    write(initial);
  } else {
    text = initial;
    targetLen = initial.length;
    if (initial !== "") {
      renderMarkdownInto(root, initial);
    }
  }

  // Initial report: DOM truth, since whitespace-only markdown renders no
  // node and the row keys on what is visible.
  blank = !live && root.firstChild === null;
  opts?.onBlankChange?.(blank);

  /** Take `delta` as pure growth past everything known so far. */
  const feed = (delta: string): void => {
    targetLen += delta.length;
    if (reveal === null) {
      write(delta);
      return;
    }
    reveal.append(delta);
  };

  /** Take `full` as the text now known. Growth only; shorter is ignored. */
  const grow = (full: string): void => {
    if (full.length <= targetLen) {
      return;
    }
    feed(full.slice(targetLen));
  };

  return {
    root,
    append(delta: string): void {
      if (delta === "") {
        return;
      }
      feed(delta);
    },
    setText: grow,
    end(): void {
      if (ending) {
        return;
      }
      ending = true;
      if (reveal === null || reveal.idle) {
        seal();
      }
    },
    finishNow(): void {
      ending = true;
      if (reveal === null) {
        seal();
        return;
      }
      reveal.finishNow();
    },
  };
}
