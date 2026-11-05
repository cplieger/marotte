import { describe, it, expect, vi, beforeAll } from "vitest";

// The rail's render signature runs on every transcript paint, so its cost is a switch's cost:
// keyenc is wrapped as a counting passthrough to see whether a render of an unchanged map hashes.
const { joins } = vi.hoisted(() => ({ joins: { n: 0 } }));
vi.mock("@cplieger/keyenc", async (importOriginal) => {
  const real = await importOriginal<typeof import("@cplieger/keyenc")>();
  return {
    ...real,
    join: (...args: Parameters<typeof real.join>) => {
      joins.n++;
      return real.join(...args);
    },
  };
});
// The rail's visibility gate and scroll reads; a transcript with room to scroll shows the map.
vi.mock("./scroll.js", () => {
  const el = document.createElement("div");
  return {
    scrollableBy: () => 500,
    readingLineOffset: () => 0,
    atLiveEdgeNow: () => false,
    getScrollEl: () => el,
    beginSelfScroll: () => undefined,
    endSelfScroll: () => undefined,
    scrollToOffset: () => undefined,
    onReaderGesture: () => () => undefined,
    onTranscriptMutate: () => () => undefined,
    onContentResize: () => () => undefined,
    onAttach: () => () => undefined,
  };
});
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./store-load.js", () => ({
  loadOlderTurns: vi.fn(),
  loadList: vi.fn(),
}));

import { mountTurnRail, loadTurnRail, refreshTurnRail, setResidentTurns } from "./turn-rail.js";
import { apiGet } from "./api-client.js";
import type { TurnSummary } from "./rail-merge.js";

const TURNS = 200;

function summaries(failedAt?: number): TurnSummary[] {
  return Array.from({ length: TURNS }, (_, i) => ({
    id: `m${String(i + 1)}`,
    n: i + 1,
    outcome: i + 1 === failedAt ? "failed" : "completed",
    ts: (i + 1) * 60_000,
  }));
}

function frames(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

/** Every mutation under the map from now on; the callback keeps the records a microtask
 *  delivery would otherwise take before `takeRecords` sees them. */
function watchWrites(): { stop: () => MutationRecord[] } {
  const seen: MutationRecord[] = [];
  const observer = new MutationObserver((records) => {
    seen.push(...records);
  });
  observer.observe(rail(), {
    subtree: true,
    attributes: true,
    childList: true,
    characterData: true,
  });
  return {
    stop: () => {
      seen.push(...observer.takeRecords());
      observer.disconnect();
      return seen;
    },
  };
}

function rail(): HTMLElement {
  const el = document.querySelector<HTMLElement>(".turn-map");
  if (el === null) {
    throw new Error("turn map not mounted");
  }
  return el;
}

describe("the rail's render signature", () => {
  beforeAll(async () => {
    const css = document.createElement("style");
    css.textContent = ".turn-map-track{display:block;block-size:2000px}";
    document.head.appendChild(css);
    const host = document.createElement("div");
    document.body.appendChild(host);
    mountTurnRail(host);
    await frames();
    vi.mocked(apiGet).mockResolvedValue({ turns: summaries() });
    await loadTurnRail("c-sig");
    await frames();
  });

  it("re-renders an unchanged map of 200 turns with no hash and no DOM write", async () => {
    expect(rail().querySelectorAll(".turn-pill").length).toBeGreaterThan(0);
    const watch = watchWrites();
    joins.n = 0;
    for (let i = 0; i < 5; i++) {
      setResidentTurns([]);
    }
    await frames();
    const writes = watch.stop();
    expect(joins.n, "keyenc join calls from unchanged renders").toBe(0);
    expect(writes, "DOM writes from unchanged renders").toHaveLength(0);
  });

  it("repaints when one turn's severity changes", async () => {
    const watch = watchWrites();
    vi.mocked(apiGet).mockResolvedValue({ turns: summaries(TURNS) });
    await refreshTurnRail("c-sig");
    await frames();
    const writes = watch.stop();
    expect(writes.length).toBeGreaterThan(0);
  });
});
