// Fling scenes on the events' own clock: `context-hold.ts` caughtFling compares a press's `timeStamp`
// with its scroller's last `scroll`, and a synthetic event takes its stamp when it is built, so two
// built back to back on a busy page can sit further apart than a fling's window.

/** A momentum scroll fires `scroll` once a frame, 16 ms apart at 60 Hz. */
export const FLING_FRAME_MS = 16;

/** Longer than any momentum scroll's gap between two `scroll` events: the scroller had stopped. */
export const SCROLLER_RESTED_MS = 150;

/** Dispatch a `scroll` on `scroller` stamped `ago` ms before `press`, then `press` on `target`. */
export function pressAfterScroll(
  scroller: EventTarget,
  target: EventTarget,
  press: PointerEvent,
  ago = FLING_FRAME_MS,
): void {
  const scroll = new Event("scroll");
  Object.defineProperty(scroll, "timeStamp", { value: press.timeStamp - ago });
  scroller.dispatchEvent(scroll);
  target.dispatchEvent(press);
}
