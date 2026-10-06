import { vi, describe, it, expect, beforeEach } from "vitest";
import type { GitRepoStatus } from "./git-types.js";

const mockApiGet = vi.fn();
vi.mock("./api-client.js", () => ({
  apiGet: (...args: unknown[]) => mockApiGet(...args),
  // Present-but-inert for real-ESM linking.
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
}));

const { renderRecentCommits } = await import("./git-changes-commit.js");

function repoStatus(): GitRepoStatus {
  return {
    repo: "marotte",
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    files: [],
    has_dirty: false,
    stashes: 0,
  };
}

/** The recent-commits section reads only `diffAbort`. */
function deps(): Parameters<typeof renderRecentCommits>[1] {
  return {
    commitMessages: new Map<string, string>(),
    diffAbort: null,
    // A throwing stub turns a future coupling into a loud failure.
    press: () => {
      throw new Error("press: not reached by the recent-commits section");
    },
  };
}

/** Expanding triggers the fetch; this flushes the apiGet().then(render) chain. */
async function expand(section: HTMLElement): Promise<void> {
  (section as HTMLDetailsElement).open = true;
  section.dispatchEvent(new Event("toggle"));
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("renderRecentCommits commit hash", () => {
  it("links each hash to the forge commit page the server derived", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject", "e4f5a6b second subject"],
      remote: "https://github.com/cplieger/marotte.git",
      behind: 0,
      commit_url_prefix: "https://github.com/cplieger/marotte/commit/",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    const links = [...section.querySelectorAll<HTMLAnchorElement>("a.git-recent-commits-sha-link")];
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://github.com/cplieger/marotte/commit/a1b2c3d",
      "https://github.com/cplieger/marotte/commit/e4f5a6b",
    ]);

    const first = section.querySelector<HTMLAnchorElement>("a.git-recent-commits-sha-link");
    // The hash alone is not a usable name; the label still contains the visible text (WCAG 2.5.3).
    expect(first?.getAttribute("aria-label")).toBe("Open commit a1b2c3d on github.com");
    expect(first?.textContent).toBe("a1b2c3d");
    // A new tab must not hand the opener a window reference.
    expect(first?.getAttribute("target")).toBe("_blank");
    expect(first?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(section.querySelector(".git-recent-commits-subject")?.textContent).toBe("first subject");
  });

  it("keeps the hash as plain text when the server derived no location", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      remote: "",
      behind: 0,
      commit_url_prefix: "",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a.git-recent-commits-sha-link")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  it("keeps the hash as plain text when the field is absent", async () => {
    mockApiGet.mockResolvedValue({ entries: ["a1b2c3d first subject"] });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a.git-recent-commits-sha-link")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  // The prefix comes from a repository's own origin remote, so the client re-checks the scheme. The `//host/`
  // spellings exercise the scheme guard: a non-special scheme with an authority has a non-empty host, so dropping
  // isSafeURL would put that href on the page.
  it.each([
    "javascript://evil.com/x",
    "data://evil.com/x",
    "vbscript://evil.com/",
    "javascript:alert(1)//",
    "data:text/html,x",
    "file:///etc/passwd",
    "not a url",
  ])("refuses to link a %s prefix", async (prefix) => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      commit_url_prefix: prefix,
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    expect(section.querySelectorAll("a")).toHaveLength(0);
    expect(section.querySelector(".git-recent-commits-sha")?.textContent).toBe("a1b2c3d");
  });

  it("renders a non-GitHub forge shape verbatim from the server prefix", async () => {
    mockApiGet.mockResolvedValue({
      entries: ["a1b2c3d first subject"],
      commit_url_prefix: "https://gitlab.example.com/grp/sub/bar/-/commit/",
    });

    const section = renderRecentCommits(repoStatus(), deps());
    await expand(section);

    const link = section.querySelector<HTMLAnchorElement>("a.git-recent-commits-sha-link");
    expect(link?.getAttribute("href")).toBe(
      "https://gitlab.example.com/grp/sub/bar/-/commit/a1b2c3d",
    );
    expect(link?.getAttribute("aria-label")).toBe("Open commit a1b2c3d on gitlab.example.com");
  });
});
