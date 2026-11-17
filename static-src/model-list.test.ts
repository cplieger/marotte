import { beforeEach, describe, expect, it, vi } from "vitest";
import { effect } from "@cplieger/reactive";
import type * as Announce from "./legacy-ui/announce.js";
import type { ModelInfo } from "./types.js";

// Captured rather than read off the live region, which `announce()` writes behind a 100ms timer.
const { announced } = vi.hoisted(() => ({ announced: { said: [] as string[] } }));
vi.mock("./legacy-ui/announce.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Announce>()),
  announce: (message: string) => {
    announced.said.push(message);
  },
}));

import {
  catalogNotice,
  retryCatalog,
  setCatalogModels,
  setCatalogPhase,
  watchCatalog,
} from "./model-list.js";

const MODEL_A: ModelInfo = { model_id: "model-a", model_name: "Model A", rate_multiplier: 1 };

/** What the next Retry's read lands, applied while that read is in flight as app.ts's fetch does. */
let nextReadLands: ModelInfo[] | null = null;
let retries = 0;

beforeEach(() => {
  setCatalogModels([]);
  setCatalogPhase("unknown");
  announced.said.length = 0;
  nextReadLands = null;
  retries = 0;
});

function reread(): Promise<void> {
  retries += 1;
  return Promise.resolve().then(() => {
    if (nextReadLands !== null) {
      setCatalogModels(nextReadLands);
    }
  });
}

describe("the catalog notice", () => {
  it("says it is loading, with no Retry, while nothing has answered", () => {
    expect(catalogNotice()).toEqual({ text: "Loading models…", busy: true, retry: false });
  });

  it("claims no cause for a settled empty catalog and offers a Retry", () => {
    setCatalogPhase("ready");
    const notice = catalogNotice();
    expect(notice).toEqual({ text: "No models available yet.", busy: false, retry: true });
    expect(notice?.text.toLowerCase()).not.toMatch(/account|entitle/);
  });

  it("says the read failed once the bounded refresh gives up", () => {
    setCatalogPhase("unavailable");
    expect(catalogNotice()).toEqual({
      text: "Could not load the model list.",
      busy: false,
      retry: true,
    });
  });

  it("stands down while a cached list exists, whatever a later read answered", () => {
    setCatalogModels([MODEL_A]);
    setCatalogPhase("ready");
    expect(catalogNotice()).toBeNull();
    setCatalogPhase("unavailable");
    expect(catalogNotice()).toBeNull();
  });
});

describe("watchCatalog", () => {
  it("re-runs an effect when the models or the phase move, and not on an unchanged phase", () => {
    let runs = 0;
    const stop = effect(() => {
      watchCatalog();
      runs += 1;
    });
    expect(runs).toBe(1);

    setCatalogPhase("ready");
    expect(runs).toBe(2);
    setCatalogPhase("ready");
    expect(runs).toBe(2);
    setCatalogModels([MODEL_A]);
    expect(runs).toBe(3);
    stop();
  });
});

describe("retryCatalog", () => {
  it("runs the read it is given", () => {
    retryCatalog(reread);
    expect(retries).toBe(1);
  });

  it("says it is asking again, then what a failed re-read answered", async () => {
    setCatalogPhase("unavailable");

    retryCatalog(reread);

    expect(announced.said).toEqual(["Reloading the model list…"]);
    await vi.waitFor(() => {
      expect(announced.said).toEqual([
        "Reloading the model list…",
        "Could not load the model list.",
      ]);
    });
  });

  it("reports a catalog that arrived during the read, not the line it replaced", async () => {
    setCatalogPhase("ready");
    nextReadLands = [MODEL_A];

    retryCatalog(reread);

    await vi.waitFor(() => {
      expect(announced.said).toEqual(["Reloading the model list…", "Model list loaded."]);
    });
  });
});
