import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  applyFilter,
  entry,
  frame,
  githubForge,
  gitlabForge,
  load,
  mount,
  notes,
  pr,
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

describe("the PR filter", () => {
  const HOUR_MS = 3_600_000;

  /** Six rows across two repos rendering every string a row can carry. */
  const fixture = (): Record<string, unknown>[] => [
    pr(
      1234,
      0,
      {
        title: "Tighten the upload policy",
        draft: true,
        author: "renovate-bot",
        updated_at: Date.now() - 3 * HOUR_MS,
        source_branch: "feat/upload-policy",
      },
      { checks: "failing", checks_total: 5, checks_failing: 3, merge_blocked: "checks_failing" },
    ),
    pr(
      1235,
      0,
      { title: "Bump keyenc", author: "cplieger" },
      { checks: "pending", checks_total: 2, merge_blocked: "checks_running" },
    ),
    pr(
      1236,
      0,
      { title: "Armed already" },
      { checks: "pending", auto_merge_armed: "yes", merge_blocked: "checks_running" },
    ),
    pr(9, 1, { title: "Green and quiet" }, { checks: "passing", checks_total: 1 }),
    pr(10, 1, { title: "Closed last week", state: "closed" }),
    pr(11, 1, { title: "Zebra crossing", source_branch: "fix/zebra", target_branch: "dev" }),
  ];

  function shownNumbers(): string[] {
    return [...mount().querySelectorAll(".git-pr-row-number")].map((e) => e.textContent ?? "");
  }

  /** Every non-blank text node under `row`: what a reader sees. */
  function renderedStrings(row: HTMLElement): string[] {
    const out: string[] = [];
    const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const text = node.textContent?.trim() ?? "";
      if (text !== "") {
        out.push(text);
      }
    }
    return out;
  }

  it("reaches every string a row renders, and the census walks them all back in", async () => {
    // One list feeds both the row and the haystack, so any string a reader can see keeps the row; this types every text
    // node into the box. GitLab, whose rows offer every control.
    routeAPI({ forges: [gitlabForge] });
    // The states only this row renders: a queue position, a neutral verdict, Merge waiting on an undecided verdict.
    const queued = pr(
      12,
      1,
      { title: "Waiting its turn" },
      {
        checks: "neutral",
        checks_total: 1,
        queue_state: "queued",
        queue_position: 2,
        merge_blocked: "unknown",
      },
    );
    serveRows([...fixture(), queued], gitlabForge);
    const { refreshPRs } = await load();
    await refreshPRs();
    // GitLab offers Re-run once its connection's capability has answered.
    await vi.advanceTimersByTimeAsync(0);
    const rows = [...mount().querySelectorAll<HTMLElement>(".git-pr-row")];
    expect(rows).toHaveLength(7);

    const seen = new Set<string>();
    for (const row of rows) {
      const num = row.querySelector(".git-pr-row-number")?.textContent ?? "";
      for (const text of renderedStrings(row)) {
        seen.add(text);
        applyFilter(text.toLowerCase());
        expect(shownNumbers(), `typing "${text}" should keep ${num}`).toContain(num);
      }
    }
    applyFilter("");
    // The premise: the fixture renders strings only a badge, an author or a branch carries, so the loop is a census.
    for (const literal of [
      "#1234",
      "draft",
      "3 failing",
      "checks running",
      "auto-merge",
      "by @renovate-bot · 3 hours ago · feat/upload-policy → main",
      "Merge when green",
      "Re-run",
      "Reopen",
      "Close",
      "Cannot merge: a required check is failing.",
      "Cannot merge yet: the forge has not said whether this can merge yet.",
      "in merge queue, position 2",
      "checks neutral",
    ]) {
      expect(seen).toContain(literal);
    }
  });

  it("reaches every string a contribution elsewhere renders", async () => {
    routeAPI();
    serve(
      entry(githubForge, "1", [], {
        scopes: [
          { scope: "owner", owner: "cplieger", rows: [] },
          {
            scope: "authored",
            rows: [
              pr(
                41,
                3,
                {
                  title: "Port the lib",
                  draft: true,
                  author: "cplieger",
                  updated_at: Date.now() - 3 * HOUR_MS,
                  source_branch: "feat/port",
                },
                { checks: "failing", checks_total: 2, checks_failing: 1, auto_merge_armed: "yes" },
              ),
            ],
          },
        ],
      }),
    );
    const { refreshPRs } = await load();
    await refreshPRs();
    const row = mount().querySelector<HTMLElement>('[data-group="elsewhere"] .git-pr-row');
    if (row === null) {
      throw new Error("no contribution row");
    }

    const seen = renderedStrings(row);
    for (const text of seen) {
      applyFilter(text.toLowerCase());
      expect(shownNumbers(), `typing "${text}" should keep the contribution`).toEqual([
        "other/lib#41",
      ]);
    }
    applyFilter("");
    for (const literal of [
      "other/lib#41",
      "Port the lib",
      "draft",
      "1 failing",
      "auto-merge",
      "by @cplieger · 3 hours ago · feat/port → main",
    ]) {
      expect(seen).toContain(literal);
    }
  });

  it("matches a substring of the authorship line: an author, a branch, an age", async () => {
    routeAPI();
    serveRows(fixture());
    const { refreshPRs } = await load();
    await refreshPRs();

    applyFilter("renovate");
    expect(shownNumbers()).toEqual(["#1234"]);
    applyFilter("fix/zebra");
    expect(shownNumbers()).toEqual(["#11"]);
    applyFilter("→ dev");
    expect(shownNumbers()).toEqual(["#11"]);
    applyFilter("3 hours ago");
    expect(shownNumbers()).toEqual(["#1234"]);
    applyFilter("1235");
    expect(shownNumbers()).toEqual(["#1235"]);
  });

  it("keeps every PR of a repo whose NAME matches", async () => {
    routeAPI();
    serveRows(fixture());
    const { refreshPRs } = await load();
    await refreshPRs();

    applyFilter("cplieger/two");
    expect(shownNumbers()).toEqual(["#11", "#10", "#9"]);
  });

  it("states how many rows the filter kept, in the shared grammar", async () => {
    routeAPI();
    serveRows(fixture());
    const { refreshPRs } = await load();
    await refreshPRs();
    // Silent with no filter: a count restating the list is noise.
    expect(notes.at(-1)).toBe("");

    applyFilter("check");
    // `checks running` ×2 (the armed row's chip too), `checks passed`, and the failing row through its Merge-disabled sentence.
    expect(notes.at(-1)).toBe("4 pull requests; 6 pull requests scanned");

    applyFilter("zebra");
    expect(notes.at(-1)).toBe("1 pull request; 6 pull requests scanned");
  });

  it("says No matches when the filter drops every row, and offers no advice", async () => {
    // Every rendered string is reachable, so the note says what was found instead of advising a change.
    routeAPI();
    serveRows(fixture());
    const { refreshPRs } = await load();
    await refreshPRs();

    applyFilter("zzz-matches-nothing");
    expect(notes.at(-1)).toBe("No matches");
    expect(mount().querySelector(".git-multirepo-empty-title")?.textContent).toBe(
      "No matching pull requests",
    );
    expect(mount().querySelector(".git-multirepo-empty-hint")).toBeNull();
    expect(shownNumbers()).toEqual([]);
  });

  it("opens a section the reader had collapsed when it holds a matching row", async () => {
    // The section element survives paints, so the open state is decided again on every paint: a filter outranks the
    // reader's latch while it stands, or a match sits inside an aria-hidden, inert region.
    routeAPI();
    serveRows(fixture());
    const { refreshPRs } = await load();
    await refreshPRs();
    const toggle = (): HTMLElement | null =>
      mount().querySelector<HTMLElement>(
        '[data-repo="cplieger/one"] .git-repo-section-header-toggle',
      );
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");

    toggle()?.click();
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");

    applyFilter("upload-policy");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(shownNumbers()).toEqual(["#1234"]);

    applyFilter("");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps a section the reader collapsed closed across a refresh", async () => {
    // A refresh with no filter reads the latch, so a collapsed section stays collapsed.
    routeAPI();
    serveRows(fixture());
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    const toggle = mount().querySelector<HTMLElement>(
      '[data-repo="cplieger/one"] .git-repo-section-header-toggle',
    );
    toggle?.click();
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");

    frame(entry(githubForge, "2", fixture()));
    expect(
      mount()
        .querySelector('[data-repo="cplieger/one"] .git-repo-section-header-toggle')
        ?.getAttribute("aria-expanded"),
    ).toBe("false");
  });

  it("mounts a repository's section open when its first pull request arrives", async () => {
    routeAPI();
    serveRows([pr(1, 0)]);
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    const toggle = (): HTMLElement | null =>
      mount().querySelector<HTMLElement>(
        '[data-repo="cplieger/two"] .git-repo-section-header-toggle',
      );
    expect(toggle()).toBeNull();

    frame(entry(githubForge, "2", [pr(1, 0), pr(2, 1)]));
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
  });
});
