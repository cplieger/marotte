// The DEFAULT is the browser: a file runs in real headless Chromium unless named `.node.test.ts`
// (Node) or `.touch.test.ts` (a touch-device page). No `environment` option on the browser side
// and no per-file `@vitest-environment` pragma: Browser Mode is not an environment but a
// runner. The suffix is load-bearing: a misplaced Node-needing test throws on
// import, but one needing the DOM ABSENT passes vacuously (`attention-no-dom.node.test.ts`
// asserts its premise). `*.fuzz.test.ts` is ts-ci's fuzz axis. `channel: "chromium"` is the real
// browser, not headless-shell: `npx playwright install --with-deps chromium`.
import { configDefaults, defineConfig, type TestProjectInlineConfiguration } from "vitest/config";
import { resolve } from "node:path";

import { alwaysOnInterception } from "./__test-helpers__/always-on-interception.js";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const actionsInternals = resolve(__dirname, "node_modules/@cplieger/actions/dist/src");

// Exclude compiled output and node_modules. `**/.stryker-tmp/**` keeps Stryker's `inPlace` backup
// copy from being collected twice (its fixtures are missing, so it fails). Every unit project needs
// the whole list: a project's `exclude` REPLACES the root one.
const sharedExclude = [
  ...configDefaults.exclude,
  "../static/**",
  "**/.stryker-tmp/**",
  // The `e2e-sse` project's files: node, but against a spawned marotte binary rather
  // than a fake, so no unit project may collect them.
  "e2e-sse/**",
];

// VITEST_TRACE=1 turns on trace view (a DOM snapshot per browser interaction) and the html reporter
// that serves it, single-file:
//   VITEST_TRACE=1 npx vitest run --project browser turn-residency.test.ts
//   then open .vitest/index.html
// Off by default: the snapshots cost time on every browser file and CI has nowhere to publish them.
const traceView = process.env["VITEST_TRACE"] === "1";

// A test needing a coarse pointer is `*.touch.test.ts` and runs in `browser-touch`, whose pages are
// touch devices from creation: touch emulation switched off inside a page leaves headless Chromium
// reporting no pointer at all, not the mouse a page starts with, and every later file inherits it.
function browserProject(
  name: string,
  include: string[],
  exclude: string[],
  hasTouch = false,
): TestProjectInlineConfiguration {
  return {
    extends: true,
    test: {
      name,
      include,
      exclude,
      // A later group, so touch never runs beside `browser`: a bail that lands while the other project
      // is resolving a `vi.mock` factory can end the vitest process
      // (https://github.com/vitest-dev/vitest/issues/11467).
      ...(hasTouch ? { sequence: { groupOrder: 1 } } : {}),
      // Merged with the root list (`extends: true` merges arrays). Browser-only: the gate reads `window`,
      // and a `ResizeObserver` loop is a real engine's verdict.
      setupFiles: ["./ro-loop-gate.ts"],
      // One test file at a time: the mocker's routes live on the shared Playwright CONTEXT and are
      // unrouted at each file end, so a still-resolving mock gets auto-continued (`route.fulfill: Route
      // is already handled!`, https://github.com/vitest-dev/vitest/issues/8339). The anchor route below
      // closes the zero-crossing; drop this only after a parallel run measures clean.
      fileParallelism: false,
      browser: {
        enabled: true,
        headless: true,
        traceView,
        provider: alwaysOnInterception({
          ...(hasTouch ? { contextOptions: { hasTouch } } : {}),
          launchOptions: {
            channel: "chromium",
            // Past ~49 test files in one browser session Chromium drops animation-frame
            // delivery to ~1Hz; `frame-budget.ts` owns the per-file budgets.
            args: ["--disable-frame-rate-limit"],
          },
        }),
        instances: [{ browser: "chromium" }],
        // Fixed viewport so layout-dependent assertions are reproducible; a
        // real browser computes real boxes, unlike the emulator this
        // replaced.
        viewport: { width: 1280, height: 720 },
        // A failure screenshot per failing test is noise in CI and cannot
        // be read from a job log; the assertion diff is the artifact.
        screenshotFailures: false,
        // `commands` is a `test.browser` option and NOT a `test` option; the
        // two nest one line apart and the wrong one type-checks nowhere.
        commands: {
          /** Emulate the two accessibility media features this app has arms for, per page and per test:
           *  the provider's `contextOptions` is project-global and would invert the `tab-dot.test.ts` family.
           *  Playwright's typed `emulateMedia` rather than `cdp()`, whose `CDPSession` is typed empty; one
           *  call takes both, so a test can hold all four combinations. */
          async emulateA11yMedia(
            { page },
            features: {
              reducedMotion?: "reduce" | "no-preference";
              forcedColors?: "active" | "none";
            },
          ) {
            await page.emulateMedia(features);
          },
        },
      },
    },
  };
}

