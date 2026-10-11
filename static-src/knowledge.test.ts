// api-client, the knowledge actions, confirm, toast and bus are mocked to control payloads and assert the DOM.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

vi.mock("./toast.js", () => ({ showToast: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE_UI: "<svg data-close></svg>",
  ICON_PLUS_UI: "<svg data-plus></svg>",
  ICON_TRASH_UI: "<svg data-trash></svg>",
  ICON_REFRESH: "<svg data-refresh></svg>",
}));
vi.mock("./bus.js", () => ({ onSSE: vi.fn(() => () => undefined) }));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(() => () => undefined),
  registerCleanup: vi.fn(),
}));
vi.mock("./actions/knowledge.js", () => ({
  addKnowledge: { dispatch: vi.fn() },
  removeKnowledge: { dispatch: vi.fn() },
  reindexKnowledge: { dispatch: vi.fn() },
  cancelKnowledgeIndexing: { dispatch: vi.fn() },
  clearKnowledge: { dispatch: vi.fn() },
}));
vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(),
  CancellableSlot: class {
    start(): AbortSignal {
      return new AbortController().signal;
    }
    abort(): void {
      /* noop */
    }
  },
  // Present-but-inert for real-ESM linking; no case calls them.
  apiGet: vi.fn(),
}));
vi.mock("./dom.js", () => ({ byId: (id: string) => document.getElementById(id) }));

import { apiGetTyped } from "./api-client.js";
import { onSSE } from "./bus.js";
import { confirm as confirmDialog } from "./confirm.js";
import { showToast } from "./toast.js";
import {
  addKnowledge,
  cancelKnowledgeIndexing,
  clearKnowledge,
  reindexKnowledge,
  removeKnowledge,
} from "./actions/knowledge.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

const mockGet = vi.mocked(apiGetTyped);
const mockConfirm = vi.mocked(confirmDialog);
const mockAdd = vi.mocked(addKnowledge.dispatch);
const mockRemove = vi.mocked(removeKnowledge.dispatch);
const mockReindex = vi.mocked(reindexKnowledge.dispatch);
const mockCancel = vi.mocked(cancelKnowledgeIndexing.dispatch);
const mockClear = vi.mocked(clearKnowledge.dispatch);

/** Flushes the fetch().then(render) and refreshHint chains without advancing the 1500ms poll timer. */
async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

function seedDom(): void {
  document.body.innerHTML = `
    <div id="knowledge-section">
      <button id="knowledge-add-btn"></button>
      <button id="knowledge-clear-btn" class="hidden"></button>
      <p id="knowledge-hint" hidden></p>
      <div id="knowledge-list"><div class="list-empty">No knowledge bases yet.</div></div>
    </div>`;
}

const list = (): HTMLElement => document.getElementById("knowledge-list") as HTMLElement;

const hint = (): HTMLElement => document.getElementById("knowledge-hint") as HTMLElement;

/** Two GETs through one apiGetTyped, routed by path; `settings` null models a network or decode failure. */
function routeGets(listAnswer: unknown, settings: unknown): void {
  mockGet.mockImplementation((path: string) =>
    Promise.resolve(path === "/api/settings" ? settings : listAnswer),
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  seedDom();
  // Knowledge enabled by default; tests override the list payload.
  routeGets({ contexts: [] }, settingsPayload());
});

afterEach(() => {
  // Discards any pending poll timer.
  vi.useRealTimers();
});

describe("initKnowledge", () => {
  it("sets the add icon and builds the inline form", () => {
    initKnowledge();
    expect(document.getElementById("knowledge-add-btn")?.innerHTML).toContain("data-plus");
    expect(document.getElementById("knowledge-add-form")).not.toBeNull();
  });

  // knowledge_indexing names a per-agent store this list cannot read.
  it("subscribes to no SSE at all", () => {
    initKnowledge();
    expect(vi.mocked(onSSE)).not.toHaveBeenCalled();
  });

  it("toggles the add form open on + click", () => {
    initKnowledge();
    const form = document.getElementById("knowledge-add-form") as HTMLFormElement;
    expect(form.hidden).toBe(true);
    (document.getElementById("knowledge-add-btn") as HTMLButtonElement).click();
    expect(form.hidden).toBe(false);
  });
});

