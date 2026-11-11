// The run dock's panel, opened the way a reader opens it: its rows exist only while it is open.

/** Open the panel of the dock under `scope` on `segment`, by a click on its toggle and switcher. */
export function openDockPanel(scope: ParentNode, segment: "steps" | "timeline" = "steps"): void {
  const toggle = scope.querySelector<HTMLButtonElement>(".ev-box-toggle");
  if (toggle === null) {
    throw new Error("no run dock to open");
  }
  if (toggle.getAttribute("aria-expanded") !== "true") {
    toggle.click();
  }
  scope.querySelector<HTMLButtonElement>(`[data-ev-seg="${segment}"]`)?.click();
}

/** Wait until an opened panel has played its open and its panes have redrawn for the box it
 *  settled at, which they do on the frame after a resize. */
export async function settledOpen(scope: ParentNode): Promise<void> {
  const panel = scope.querySelector<HTMLElement>(".ev-dock-panel");
  await Promise.allSettled(panel?.getAnimations().map((a) => a.finished) ?? []);
  for (let i = 0; i < 2; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}
