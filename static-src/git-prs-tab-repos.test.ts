// A row's repository: opening a new pull request from a clone, and a repository
// that moved.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";
import { prIdentity } from "./push-subject.js";
import {
  H,
  actions,
  applyFilter,
  entry,
  frame,
  githubForge,
  handle,
  inventory,
  inventoryAnswer,
  load,
  mount,
  pr,
  repos,
  routeAPI,
  serve,
  serveRows,
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

describe("opening a new pull request from a clone", () => {
  it("opens the dialog for the clone's repository when it has no open pull request", async () => {
    routeAPI();
    serve(
      entry(githubForge, "1", [], {
        clones: [{ dir: "one-clone", forge_id: githubForge.id, repo_id: repos[0]?.repo_id }],
      }),
    );
    document.body.insertAdjacentHTML(
      "beforeend",
      `<dialog id="pr-create-dialog">
        <input id="pr-base"><input id="pr-head"><input id="pr-title"><textarea id="pr-body"></textarea>
        <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
        <button id="pr-submit-btn"></button><button id="pr-generate-btn"></button>
      </dialog>`,
    );
    const { openNewPRForRepo } = await load();
    const { createPR } = await actions();
    vi.mocked(createPR.dispatch).mockReturnValue(
      Object.assign(Promise.resolve(null), {
        abort: vi.fn(),
        outcome: Promise.resolve({ status: "cancelled" as const }),
      }),
    );
    H.apiPost.mockResolvedValue({ output: "drafted" });

    await openNewPRForRepo("one-clone", "feat/x");
    expect((document.getElementById("pr-head") as HTMLInputElement).value).toBe("feat/x");
    expect(mount().querySelector(".git-multirepo-error")).toBeNull();
    // The description is drafted from the clone's own directory.
    expect(H.apiPost.mock.calls[0]?.slice(0, 2)).toEqual([
      "/api/git/pr-description",
      { repo: "one-clone", branch: "main" },
    ]);

    document.getElementById("pr-submit-btn")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(createPR.dispatch).mock.calls[0]?.[0]).toMatchObject({
      forge_id: githubForge.id,
      repo_id: repos[0]?.repo_id,
      source_branch: "feat/x",
    });
  });

  it("drafts a listed repository's description from its clone's directory", async () => {
    // The section is named by the forge's path; the description is git's, read
    // from the workspace directory the clone sits in.
    routeAPI();
    serve(
      entry(githubForge, "1", [pr(1, 0)], {
        clones: [{ dir: "one-clone", forge_id: githubForge.id, repo_id: repos[0]?.repo_id }],
      }),
    );
    document.body.insertAdjacentHTML(
      "beforeend",
      `<dialog id="pr-create-dialog">
        <input id="pr-base"><input id="pr-head"><input id="pr-title"><textarea id="pr-body"></textarea>
        <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
        <button id="pr-submit-btn"></button><button id="pr-generate-btn"></button>
      </dialog>`,
    );
    const { refreshPRs } = await load();
    await refreshPRs();
    [...mount().querySelectorAll<HTMLButtonElement>('[data-repo="cplieger/one"] button')]
      .find((b) => b.textContent === "+ New PR")
      ?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(H.apiPost.mock.calls[0]?.[1]).toEqual({ repo: "one-clone", branch: "main" });
  });

  // The page's head field is read-only (static/index.html), as these two fixtures are.
  it("lets the reader name the head branch when New PR comes from the list", async () => {
    routeAPI();
    serve(entry(githubForge, "1", [pr(1, 0)]));
    document.body.insertAdjacentHTML(
      "beforeend",
      `<dialog id="pr-create-dialog">
        <input id="pr-base"><input id="pr-head" readonly><input id="pr-title"><textarea id="pr-body"></textarea>
        <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
        <button id="pr-submit-btn"></button><button id="pr-generate-btn"></button>
      </dialog>`,
    );
    const { refreshPRs } = await load();
    await refreshPRs();
    [...mount().querySelectorAll<HTMLButtonElement>('[data-repo="cplieger/one"] button')]
      .find((b) => b.textContent === "+ New PR")
      ?.click();

    const head = document.getElementById("pr-head") as HTMLInputElement;
    expect(head.value).toBe("");
    expect(head.readOnly).toBe(false);
  });

  it("keeps the branch the Changes tab pushed read-only, after a New PR from the list", async () => {
    routeAPI();
    serve(
      entry(githubForge, "1", [pr(1, 0)], {
        clones: [{ dir: "one-clone", forge_id: githubForge.id, repo_id: repos[0]?.repo_id }],
      }),
    );
    document.body.insertAdjacentHTML(
      "beforeend",
      `<dialog id="pr-create-dialog">
        <input id="pr-base"><input id="pr-head" readonly><input id="pr-title"><textarea id="pr-body"></textarea>
        <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
        <button id="pr-submit-btn"></button><button id="pr-generate-btn"></button>
      </dialog>`,
    );
    const { openNewPRForRepo, refreshPRs } = await load();
    await refreshPRs();
    [...mount().querySelectorAll<HTMLButtonElement>('[data-repo="cplieger/one"] button')]
      .find((b) => b.textContent === "+ New PR")
      ?.click();
    await openNewPRForRepo("one-clone", "feat/x");

    const head = document.getElementById("pr-head") as HTMLInputElement;
    expect(head.value).toBe("feat/x");
    expect(head.readOnly).toBe(true);
  });

  // The Changes tab's Open PR switches to this tab first, and the tab's own refresh
  // then supersedes the read the opener started.
  it("opens the dialog when a newer refresh supersedes the read it waited on", async () => {
    routeAPI();
    const held = inventory(
      entry(githubForge, "1", [], {
        clones: [{ dir: "one-clone", forge_id: githubForge.id, repo_id: repos[0]?.repo_id }],
      }),
    );
    // The opener's read answers first; the newer one answers after it.
    let reads = 0;
    let answerNewer: (v: unknown) => void = () => undefined;
    inventoryAnswer.next = () => {
      reads += 1;
      return reads === 1
        ? Promise.resolve(held)
        : new Promise((r) => {
            answerNewer = r;
          });
    };
    document.body.insertAdjacentHTML(
      "beforeend",
      `<dialog id="pr-create-dialog">
        <input id="pr-base"><input id="pr-head"><input id="pr-title"><textarea id="pr-body"></textarea>
        <input id="pr-draft" type="checkbox"><div id="pr-dialog-status"></div>
        <button id="pr-submit-btn"></button><button id="pr-generate-btn"></button>
      </dialog>`,
    );
    H.apiPost.mockResolvedValue({ output: "drafted" });
    const { openNewPRForRepo, refreshPRs } = await load();

    const opening = openNewPRForRepo("one-clone", "feat/x");
    void refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    answerNewer(held);
    await opening;

    expect((document.getElementById("pr-head") as HTMLInputElement).value).toBe("feat/x");
  });

  it("says no connected forge knows a directory no clone joins", async () => {
    routeAPI();
    serveRows([pr(1)]);
    const { openNewPRForRepo } = await load();
    await openNewPRForRepo("elsewhere", "feat/x");
    expect(mount().querySelector(".git-multirepo-error")?.textContent).toContain("elsewhere");
  });
});