describe("loadKnowledge render", () => {
  it("renders a context row with item count + path", async () => {
    mockGet.mockResolvedValue({
      contexts: [{ name: "docs", id: "abc12345", item_count: 7, path: "internal/api" }],
    });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("docs");
    expect(list().textContent).toContain("7 items");
    expect(list().textContent).toContain("internal/api");
  });

  it("renders an indexing row with a progress bar + percentage", async () => {
    mockGet.mockResolvedValue({
      contexts: [{ name: "big", id: "op1", item_count: 0, items_display: "42%", indexing: true }],
    });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("Indexing… 42%");
    // A native <progress>; `position` because value with `max` decides what is drawn.
    const bar = list().querySelector<HTMLProgressElement>("progress.knowledge-bar");
    expect(bar).not.toBeNull();
    expect(bar?.max).toBe(100);
    expect(bar?.value).toBe(42);
    expect(bar?.position).toBeCloseTo(0.42, 5);
    // Native <progress> reports its value, so a name is all it needs.
    expect(bar?.getAttribute("aria-label")).toBe("Indexing");
  });

  // A valueless <progress> renders an animated indeterminate bar, which beside "Cancelled" claims stopped work; no bar.
  for (const display of ["Cancelled", "Failed", undefined]) {
    it(`renders no bar at all while indexing reports ${display ?? "nothing"}`, async () => {
      mockGet.mockResolvedValue({
        contexts: [
          { name: "big", id: "op1", item_count: 0, items_display: display, indexing: true },
        ],
      });
      loadKnowledge();
      await flush();
      expect(list().querySelector("progress")).toBeNull();
      // The text still says what happened.
      expect(list().textContent).toContain(display === undefined ? "Indexing…" : display);
    });
  }

  it("merges duplicate names during an add, preferring the indexing entry", async () => {
    mockGet.mockResolvedValue({
      contexts: [
        { name: "docs", id: "ctx", item_count: 0, description: "…(indexing...)" },
        { name: "docs", id: "op", item_count: 0, items_display: "12%", indexing: true },
      ],
    });
    loadKnowledge();
    await flush();
    expect(list().querySelectorAll(".knowledge-row").length).toBe(1);
    expect(list().textContent).toContain("Indexing… 12%");
  });

  it("shows the empty state for no bases", async () => {
    mockGet.mockResolvedValue({ contexts: [] });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("No knowledge bases yet.");
  });

  it("shows an error state on fetch failure", async () => {
    mockGet.mockResolvedValue(null);
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("Could not load knowledge bases.");
  });

  it("shows the enable hint when knowledge_enabled is off", async () => {
    routeGets({ contexts: [] }, settingsPayload({ knowledge_enabled: false }));
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(false);
  });

  it("keeps the enable hint hidden when knowledge_enabled is on", async () => {
    routeGets({ contexts: [] }, settingsPayload({ knowledge_enabled: true }));
    hint().hidden = false;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(true);
  });

  // A null answer is a failure, not "knowledge is off", so the hint keeps its state.
  it("leaves a visible hint visible when /api/settings answers null", async () => {
    routeGets({ contexts: [] }, null);
    hint().hidden = false;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(false);
  });

  it("leaves a hidden hint hidden when /api/settings answers null", async () => {
    routeGets({ contexts: [] }, null);
    hint().hidden = true;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(true);
  });
});

