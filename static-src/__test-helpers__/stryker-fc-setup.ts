// Mutation runs only. fc-strict-setup.ts's VeryVerbose report over diff.ts's 2001-line inputs
// outgrows the browser RPC's 100 MiB frame and crashes the Stryker worker; a kill needs no report.
import fc from "fast-check";

fc.configureGlobal({
  ...fc.readConfigureGlobal(),
  verbose: fc.VerbosityLevel.None,
  endOnFailure: true,
});
