// When @cplieger/web-terminal-ui's font gate has opened, for a host that must not act before it.

/** web-terminal-ui's FONT_READY_TIMEOUT_MS (src/kernel/pane.ts), the bound on its font wait. */
const LIBRARY_FONT_DEADLINE_MS = 3000;

/**
 * Resolves once the library has settled `fontReady` for a pane built before this call, so its
 * `PaneHandle.announceSize()` sends from then on. It waits as the library does: the load, then
 * `document.fonts.ready` if the load fails, or the deadline. Equal timeouts fire in the order they
 * were set (HTML "timer initialisation steps"), and the library's settle runs in the same task as
 * this load's or an earlier one, so resolving a frame later puts it after the library's.
 */
export function shellFontSettled(fontReady: string): Promise<void> {
  let landed: Promise<unknown>;
  try {
    landed = document.fonts.load(fontReady).catch(() => document.fonts.ready);
  } catch {
    landed = Promise.resolve();
  }
  const deadline = new Promise<void>((resolve) => {
    setTimeout(resolve, LIBRARY_FONT_DEADLINE_MS);
  });
  return Promise.race([landed, deadline]).then(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          resolve();
        });
      }),
  );
}
