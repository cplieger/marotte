// Does anything WRITE `data-severity`? `turn-outcome-css.test.ts` pins the hue partition, but a rule
// keyed on an unwritten attribute fails silently, so the two files are one guard. The writers are
// `updateTurnFooter`, `updateTurnHeader` and `turn-rail.ts`'s marker + cluster, driven for real:
// a second copy of `severityOf`'s table would pass with every writer deleted.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from "vitest";

// The spread mocks below need the ORIGINAL module's type, and `import()` type
// annotations are forbidden by the shared eslint config — so the two modules are
// type-imported here, the way handlers/turn.test.ts already does it.
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

type ApiClientModule = typeof ApiClient;
type StoreLoadModule = typeof StoreLoad;

// The rail imports scroll.ts (a `#messages` singleton at load) and api-client; neither is under
// test. Stubs go through `vi.hoisted`.
const { scrollable } = vi.hoisted(() => ({
  scrollable: { by: 500 },
}));
vi.mock("./scroll.js", () => ({
  scrollableBy: () => scrollable.by,
  readingLineOffset: () => 0,
  atLiveEdgeNow: () => false,
  beginSelfScroll: vi.fn(),
  endSelfScroll: vi.fn(),
  scrollToOffset: vi.fn(),
  onReaderGesture: () => () => {
    /* no reader in this suite */
  },
  onTranscriptMutate: () => () => undefined,
  onContentResize: () => () => undefined,
  onAttach: () => () => undefined,
  getScrollEl: () => ({
    scrollTop: 0,
    clientHeight: 600,
    clientTop: 0,
    addEventListener: () => {
      /* the rail's own re-measure is not under test */
    },
    removeEventListener: () => undefined,
    getBoundingClientRect: () => ({ top: 0, bottom: 600, height: 600 }),
  }),
}));
// `apiGet` alone, the rest real: the header's graph reaches `apiGetTyped`.
vi.mock("./api-client.js", async (orig) => ({
  ...(await orig<ApiClientModule>()),
  apiGet: vi.fn(),
}));
vi.mock("./store-load.js", async (orig) => ({
  ...(await orig<StoreLoadModule>()),
  loadMessages: vi.fn(),
  loadList: vi.fn(),
}));

import { buildTurnFooter } from "./fundamentals/turn-footer.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { mountTurnRail, loadTurnRail, resetTurnRail, type TurnSummary } from "./turn-rail.js";
import { apiGet } from "./api-client.js";
import { severityOf } from "./turn-severity.js";
import type { TurnOutcome } from "./turns.js";

/** Every outcome the wire can send. The severity coverage case in
 *  `turn-outcome-css.test.ts` is what pins this list total. */
const OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
  "empty",
];

describe("the footer stamps its severity", () => {
  it("writes data-severity from the shared table, for every outcome", () => {
    for (const outcome of OUTCOMES) {
      const footer = buildTurnFooter({ outcome });
      expect(footer.dataset["severity"], outcome).toBe(severityOf(outcome));
      // Both attributes, because they answer different questions: `data-outcome`
      // still carries the lead WORD and the one `unknown` hue exception.
      expect(footer.dataset["outcome"], outcome).toBe(outcome);
    }
  });

  it("defaults an ABSENT outcome to clean, matching the key it defaults", () => {
    // A legacy turn has no outcome and the footer defaults to `completed`; severity must default too.
    const footer = buildTurnFooter({ kindCounts: { execute: 1 } });
    expect(footer.dataset["outcome"]).toBe("completed");
    expect(footer.dataset["severity"]).toBe("clean");
  });
});

describe("the header stamps its severity", () => {
  it("writes data-severity from the shared table, for every outcome", () => {
    for (const outcome of OUTCOMES) {
      const header = buildTurnHeader({
        n: 1,
        outcome,
        ts: 1_700_000_000_000,
        request: "do the thing",
        attachments: [],
      });
      expect(header.dataset["severity"], outcome).toBe(severityOf(outcome));
      expect(header.dataset["outcome"], outcome).toBe(outcome);
    }
  });
});

describe("the turn map stamps its severity", () => {
  const host = document.createElement("div");

  // Browser Mode serves no CSS, and the map lays no rows until its track has measured a height.
  const trackCSS = document.createElement("style");
  trackCSS.textContent = ".turn-map-track{display:block;block-size:2000px}";

  beforeAll(async () => {
    document.head.appendChild(trackCSS);
    document.body.appendChild(host);
    mountTurnRail(host);
    await frames(2);
  });

  afterAll(() => {
    trackCSS.remove();
  });

  beforeEach(async () => {
    resetTurnRail();
    track().style.removeProperty("height");
    await frames(2);
  });

  function track(): HTMLElement {
    const el = host.querySelector<HTMLElement>(".turn-map .turn-map-track");
    if (el === null) {
      throw new Error("turn map not mounted");
    }
    return el;
  }

  function stack(): HTMLElement {
    const el = host.querySelector<HTMLElement>(".turn-map .turn-map-stack");
    if (el === null) {
      throw new Error("turn map not mounted");
    }
    return el;
  }

  function pills(): HTMLElement[] {
    return [...stack().querySelectorAll<HTMLElement>(".turn-pill")];
  }

  function frames(n: number): Promise<void> {
    return new Promise((resolve) => {
      const step = (left: number): void => {
        if (left === 0) {
          resolve();
          return;
        }
        requestAnimationFrame(() => {
          step(left - 1);
        });
      };
      step(n);
    });
  }

  it("writes data-severity on every row, for every outcome", async () => {
    // One turn per outcome, so the assertion is a partition rather than a sample.
    const index: TurnSummary[] = OUTCOMES.map((outcome, i) => ({
      id: `m${String(i + 1)}`,
      n: i + 1,
      outcome,
      ts: (i + 1) * 60_000,
    }));
    vi.mocked(apiGet).mockResolvedValue({ turns: index });
    await loadTurnRail("c-attr");
    await frames(2);

    const rows = pills();
    expect(rows, "one row per turn").toHaveLength(OUTCOMES.length);
    for (const [i, row] of rows.entries()) {
      const outcome = OUTCOMES[i] ?? "completed";
      expect(row.dataset["severity"], `row ${String(i)}`).toBe(severityOf(outcome));
      expect(row.querySelector("a")?.getAttribute("aria-label"), `row ${String(i)}`).toMatch(
        new RegExp(`^Turn ${String(i + 1)}\\b`, "u"),
      );
    }
  });

  it("reaches every turn of a session too long for one row each, and grades the bin worst", async () => {
    const index: TurnSummary[] = Array.from({ length: 200 }, (_, i) => ({
      id: `m${String(i + 1)}`,
      n: i + 1,
      outcome: (i === 42 ? "failed" : "completed") satisfies TurnOutcome as TurnOutcome,
      ts: (i + 1) * 60_000,
    }));
    vi.mocked(apiGet).mockResolvedValue({ turns: index });
    // A 100px track holds 50 rows at the 2px floor, so 200 turns bin four to a row.
    track().style.height = "100px";
    await loadTurnRail("c-long");
    await frames(3);

    const rows = pills();
    expect(rows).toHaveLength(50);
    const broken = rows.filter((r) => r.dataset["severity"] === "broken");
    expect(broken.map((r) => r.querySelector("a")?.getAttribute("aria-label"))).toEqual([
      "Turns 41 to 44, worst failed",
    ]);
    expect(broken[0]?.querySelector("a")?.getAttribute("href")).toMatch(/#turn-43$/u);
  });
});
