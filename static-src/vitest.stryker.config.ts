// Vitest config for Stryker mutation runs ONLY (stryker.config.json points here). Instrumentation
// slows hot loops several-fold, so the heavy fast-check properties blow the base per-test cap in the
// dry run; per-MUTANT runaway protection stays with Stryker's timeoutMS/timeoutFactor. No bare
// imports: spreading the base config needs no defineConfig helper.
import base from "./vitest.config.js";

// Suites whose SUBJECT is production source TEXT: `inPlace: true` rewrites mutate targets on disk,
// so they would parse the instrumented copy. They import no production module and can kill no
// mutant; they still run in CI and locally.
const SOURCE_TEXT_SUITES = ["**/turn-base-writers.node.test.ts"];

// Added per PROJECT, never at the root: a project's `exclude` REPLACES the root
// one rather than adding to it (vitest.config.ts states this at `sharedExclude`),
// so a root-level entry here would be silently inert.
const projects = base.test?.projects?.map((project) =>
  typeof project === "object" && "test" in project
    ? {
        ...project,
        test: {
          ...project.test,
          exclude: [...(project.test?.exclude ?? []), ...SOURCE_TEXT_SUITES],
        },
      }
    : project,
);

const baseSetupFiles = base.test?.setupFiles ?? [];

export default {
  ...base,
  test: {
    ...base.test,
    ...(projects ? { projects } : {}),
    setupFiles: [
      ...(Array.isArray(baseSetupFiles) ? baseSetupFiles : [baseSetupFiles]),
      "./__test-helpers__/stryker-fc-setup.ts",
    ],
    // 90s: the Hirschberg large-input properties (2001x2001-line diffs) exceed 30s under
    // instrumentation.
    testTimeout: 90_000,
  },
};
