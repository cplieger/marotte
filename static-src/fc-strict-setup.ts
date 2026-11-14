// Strict fast-check defaults for every property test, loaded through vitest's `setupFiles`.

import fc from "fast-check";

fc.configureGlobal({
  numRuns: 1000,
  verbose: fc.VerbosityLevel.VeryVerbose,
  endOnFailure: false,
  interruptAfterTimeLimit: 10000,
  markInterruptAsFailure: true,
});
