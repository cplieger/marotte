// The one owner of the `parkPointer` command's `BrowserCommands` augmentation (declared in vitest.config.ts). A
// worker's page keeps the real pointer where an earlier file left it, and Chromium re-hovers whatever a later test
// lays out under it, so a measurement or screenshot can carry a hover style no test made.
import { server } from "vitest/browser";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    parkPointer(): Promise<void>;
  }
}

/** Whether a real pointer sits over this page: the tester's own document or the page that frames it. */
export function pointerInPage(): boolean {
  if (document.documentElement.matches(":hover")) {
    return true;
  }
  try {
    return window.parent !== window && window.parent.document.documentElement.matches(":hover");
  } catch {
    return false;
  }
}

/** Move the pointer off the page when one is on it. A page no pointer has entered is left alone: a first real
 *  mouse event turns Chromium's hover tracking on for the page's later files. */
export async function parkPointer(): Promise<void> {
  if (pointerInPage()) {
    await server.commands.parkPointer();
  }
}
