import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(() => Promise.resolve(null)),
  apiPost: vi.fn(() => Promise.resolve(null)),
}));

// Routed through the mocked client so the forge list is one more path the case answers.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const forgeRead = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return {
    ...orig,
    refreshForges: forgeRead,
    ensureForges: forgeRead,
    initForgeStore: vi.fn(),
  };
});

import { resetActionFramework } from "@cplieger/actions/testing";
import { apiGetTyped } from "./api-client.js";
import { initSourcesTab } from "./git-sources-tab.js";

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
            connected: true,
            reconnect_required: false,
          },
        ],
        kinds: KINDS,
      });
    }
    if (url === "/api/git/repos") {
      return Promise.resolve(local === null ? null : decode(local));
    }
    if (url.startsWith(REPOS)) {
      return Promise.resolve(page === null ? null : decode(page));
    }
    return Promise.resolve(null);
  }) as typeof apiGetTyped);
}

function repoReads(): string[] {
  return mockedApiGet.mock.calls.map(([url]) => url).filter((url) => url.startsWith(REPOS));
}

describe("the Sources tab's refresh", () => {
  beforeEach(() => {
    resetActionFramework();
    document.body.innerHTML = `
      <div class="git-tab-toolbar">
        <button type="button" id="git-refresh-sources-btn" class="icon-btn" aria-label="Refresh accounts and repositories"></button>
      </div>
      <div id="git-sources-mount"></div>`;
  });

  function refresh(): HTMLButtonElement {
    return document.getElementById("git-refresh-sources-btn") as HTMLButtonElement;
  }

  // Each paint is followed by a background re-probe that reads the list once more through the cache.
  it("reads every repository list past the server's cache, where opening the tab reads through it", async () => {
    answer({ repos: [] });
    initSourcesTab();
    await vi.waitFor(() => expect(repoReads()).toEqual([REPOS, REPOS]));

    refresh().click();

    await vi.waitFor(() =>
      expect(repoReads()).toEqual([REPOS, REPOS, `${REPOS}?refresh=1`, REPOS]),
    );
    expect(refresh().dataset["asyncStatus"]).toBe("success");
  });

  it("reports a repository list it could not read as a failed refresh", async () => {
    answer({ repos: [] });
    initSourcesTab();
    await vi.waitFor(() => expect(repoReads()).toHaveLength(2));
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
