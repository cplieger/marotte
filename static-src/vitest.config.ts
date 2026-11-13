// The DEFAULT is the browser: a file runs in real headless Chromium unless named `.node.test.ts`
// (Node), `.touch.test.ts` (a touch-device page) or `.pointer.test.ts` (a page of its own for the
// real mouse). No `environment` option on the browser side and no per-file `@vitest-environment`
// pragma: Browser Mode is not an environment but a runner. The suffix is load-bearing: a
// misplaced Node-needing test throws on import, but one needing the DOM ABSENT passes vacuously
// (`attention-no-dom.node.test.ts` asserts its premise). `*.fuzz.test.ts` is ts-ci's fuzz axis. `channel: "chromium"` is the real
// browser, not headless-shell: `npx playwright install --with-deps chromium`.
import { configDefaults, defineConfig, type TestProjectInlineConfiguration } from "vitest/config";
import type { BrowserCommand } from "vitest/node";
import { playwright } from "@vitest/browser-playwright";
import type { CDPSession } from "playwright";
import { msw } from "msw/vite";

import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { CostSequencer } from "./__test-helpers__/shard-sequencer.js";

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

const dprSessions = new WeakMap<object, { cdp: CDPSession; initialDpr: number }>();

/** A real tap through Playwright's touchscreen, which every engine implements, unlike CDP input. */
const tapCommand: BrowserCommand<[selector: string]> = async ({ iframe }, selector) => {
  await iframe.locator(selector).tap();
};

/** Move the real pointer to (-1, -1), outside the viewport, where it hovers nothing. */
const parkPointerCommand: BrowserCommand<[]> = async ({ page }) => {
  await page.mouse.move(-1, -1);
};

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
      // Merged with the root list (`extends: true` merges arrays). Browser-only: both read `window`,
      // and a `ResizeObserver` loop is a real engine's verdict.
      setupFiles: ["./ro-loop-gate.ts", "./storage-reset-setup.ts", "./pointer-park-setup.ts"],
      // Files run in parallel because each worker session opens its own Playwright context, and the
      // mocker's routes live on the context: two concurrent files never share one
      // (https://github.com/vitest-dev/vitest/issues/8339).
      browser: {
        enabled: true,
        headless: true,
        traceView,
        provider: playwright({
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
          tap: tapCommand,
          parkPointer: parkPointerCommand,
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
          /** Set the page's device pixel ratio; null restores the ratio the page had when this command first ran on
           *  it. Chromium only, CI's engine. The override is page-wide and outlives the test that set it. A restore
           *  that leaves the top page at any size but its viewport throws. */
          async setDeviceScaleFactor({ page }, dpr: number | null) {
            // An Emulation override lasts as long as the CDP session that set it, so the session is kept per page.
            let session = dprSessions.get(page);
            if (session === undefined) {
              session = {
                cdp: await page.context().newCDPSession(page),
                initialDpr: await page.evaluate<number>("window.devicePixelRatio"),
              };
              dprSessions.set(page, session);
            }
            const size = page.viewportSize() ?? { width: 1280, height: 720 };
            // Not `Emulation.clearDeviceMetricsOverride`: Chromium disables the page's device emulation outright
            // (content/browser/devtools/protocol/emulation_handler.cc), Playwright's viewport included, and every
            // later file on the page inherits the window's content area, since Playwright (`_updateViewport`) and
            // vitest (`currentViewport`) cache the viewport they last set. The restore re-sends Playwright's override.
            await session.cdp.send("Emulation.setDeviceMetricsOverride", {
              width: size.width,
              height: size.height,
              screenWidth: size.width,
              screenHeight: size.height,
              deviceScaleFactor: dpr ?? session.initialDpr,
              mobile: false,
              screenOrientation: { angle: 0, type: "landscapePrimary" },
            });
            if (dpr !== null) {
              return;
            }
            const [width, height] = await page.evaluate<[number, number]>(
              "[window.innerWidth, window.innerHeight]",
            );
            if (width !== size.width || height !== size.height) {
              throw new Error(
                `the DPR restore left the page at ${String(width)}x${String(height)}, not its ` +
                  `${String(size.width)}x${String(size.height)} viewport`,
              );
            }
          },
          /** Real mouse clicks at `points` (CSS px in a tester frame `frameWidth` wide), each pressed
           *  `gapMs` after the last one's release and released `holdMs` after its press; answers once
           *  they are scheduled. Raw CDP input, unawaited: Playwright's `mouse` waits for each
           *  event's ack, and a move is acked only after a busy main thread frees, so its press would
           *  reach the page after the long task instead of waiting in the input queue behind it. */
          async queueClicks(
            { page, frame },
            points: readonly { readonly x: number; readonly y: number }[],
            gapMs: number,
            holdMs: number,
            frameWidth: number,
          ) {
            const box = await (await (await frame()).frameElement()).boundingBox();
            if (box === null) {
              throw new Error("the tester frame has no box");
            }
            const scale = box.width / frameWidth;
            const cdp = await page.context().newCDPSession(page);
            const sent: Promise<unknown>[] = [];
            const send = (
              type: "mouseMoved" | "mousePressed" | "mouseReleased",
              x: number,
              y: number,
            ): void => {
              const pressed = type === "mousePressed";
              const ack = cdp
                .send("Input.dispatchMouseEvent", {
                  type,
                  x,
                  y,
                  button: type === "mouseMoved" ? "none" : "left",
                  buttons: pressed ? 1 : 0,
                  clickCount: type === "mouseMoved" ? 0 : 1,
                })
                .catch((e: unknown) => {
                  console.error("queueClicks", type, e);
                });
              sent.push(ack);
            };
            const sleep = (ms: number): Promise<void> =>
              new Promise((resolve) => setTimeout(resolve, ms));
            void (async () => {
              for (const p of points) {
                await sleep(gapMs);
                const x = box.x + p.x * scale;
                const y = box.y + p.y * scale;
                send("mouseMoved", x, y);
                send("mousePressed", x, y);
                await sleep(holdMs);
                send("mouseReleased", x, y);
              }
              // Detach only once every event is acked, so no event is still in flight on the session.
              await Promise.all(sent);
              await cdp.detach().catch((e: unknown) => {
                console.error("queueClicks detach", e);
              });
            })();
          },
        },
      },
    },
  };
}

