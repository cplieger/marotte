// The PR tab's listing over the inventory: loading, reconciliation, row identity,
// partial inventories and the contributions group.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { prIdentity } from "./push-subject.js";
import type { Forge } from "./__test-helpers__/git-prs-tab-harness.js";
import {
  H,
  actions,
  applyFilter,
  entry,
  frame,
  giteaForge,
  githubForge,
  gitlabForge,
  inventory,
  inventoryAnswer,
  load,
  mount,
  notes,
  pr,
  repos,
  requestedURLs,
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

describe("PRs tab loading state", () => {
  it("paints a skeleton while the inventory read is in flight", async () => {
    routeAPI();
    let answer: (v: unknown) => void = () => undefined;
    inventoryAnswer.next = () =>
      new Promise((r) => {
        answer = r;
      });
    const { refreshPRs } = await load();
    const done = refreshPRs();

    // The show delay is 150ms, so nothing is painted before it elapses.
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    await vi.advanceTimersByTimeAsync(150);

    const skel = mount().querySelector(".git-repo-skeleton");
    expect(skel).not.toBeNull();
    // aria-hidden: the mount is aria-live, so placeholders must not be announced.
    expect(skel?.getAttribute("aria-hidden")).toBe("true");
    expect(skel?.querySelectorAll(".skeleton").length).toBeGreaterThan(0);

    answer(inventory(entry(githubForge, "1", [pr(7)])));
    await done;
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
  });

  it("skips the skeleton when the mount already holds keyed rows", async () => {
    routeAPI();
    inventoryAnswer.next = () => new Promise(() => undefined);
    const row = document.createElement("section");
    row.setAttribute("data-reconcile-key", "cplieger/one");
    mount().appendChild(row);

    const { refreshPRs } = await load();
    void refreshPRs();
    await vi.advanceTimersByTimeAsync(150);

    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    expect(mount().querySelector("[data-reconcile-key]")).not.toBeNull();
  });

  it("arms nothing for an inventory with NO open PRs once it has answered", async () => {
    // No open PRs anywhere is an ANSWER, and the container cannot tell it from a set
    // this client has never read. A gap reaches this refresh with no tab switch behind
    // it, so without the answered flag it would shimmer over a settled pane.
    routeAPI();
    serveRows([]);
    const { refreshPRs } = await load();
    await refreshPRs();
    expect(mount().querySelector(".git-multirepo-empty-title")?.textContent).toBe("All caught up");

    inventoryAnswer.next = () => new Promise(() => undefined);
    void refreshPRs();
    await vi.advanceTimersByTimeAsync(150);
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
  });

  it("paints an error into the mount when the forge list cannot be read", async () => {
    routeAPI({ forgesNull: true });
    const { refreshPRs } = await load();

    await expect(refreshPRs()).rejects.toThrow(/forges/i);

    // The action's toast is transient, so a blank pane would be the only lasting
    // record of the failure.
    const err = mount().querySelector(".git-multirepo-error");
    expect(err?.textContent).toContain("Could not load pull requests");
  });

  it("paints an error into the mount when the inventory cannot be read", async () => {
    routeAPI();
    inventoryAnswer.next = () => Promise.resolve(null);
    const { refreshPRs } = await load();

    await expect(refreshPRs()).rejects.toThrow(/inventory/i);
    expect(mount().querySelector(".git-multirepo-error")?.textContent).toContain(
      "Could not load pull requests",
    );
  });
});

describe("PRs tab over the inventory", () => {
  it("reads the inventory once and asks no repository for its list", async () => {
    routeAPI();
    serveRows([pr(7, 0), pr(8, 1)]);
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(requestedURLs()).toEqual(["/api/forges/inventory"]);
    expect(requestedURLs().some((u) => u.includes("/repos"))).toBe(false);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(2);
  });

  it("replaces only the framed connection's groups", async () => {
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(githubForge, "4", [pr(1, 0)]), entry(gitlabForge, "4", [pr(2, 1)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    const gitlabRow = mount().querySelector('[data-repo="cplieger/two"] .git-pr-row');
    expect(gitlabRow).not.toBeNull();

    frame(entry(githubForge, "5", [pr(9, 0), pr(3, 2)]));
    expect(
      [...mount().querySelectorAll(".git-repo-section")].map((s) => s.getAttribute("data-repo")),
    ).toEqual(["cplieger/one", "cplieger/three", "cplieger/two"]);
    expect(
      mount().querySelector('[data-repo="cplieger/one"] .git-pr-row-number')?.textContent,
    ).toBe("#9");
    // The other connection's row is the same element: its entry did not move.
    expect(mount().querySelector('[data-repo="cplieger/two"] .git-pr-row')).toBe(gitlabRow);
  });

  it("ignores a frame whose cycle is not after the held one", async () => {
    routeAPI();
    serveRows([pr(1)], githubForge, "6");
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();

    frame(entry(githubForge, "5", [pr(2)]));
    frame(entry(githubForge, "6", [pr(3)]));
    expect([...mount().querySelectorAll(".git-pr-row-number")].map((n) => n.textContent)).toEqual([
      "#1",
    ]);

    // Ordered as numbers: cycle 10 is after cycle 9.
    frame(entry(githubForge, "10", [pr(4)]));
    expect(mount().querySelector(".git-pr-row-number")?.textContent).toBe("#4");
  });

  it("paints a loading connection's skeleton beside the other connections' rows", async () => {
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(githubForge, "0", [], { state: "loading" }), entry(gitlabForge, "3", [pr(2, 1)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();

    const skel = mount().querySelector<HTMLElement>(".git-prs-connection .git-repo-skeleton");
    expect(skel?.querySelector(".git-repo-skel-label")?.textContent).toContain("github.com");
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    expect(mount().getAttribute("aria-busy")).toBe("true");

    frame(entry(githubForge, "4", [pr(1, 0)]));
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(2);
    expect(mount().getAttribute("aria-busy")).toBeNull();
  });

  it("paints a failed connection's error in place of its groups", async () => {
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(
      entry(githubForge, "3", [pr(1, 0)], {
        state: "failed",
        error: { code: "scope_insufficient", kind: "forbidden" },
      }),
      entry(gitlabForge, "3", [pr(2, 1)]),
    );
    const { refreshPRs } = await load();
    await refreshPRs();

    const err = mount().querySelector(".git-prs-connection .git-multirepo-error");
    expect(err?.textContent).toContain("github.com");
    expect(err?.textContent).toContain("missing a permission");
    expect(mount().querySelector('[data-repo="cplieger/one"]')).toBeNull();
    expect(mount().querySelector('[data-repo="cplieger/two"]')).not.toBeNull();
  });

  it("names the wait a rate-limited connection was given", async () => {
    routeAPI();
    serve(
      entry(githubForge, "3", [], {
        state: "failed",
        fetched_at: Date.now(),
        error: { code: "rate_limited", kind: "rate_limited", retry_after_s: 42 },
      }),
    );
    const { refreshPRs } = await load();
    await refreshPRs();
    expect(mount().querySelector(".git-multirepo-error")?.textContent).toContain("42 seconds");
  });

  it("asks for a cycle when a connected forge has no entry yet, and paints it loading", async () => {
    routeAPI({ forges: [githubForge, gitlabForge] });
    serve(entry(gitlabForge, "3", [pr(2, 1)]));
    const { refreshPRs } = await load();
    await refreshPRs();

    const { requestPRCycle } = await actions();
    expect(requestPRCycle.dispatch).toHaveBeenCalledTimes(1);
    expect(
      mount().querySelector(".git-prs-connection .git-repo-skel-label")?.textContent,
    ).toContain("github.com");
  });

  it("asks for no cycle when every connected forge has an entry", async () => {
    routeAPI();
    serveRows([pr(1)]);
    const { refreshPRs } = await load();
    await refreshPRs();
    const { requestPRCycle } = await actions();
    expect(requestPRCycle.dispatch).not.toHaveBeenCalled();
  });

  it("adopts the inventory whole on a reconcile, whatever the held cycle", async () => {
    // A server that restarted counts its cycles from 1 again, and the reconcile is
    // the stream saying the held entries cannot be trusted.
    routeAPI();
    serveRows([pr(1)], githubForge, "500");
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();

    serveRows([pr(2)], githubForge, "2");
    H.bus.get("transport:reconcile")?.({
      cause: "full:hello",
      signal: new AbortController().signal,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(mount().querySelector(".git-pr-row-number")?.textContent).toBe("#2");
  });

  it("records the stamp of each entry it adopts for the wake digest, and not of one it kept", async () => {
    const { versionMap } = await import("./subject-versions.js");
    const held = (): string | undefined =>
      versionMap()
        .snapshot()
        .held.find((h) => h.kind === "forge_inventory" && h.ref === giteaForge.id)?.version;
    routeAPI({ forges: [giteaForge] });
    serveRows([pr(1)], giteaForge, "31");
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    expect(held()).toBe("31");

    frame(entry(giteaForge, "33", [pr(2)]));
    serveRows([pr(1)], giteaForge, "32");
    await refreshPRs();
    expect(held()).toBe("31");
  });

  it("names the repository by repo_id in every action a row dispatches", async () => {
    routeAPI();
    serveRows([pr(7, 0, {}, { merge_blocked: "none" })]);
    const { refreshPRs } = await load();
    const { mergePR, closePR } = await actions();
    await refreshPRs();

    const row = mount().querySelector(".git-pr-row");
    for (const label of ["Merge", "Close"]) {
      const button = [...(row?.querySelectorAll("button") ?? [])].find(
        (b) => b.textContent === label,
      );
      button?.click();
      await vi.advanceTimersByTimeAsync(0);
    }
    // A close is sent once its undo window lapses.
    await vi.advanceTimersByTimeAsync(8000);

    for (const dispatch of [mergePR.dispatch, closePR.dispatch]) {
      expect(vi.mocked(dispatch).mock.calls[0]?.[0]).toMatchObject({
        forge_id: "github:github.com",
        repo_id: repos[0]?.repo_id,
        owner: "cplieger",
        name: "one",
        pr_number: 7,
      });
    }
  });
});

describe("PRs tab row identity across paints", () => {
  it("paints each PR exactly once when the tab repaints", async () => {
    routeAPI();
    serveRows([pr(7)]);
    const { refreshPRs } = await load();

    // Arriving at the tab, then any second paint at all: pressing refresh, one
    // keystroke in the filter, a forges_changed frame, leaving and coming back.
    await refreshPRs();
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);

    serveRows([pr(7)], githubForge, "2");
    await refreshPRs();
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);

    // A row reconcile cannot see, match or remove a row that carries no key,
    // so an unkeyed row is a row the next paint duplicates.
    for (const row of mount().querySelectorAll(".git-pr-row")) {
      expect(row.getAttribute("data-reconcile-key")).toBe("github:github.com:7");
    }
  });

  it("repaints a surviving row from the newer entry", async () => {
    routeAPI();
    // merge_blocked is per-cycle: the forge answers `unknown` while it is still
    // computing mergeability and `none` once the PR is mergeable. `:not(.btn-danger)`
    // excludes Close, and Merge is the first button appended to the actions row.
    serveRows([pr(7, 0, {}, { merge_blocked: "unknown" })]);
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    const merge = (): HTMLButtonElement | null =>
      mount().querySelector<HTMLButtonElement>(".git-pr-row-actions .btn-small:not(.btn-danger)");
    expect(merge()?.disabled).toBe(true);

    frame(entry(githubForge, "2", [pr(7, 0, {}, { merge_blocked: "none" })]));
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    expect(merge()?.disabled).toBe(false);
  });

  // The Gitea family names no block cause on any row, so the verdict decides:
  // reading `unknown` alone as blocked would disable Merge on every Gitea and
  // Codeberg row.
  it("enables Merge on a Gitea row that names no cause and reads mergeable", async () => {
    routeAPI({ forges: [giteaForge] });
    serveRows([pr(7, 0, {}, { merge_blocked: "unknown", mergeable: "yes" })], giteaForge);
    const { refreshPRs } = await load();
    const merge = (): HTMLButtonElement | null =>
      mount().querySelector<HTMLButtonElement>(".git-pr-row-actions .btn-small:not(.btn-danger)");
    await refreshPRs();
    expect(merge()?.disabled).toBe(false);

    serveRows([pr(7, 0, {}, { merge_blocked: "unknown", mergeable: "unknown" })], giteaForge, "2");
    await refreshPRs();
    expect(merge()?.disabled).toBe(true);
  });
});

describe("Contributions elsewhere", () => {
  /** A ready GitHub entry: `owned` in the owner scope, `mine` in the authored one. */
  function scoped(
    cycle: string,
    owned: Record<string, unknown>[],
    mine: Record<string, unknown>[],
  ): Record<string, unknown> {
    return entry(githubForge, cycle, owned, {
      scopes: [
        { scope: "owner", owner: "cplieger", rows: owned },
        { scope: "authored", rows: mine },
      ],
    });
  }

  function group(): HTMLElement | null {
    return mount().querySelector<HTMLElement>('[data-group="elsewhere"]');
  }

  function toggle(): HTMLElement | null {
    return group()?.querySelector<HTMLElement>(".git-repo-section-header-toggle") ?? null;
  }

  function numbers(root: ParentNode | null | undefined): string[] {
    return [...(root?.querySelectorAll(".git-pr-row-number") ?? [])].map(
      (n) => n.textContent ?? "",
    );
  }

  it("lists an authored pull request outside the owner scopes there, last, and in no repository section", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(5, 3)]));
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(numbers(group())).toEqual(["other/lib#5"]);
    expect(mount().querySelector('[data-repo="other/lib"]')).toBeNull();
    expect(numbers(mount().querySelector('[data-repo="cplieger/one"]'))).toEqual(["#1"]);
    expect(mount().lastElementChild).toBe(group());
  });

  it("lists an authored pull request in an owner's repository once, in that repository's section", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(1, 0), pr(2, 0)]));
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(numbers(mount())).toEqual(["#2", "#1"]);
    expect(group()).toBeNull();
  });

  it("renders collapsed, counting its rows, each a link to the forge with no action control", async () => {
    routeAPI();
    serve(
      scoped(
        "1",
        [pr(1, 0)],
        [pr(5, 3, {}, { checks: "failing", merge_blocked: "checks_failing" }), pr(6, 3)],
      ),
    );
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
    expect(toggle()?.textContent).toContain("Contributions elsewhere");
    expect(group()?.querySelector(".git-repo-section-meta")?.textContent).toBe("2 open");
    expect([...(group()?.querySelectorAll("button") ?? [])]).toEqual([toggle()]);
    expect(group()?.querySelector(".git-pr-row-actions")).toBeNull();
    expect(
      [...(group()?.querySelectorAll("a.git-pr-row-title") ?? [])].map((a) =>
        a.getAttribute("href"),
      ),
    ).toEqual(["https://example.test/pr/6", "https://example.test/pr/5"]);
  });

  it("is absent when no authored pull request lies outside the scopes", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], []));
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(group()).toBeNull();
    expect(mount().querySelectorAll(".git-repo-section")).toHaveLength(1);
  });

  it("goes when a later entry no longer carries a contribution", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(5, 3)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();
    expect(group()).not.toBeNull();

    frame(scoped("2", [pr(1, 0)], []));
    expect(group()).toBeNull();
  });

  it("is the pane's content, with no All caught up, when only contributions are open", async () => {
    routeAPI();
    serve(scoped("1", [], [pr(5, 3)]));
    const { refreshPRs } = await load();
    await refreshPRs();

    expect(mount().querySelector(".git-multirepo-empty")).toBeNull();
    expect(numbers(group())).toEqual(["other/lib#5"]);
  });

  it("opens under a filter that matches a contribution, and closes again when it is cleared", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(5, 3, { title: "Teach the lib to fly" }), pr(6, 3)]));
    const { refreshPRs } = await load();
    await refreshPRs();

    applyFilter("fly");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(numbers(mount())).toEqual(["other/lib#5"]);
    expect(group()?.querySelector(".git-repo-section-meta")?.textContent).toBe("2 open");
    expect(notes.at(-1)).toBe("1 pull request; 3 pull requests scanned");

    applyFilter("cplieger/one");
    expect(group()).toBeNull();

    applyFilter("");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps the reader's open across a later entry, which repaints its rows", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(5, 3)]));
    const { refreshPRs, initPRsTab } = await load();
    initPRsTab();
    await refreshPRs();

    toggle()?.click();
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    frame(scoped("2", [pr(1, 0)], [pr(5, 3, { title: "a newer title" }), pr(7, 3)]));
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(numbers(group())).toEqual(["other/lib#7", "other/lib#5"]);
    expect(group()?.querySelectorAll(".git-pr-row-text")[1]?.textContent).toBe("a newer title");
  });

  it("opens for a notification that names one of its pull requests", async () => {
    routeAPI();
    serve(scoped("1", [pr(1, 0)], [pr(5, 3)]));
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    requestPRFocus(prIdentity(githubForge.id, repos[3]?.repo_id ?? "", 5));
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(
      group()?.querySelector(
        `[data-pr="${CSS.escape(prIdentity(githubForge.id, repos[3]?.repo_id ?? "", 5))}"]`,
      ),
    ).not.toBeNull();
  });
});