describe("add flow", () => {
  /** Only `.outcome` is read: `error: false` keeps the failure off the toasts and the outcome carries the message. */
  function addAnswers(outcome: { status: string; value?: unknown; error?: { message: string } }) {
    mockAdd.mockReturnValue(
      Object.assign(
        Promise.resolve(outcome.status === "success" ? (outcome.value ?? null) : null),
        {
          abort: () => undefined,
          outcome: Promise.resolve(outcome),
        },
      ) as never,
    );
  }

  function submit(path: string): void {
    (document.getElementById("knowledge-add-path") as HTMLInputElement).value = path;
    (document.getElementById("knowledge-add-form") as HTMLFormElement).dispatchEvent(
      new Event("submit", { cancelable: true }),
    );
  }

  function errorText(): string {
    return document.getElementById("knowledge-add-error")?.textContent ?? "";
  }

  it("dispatches knowledge.add with the entered path and refetches", async () => {
    initKnowledge();
    addAnswers({ status: "success", value: { message: "Indexing 'docs' in background" } });
    mockGet.mockResolvedValue({ contexts: [] });

    submit("docs");
    await flush();

    expect(mockAdd).toHaveBeenCalledWith({ path: "docs", name: "" });
    expect(vi.mocked(showToast)).toHaveBeenCalled();
    expect((document.getElementById("knowledge-add-form") as HTMLFormElement).hidden).toBe(true);
  });

  // `error: false`, so nothing else reports this failure.
  it("writes the server's own message beside the field when the add fails", async () => {
    initKnowledge();
    addAnswers({ status: "error", error: { message: "path does not exist: bad/path" } });
    (document.getElementById("knowledge-add-form") as HTMLFormElement).hidden = false;

    submit("bad/path");
    await flush();

    expect(errorText()).toBe("path does not exist: bad/path");
    expect(vi.mocked(showToast)).not.toHaveBeenCalled();
    expect((document.getElementById("knowledge-add-form") as HTMLFormElement).hidden).toBe(false);
  });

  it("clears a previous failure when the next attempt succeeds", async () => {
    initKnowledge();
    addAnswers({ status: "error", error: { message: "path does not exist: bad/path" } });
    (document.getElementById("knowledge-add-form") as HTMLFormElement).hidden = false;
    submit("bad/path");
    await flush();
    expect(errorText()).not.toBe("");

    addAnswers({ status: "success", value: {} });
    mockGet.mockResolvedValue({ contexts: [] });
    submit("docs");
    await flush();

    expect(errorText()).toBe("");
  });
});

describe("remove flow", () => {
  async function renderOneRow(): Promise<void> {
    mockGet.mockResolvedValue({ contexts: [{ name: "docs", id: "a", item_count: 3 }] });
    loadKnowledge();
    await flush();
  }

  it("dispatches knowledge.remove after a confirmed destructive prompt", async () => {
    await renderOneRow();
    mockConfirm.mockResolvedValue(true);
    (list().querySelector(".knowledge-remove") as HTMLButtonElement).click();
    await flush();
    expect(mockConfirm).toHaveBeenCalledWith(expect.any(String), "Remove", "destructive");
    expect(mockRemove).toHaveBeenCalledWith({ name: "docs" }, expect.any(Object));
  });

  it("does not remove when the confirm is cancelled", async () => {
    await renderOneRow();
    mockConfirm.mockResolvedValue(false);
    (list().querySelector(".knowledge-remove") as HTMLButtonElement).click();
    await flush();
    expect(mockRemove).not.toHaveBeenCalled();
  });
});

