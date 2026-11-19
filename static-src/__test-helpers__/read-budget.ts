// The read-budget check: the canned app (canned-app.ts) driven through a tier 2 scenario's windows,
// counting every request by route and the largest answer per route in each window.
import { commands } from "vitest/browser";
import { expect, it, vi } from "vitest";

import { bootCanned, clickRow, QUIET_MS, tabRow, type WindowName } from "./canned-app.js";

declare module "vitest/internal/browser" {
  interface BrowserCommands {
    readBudget(view: string, counts: ViewCounts): Promise<{ recording: boolean; budget: unknown }>;
  }
}

const WINDOWS = ["cold", "rest", "live", "away", "back"] as const satisfies readonly WindowName[];

interface RouteCount {
  count: number;
  maxBytes: number;
}

/** One window's requests by route, and the page's stream topics at its end (null: everything). */
interface WindowCounts {
  readonly routes: { [route: string]: RouteCount };
  readonly topics: readonly string[] | null;
}

type ViewCounts = { readonly [W in WindowName]: WindowCounts };

/** The tree read every run surface replaced with its shapes; no window may issue it. */
const RUN_TREE_ROUTE = "GET /api/runs/{run}";

// view.mjs's own rest window.
const REST_MS = 15_000;

/** Boots the app at `scenario`'s view and runs its windows, returning each window's counts. */
async function measure(scenario: string): Promise<{ counts: ViewCounts; missing: Set<string> }> {
  const app = await bootCanned(scenario);
  const { view, drivers } = app;
  const out: Partial<Record<WindowName, WindowCounts>> = {};
  let from = 0;
  const close = (name: WindowName): void => {
    const routes: { [route: string]: RouteCount } = {};
    for (const c of app.counted.slice(from)) {
      const r = (routes[c.route] ??= { count: 0, maxBytes: 0 });
      r.count++;
      r.maxBytes = Math.max(r.maxBytes, c.bytes);
    }
    out[name] = { routes, topics: app.hub.held() };
  };
  const measureWindow = async (name: WindowName, body: () => Promise<void>): Promise<void> => {
    from = app.counted.length;
    await body();
    close(name);
  };

  close("cold");
  await app.advance(QUIET_MS);
  await measureWindow("rest", () => app.advance(REST_MS));
  await measureWindow("live", async () => {
    await app.replay("live");
    await app.settle(false);
  });
  await measureWindow("away", async () => {
    if (view.shell !== true) {
      clickRow(tabRow(view.away));
      await app.settle(false);
    } else if (drivers["away"] === "shell-closed") {
      app.toggleShell();
    }
    await app.replay("away");
    await app.settle(false);
  });
  await measureWindow("back", async () => {
    if (view.shell === true) {
      app.toggleShell();
    } else {
      clickRow(view.opener ?? tabRow(view.tab));
    }
    await app.settle(true);
  });
  vi.useRealTimers();
  return { counts: out as ViewCounts, missing: new Set(app.missing) };
}

/** The read-budget case for one tier 2 scenario: its windows' counts equal its entry in
 *  perf/read-budgets.json, which READ_BUDGET_RECORD=1 rewrites from this run. */
export function readBudgetCase(scenario: string): void {
  it(`${scenario} reads what its budget holds in every window`, { timeout: 180_000 }, async () => {
    const { counts, missing } = await measure(scenario);
    expect(
      [...missing],
      "routes the canned set has no answer for: run tests/perf/record-canned.sh",
    ).toEqual([]);
    for (const w of WINDOWS) {
      expect(Object.keys(counts[w].routes), `${w} read the whole run tree`).not.toContain(
        RUN_TREE_ROUTE,
      );
    }
    const { recording, budget } = await commands.readBudget(scenario, counts);
    if (!recording) {
      expect(
        counts,
        `perf/read-budgets.json's ${scenario}; after a deliberate change, rerun with READ_BUDGET_RECORD=1`,
      ).toEqual(budget);
    }
  });
}