export default defineConfig({
  // cmd/bundle injects the SSE worker's content-hashed URL into the page bundle; a test
  // build ships no worker, and the empty string is the adapter's "run the per-tab
  // stream" reading (static-src/globals.d.ts).
  define: { __SSE_WORKER_URL__: JSON.stringify("") },
  // Vite discovers a dependency on the first test file that imports it and then
  // reloads the browser mid-run, which fails whichever file is loading at that
  // moment. Pre-bundling the terminal's entry points up front avoids the reload.
  optimizeDeps: {
    include: [
      "@cplieger/web-terminal-ui",
      "@cplieger/web-terminal-ui/features/mobile-toolbar",
      "@cplieger/web-terminal-ui/presets/single",
    ],
  },
  resolve: {
    alias: [
      // Allow deep imports into @cplieger/actions internals for test reset
      // utilities (_resetForTest). The package "exports" field restricts
      // access to "." only; this alias bypasses that for tests.
      {
        find: /^@cplieger\/actions\/dist\/src\/(.+)$/,
        replacement: `${actionsInternals}/$1`,
      },
    ],
  },
  server: {
    fs: {
      // The `?raw` reads across the package boundary (the shipped page, Go sources the cross-language
      // tests pin, the scripts a CSS guard reads). Narrowest set; NOT the repo root.
      allow: ["../static", "../internal", "../scripts"],
    },
  },
  test: {
    ...(traceView ? { reporters: ["default", ["html", { singleFile: true }]] as const } : {}),
    projects: [
      {
        // `extends` is a PROJECT key: inside `test` it is silently ignored and every root option is lost
        // with the suite still green (verified with a zero-assertion probe).
        extends: true,
        test: {
          name: "node",
          environment: "node",
          // threads pool: faster than forks for pure Node.js tests with no
          // native modules (no Prisma, bcrypt, canvas).
          pool: "threads",
          // Each test file gets its own module graph: an action file's vi.mock() otherwise leaks into other
          // files in the worker. Browser Mode isolates per file, so the browser project needs neither option.
          isolate: true,
          // Package-root-relative, because the tests sit at the package root.
          include: ["**/*.node.test.ts"],
          exclude: sharedExclude,
        },
      },
      {
        // The SSE lifecycle against the REAL server: `SSE_FIXTURE` names a `-tags marotte_test` binary the
        // globalSetup starts on a scratch dir and free port; every file skips when it is unset. Node, not
        // the browser: the fixture refuses cross-site POSTs and sends no CORS header.
        extends: true,
        test: {
          name: "e2e-sse",
          environment: "node",
          pool: "threads",
          isolate: true,
          include: ["e2e-sse/**/*.test.ts"],
          exclude: [...configDefaults.exclude, "../static/**", "**/.stryker-tmp/**"],
          globalSetup: ["./__test-helpers__/marotte-server.setup.ts"],
          // One file at a time: the cases arm the server's close-after hook, which
          // cuts the NEXT connection whoever opens it.
          fileParallelism: false,
        },
      },
      browserProject(
        "browser",
        ["**/*.test.ts"],
        [...sharedExclude, "**/*.node.test.ts", "**/*.touch.test.ts"],
      ),
      browserProject("browser-touch", ["**/*.touch.test.ts"], sharedExclude, true),
    ],

    // Fail loudly if the include pattern matches nothing.
    passWithNoTests: false,

    // Forbid .only tests unconditionally — not just in CI.
    allowOnly: false,

    // Require explicit imports of describe/it/expect from "vitest".
    globals: false,

    // Force every test to call at least one expect(). Catches tests that
    // accidentally pass because they never assert anything.
    expect: {
      requireAssertions: true,
    },

    // Reset mocks, spies, env and global stubs before each test.
    clearMocks: true,
    mockReset: true,
    restoreMocks: true,
    unstubEnvs: true,
    unstubGlobals: true,

    // Fail fast on first suite error in CI; run all in watch mode.
    bail: process.env["CI"] ? 1 : 0,

    // ONE CI retry, for a timing miss on a loaded runner; it cannot recover a file-level failure, and
    // vitest reports a retry-only pass as `flaky`.
    retry: process.env["CI"] ? 1 : 0,

    // The per-test default may not sit BELOW a bound the suite already widened (fast-check's
    // `interruptAfterTimeLimit`, `vi.waitFor`'s patched default), or it preempts it with a bare
    // deadline. `frame-budget.ts` owns why 10s is the floor: deep into the run this browser delivers
    // frames at 1Hz. A FAILURE bound only; a file needing more states its own.
    testTimeout: testTimeoutFor(FRAME_BUDGET_MS),
    hookTimeout: 5000,

    // Flag tests slower than 100ms. Root-only: `slowTestThreshold` is a
    // NonProjectOption, so vitest rejects it inside a project.
    slowTestThreshold: 100,

    // Reproducible ordering. hooks: "stack" = afterEach/afterAll run in
    // reverse definition order (correct teardown semantics).
    sequence: {
      shuffle: { files: false, tests: false },
      concurrent: false,
      hooks: "stack",
    },

    // Loaded once per worker before any test file. Configures fast-check
    // global defaults (numRuns, verbosity, time limits). See file for
    // tuning rationale.
    setupFiles: ["./fc-strict-setup.ts", "./waitfor-budget-setup.ts"],

    // Print stack traces with every console.* call in tests.
    printConsoleTrace: true,

    // Show full diff when a snapshot fails, not just a patch.
    expandSnapshotDiff: true,

    // TypeScript type-checking is handled by tsc --noEmit (via validate-local.sh
    // and CI). Vitest's built-in typecheck is experimental and redundant here.
    // typecheck: { enabled: false } is the default; omitted for clarity.

    // V8 coverage with AST-accurate remapping, as good as Istanbul.
    coverage: {
      provider: "v8",

      // Report every TS source file. `**/` is load-bearing on vitest 5: a bare `*.ts` matches only the
      // top level. `node_modules/**` is too: this list feeds tinyglobby's `ignore` for untested-file
      // discovery, which otherwise walks dependencies (4439 extra files).
      include: ["**/*.ts"],
      exclude: [
        "node_modules/**",
        "**/.stryker-tmp/**",
        "**/*.test.ts",
        "**/*.d.ts",
        // Test-only helpers, imported by tests and shipped to nobody. Same
        // classification @cplieger/actions makes with "src/test-helpers/**".
        "**/__test-helpers__/**",
        // sw.ts: service worker — runs in ServiceWorkerGlobalScope, not
        // Window, so a page-context runner cannot host PushEvent /
        // NotificationEvent / ServiceWorkerRegistration either.
        "sw.ts",
        // upload.ts: uses XMLHttpRequest.upload progress events, which need a
        // real server to upload to. Chromium can back these — a genuine
        // follow-up, not a permanent exclusion.
        "upload.ts",
        // shell.ts: the terminal is web-terminal-ui's createTerminal (canvas + WebSocket); shell.test.ts
        // covers the panel wiring with it mocked.
        "shell.ts",
      ],

      // Generate coverage even when tests fail.
      reportOnFailure: true,

      // text: human-readable table. text-summary: always-visible totals.
      // lcov: machine-readable for future CI integration.
      reporter: ["text", "text-summary", "lcov"],

      // perFile: true fails on any single file below threshold, not just
      // the aggregate. Start conservative; raise as tests are written.
      thresholds: {
        lines: 60,
        functions: 60,
        branches: 50,
        statements: 60,
        perFile: true,
      },
    },

    // Show full diffs in assertion errors — default truncates at 40 chars.
    chaiConfig: {
      truncateThreshold: 0,
      showDiff: true,
      includeStack: true,
    },
  },
});