describe("a repository that moved", () => {
  const successor = { repo_id: "v1.6e65772f6f6e65", display_path: "new/one" };

  function row(n: number, repoID = repos[0]?.repo_id ?? ""): HTMLElement | null {
    return mount().querySelector<HTMLElement>(
      `[data-pr="${CSS.escape(prIdentity(githubForge.id, repoID, n))}"]`,
    );
  }

  function buttonOf(r: HTMLElement | null, label: string): HTMLButtonElement | undefined {
    return [...(r?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (b) => b.textContent.trim() === label,
    );
  }

  function statusOf(r: HTMLElement | null): string {
    return r?.querySelector(".git-pr-row-status[role='status']")?.textContent ?? "";
  }

  /** A refusal saying the row's repository moved, its body naming `to` when given. */
  function stale(to?: Record<string, unknown>): unknown {
    const body = {
      error: "repo_ref_stale: the repository moved",
      code: "repo_ref_stale",
      kind: "not_found",
      ...(to === undefined ? {} : { successor: to }),
    };
    return handle(
      Promise.resolve({
        status: "error",
        error: { message: body.error, code: "repo_ref_stale", status: 404, cause: body },
      }),
    );
  }

  const open = { merge_blocked: "none" };

  async function shown(rows: Record<string, unknown>[]): Promise<typeof ModPRs> {
    routeAPI();
    serveRows(rows);
    const mod = await load();
    mod.initPRsTab();
    await mod.refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    return mod;
  }

  it("says where a refused merge's repository went, and offers the re-point on that row", async () => {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(() => stale(successor) as never);
    await shown([pr(7, 0, {}, open), pr(8, 0, {}, open)]);

    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(statusOf(row(7))).toBe("Could not merge. This repository moved to new/one.");
    expect(buttonOf(row(7), "Use new/one")).toBeDefined();

    // A repaint keeps the offer on the refused row alone.
    await vi.advanceTimersByTimeAsync(1500);
    applyFilter("");
    expect(buttonOf(row(7), "Use new/one")).toBeDefined();
    expect(buttonOf(row(8), "Use new/one")).toBeUndefined();
  });

  it("re-points every row of the repository to the successor, and asks for a cycle", async () => {
    const { mergePR, requestPRCycle } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(() => stale(successor) as never);
    await shown([pr(7, 0, {}, open), pr(8, 0, {}, open)]);
    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);

    buttonOf(row(7), "Use new/one")?.click();

    expect(statusOf(row(7))).toBe("Now acting on new/one.");
    expect(buttonOf(row(7), "Use new/one")).toBeUndefined();
    expect(vi.mocked(requestPRCycle.dispatch)).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1500);
    buttonOf(row(8), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(mergePR.dispatch).mock.calls[1]?.[0]).toMatchObject({
      repo_id: successor.repo_id,
      pr_number: 8,
    });
    // The dialog reads the strategies of the repository the merge goes to.
    const { openMergeMethodDialog } = await import("./merge-dialog.js");
    expect(vi.mocked(openMergeMethodDialog).mock.lastCall?.[0]).toMatchObject({
      repo_id: successor.repo_id,
    });
  });

  it("gives the focus back to the refused control when the re-point held it", async () => {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(() => stale(successor) as never);
    await shown([pr(7, 0, {}, open)]);
    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(1500);

    const use = buttonOf(row(7), "Use new/one");
    use?.focus();
    use?.click();

    expect(document.activeElement).toBe(buttonOf(row(7), "Merge"));
  });

  it("offers no re-point when the forge did not say where the repository went", async () => {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(() => stale() as never);
    await shown([pr(7, 0, {}, open)]);

    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(1500);

    expect(statusOf(row(7))).toBe(
      "Could not merge. This repository moved, and the forge did not say where. Refresh the list to read it again.",
    );
    expect(
      [...(row(7)?.querySelectorAll("button") ?? [])].map((b) => b.textContent.trim()),
    ).toEqual(["Merge", "Close"]);
  });

  it("offers the re-point when a held close is refused for a moved repository", async () => {
    const { closePR } = await actions();
    vi.mocked(closePR.dispatch).mockImplementationOnce(() => stale(successor) as never);
    await shown([pr(7)]);

    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(8000);
    expect(statusOf(row(7))).toBe("Could not close. This repository moved to new/one.");

    buttonOf(row(7), "Use new/one")?.click();
    buttonOf(row(7), "Close")?.click();
    await vi.advanceTimersByTimeAsync(8000);
    expect(vi.mocked(closePR.dispatch).mock.calls[1]?.[0]).toMatchObject({
      repo_id: successor.repo_id,
      pr_number: 7,
    });
    // The row the list still names by the old id leaves as its close is sent.
    expect(row(7)).toBeNull();
  });

  it("forgets the re-point once the inventory no longer lists the repository it moved from", async () => {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(() => stale(successor) as never);
    await shown([pr(7, 0, {}, open)]);
    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    buttonOf(row(7), "Use new/one")?.click();

    // The next cycle lists the row under its new id, and a later one finds a new
    // repository at the old path, which the re-point must not reach.
    const moved = pr(7, 0, { repo_id: successor.repo_id, repo: "new/one" }, open);
    frame(entry(githubForge, "2", [moved]));
    expect(row(7)).toBeNull();
    expect(row(7, successor.repo_id)).not.toBeNull();
    frame(entry(githubForge, "3", [moved, pr(9, 0, {}, open)]));
    await vi.advanceTimersByTimeAsync(1500);

    buttonOf(row(9), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(mergePR.dispatch).mock.calls[1]?.[0]).toMatchObject({
      repo_id: repos[0]?.repo_id,
      pr_number: 9,
    });
  });
});
