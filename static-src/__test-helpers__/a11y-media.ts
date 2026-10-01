// Emulating the two accessibility media features this app declares arms for, for
// the suites that assert a RENDERED arm rather than a source declaration.
//
// The mechanism is the `emulateA11yMedia` custom browser command declared in the
// browser project's `test.browser` block (vitest.config.ts), which reaches a
// fully-typed Playwright `page.emulateMedia`. This module exists so the
// `BrowserCommands` augmentation has ONE owner: two suites need the command, and
// a per-file `declare module` would put one wire contract in two places.
//
// A source read cannot answer these questions. `ruleContaining` can say a
// declaration sits inside the right media query; only an engine can say what the
// element computes when the query MATCHES, and for forced colours the defect is
// the UA's own forcing of `background` to `Canvas`, which no stylesheet read
// reproduces.

// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// whose module body THROWS ("can be imported only inside the Browser Mode"), so
// the wrong specifier fails the whole file's import rather than one assertion.
import { server } from "vitest/browser";

// `vitest/browser`'s own types re-export this interface from
// `vitest/internal/browser`, which is therefore the module to augment.
declare module "vitest/internal/browser" {
  interface BrowserCommands {
    emulateA11yMedia(features: A11yMediaFeatures): Promise<void>;
  }
}

export interface A11yMediaFeatures {
  reducedMotion?: "reduce" | "no-preference";
  forcedColors?: "active" | "none";
}

/** Turn one or both preferences on for this page. Page-level, so the tester
 *  iframe inherits it. Reverse it in `afterAll` with `resetA11yMedia`, or the
 *  next file to run in this browser inherits the emulation. */
export async function emulateA11yMedia(features: A11yMediaFeatures): Promise<void> {
  await server.commands.emulateA11yMedia(features);
}

/** Both features back to what a reader with no preference gets. Each is named
 *  EXPLICITLY rather than omitted: `emulateMedia` reads an absent key as "leave
 *  this one as it is", so omitting a feature does not clear an earlier call. */
export async function resetA11yMedia(): Promise<void> {
  await server.commands.emulateA11yMedia({
    reducedMotion: "no-preference",
    forcedColors: "none",
  });
}
