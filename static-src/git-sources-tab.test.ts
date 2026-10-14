import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiPost: vi.fn(() => Promise.resolve(null)),
}));

// The rows read is routed through the mocked client, so each case answers the forge list first.
vi.mock("./actions/forge-list.js", async (importOriginal) => {
  const { apiGetTyped } = await import("./api-client.js");
  return {
    ...(await importOriginal<Record<string, unknown>>()),
    listForges: {
      dispatch: () =>
        Promise.resolve(apiGetTyped("/api/forges", (v: unknown) => v)).then((v) => v ?? null),
    },
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { apiGetTyped } from "./api-client.js";
import { initSourcesTab } from "./git-sources-tab.js";
import { _resetForTest as resetForgeStore } from "./forge-store.js";

const mockedApiGet = vi.mocked(apiGetTyped);
const KINDS = ["github", "gitlab", "codeberg", "gitea"];
const REPOS = "/api/forges/github%3Agithub.com/repos";

function answer(page: unknown, local: unknown = { repos: [] }): void {
  mockedApiGet.mockImplementation(((url: string, decode: (v: unknown) => unknown) => {
    if (url === "/api/forges") {
      return Promise.resolve({
        forges: [
          {
            id: "github:github.com",
            kind: "github",
            host: "github.com",
            username: "alice",
            account: "acct-1",
            status: "connected",
          },
        ],
        kinds: KINDS,
      });
    }
    if (url === "/api/git/repos") {
      return Promise.resolve(local === null ? null : decode(local));
    }
    if (url.startsWith(REPOS)) {
      return Promise.resolve(
        page === null ? null : decode({ account: "acct-1", ...(page as object) }),
      );
    }
    return Promise.resolve(null);
  }) as typeof apiGetTyped);
}

function repoReads(): string[] {
  return mockedApiGet.mock.calls.map(([url]) => url).filter((url) => url.startsWith(REPOS));
}

describe("the Sources tab's refresh", () => {
  beforeEach(() => {
    resetForgeStore();
    resetActionFramework();
    document.body.innerHTML = `
      <div class="page-toolbar">
        <button type="button" id="git-refresh-sources-btn" class="action-pill" aria-label="Refresh accounts and repositories"></button>
      </div>
      <div id="git-sources-mount"></div>`;
  });

  function refresh(): HTMLButtonElement {
    return document.getElementById("git-refresh-sources-btn") as HTMLButtonElement;
  }

  it("reads every repository list past the server's cache, where opening the tab reads through it", async () => {
    answer({ repos: [] });
    initSourcesTab();
    await vi.waitFor(() => expect(repoReads()).toEqual([REPOS]));

    refresh().click();

    await vi.waitFor(() => expect(repoReads()).toEqual([REPOS, `${REPOS}?refresh=1`]));
    await vi.waitFor(() => expect(refresh().dataset["asyncStatus"]).toBe("success"));
  });

  it("reports a repository list it could not read as a failed refresh", async () => {
    answer({ repos: [] });
    initSourcesTab();
    await vi.waitFor(() => expect(repoReads()).toHaveLength(1));
    answer(null);

    refresh().click();

    await vi.waitFor(() => expect(refresh().dataset["asyncStatus"]).toBe("error"));
  });

  it("reports a workspace read it could not make as a failed refresh and keeps the clone state", async () => {
    const page = {
      repos: [
        {
          repo_id: "id:alice/alpha",
          owner: "alice",
          name: "alpha",
          full_name: "alice/alpha",
          clone_url: "https://github.com/alice/alpha.git",
          affordances: {
            has_issues: { support: "unknown", source: "unknown", detail: "" },
            can_push: { support: "unknown", source: "unknown", detail: "" },
            merge_train: { support: "unknown", source: "unknown", detail: "" },
            default_branch: "",
            merge_strategies: [],
          },
        },
      ],
    };
    const state = (): string | null | undefined =>
      document
        .querySelector(".forge-account-repo-state")
        ?.querySelector("[aria-label]")
        ?.getAttribute("aria-label");
    answer(page, { repos: ["alpha"] });
    initSourcesTab();
    await vi.waitFor(() => expect(state()).toBe("Cloned"));
    answer(page, null);

    refresh().click();

    await vi.waitFor(() => expect(refresh().dataset["asyncStatus"]).toBe("error"));
    expect(state()).toBe("Cloned");
  });
});
