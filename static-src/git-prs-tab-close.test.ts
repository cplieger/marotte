import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";
import { prIdentity } from "./push-subject.js";
import {
  actions,
  applyFilter,
  entry,
  frame,
  githubForge,
  handle,
  load,
  mount,
  pr,
  refuses,
  repos,
  routeAPI,
  serveRows,
  succeeds,
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

describe("closing a pull request", () => {
  const WINDOW_MS = 8000;

  function row(n: number): HTMLElement | null {
    return mount().querySelector<HTMLElement>(
      `[data-pr="${CSS.escape(prIdentity(githubForge.id, repos[0]?.repo_id ?? "", n))}"]`,
    );
  }

  function buttonOf(r: HTMLElement | null, label: string): HTMLButtonElement | undefined {
    return [...(r?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (b) => b.textContent.trim() === label,
    );
  }

  function labels(r: HTMLElement | null): string[] {
    return [...(r?.querySelectorAll("button") ?? [])].map((b) => b.textContent.trim());
  }

  function statusOf(r: HTMLElement | null): string {
    return r?.querySelector(".git-pr-row-status[role='status']")?.textContent ?? "";
  }

  async function closed(): Promise<number[]> {
    const { closePR } = await actions();
    return vi.mocked(closePR.dispatch).mock.calls.map((c) => c[0].pr_number);
  }

  async function shown(rows: Record<string, unknown>[]): Promise<typeof ModPRs> {
    routeAPI();
    serveRows(rows);
    const mod = await load();
    mod.initPRsTab();
    await mod.refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    return mod;
  }

  it("turns the row into an Undo bar at once, asking nothing and sending nothing before the window lapses", async () => {
    const { confirm } = await import("./confirm.js");
    vi.mocked(confirm).mockClear();
    await shown([pr(7), pr(8)]);

    buttonOf(row(7), "Close")?.click();

    expect(labels(row(7))).toEqual(["Undo"]);
    expect(statusOf(row(7))).toBe("PR #7 closes in a few seconds.");
    expect(row(7)?.classList.contains("git-pr-row-closing")).toBe(true);
    // The press did not hold focus, so none is taken.
    expect(document.activeElement).toBe(document.body);
    const undo = buttonOf(row(7), "Undo");
    expect(document.getElementById(undo?.getAttribute("aria-describedby") ?? "")?.textContent).toBe(
      "PR #7 closes in a few seconds.",
    );
    expect(labels(row(8))).toContain("Close");
    await vi.advanceTimersByTimeAsync(WINDOW_MS - 1);
    expect(await closed()).toEqual([]);
    expect(vi.mocked(confirm)).not.toHaveBeenCalled();
  });

  it("puts the row back on Undo and never asks the forge", async () => {
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(3000);

    buttonOf(row(7), "Undo")?.click();

    expect(labels(row(7))).toEqual(["Merge", "Close"]);
    expect(row(7)?.classList.contains("git-pr-row-closing")).toBe(false);
    expect(statusOf(row(7))).toBe("");
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await closed()).toEqual([]);
  });

  it("sends exactly one close when the window lapses, and the row leaves the list", async () => {
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();

    await vi.advanceTimersByTimeAsync(WINDOW_MS);
    expect(await closed()).toEqual([7]);
    const { closePR } = await actions();
    expect(vi.mocked(closePR.dispatch).mock.calls[0]?.[0]).toEqual({
      forge_id: githubForge.id,
      repo_id: repos[0]?.repo_id,
      owner: "cplieger",
      name: "one",
      pr_number: 7,
    });
    expect(row(7)).toBeNull();
    expect(row(8)).not.toBeNull();

    await vi.advanceTimersByTimeAsync(60_000);
    expect(await closed()).toEqual([7]);
  });

  it("puts a refused close's row back with the forge's reason", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementation(
      () => refuses("conflict", "the pull request is locked") as never,
    );
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();

    await vi.advanceTimersByTimeAsync(WINDOW_MS);

    expect(labels(row(7))).toEqual(["Merge", "Close"]);
    expect(statusOf(row(7))).toBe("Could not close. the pull request is locked");
    expect(row(7)?.querySelector(".git-pr-row-status")?.classList.contains("err")).toBe(true);

    // The next Close is the row's next press, so the refusal goes with it.
    buttonOf(row(7), "Close")?.click();
    buttonOf(row(7), "Undo")?.click();
    expect(statusOf(row(7))).toBe("");
  });

  it("keeps a row hidden while its close is in flight, and names the refusal that brings it back", async () => {
    const { closePR } = await actions();
    let answer: (o: Record<string, unknown>) => void = () => undefined;
    vi.mocked(closePR.dispatch).mockImplementation(
      () => handle(new Promise((r) => (answer = r))) as never,
    );
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(WINDOW_MS);
    frame(entry(githubForge, "2", [pr(7)]));
    expect(row(7)).toBeNull();

    answer({ status: "error", error: { message: "the pull request is locked", code: "conflict" } });
    await vi.advanceTimersByTimeAsync(0);

    expect(labels(row(7))).toEqual(["Merge", "Close"]);
    expect(statusOf(row(7))).toBe("Could not close. the pull request is locked");
  });

  it("shows a closed row as it is when the cycle its close named still lists it", async () => {
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(WINDOW_MS);
    expect(row(7)).toBeNull();

    frame(entry(githubForge, "2", [pr(7)]));

    expect(labels(row(7))).toEqual(["Merge", "Close"]);
  });

  it("keeps a closed row hidden through a cycle that began before the close", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementation(
      () => succeeds({ pr: {}, cycle_id: "3" }) as never,
    );
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(WINDOW_MS);

    frame(entry(githubForge, "2", [pr(7)]));
    expect(row(7)).toBeNull();

    frame(entry(githubForge, "3", []));
    expect(row(7)).toBeNull();
  });

  it("puts a row back without a sentence when its close was cancelled", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementation(
      () => handle(Promise.resolve({ status: "cancelled" })) as never,
    );
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();

    await vi.advanceTimersByTimeAsync(WINDOW_MS);

    expect(labels(row(7))).toEqual(["Merge", "Close"]);
    expect(statusOf(row(7))).toBe("");
  });

  it("holds two closes in one window as two, each sent at its own lapse", async () => {
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(2000);
    buttonOf(row(8), "Close")?.click();

    await vi.advanceTimersByTimeAsync(WINDOW_MS - 2000);
    expect(await closed()).toEqual([7]);
    expect(labels(row(8))).toEqual(["Undo"]);

    await vi.advanceTimersByTimeAsync(2000);
    expect(await closed()).toEqual([7, 8]);
  });

  it("keeps the Undo bar through a cycle that still lists the row", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementation(
      () => succeeds({ pr: {}, cycle_id: "3" }) as never,
    );
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();

    frame(entry(githubForge, "2", [pr(7, 0, { title: "renamed" }), pr(8)]));

    expect(labels(row(7))).toEqual(["Undo"]);
    await vi.advanceTimersByTimeAsync(WINDOW_MS);
    expect(await closed()).toEqual([7]);
    expect(row(7)).toBeNull();
  });

  it("gives a Close made after an Undo its own whole window", async () => {
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(3000);
    buttonOf(row(7), "Undo")?.click();
    await vi.advanceTimersByTimeAsync(2000);
    buttonOf(row(7), "Close")?.click();

    // The first press's window would have lapsed here.
    await vi.advanceTimersByTimeAsync(3000);
    expect(await closed()).toEqual([]);
    await vi.advanceTimersByTimeAsync(WINDOW_MS - 3000);
    expect(await closed()).toEqual([7]);
  });

  function stageView(): HTMLElement {
    document.body.innerHTML = `
      <div id="git-view" data-tab-view>
        <div data-git-panel="prs" class="git-panel">
          <div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>
        </div>
      </div>`;
    const panel = document.querySelector<HTMLElement>('[data-git-panel="prs"]');
    if (panel === null) {
      throw new Error("view not staged");
    }
    return panel;
  }

  it("sends every pending close at once when the list leaves the screen", async () => {
    const panel = stageView();
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();
    buttonOf(row(8), "Close")?.click();
    await vi.advanceTimersByTimeAsync(1000);
    expect(await closed()).toEqual([]);

    panel.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);

    expect(await closed()).toEqual([7, 8]);
    await vi.advanceTimersByTimeAsync(WINDOW_MS);
    expect(await closed()).toEqual([7, 8]);
  });

  it("keeps a pending close through a hidden page, and sends it with keepalive when the page goes", async () => {
    stageView();
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();
    buttonOf(row(8), "Close")?.click();
    const state = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(0);
    expect(await closed()).toEqual([]);
    expect(labels(row(7))).toEqual(["Undo"]);

    window.dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(WINDOW_MS);

    const { sendCloseOnUnload } = await actions();
    // Earlier suites' module instances hold their own closes and hear the same event.
    expect(vi.mocked(sendCloseOnUnload).mock.calls.map((c) => c[0].pr_number)).toEqual(
      expect.arrayContaining([7, 8]),
    );
    expect(await closed()).toEqual([]);
    expect(row(7)).toBeNull();
    expect(row(8)).toBeNull();
    state.mockRestore();
  });

  it("does not send a pending close when the list says again that it is on screen", async () => {
    stageView();
    await shown([pr(7)]);
    // The view signal renews every 60 s while the list stays shown.
    await vi.advanceTimersByTimeAsync(55_000);
    buttonOf(row(7), "Close")?.click();

    await vi.advanceTimersByTimeAsync(5000);

    expect(await closed()).toEqual([]);
  });

  it("gives a Close after a refused early send its own whole window", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementationOnce(
      () => refuses("conflict", "the pull request is locked") as never,
    );
    const panel = stageView();
    await shown([pr(7)]);
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(1000);
    panel.classList.add("hidden");
    await vi.advanceTimersByTimeAsync(0);
    expect(await closed()).toEqual([7]);
    panel.classList.remove("hidden");
    await vi.advanceTimersByTimeAsync(0);

    buttonOf(row(7), "Close")?.click();

    // The first press's window would have lapsed here.
    await vi.advanceTimersByTimeAsync(WINDOW_MS - 1000);
    expect(await closed()).toEqual([7]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(await closed()).toEqual([7, 7]);
  });

  it("moves focus to Undo, and back to the row's Close on Undo", async () => {
    await shown([pr(7)]);
    const close = buttonOf(row(7), "Close");
    close?.focus();

    close?.click();
    expect(document.activeElement).toBe(buttonOf(row(7), "Undo"));

    buttonOf(row(7), "Undo")?.click();
    expect(document.activeElement).toBe(buttonOf(row(7), "Close"));
  });

  it("lets the filter reach what the Undo bar says", async () => {
    await shown([pr(7), pr(8)]);
    buttonOf(row(7), "Close")?.click();

    for (const text of ["closes in a few seconds", "undo"]) {
      applyFilter(text);
      expect(row(7), text).not.toBeNull();
      expect(row(8), text).toBeNull();
    }
  });
});
