import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";
import { prIdentity } from "./push-subject.js";
import type { Forge } from "./__test-helpers__/git-prs-tab-harness.js";
import {
  H,
  actions,
  applyFilter,
  capabilities,
  entry,
  frame,
  giteaForge,
  githubForge,
  gitlabForge,
  handle,
  load,
  mount,
  pr,
  refuses,
  repos,
  routeAPI,
  serve,
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

describe("PRs tab merge outcome", () => {
  const mergeable = (): Record<string, unknown>[] => [pr(7, 0, {}, { merge_blocked: "none" })];

  function button(label: string): HTMLButtonElement | undefined {
    return [...mount().querySelectorAll<HTMLButtonElement>(".git-pr-row button")].find(
      (b) => b.textContent.trim() === label,
    );
  }

  function rowStatus(): string {
    return mount().querySelector(".git-pr-row-status[role='status']")?.textContent ?? "";
  }

  async function statusReads(): Promise<number> {
    const { readMergeStatus } = await actions();
    return vi.mocked(readMergeStatus.dispatch).mock.calls.length;
  }

  /** The merge state reads answer `merged` in turn, the last one from then on. */
  async function statusAnswers(...merged: string[]): Promise<void> {
    const { readMergeStatus } = await actions();
    let n = 0;
    vi.mocked(readMergeStatus.dispatch).mockImplementation(() => {
      const m = merged[Math.min(n, merged.length - 1)] ?? "no";
      n += 1;
      return succeeds({ merged: m, queue_state: "none" }) as never;
    });
  }

  /** A shown GitHub list of one mergeable row, its paint bound as boot does. */
  async function shownMergeable(): Promise<void> {
    routeAPI();
    serveRows(mergeable());
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
  }

  async function mergeAnswering(state: string): Promise<void> {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockReturnValueOnce(
      succeeds({ outcome: { state, queue_state: "unknown", queue_position: -1 } }) as never,
    );
    button("Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
  }

  it("sends the row's forge kind with the merge", async () => {
    routeAPI({ forges: [gitlabForge] });
    serveRows(mergeable(), gitlabForge);
    const { refreshPRs } = await load();
    const { mergePR } = await actions();
    await refreshPRs();

    await mergeAnswering("merged");
    expect(vi.mocked(mergePR.dispatch).mock.calls[0]?.[0]).toMatchObject({
      forge_kind: "gitlab",
      head_sha: "abc1234",
      strategy: "rebase",
      pr_number: 7,
    });
  });

  it("opens the merge dialog on the row's repository", async () => {
    await shownMergeable();
    const { openMergeMethodDialog } = await import("./merge-dialog.js");

    await mergeAnswering("merged");
    expect(vi.mocked(openMergeMethodDialog).mock.calls[0]?.[0]).toMatchObject({
      forge_id: githubForge.id,
      repo_id: repos[0]?.repo_id,
      message: 'PR #7 "a change"',
    });
  });

  it("removes a merged row, and it stays gone when another connection's frame lands", async () => {
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(githubForge, "3", mergeable()), entry(gitlabForge, "3", [pr(2, 1)]));
    const { refreshPRs, initPRsTab } = await load();
    // Binds this instance's paint to the shared state, as boot does.
    initPRsTab();
    await refreshPRs();

    await mergeAnswering("merged");
    expect(mount().querySelector('[data-repo="cplieger/one"]')).toBeNull();

    frame(entry(gitlabForge, "4", [pr(2, 1)]));
    expect(mount().querySelector('[data-repo="cplieger/one"]')).toBeNull();
  });

  // GitHub merges in the background: the row is still open, so removing it at once would show a merge not yet done.
  it("keeps an accepted row merging, and follows it until the forge reads it merged", async () => {
    await shownMergeable();
    await statusAnswers("no", "no", "yes");

    await mergeAnswering("accepted");
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    expect(rowStatus()).toBe("Merging…");
    expect(button("Merge")?.getAttribute("aria-busy")).toBe("true");
    expect(await statusReads()).toBe(0);

    await vi.advanceTimersByTimeAsync(3000);
    expect(await statusReads()).toBe(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(await statusReads()).toBe(3);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(0);

    const { readMergeStatus } = await actions();
    expect(vi.mocked(readMergeStatus.dispatch).mock.calls[0]?.[0]).toMatchObject({
      forge_id: githubForge.id,
      repo_id: repos[0]?.repo_id,
      pr_number: 7,
    });
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await statusReads()).toBe(3);
  });

  it("ends the follow-up at its bound even when a state read never answers", async () => {
    await shownMergeable();
    const { readMergeStatus } = await actions();
    const aborts: ReturnType<typeof vi.fn>[] = [];
    vi.mocked(readMergeStatus.dispatch).mockImplementation(() => {
      let settle: (o: Record<string, unknown>) => void = () => undefined;
      const outcome = new Promise<Record<string, unknown>>((resolve) => {
        settle = resolve;
      });
      const abort = vi.fn(() => {
        settle({ status: "cancelled" });
      });
      aborts.push(abort);
      return Object.assign(
        outcome.then(() => null),
        { outcome, abort },
      ) as never;
    });

    await mergeAnswering("accepted");
    await vi.advanceTimersByTimeAsync(60_000);

    expect(rowStatus()).toBe(
      "The forge has not finished the merge yet. The list shows it once it does.",
    );
    expect(await statusReads()).toBe(1);
    expect(aborts[0]).toHaveBeenCalledTimes(1);
  });

  it("says a queued merge waits in the merge queue while it follows it", async () => {
    await shownMergeable();

    await mergeAnswering("enqueued");
    expect(rowStatus()).toBe("Waiting in the merge queue…");
  });

  it("reads a merge's state again after a read that failed", async () => {
    await shownMergeable();
    const { readMergeStatus } = await actions();
    vi.mocked(readMergeStatus.dispatch)
      .mockImplementationOnce(() => refuses("", "the forge is down") as never)
      .mockImplementation(() => succeeds({ merged: "yes", queue_state: "none" }) as never);

    await mergeAnswering("accepted");
    await vi.advanceTimersByTimeAsync(3000);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(0);
  });

  it("stops at its bound with the row kept, a sentence, and a cycle asked", async () => {
    await shownMergeable();
    const { requestPRCycle } = await actions();

    await mergeAnswering("accepted");
    await vi.advanceTimersByTimeAsync(59_999);
    expect(rowStatus()).toBe("Merging…");
    expect(requestPRCycle.dispatch).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    // The read due at the instant the bound ends could not answer inside it.
    expect(await statusReads()).toBe(19);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    expect(rowStatus()).toBe(
      "The forge has not finished the merge yet. The list shows it once it does.",
    );
    expect(requestPRCycle.dispatch).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1200);
    expect(button("Merge")?.disabled).toBe(false);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await statusReads()).toBe(19);
  });

  it("stops following once the list no longer holds the row", async () => {
    await shownMergeable();

    await mergeAnswering("accepted");
    frame(entry(githubForge, "2", []));
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await statusReads()).toBe(0);
  });

  it("stops following a row its repository dropped, whatever another repository lists", async () => {
    const other = pr(7, 1, {}, { merge_blocked: "none" });
    routeAPI();
    serveRows([...mergeable(), other]);
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockReturnValueOnce(
      succeeds({
        outcome: { state: "accepted", queue_state: "none", queue_position: -1 },
      }) as never,
    );
    const first = mount().querySelector(
      `[data-pr="${CSS.escape(prIdentity(githubForge.id, repos[0]?.repo_id ?? "", 7))}"]`,
    );
    [...(first?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
      .find((b) => b.textContent.trim() === "Merge")
      ?.click();
    await vi.advanceTimersByTimeAsync(0);

    frame(entry(githubForge, "2", [other]));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(await statusReads()).toBe(0);
  });

  // The library arms the forge's own auto-merge on every family.
  it("offers Merge when green on GitHub, Gitea and GitLab", async () => {
    const pending = pr(
      7,
      0,
      {},
      {
        checks: "pending",
        checks_total: 1,
        merge_blocked: "checks_running",
      },
    );
    for (const forge of [githubForge, giteaForge, gitlabForge]) {
      const { _resetForTest } = await import("./git-prs-state.js");
      _resetForTest();
      routeAPI({ forges: [forge] });
      serveRows([pending], forge);
      const { refreshPRs } = await load();
      await refreshPRs();
      expect(button("Merge when green"), `${forge.kind} offers it`).toBeDefined();
    }
  });
});