/** The scroll suite in the engines CI does not run, at the widths and pixel ratios of a desktop and a phone. Opt-in
 *  (`SCROLL_ENGINES=1`): it needs `npx playwright install firefox webkit`. */
const engineFiles = ["scroll.visual.test.ts", "scroll.platform.test.ts"];
const engineInstances = (
  [
    { browser: "firefox", width: 1440, height: 900, dpr: 1, touch: false },
    { browser: "firefox", width: 1440, height: 900, dpr: 1.5, touch: false },
    { browser: "firefox", width: 390, height: 844, dpr: 3, touch: true },
    { browser: "webkit", width: 1440, height: 900, dpr: 1.5, touch: true },
    { browser: "webkit", width: 390, height: 844, dpr: 2, touch: true },
    { browser: "webkit", width: 390, height: 844, dpr: 3, touch: true },
  ] as const
).map(({ browser, width, height, dpr, touch }) => ({
  browser,
  name: `${browser}-${String(width)}-dpr${String(dpr)}`,
  viewport: { width, height },
  provider: playwright({ contextOptions: { deviceScaleFactor: dpr, hasTouch: touch } }),
}));

/** The touch files whose taps go through `tap` rather than CDP, run again in WebKit on a phone's
 *  viewport. Opt-in (`TOUCH_ENGINES=1`): it needs `npx playwright install webkit`, which CI does not. */
const touchEngineFiles = ["slash-menu.touch.test.ts"];