describe("reindex flow", () => {
  async function renderSettledRow(): Promise<void> {
    mockGet.mockResolvedValue({
      contexts: [{ name: "docs", id: "a", item_count: 3, path: "internal/api" }],
    });
    loadKnowledge();
    await flush();
  }

  const control = (): HTMLButtonElement =>
    list().querySelector(".knowledge-reindex") as HTMLButtonElement;

  it("dispatches knowledge.reindex for the row's own name, and confirms nothing", async () => {
    await renderSettledRow();
    control().click();
    await flush();
    // Not destructive: only the index is rebuilt.
    expect(mockConfirm).not.toHaveBeenCalled();
    expect(mockReindex).toHaveBeenCalledWith({ name: "docs" }, expect.any(Object));
  });

  // The row's signature does not move when a re-index starts, so the refetch turns it into an indexing row.
  it("says so and refetches on success", async () => {
    await renderSettledRow();
    const before = mockGet.mock.calls.length;
    control().click();
    await flush();

    const opts = mockReindex.mock.calls[0]?.[1] as { onSuccess: () => void } | undefined;
    opts?.onSuccess();
    await flush();

    expect(vi.mocked(showToast)).toHaveBeenCalledWith(
      expect.stringContaining('Re-indexing "docs"'),
      "success",
    );
    expect(mockGet.mock.calls.length).toBeGreaterThan(before);
  });

  // A base already indexing shows progress, and the server refuses a name no settled base holds.
  it("offers no control while the base is still indexing", async () => {
    mockGet.mockResolvedValue({
      contexts: [{ name: "docs", id: "a", item_count: 0, items_display: "12%", indexing: true }],
    });
    loadKnowledge();
    await flush();
    expect(list().querySelector(".knowledge-reindex")).toBeNull();
  });
});

// The poll's stall budget: slow is not wedged.

