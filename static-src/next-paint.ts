/** Run `fn` in a task after the next frame paints, so what the current task wrote is on screen
 *  first. A hidden page fires no frame callbacks (a notification click can act on one), so it gets
 *  a bare task. */
export function afterNextPaint(fn: () => void): void {
  if (document.hidden) {
    setTimeout(fn, 0);
    return;
  }
  requestAnimationFrame(() => {
    setTimeout(fn, 0);
  });
}
