// The PR tab's cycle: the view signal while the tab is shown, and the refresh button.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  actions,
  entry,
  frame,
  githubForge,
  gitlabForge,
  handle,
  load,
  mount,
  pr,
  requestedURLs,
  routeAPI,
  serve,
  resetPRsTab,
  restorePRsTab,
} from "./__test-helpers__/git-prs-tab-harness.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.apiClient(),
}));
vi.mock("./forge-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.forgeStore(),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.bus(),
}));
vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.sseAdapter(),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.confirm(),
}));
vi.mock("./merge-dialog.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.mergeDialog(),
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.actionsIndex(),
}));
vi.mock("./actions/git-prs.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.gitPRs(),
}));
vi.mock("./search-popup.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.searchPopup(),
}));
vi.mock("@cplieger/ui-primitives/dialog", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.dialog(),
}));
vi.mock("./git-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/git-prs-tab-harness.js")).mocks.gitScroll(),
}));

beforeEach(resetPRsTab);
afterEach(restorePRsTab);

describe("the view signal", () => {
  /** The git view's real shape: the view, the PR panel inside it, the mount. */
  function stageView(): { view: HTMLElement; panel: HTMLElement } {
    document.body.innerHTML = `
      <div id="git-view" class="hidden" data-tab-view>
        <div data-git-panel="prs" class="git-panel hidden">
          <button type="button" id="git-refresh-prs-btn"></button>
          <div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>
        </div>
      </div>`;
    const view = document.getElementById("git-view");
    const panel = document.querySelector<HTMLElement>('[data-git-panel="prs"]');
    if (view === null || panel === null) {
      throw new Error("view not staged");
    }
    return { view, panel };
  }

  /** What each watch said and under which stream tag; the page is pinned on its own. */
  async function watched(): Promise<unknown[]> {
    const { watchPRView } = await actions();
    return vi
      .mocked(watchPRView.dispatch)
      .mock.calls.map((c) => ({ watching: c[0].watching, tag: c[0].tag }));
  }

  // Sibling pages of one browser profile share the stream tag, so one page
  // leaving must not end the other's watch.
  it("names its page on every watch, the same page each time", async () => {
    routeAPI();
    const { view, panel } = stageView();
    view.classList.remove("hidden");
    panel.classList.remove("hidden");
    const { initPRsTab } = await load();
    initPRsTab();
    await vi.advanceTimersByTimeAsync(0);
    panel.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);

    const { watchPRView } = await actions();
    const pages = vi.mocked(watchPRView.dispatch).mock.calls.map((c) => c[0].page);
    expect(pages).toHaveLength(2);
    expect(pages[0]).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(pages[1]).toBe(pages[0]);
  });

  it("says the list is watched while the PR panel is on screen, and not once it leaves", async () => {
    routeAPI();
    const { view, panel } = stageView();
    const { initPRsTab } = await load();
    initPRsTab();
    await vi.advanceTimersByTimeAsync(0);
    expect(await watched()).toEqual([]);

    view.classList.remove("hidden");
    panel.classList.remove("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect(await watched()).toEqual([{ watching: true, tag: "profile-tag" }]);

    // Another git sub-tab.
    panel.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect(await watched()).toEqual([
      { watching: true, tag: "profile-tag" },
      { watching: false, tag: "profile-tag" },
    ]);

    // Back, then another view of the app.
    panel.classList.remove("hidden");
    await vi.advanceTimersByTimeAsync(0);
    view.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect((await watched()).slice(2)).toEqual([
      { watching: true, tag: "profile-tag" },
      { watching: false, tag: "profile-tag" },
    ]);
  });

  it("says it is not watched while the page is hidden", async () => {
    routeAPI();
    const { view, panel } = stageView();
    view.classList.remove("hidden");
    panel.classList.remove("hidden");
    const { initPRsTab } = await load();
    initPRsTab();
    await vi.advanceTimersByTimeAsync(0);
    expect(await watched()).toEqual([{ watching: true, tag: "profile-tag" }]);

    const state = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect((await watched()).at(-1)).toEqual({ watching: false, tag: "profile-tag" });

    state.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect((await watched()).at(-1)).toEqual({ watching: true, tag: "profile-tag" });
  });

  it("says so again each poll interval while it stays on screen", async () => {
    routeAPI();
    const { view, panel } = stageView();
    view.classList.remove("hidden");
    panel.classList.remove("hidden");
    const { initPRsTab } = await load();
    initPRsTab();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await watched()).toEqual([
      { watching: true, tag: "profile-tag" },
      { watching: true, tag: "profile-tag" },
    ]);

    panel.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(120_000);
    expect((await watched()).slice(2)).toEqual([{ watching: false, tag: "profile-tag" }]);
  });
});

