// Types the `tap` browser command that vitest.config.ts registers.

import { server } from "vitest/browser";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    tap(selector: string): Promise<void>;
  }
}

/** A real tap through Playwright's touchscreen on the element `selector` (a Playwright selector)
 *  names in the test frame, in any engine. The page must be a touch device (`hasTouch`). */
export async function tap(selector: string): Promise<void> {
  await server.commands.tap(selector);
}