describe("an inventory that is not whole", () => {
  function hints(): string[] {
    return [...mount().querySelectorAll(".git-prs-connection .section-hint")].map(
      (n) => n.textContent,
    );
  }

  async function shown(forges: readonly Forge[], ...entries: Record<string, unknown>[]) {
    routeAPI({ forges });
    serve(...entries);
    const mod = await load();
    mod.initPRsTab();
    await mod.refreshPRs();
    return mod;
  }

  it("says a scope is still being read, with no empty state and no busy pane", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [], {
        scopes: [{ scope: "owner", owner: "cplieger", rows: [], next: "c2" }],
      }),
    );
    expect(hints()).toEqual([
      "Still reading the pull requests in cplieger on github.com, a page per cycle.",
    ]);
    expect(mount().querySelector(".git-multirepo-empty")).toBeNull();
    expect(mount().getAttribute("aria-busy")).toBeNull();
  });

  it("names the pull requests the account opened when that scope is still being read", async () => {
    await shown(
      [gitlabForge],
      entry(gitlabForge, "1", [pr(7)], {
        scopes: [{ scope: "authored", rows: [pr(7)], next: "c2" }],
      }),
    );
    expect(hints()).toEqual([
      "Still reading the pull requests you opened on gitlab.com, a page per cycle.",
    ]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
  });

  it("names why a scope stopped short, beside the rows it read", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [], {
        state: "partial",
        scopes: [
          {
            scope: "added",
            owner: "acme",
            rows: [pr(7)],
            partial: { reason: "result_window", fetched: 1, omitted_at_least: 3 },
          },
        ],
      }),
    );
    expect(hints()).toEqual([
      "Not every pull request in acme on github.com was read: the forge serves only part of this list. At least 3 more were not read.",
    ]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
  });

  it("names no page limit the walk continues past", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [], {
        state: "partial",
        scopes: [
          {
            scope: "owner",
            owner: "cplieger",
            rows: [pr(7)],
            next: "c2",
            partial: { reason: "pagination_cap", fetched: 1, omitted_at_least: 1 },
          },
        ],
      }),
    );
    expect(hints()).toEqual([
      "Still reading the pull requests in cplieger on github.com, a page per cycle.",
    ]);
  });

  it("names a list that failed beside the lists that were read", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [pr(7)], {
        state: "partial",
        error: { code: "", kind: "transient" },
      }),
    );
    expect(hints()).toEqual([
      "Could not read one of the lists on github.com. The forge did not answer as expected. The next cycle tries again.",
    ]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
  });

  it("keeps a connection that needs a new sign-in on the list, saying so", async () => {
    const lapsed = { ...gitlabForge, connected: false, reconnect_required: true };
    await shown([githubForge, lapsed], entry(githubForge, "1", [pr(7)]));
    expect(mount().querySelector(".git-multirepo-error")?.textContent).toBe(
      "Could not list pull requests on gitlab.com. This account needs a new sign-in. Reconnect it in Sources.",
    );
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
  });

  it("says a lone connection needs a new sign-in rather than that none is connected", async () => {
    const lapsed = { ...gitlabForge, connected: false, reconnect_required: true };
    await shown([lapsed]);
    expect(mount().querySelector(".git-multirepo-error")?.textContent).toBe(
      "Could not list pull requests on gitlab.com. This account needs a new sign-in. Reconnect it in Sources.",
    );
  });

  it("restates the notes when a later cycle's lists say something else", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [pr(7)], {
        state: "partial",
        scopes: [
          {
            scope: "owner",
            owner: "cplieger",
            rows: [pr(7)],
            partial: { reason: "rate_limited", fetched: 1, omitted_at_least: 0 },
          },
        ],
      }),
    );
    frame(
      entry(githubForge, "2", [pr(7)], {
        state: "partial",
        scopes: [
          {
            scope: "owner",
            owner: "cplieger",
            rows: [pr(7)],
            partial: { reason: "budget", fetched: 1, omitted_at_least: 0 },
          },
        ],
      }),
    );
    expect(hints()).toEqual([
      "Not every pull request in cplieger on github.com was read: the read stopped at its request budget.",
    ]);
  });

  it("drops the notes once a cycle reads the whole list", async () => {
    await shown(
      [githubForge],
      entry(githubForge, "1", [pr(7)], {
        scopes: [{ scope: "owner", owner: "cplieger", rows: [pr(7)], next: "c2" }],
      }),
    );
    frame(entry(githubForge, "2", [pr(7)]));
    expect(hints()).toEqual([]);
    expect(mount().querySelector(".git-prs-connection")).toBeNull();
  });
});
