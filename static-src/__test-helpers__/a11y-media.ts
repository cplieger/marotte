// The one owner of the `emulateA11yMedia` command's `BrowserCommands` augmentation (declared in
// vitest.config.ts), for suites asserting a RENDERED media arm, which a source read cannot answer.

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

/** Page-level, so the tester iframe inherits it. Reverse it in `afterAll` with `resetA11yMedia`, or
 *  the next file to run in this browser inherits the emulation. */
export async function emulateA11yMedia(features: A11yMediaFeatures): Promise<void> {
  await server.commands.emulateA11yMedia(features);
}

/** Each is named EXPLICITLY rather than omitted: `emulateMedia` reads an absent key as "leave this
 *  one as it is", so omitting a feature does not clear an earlier call. */
export async function resetA11yMedia(): Promise<void> {
  await server.commands.emulateA11yMedia({
    reducedMotion: "no-preference",
    forcedColors: "none",
  });
}
