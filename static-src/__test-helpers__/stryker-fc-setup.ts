// Mutation runs only (vitest.stryker.config.ts). A killed mutant fails a
// fast-check property on every run, and the VeryVerbose report that
// fc-strict-setup.ts asks for, over diff.ts's 2001-line inputs, outgrows the
// browser RPC's 100 MiB frame and crashes the Stryker worker. A kill needs the
// failure, not the report.
import fc from "fast-check";

fc.configureGlobal({
  ...fc.readConfigureGlobal(),
  verbose: fc.VerbosityLevel.None,
  endOnFailure: true,
});