describe("indexing poll", () => {
  function indexing(items: number) {
    return { contexts: [{ name: "big", id: "1", item_count: items, indexing: true }] };
  }

  it("keeps polling while progress advances past the old 5-minute ceiling", async () => {
    vi.useFakeTimers();
    try {
      let items = 0;
      mockGet.mockImplementation(() => {
        items += 10;
        return Promise.resolve(indexing(items));
      });
      loadKnowledge();
      await flush();

      // 400 ticks, every one advancing, so every one is followed by another.
      for (let i = 0; i < 400; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }
      expect(mockGet.mock.calls.length).toBeGreaterThan(300);
    } finally {
      vi.useRealTimers();
    }
    // 400 real fetch-shaped turns do not fit the 5s default.
  }, 30_000);

  it("gives up once progress stalls", async () => {
    vi.useFakeTimers();
    try {
      // Same item_count every time: running but not moving.
      mockGet.mockImplementation(() => Promise.resolve(indexing(42)));
      loadKnowledge();
      await flush();

      for (let i = 0; i < 200; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }
      // Bounded well below the tick count, so a wedged index stops polling.
      expect(mockGet.mock.calls.length).toBeLessThan(60);
    } finally {
      vi.useRealTimers();
    }
  });

  // Nothing will move the last `Indexing… 42%`, so the row says it gave up and names the recovery (re-activating the tab).
  it("says so on the row when it abandons a stalled index", async () => {
    vi.useFakeTimers();
    try {
      mockGet.mockImplementation(() => Promise.resolve(indexing(42)));
      loadKnowledge();
      await flush();
      for (let i = 0; i < 40; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }

      const text = list().querySelector(".knowledge-progress-text")?.textContent ?? "";
      expect(text).toContain("stalled");
      expect(text).toContain("Reopen this tab");
      // No bar: a value nothing will advance is worse than none.
      expect(list().querySelector(".knowledge-bar")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  // A base name is free text, so a "|"-joined stall signature can collapse two progress states and abandon a healthy
  // index.
  it("keeps polling across two states the old '|'-joined progress signature collapsed", async () => {
    /** A template-literal "|" join, the collision-prone shape. */
    function oldProgressSig(ctxs: readonly { name: string; item_count: number }[]): string {
      return ctxs
        .map((c) => `${c.name}:${String(c.item_count)}:`)
        .sort((a, b) => a.localeCompare(b))
        .join("|");
    }
    const two = [
      { name: "docs", id: "1", item_count: 1, indexing: true },
      { name: "refs", id: "2", item_count: 2, indexing: true },
    ];
    const one = [{ name: "docs:1:|refs", id: "3", item_count: 2, indexing: true }];
    // Precondition: the "|" join really collapses these.
    expect(oldProgressSig(two)).toBe(oldProgressSig(one));

    vi.useFakeTimers();
    try {
      let tick = 0;
      mockGet.mockImplementation(() => {
        tick += 1;
        return Promise.resolve({ contexts: tick % 2 === 0 ? [...one] : [...two] });
      });
      loadKnowledge();
      await flush();
      for (let i = 0; i < 100; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }
      // Under the "|" join every tick reads as a stall.
      expect(mockGet.mock.calls.length).toBeGreaterThan(60);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops polling when nothing is indexing", async () => {
    vi.useFakeTimers();
    try {
      mockGet.mockResolvedValue({ contexts: [{ name: "done", id: "1", item_count: 9 }] });
      loadKnowledge();
      await flush();
      const after = mockGet.mock.calls.length;
      await vi.advanceTimersByTimeAsync(1500 * 5);
      await flush();
      expect(mockGet.mock.calls.length).toBe(after);
    } finally {
      vi.useRealTimers();
    }
  });
});

// The signature gates only a row's rebuild (identity is `kb:${name}`), so a collision leaves a stale row.
// `items_display` and `path` are adjacent free-form fields.

describe("loadKnowledge row signature", () => {
  /** A template-literal "|" join, the collision-prone shape. */
  function oldSig(c: {
    indexing?: boolean;
    item_count: number;
    items_display?: string;
    path?: string;
  }): string {
    return `${c.indexing === true ? "1" : "0"}|${String(c.item_count)}|${c.items_display ?? ""}|${c.path ?? ""}`;
  }

  async function sigFor(items_display: string, path: string): Promise<string> {
    mockGet.mockResolvedValue({
      contexts: [{ name: "kb", id: "i", item_count: 3, items_display, path }],
    });
    loadKnowledge();
    await flush();
    return list().querySelector(".knowledge-row")?.getAttribute("data-sig") ?? "";
  }

  it("distinguishes two states the old '|'-joined signature collapsed", async () => {
    // Free-form and adjacent, so a "|" in items_display could impersonate the boundary.
    const a = { item_count: 3, items_display: "42%|eta", path: "docs" };
    const b = { item_count: 3, items_display: "42%", path: "eta|docs" };
    // Precondition: the "|" join really collapses these.
    expect(oldSig(a)).toBe(oldSig(b));

    // Same row key for both loads, so data-sig changes only with the signature.
    const sigA = await sigFor(a.items_display, a.path);
    const sigB = await sigFor(b.items_display, b.path);
    expect(sigA).not.toBe(sigB);
  });

  it("emits verbatim components for ordinary input", async () => {
    // A settled row renders count and path only, so those are the whole signature.
    expect(await sigFor("42%", "internal/api")).toBe("0:3:internal/api");
  });

  it("escapes a reserved character instead of emitting a bare separator", async () => {
    expect(await sigFor("", "a:b")).toBe("0:3:a\\:b");
  });
});

describe("stop indexing", () => {
  function indexingAt(display: string, items = 0) {
    return {
      contexts: [
        { name: "big", id: "op1", item_count: items, items_display: display, indexing: true },
      ],
    };
  }

  const stop = (): HTMLButtonElement | null =>
    list().querySelector<HTMLButtonElement>(".knowledge-cancel");

  it("offers Stop on an indexing row only", async () => {
    mockGet.mockResolvedValue({
      contexts: [
        { name: "big", id: "op1", item_count: 0, items_display: "5%", indexing: true },
        { name: "docs", id: "a", item_count: 3 },
      ],
    });
    loadKnowledge();
    await flush();
    expect(list().querySelectorAll(".knowledge-cancel").length).toBe(1);
    expect(stop()?.getAttribute("aria-label")).toBe("Stop indexing knowledge base big");
  });

  it("posts the cancel for the row's name, confirms nothing, and refetches", async () => {
    mockGet.mockResolvedValue(indexingAt("5%"));
    mockCancel.mockReturnValue(
      Object.assign(Promise.resolve(null), {
        abort: () => undefined,
        outcome: Promise.resolve({ status: "error", error: { message: "gone" } }),
      }) as never,
    );
    loadKnowledge();
    await flush();
    const before = mockGet.mock.calls.length;
    stop()?.click();
    await flush();
    expect(mockConfirm).not.toHaveBeenCalled();
    expect(mockCancel).toHaveBeenCalledWith({ name: "big" });
    // A refused stop still refetches: the index usually finished first.
    expect(mockGet.mock.calls.length).toBeGreaterThan(before);
  });

  // The poll repaints every 1.5s; rebuilding the row would blur a keyboard reader on Stop.
  it("keeps the same focused Stop button across a progress tick", async () => {
    let display = "5%";
    let items = 1;
    mockGet.mockImplementation((path: string) =>
      Promise.resolve(path === "/api/settings" ? settingsPayload() : indexingAt(display, items)),
    );
    loadKnowledge();
    await flush();
    const first = stop();
    first?.focus();
    expect(document.activeElement).toBe(first);

    display = "60%";
    items = 9;
    await vi.advanceTimersByTimeAsync(1500);
    await flush();

    expect(list().textContent).toContain("Indexing… 60%");
    expect(stop()).toBe(first);
    expect(document.activeElement).toBe(first);
  });
});

describe("remove all", () => {
  const clearBtn = (): HTMLButtonElement =>
    document.getElementById("knowledge-clear-btn") as HTMLButtonElement;

  it("hides the control while there is nothing to remove", async () => {
    initKnowledge();
    mockGet.mockResolvedValue({ contexts: [] });
    loadKnowledge();
    await flush();
    expect(clearBtn().classList.contains("hidden")).toBe(true);

    mockGet.mockResolvedValue({ contexts: [{ name: "docs", id: "a", item_count: 3 }] });
    loadKnowledge();
    await flush();
    expect(clearBtn().classList.contains("hidden")).toBe(false);
  });

  it("names the count and the shared list in a destructive confirm before clearing", async () => {
    initKnowledge();
    mockGet.mockResolvedValue({
      contexts: [
        { name: "docs", id: "a", item_count: 3 },
        { name: "refs", id: "b", item_count: 4 },
      ],
    });
    loadKnowledge();
    await flush();
    mockConfirm.mockResolvedValue(true);
    clearBtn().click();
    await flush();
    expect(mockConfirm).toHaveBeenCalledWith(
      "Remove all 2 knowledge bases? kiro-cli on this machine uses the same list.",
      "Remove all",
      "destructive",
    );
    expect(mockClear).toHaveBeenCalledWith(undefined, expect.any(Object));
  });

  it("refetches the list once the clear succeeds", async () => {
    initKnowledge();
    mockGet.mockResolvedValue({ contexts: [{ name: "docs", id: "a", item_count: 3 }] });
    loadKnowledge();
    await flush();
    mockConfirm.mockResolvedValue(true);
    mockClear.mockImplementation((_args, opts) => {
      opts?.onSuccess?.(undefined as never, undefined);
      return Object.assign(Promise.resolve(null), {
        abort: () => undefined,
        outcome: Promise.resolve({ status: "success", data: undefined }),
      }) as never;
    });
    const before = mockGet.mock.calls.length;
    clearBtn().click();
    await flush();
    expect(mockGet.mock.calls.length).toBeGreaterThan(before);
  });

  it("clears nothing when the confirm is cancelled", async () => {
    initKnowledge();
    mockGet.mockResolvedValue({ contexts: [{ name: "docs", id: "a", item_count: 3 }] });
    loadKnowledge();
    await flush();
    mockConfirm.mockResolvedValue(false);
    clearBtn().click();
    await flush();
    expect(mockClear).not.toHaveBeenCalled();
  });
});
