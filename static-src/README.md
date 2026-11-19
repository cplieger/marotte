# web/static-src

TypeScript source for the Marotte web UI. Compiled to static assets
served by the Go backend.

## Structure

| Path | Purpose |
| --- | --- |
| `actions/` | Declarative action framework (mutations, retry, scope, toast) |
| `handlers/` | SSE message handlers (turn, pending, system) |
| `wire/` | Generated protocol decoders and types |
| `css/` | Stylesheets (numbered for load order) |
| `legacy-ui/` | The popup, popover, dialog, toast, tooltip and other widgets, with their tests and base stylesheet |
| `*.ts` | UI modules (DOM, state, rendering) |
| `*.test.ts` | Co-located unit tests |

## Development

```sh
npm test              # single pass (vitest --run)
npm run test:watch    # watch mode
npm run test:coverage # with V8 coverage
npm run typecheck     # tsc type-check (source, service worker, vitest configs)
npm run typecheck:tests # tsc type-check (tests)
```

## Test environment

Six vitest projects, and the DEFAULT is the browser (the sixth, `e2e-sse`, runs against a real server binary: CI builds it and runs the project, and locally it skips unless `SSE_FIXTURE` is set; see CONTRIBUTING.md "Checks"):

- **browser**: headless Chromium via `@vitest/browser-playwright`. Every test
  runs here unless its filename opts out. No `environment` option and no
  per-file pragma: Browser Mode is a runner, not an environment.
- **browser-touch**: the `*.touch.test.ts` suffix, for a test that needs a
  touch-only device: the same Chromium, with pages that are touch devices from
  creation and no mouse. Touch emulation cannot be switched off inside a page
  without leaving it with no pointer at all, so it gets its own project.
- **browser-pointer**: the `*.pointer.test.ts` suffix runs a file in a page of
  its own, so the real mouse it moves (a `userEvent` click or hover) reaches no
  later file: once a real mouse event reaches a page, Chromium keeps tracking
  hover there, which changes when layout runs in every file after it.
- **browser-align-dpr2**: `text-alignment.test.ts` again in Chromium at a real
  device scale of 2, where Chromium rounds text metrics to device pixels and
  some rows are off only there. `ALIGN_ENGINES=1` adds WebKit at 1 and 2.
- **node**: the `*.node.test.ts` suffix, for a test that needs genuine Node
  capabilities (spawning a process, walking a directory) or needs the DOM to be
  ABSENT. The reason goes in the stem, the placement in the suffix:
  `attention-no-dom.node.test.ts`.
- Reading a shipped file uses a Vite `?raw` import rather than `node:fs`, which
  is what lets a suite that checks source text stay in the browser project.
- Property-based tests use `fast-check` with strict setup (`fc-strict-setup.ts`)
- All mocks auto-reset between tests (`restoreMocks: true`)
- A `--run` fails, printing `Stall watchdog`, when a running file sends no
  test or hook event for the longer of 150 s and what its running test may
  take (its timeout times its attempts, plus 30 s), or the run sends none for
  150 s while no file runs (`__test-helpers__/stall-watchdog.ts`): a page
  stopped answering, which would otherwise hold the run until the CI job's
  timeout. A hook gets no allowance of its own, so a hook given a timeout
  above `SILENCE_FLOOR_MS` in that file needs the constant raised above that
  timeout.

The browser is installed once with `npx --no-install playwright install chromium`;
CI installs it from the `@vitest/browser-playwright` devDependency.

## Key conventions

- `requireAssertions: true`: every test must call `expect()`
- Browser Mode isolates per test FILE; the node project sets `isolate: true`
- `vi.resetModules()` does NOT re-evaluate a module in the browser (the module
  map is URL-keyed). A test that needs a fresh module graph busts the specifier:
  ``await import(/* @vite-ignore */ `./x.ts?boot=${n}`)``; keep the `.ts`.
- A `vi.mock` factory must name every export the graph imports, because the
  browser links ESM for real, and it runs ONCE: a per-test `vi.doMock` cannot
  replace a mock that is already cached, so per-test values reach a factory
  through module state rather than a closure.
- Test files co-located with source: `foo.ts` → `foo.test.ts`
- Actions use `defineAction` / `apiAction` / `transportAction` factories