describe("the refresh button", () => {
  /** The PR panel's toolbar and mount, as the page declares them. */
  function stageToolbar(): HTMLButtonElement {
    document.body.innerHTML = `
      <div class="git-tab-toolbar">
        <button type="button" id="git-refresh-prs-btn" class="icon-btn" aria-label="Refresh pull requests"></button>
      </div>
      <div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>`;
    const btn = document.getElementById("git-refresh-prs-btn");
    if (!(btn instanceof HTMLButtonElement)) {
      throw new Error("button not staged");
    }
    return btn;
  }

  /** The sentence the toolbar shows beside the button. */
  function statusText(): string {
    return document.querySelector(".git-tab-toolbar [role='status']")?.textContent ?? "";
  }

  /** The refresh route's answer, naming the cycle that serves the press. */
  async function answerCycle(cycle: string | Promise<Record<string, unknown>>): Promise<void> {
    const { requestPRCycle } = await actions();
    const outcome =
      typeof cycle === "string"
        ? Promise.resolve({ status: "success", value: { cycle_id: cycle } })
        : cycle;
    vi.mocked(requestPRCycle.dispatch).mockReturnValue(
      handle(outcome) as ReturnType<typeof requestPRCycle.dispatch>,
    );
  }

  async function cycleAsks(): Promise<number> {
    const { requestPRCycle } = await actions();
    return vi.mocked(requestPRCycle.dispatch).mock.calls.length;
  }

  /** Two connections, both read at cycle 3, the tab initialised over them. */
  async function twoConnections(): Promise<HTMLButtonElement> {
    const btn = stageToolbar();
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(githubForge, "3", [pr(1, 0)]), entry(gitlabForge, "3", [pr(2, 1)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    return btn;
  }

  it("is disabled and busy, its icon a spinner, in the frame of the press", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    expect(btn.querySelector("svg")).not.toBeNull();

    btn.click();

    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.querySelector(".btn-async-spinner")).not.toBeNull();
    expect(btn.querySelector("svg")).toBeNull();
    expect(await cycleAsks()).toBe(1);
  });

  it("stays busy through an older cycle's frame and settles on the one that completes the asked cycle", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    btn.click();
    await vi.advanceTimersByTimeAsync(0);

    // A cycle that began before the press is adopted and painted, and settles nothing.
    frame(entry(githubForge, "4", [pr(7, 0)]));
    frame(entry(gitlabForge, "4", [pr(2, 1)]));
    expect(
      mount().querySelector('[data-repo="cplieger/one"] .git-pr-row-number')?.textContent,
    ).toBe("#7");
    expect(btn.getAttribute("aria-busy")).toBe("true");

    // Every connection has to reach the asked cycle, not just the first to answer.
    frame(entry(githubForge, "5", [pr(1, 0)]));
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");

    frame(entry(gitlabForge, "6", [pr(2, 1)]));
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.dataset["asyncStatus"]).toBe("success");
    expect(statusText()).toBe("");

    await vi.advanceTimersByTimeAsync(1200);
    expect(btn.disabled).toBe(false);
    expect(btn.querySelector("svg")).not.toBeNull();
  });

  it("settles from a re-read of the inventory that carries the asked cycle", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    serve(entry(githubForge, "5", [pr(1, 0)]), entry(gitlabForge, "5", [pr(2, 1)]));
    const reads = requestedURLs().length;

    btn.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(requestedURLs().slice(reads)).toEqual(["/api/forges/inventory"]);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.dataset["asyncStatus"]).toBe("success");
  });

  it("counts the frames that landed before the route named its cycle", async () => {
    const btn = await twoConnections();
    let answer: (o: Record<string, unknown>) => void = () => undefined;
    await answerCycle(
      new Promise((r) => {
        answer = r;
      }),
    );
    btn.click();
    frame(entry(githubForge, "5", [pr(1, 0)]));
    frame(entry(gitlabForge, "5", [pr(2, 1)]));
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.getAttribute("aria-busy")).toBe("true");

    answer({ status: "success", value: { cycle_id: "5" } });
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.dataset["asyncStatus"]).toBe("success");
  });

  it("waits for a connection the inventory holds no entry for yet", async () => {
    const btn = stageToolbar();
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(githubForge, "3", [pr(1, 0)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    await answerCycle("5");
    serve(entry(githubForge, "5", [pr(1, 0)]));

    btn.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.getAttribute("aria-busy")).toBe("true");

    frame(entry(gitlabForge, "5", [pr(2, 1)]));
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.dataset["asyncStatus"]).toBe("success");
  });

  it("sends nothing on a second press while one runs", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    btn.click();
    btn.click();
    await vi.advanceTimersByTimeAsync(0);
    btn.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(await cycleAsks()).toBe(1);
  });

  it("re-enables with the server's refusal beside it when the route refuses", async () => {
    const btn = await twoConnections();
    await answerCycle(
      Promise.resolve({
        status: "error",
        error: { message: "the pull-request inventory is not running", status: 503 },
      }),
    );
    btn.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.dataset["asyncStatus"]).toBe("error");
    expect(statusText()).toContain("the pull-request inventory is not running");
    await vi.advanceTimersByTimeAsync(1200);
    expect(btn.disabled).toBe(false);
  });

  it("gives up at its bound with a sentence, and the next press clears it", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    btn.click();
    await vi.advanceTimersByTimeAsync(89_999);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(statusText()).toBe("");

    await vi.advanceTimersByTimeAsync(1);
    expect(btn.getAttribute("aria-busy")).toBeNull();
    expect(btn.dataset["asyncStatus"]).toBe("error");
    expect(statusText()).toContain("did not finish");
    await vi.advanceTimersByTimeAsync(1200);
    expect(btn.disabled).toBe(false);

    // The late cycle still lands in the list.
    frame(entry(githubForge, "5", [pr(9, 0)]));
    expect(
      mount().querySelector('[data-repo="cplieger/one"] .git-pr-row-number')?.textContent,
    ).toBe("#9");

    await answerCycle("6");
    btn.click();
    expect(statusText()).toBe("");
  });

  it("names a connection the asked cycle could not read", async () => {
    const btn = await twoConnections();
    await answerCycle("5");
    btn.click();
    await vi.advanceTimersByTimeAsync(0);

    frame(entry(githubForge, "5", [pr(1, 0)]));
    frame(
      entry(gitlabForge, "5", [], {
        state: "failed",
        error: { code: "", kind: "transient" },
      }),
    );
    await vi.advanceTimersByTimeAsync(0);
    expect(btn.dataset["asyncStatus"]).toBe("error");
    expect(statusText()).toContain("gitlab.com");
    expect(statusText()).not.toContain("github.com");
  });
});
