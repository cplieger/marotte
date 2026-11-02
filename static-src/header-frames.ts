// The order of the stream's chat header frames against the HTTP reads that also carry a header. A
// LEAF: `handlers/chat.ts` notes each frame, and `store-load.ts` marks each read at issue.

let frames = 0;
const newest = new Map<string, number>();

/** Count a `chat_created` or `chat_updated` frame for the chat it names. */
export function noteHeaderFrame(chatID: string): void {
  frames += 1;
  newest.set(chatID, frames);
}

/** The mark an HTTP read takes when it is issued. */
export function headerFrameMark(): number {
  return frames;
}

/** Whether a header frame for the chat landed after `mark`: a reply issued at the mark may then
 *  describe the chat before that frame, so the frame's read position stands over the reply's. A
 *  change the reply saw past the frame sends a frame of its own. */
export function framedSince(chatID: string, mark: number): boolean {
  return (newest.get(chatID) ?? 0) > mark;
}

/** Test isolation only; production never resets. */
// deadset:ignore DS1004 -- test seam: resets the frame order between cases
export function _resetForTest(): void {
  frames = 0;
  newest.clear();
}