describe("the pull-request row's action state", () => {
  function row(n: number): HTMLElement | null {
    return mount().querySelector<HTMLElement>(
      `[data-pr="${CSS.escape(prIdentity(githubForge.id, repos[0]?.repo_id ?? "", n))}"]`,
    );
  }

  /** Read with any spinner beside it. */
  function buttonOf(r: HTMLElement | null, label: string): HTMLButtonElement | undefined {
    return [...(r?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (b) => b.textContent.trim() === label,
    );
  }

  function noteOf(r: HTMLElement | null, kind: string): string {
    return r?.querySelector(`.git-pr-row-note[data-note="${kind}"]`)?.textContent ?? "";
  }

  function statusOf(r: HTMLElement | null): string {
    return r?.querySelector(".git-pr-row-status[role='status']")?.textContent ?? "";
  }

  async function shown(
    rows: Record<string, unknown>[],
    forge: Forge = githubForge,
  ): Promise<typeof ModPRs> {
    routeAPI({ forges: [forge] });
    serveRows(rows, forge);
    const mod = await load();
    mod.initPRsTab();
    await mod.refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    return mod;
  }

  const failing = {
    checks: "failing",
    checks_total: 2,
    checks_failing: 1,
    merge_blocked: "checks_failing",
  };

  it("states why Merge is disabled, an unknown verdict apart from a refusal", async () => {
    await shown([
      pr(7, 0, {}, { merge_blocked: "unknown", mergeable: "unknown" }),
      pr(8, 0, {}, { merge_blocked: "unknown", mergeable: "no" }),
      pr(9, 0, {}, { merge_blocked: "none" }),
    ]);

    const unknown = buttonOf(row(7), "Merge");
    expect(unknown?.disabled).toBe(true);
    expect(noteOf(row(7), "merge")).toBe(
      "Cannot merge yet: the forge has not said whether this can merge yet.",
    );
    expect(row(7)?.querySelector('[data-note="merge"]')?.getAttribute("data-state")).toBe(
      "unknown",
    );
    const described = unknown?.getAttribute("aria-describedby") ?? "";
    expect(described).not.toBe("");
    expect(document.getElementById(described)?.textContent).toBe(noteOf(row(7), "merge"));

    expect(buttonOf(row(8), "Merge")?.disabled).toBe(true);
    expect(noteOf(row(8), "merge")).toBe(
      "Cannot merge: the forge reports this PR is not mergeable and does not say why.",
    );
    expect(row(8)?.querySelector('[data-note="merge"]')?.getAttribute("data-state")).toBe(
      "blocked",
    );

    expect(buttonOf(row(9), "Merge")?.disabled).toBe(false);
    expect(row(9)?.querySelector(".git-pr-row-note")).toBeNull();
    // No row fails its checks, so no Re-run waits on the capability.
    const { readCapabilities } = await actions();
    expect(vi.mocked(readCapabilities.dispatch)).not.toHaveBeenCalled();
  });

  it("renders a neutral verdict and a queued row's position", async () => {
    await shown([
      pr(7, 0, {}, { checks: "neutral", checks_neutral: 1, checks_total: 1 }),
      pr(8, 0, {}, { queue_state: "queued", queue_position: 4 }),
    ]);

    expect(row(7)?.querySelector(".git-pr-check-neutral")?.textContent).toBe("checks neutral");
    expect(row(8)?.querySelector(".git-pr-row-sub")?.textContent).toContain(
      "in merge queue, position 4",
    );
  });

  it("says a check verdict the family's list does not carry has not been read", async () => {
    const gitlabRow = (n: number, reason: string): Record<string, unknown> =>
      pr(n, 0, { fill: [{ field: "checks", reason }] }, { checks: "unknown" });
    await shown([gitlabRow(7, "not_on_list"), gitlabRow(8, "unread")], gitlabForge);
    const chips = [...mount().querySelectorAll(".git-pr-check-unknown")].map((c) => c.textContent);

    expect(chips).toEqual(["checks unread", "checks not read"]);
  });

  it("keeps GitHub's Re-run enabled when the capability reads unknown", async () => {
    const { readCapabilities } = await actions();
    vi.mocked(readCapabilities.dispatch).mockImplementation(
      () => succeeds(capabilities("unknown", "no wire field: per repository")) as never,
    );
    await shown([pr(7, 0, {}, failing)]);

    expect(vi.mocked(readCapabilities.dispatch)).toHaveBeenCalledWith({ forge_id: githubForge.id });
    expect(buttonOf(row(7), "Re-run")?.disabled).toBe(false);
    expect(row(7)?.querySelector('[data-note="rerun"]')).toBeNull();
  });

  it("reads a connection's capability once, however often the list repaints", async () => {
    const { readCapabilities } = await actions();
    let answer: (v: Record<string, unknown>) => void = () => undefined;
    vi.mocked(readCapabilities.dispatch).mockImplementation(
      () => handle(new Promise((r) => (answer = r))) as never,
    );
    await shown([pr(7, 0, {}, failing)]);
    frame(entry(githubForge, "2", [pr(7, 0, { title: "again" }, failing)]));
    frame(entry(githubForge, "3", [pr(7, 0, { title: "and again" }, failing)]));
    answer({ status: "success", value: capabilities("yes") });
    await vi.advanceTimersByTimeAsync(0);
    frame(entry(githubForge, "4", [pr(7, 0, { title: "once more" }, failing)]));

    expect(vi.mocked(readCapabilities.dispatch)).toHaveBeenCalledTimes(1);
  });

  // A failed read is no verdict: the press is offered and a refusal decides.
  it("offers Re-run when the capability could not be read", async () => {
    const { readCapabilities } = await actions();
    vi.mocked(readCapabilities.dispatch).mockImplementation(
      () => handle(Promise.resolve({ status: "error", error: { message: "down" } })) as never,
    );
    await shown([pr(7, 0, {}, failing)], giteaForge);
    const rerun = [...mount().querySelectorAll<HTMLButtonElement>(".git-pr-row button")].find(
      (b) => b.textContent.trim() === "Re-run",
    );

    expect(rerun?.disabled).toBe(false);
  });

  it("disables Re-run with the evidence where another family's capability reads unknown", async () => {
    const { readCapabilities } = await actions();
    vi.mocked(readCapabilities.dispatch).mockImplementation(
      () => succeeds(capabilities("unknown", "no swagger document on this instance")) as never,
    );
    await shown([pr(7, 0, {}, failing)], giteaForge);
    const r = mount().querySelector<HTMLElement>(".git-pr-row");

    expect(buttonOf(r, "Re-run")?.disabled).toBe(true);
    expect(noteOf(r, "rerun")).toBe(
      "Cannot re-run checks: the forge could not establish that it can re-run checks (no swagger document on this instance).",
    );
  });

  it("hides Re-run where the connection says it cannot re-run checks", async () => {
    const { readCapabilities } = await actions();
    vi.mocked(readCapabilities.dispatch).mockImplementation(
      () => succeeds(capabilities("no", "swagger has no rerun verb")) as never,
    );
    await shown([pr(7, 0, {}, failing)], giteaForge);

    expect(mount().querySelector(".git-pr-row")).not.toBeNull();
    const labels = [...mount().querySelectorAll(".git-pr-row button")].map((b) =>
      b.textContent.trim(),
    );
    expect(labels).not.toContain("Re-run");
  });

  it("disables Re-run on every row of a repository whose re-run is refused with a code, and only there", async () => {
    const { rerunChecks } = await actions();
    vi.mocked(rerunChecks.dispatch).mockImplementation(
      () => refuses("capability_unsupported", "no re-run verb") as never,
    );
    await shown([pr(7, 0, {}, failing), pr(8, 0, {}, failing), pr(9, 1, {}, failing)]);
    const other = (): HTMLElement | null =>
      mount().querySelector<HTMLElement>(
        `[data-pr="${CSS.escape(prIdentity(githubForge.id, repos[1]?.repo_id ?? "", 9))}"]`,
      );

    buttonOf(row(7), "Re-run")?.click();
    await vi.advanceTimersByTimeAsync(0);

    const reason =
      "Cannot re-run checks: this forge cannot re-run checks for this repository (no re-run verb).";
    for (const n of [7, 8]) {
      expect(buttonOf(row(n), "Re-run")?.disabled, `#${String(n)}`).toBe(true);
      expect(noteOf(row(n), "rerun"), `#${String(n)}`).toBe(reason);
    }
    expect(buttonOf(other(), "Re-run")?.disabled).toBe(false);
    expect(noteOf(other(), "rerun")).toBe("");

    // For the life of the list: a later entry does not re-enable it.
    frame(
      entry(githubForge, "2", [
        pr(7, 0, {}, failing),
        pr(8, 0, {}, failing),
        pr(9, 1, {}, failing),
      ]),
    );
    expect(buttonOf(row(8), "Re-run")?.disabled).toBe(true);
  });

  it("disables Re-run for a token missing the permission, naming it", async () => {
    const { rerunChecks } = await actions();
    vi.mocked(rerunChecks.dispatch).mockImplementation(
      () => refuses("scope_insufficient", "needs the workflow scope") as never,
    );
    await shown([pr(7, 0, {}, failing)]);

    buttonOf(row(7), "Re-run")?.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(buttonOf(row(7), "Re-run")?.disabled).toBe(true);
    expect(noteOf(row(7), "rerun")).toBe(
      "Cannot re-run checks: the token is missing a permission this needs. Add it to the token on the forge. needs the workflow scope",
    );
  });

  // Each starts a request once its dialog is confirmed.
  const presses = [
    {
      label: "Merge",
      action: "mergePR",
      row: pr(7, 0, {}, { merge_blocked: "none" }),
      forge: githubForge,
    },
    {
      label: "Merge when green",
      action: "armAutoMerge",
      row: pr(7, 0, {}, { checks: "pending", merge_blocked: "checks_running" }),
      forge: gitlabForge,
    },
    { label: "Re-run", action: "rerunChecks", row: pr(7, 0, {}, failing), forge: githubForge },
    { label: "Reopen", action: "reopenPR", row: pr(7, 0, { state: "closed" }), forge: githubForge },
  ] as const;

  function row7(forge: Forge): HTMLElement | null {
    return mount().querySelector<HTMLElement>(
      `[data-pr="${CSS.escape(prIdentity(forge.id, repos[0]?.repo_id ?? "", 7))}"]`,
    );
  }

  for (const c of presses) {
    it(`shows ${c.label} busy in the frame its dialog is confirmed, and through a repaint while it runs`, async () => {
      const a = await actions();
      let answer: (o: Record<string, unknown>) => void = () => undefined;
      vi.mocked(a[c.action].dispatch).mockImplementation(
        () => handle(new Promise((r) => (answer = r))) as never,
      );
      await shown([c.row], c.forge);

      buttonOf(row7(c.forge), c.label)?.click();
      // The dialog answers on a microtask; no timer, so no frame, runs.
      for (let i = 0; i < 5; i++) {
        await Promise.resolve();
      }
      const pressed = buttonOf(row7(c.forge), c.label);
      expect(pressed?.disabled).toBe(true);
      expect(pressed?.getAttribute("aria-busy")).toBe("true");
      expect(vi.mocked(a[c.action].dispatch)).toHaveBeenCalledTimes(1);

      // A cycle lands while the request runs, and the row repaints.
      frame(entry(c.forge, "2", [{ ...c.row, title: "a newer title" }]));
      const repainted = buttonOf(row7(c.forge), c.label);
      expect(repainted).not.toBe(pressed);
      expect(repainted?.disabled).toBe(true);
      expect(repainted?.getAttribute("aria-busy")).toBe("true");

      answer({ status: "error", error: { message: "the forge said no", code: "", status: 409 } });
      await vi.advanceTimersByTimeAsync(0);
      const settled = buttonOf(row7(c.forge), c.label);
      expect(settled?.disabled).toBe(false);
      expect(settled?.getAttribute("aria-busy")).toBeNull();
      expect(statusOf(row7(c.forge))).toContain("the forge said no");
    });
  }

  it("ends a success sentence at the next entry with no repaint between", async () => {
    const { refreshPRs } = await shown([pr(7, 0, {}, failing)]);
    buttonOf(row(7), "Re-run")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(statusOf(row(7))).toBe("Re-run started.");

    serveRows([pr(7, 0, {}, failing)], githubForge, "2");
    await refreshPRs();
    expect(statusOf(row(7))).toBe("");
  });

  it("re-enables a press its dispatch cancelled, with nothing to say", async () => {
    const { mergePR } = await actions();
    let answer: (o: Record<string, unknown>) => void = () => undefined;
    vi.mocked(mergePR.dispatch).mockImplementation(
      () => handle(new Promise((r) => (answer = r))) as never,
    );
    await shown([pr(7, 0, {}, { merge_blocked: "none" })]);

    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    frame(
      entry(githubForge, "2", [pr(7, 0, { title: "a newer title" }, { merge_blocked: "none" })]),
    );
    expect(buttonOf(row(7), "Merge")?.getAttribute("aria-busy")).toBe("true");

    answer({ status: "cancelled" });
    await vi.advanceTimersByTimeAsync(0);
    expect(buttonOf(row(7), "Merge")?.disabled).toBe(false);
    expect(buttonOf(row(7), "Merge")?.getAttribute("aria-busy")).toBeNull();
    expect(statusOf(row(7))).toBe("");
  });

  it("shows a refused merge's reason in its row, and clears it at the next press", async () => {
    const { mergePR } = await actions();
    vi.mocked(mergePR.dispatch).mockImplementationOnce(
      () => refuses("", "Base branch was modified") as never,
    );
    await shown([pr(7, 0, {}, { merge_blocked: "none" })]);

    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(statusOf(row(7))).toBe("Could not merge. Base branch was modified");
    await vi.advanceTimersByTimeAsync(1500);
    expect(buttonOf(row(7), "Merge")?.disabled).toBe(false);

    // The forge's next entry does not answer a refusal, so it stands.
    frame(entry(githubForge, "2", [pr(7, 0, {}, { merge_blocked: "none" })]));
    expect(statusOf(row(7))).toBe("Could not merge. Base branch was modified");

    vi.mocked(mergePR.dispatch).mockImplementationOnce(
      () => handle(new Promise(() => undefined)) as never,
    );
    buttonOf(row(7), "Merge")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(statusOf(row(7))).toBe("");
  });

  it("says a re-run started, until the connection's next entry", async () => {
    const { refreshPRs } = await shown([pr(7, 0, {}, failing)]);

    buttonOf(row(7), "Re-run")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(statusOf(row(7))).toBe("Re-run started.");
    // A repaint inside the same cycle keeps it.
    applyFilter("");
    expect(statusOf(row(7))).toBe("Re-run started.");

    // The next entry repeats the row exactly: the cycle, not new data, ends the sentence.
    serveRows([pr(7, 0, {}, failing)], githubForge, "2");
    await refreshPRs();
    expect(statusOf(row(7))).toBe("");
  });

  it("forgets a refusal when the connections change", async () => {
    const { rerunChecks } = await actions();
    vi.mocked(rerunChecks.dispatch).mockImplementationOnce(
      () => refuses("scope_insufficient", "needs the workflow scope") as never,
    );
    await shown([pr(7, 0, {}, failing)]);
    buttonOf(row(7), "Re-run")?.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(buttonOf(row(7), "Re-run")?.disabled).toBe(true);

    // A reconnect may grant the permission: the next read decides again.
    H.sse.get("forges_changed")?.("", undefined);
    frame(entry(githubForge, "2", [pr(7, 0, {}, failing)]));
    await vi.advanceTimersByTimeAsync(0);
    expect(buttonOf(row(7), "Re-run")?.disabled).toBe(false);
    const { readCapabilities } = await actions();
    expect(vi.mocked(readCapabilities.dispatch)).toHaveBeenCalledTimes(2);
  });
});