export default defineConfig({
  // Serves the MSW worker script from the package for the browser tests that mock the network;
  // each such test starts its own worker.
  plugins: [msw({ mode: "worker-only" })],
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
        // Suites against the REAL server (the SSE lifecycle, a preview's own tab driven through Playwright):
        // `SSE_FIXTURE` names a `-tags marotte_test` binary the globalSetup starts on a scratch dir and free
        // port; every file skips when it is unset. Node, not the browser: the fixture refuses cross-site
        // POSTs and sends no CORS header.
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
        [...sharedExclude, "**/*.node.test.ts", "**/*.touch.test.ts", "**/*.pointer.test.ts"],
      ),
      // `*.pointer.test.ts` runs in a page of its own, so the real mouse it moves reaches no later file: after one real
      // mouse event Chromium keeps the page's hover tracking on, and its hit tests force layouts a later file never
      // sees otherwise. Moving the pointer out of the viewport does not turn it off.
      browserProject("browser-pointer", ["**/*.pointer.test.ts"], sharedExclude),
      browserProject("browser-touch", ["**/*.touch.test.ts"], sharedExclude, true),
      ...(process.env["SCROLL_ENGINES"] === "1"
        ? [
            {
              extends: true,
              test: {
                name: "browser-engines",
                include: engineFiles,
                exclude: sharedExclude,
                setupFiles: [
                  "./ro-loop-gate.ts",
                  "./storage-reset-setup.ts",
                  "./pointer-park-setup.ts",
                ],
                browser: {
                  enabled: true,
                  headless: true,
                  screenshotFailures: false,
                  provider: playwright(),
                  instances: engineInstances,
                  commands: { parkPointer: parkPointerCommand },
                },
              },
            } satisfies TestProjectInlineConfiguration,
          ]
        : []),
      ...(process.env["TOUCH_ENGINES"] === "1"
        ? [
            {
              extends: true,
              test: {
                name: "browser-touch-webkit",
                include: touchEngineFiles,
                exclude: sharedExclude,
                setupFiles: [
                  "./ro-loop-gate.ts",
                  "./storage-reset-setup.ts",
                  "./pointer-park-setup.ts",
                ],
                browser: {
                  enabled: true,
                  headless: true,
                  screenshotFailures: false,
                  provider: playwright({ contextOptions: { hasTouch: true } }),
                  instances: [{ browser: "webkit" }],
                  viewport: { width: 390, height: 844 },
                  commands: { tap: tapCommand, parkPointer: parkPointerCommand },
                },
              },
            } satisfies TestProjectInlineConfiguration,
          ]
        : []),
    ],

    // Two workers, so two Chromium pages at once: CI's runner has 4 vCPUs, and each page's renderer
    // shares them with the Vite server and the runner process. Root-only: a per-project value needs a
    // distinct `sequence.groupOrder` per project. Stryker's runner forces its own value of 1.
    maxWorkers: 2,

    // Fail loudly if the include pattern matches nothing.
    passWithNoTests: false,

    // Forbid .only tests unconditionally — not just in CI.
    allowOnly: false,

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
      sequencer: CostSequencer,
      shuffle: { files: false, tests: false },
      concurrent: false,
      hooks: "stack",
    },

    // Loaded once per worker before any test file. Configures fast-check
    // global defaults (numRuns, verbosity, time limits). See file for
    // tuning rationale.
    setupFiles: ["./fc-strict-setup.ts", "./waitfor-budget-setup.ts"],

    printConsoleTrace: true,

    // Show full diff when a snapshot fails, not just a patch.
    expandSnapshotDiff: true,

    // TypeScript type-checking is handled by tsc --noEmit (via validate-local.sh
    // and CI). Vitest's built-in typecheck is experimental and redundant here.
    // typecheck: { enabled: false } is the default; omitted for clarity.

    coverage: {
      provider: "v8",

      // `**/` is load-bearing on vitest 5: a bare `*.ts` matches only the top level.
      // `node_modules/**` is too: this list feeds tinyglobby's `ignore` for untested-file
      // discovery, which otherwise walks dependencies (4439 extra files).
      include: ["**/*.ts"],
      exclude: [
        "node_modules/**",
        "**/.stryker-tmp/**",
        "**/*.test.ts",
        "**/*.d.ts",
        // Test-only helpers, imported by tests and shipped to nobody.
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
