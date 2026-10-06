import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("./confirm.js", () => ({
  confirm: vi.fn(() => Promise.resolve(true)),
}));

const mocks = vi.hoisted(() => ({
  cloneDispatch: vi.fn(),
  deleteDispatch: vi.fn(),
}));

// Only the two dispatches these rows call; the rest stays real so ESM linking resolves every imported name.
vi.mock("./actions/forge.js", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const orig = await importOriginal<typeof import("./actions/forge.js")>();
  return {
    ...orig,
    cloneRepo: { ...orig.cloneRepo, dispatch: mocks.cloneDispatch },
    deleteLocal: { ...orig.deleteLocal, dispatch: mocks.deleteDispatch },
  };
});

import {
  batchFailure,
  cloneAllForAccount,
  listedRepos,
  readFirstPage,
  readNextPage,
  renderRepoActions,
  renderRepoIdentity,
  renderRepoRow,
  type RepoDeps,
} from "./forge-auth-repos.js";
import { error as toastError } from "./toast.js";
import type { Repo, RepoList } from "./wire/types.gen.js";

/** What a repository allows, with nothing read: these rows never consult it. */
const NO_AFFORDANCES: Repo["affordances"] = {
  has_issues: { support: "unknown", source: "unknown", detail: "" },
  can_push: { support: "unknown", source: "unknown", detail: "" },
  merge_train: { support: "unknown", source: "unknown", detail: "" },
  default_branch: "",
  merge_strategies: [],
};

const KIRO: Repo = {
  repo_id: "v1.63706c69656765722f2e6b69726f",
  owner: "cplieger",
  name: ".kiro",
  full_name: "cplieger/.kiro",
  clone_url: "https://github.com/cplieger/.kiro.git",
  affordances: NO_AFFORDANCES,
};

function deps(): RepoDeps {
  const running = new Map<string, Promise<void>>();
  return {
    isCloned: vi.fn(() => false),
    addCloned: vi.fn(),
    removeCloned: vi.fn(),
    bumpState: vi.fn(),
    start: (key, fn) => {
      if (running.has(key)) {
        return undefined;
      }
      const p = Promise.resolve().then(fn);
      running.set(key, p);
      const clear = (): void => {
        running.delete(key);
      };
      void p.then(clear, clear);
      return p;
    },
    running: (key) => running.get(key),
  };
}

function cloneAnswer(outcome: unknown): { outcome: Promise<unknown> } {
  return { outcome: Promise.resolve(outcome) };
}

function cloneButton(cloned: boolean, d: RepoDeps): HTMLButtonElement {
  const row = renderRepoActions(KIRO, cloned, d);
  document.body.appendChild(row);
  const btn = row.querySelector<HTMLButtonElement>('button[aria-label="Clone into workspace"]');
  if (btn === null) {
    throw new Error("clone button not rendered");
  }
  return btn;
}

function note(repo: Repo = KIRO): string {
  return renderRepoIdentity(repo).querySelector(".forge-account-error")?.textContent ?? "";
}

describe("repo row clone feedback", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    mocks.cloneDispatch.mockReset();
    mocks.deleteDispatch.mockReset();
    vi.mocked(toastError).mockReset();
  });

  // /api/git/clone answers 200 with {"error": …}; the reason is the row's, beside the pressed control.
  it("says the server's reason in the row when the clone fails, and toasts nothing", async () => {
    const reason = "fatal: destination path '.kiro' already exists and is not an empty directory.";
    mocks.cloneDispatch.mockReturnValue(
      cloneAnswer({ status: "success", value: { error: reason } }),
    );

    const d = deps();
    cloneButton(false, d).click();

    await vi.waitFor(() => {
      expect(note()).toBe(`Could not clone. ${reason}`);
    });
    expect(d.addCloned).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("says a dispatch-level failure in the row", async () => {
    mocks.cloneDispatch.mockReturnValue(
      cloneAnswer({ status: "error", error: { message: "network down" } }),
    );

    cloneButton(false, deps()).click();

    await vi.waitFor(() => {
      expect(note()).toBe("Could not clone. network down");
    });
  });

  it("marks the repo cloned and drops the row's last refusal on success", async () => {
    mocks.cloneDispatch.mockReturnValue(
      cloneAnswer({ status: "success", value: { output: "Cloning into '.kiro'..." } }),
    );

    const d = deps();
    cloneButton(false, d).click();

    await vi.waitFor(() => {
      expect(d.addCloned).toHaveBeenCalledWith(".kiro");
    });
    expect(note()).toBe("");
    expect(toastError).not.toHaveBeenCalled();
  });
});

describe("a repository row's clone state", () => {
  // ARIA forbids a name on a generic element, so a label on a bare span is dropped.
  it.each([
    [true, "Cloned"],
    [false, "Remote, not cloned"],
  ])(
    "names a row whose cloned state is %s as %j on an element that may carry a name",
    (cloned, label) => {
      const d = deps();
      vi.mocked(d.isCloned).mockReturnValue(cloned);
      const state = renderRepoRow(KIRO, d).querySelector(".forge-account-repo-state")!;
      const named = [state, ...state.querySelectorAll("[aria-label]")].filter((e) =>
        e.hasAttribute("aria-label"),
      );

      expect(named.map((e) => [e.getAttribute("role"), e.getAttribute("aria-label")])).toEqual([
        ["img", label],
      ]);
    },
  );
});

describe("batch clone outcome", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    mocks.cloneDispatch.mockReset();
    vi.mocked(toastError).mockReset();
  });

  function repo(name: string): Repo {
    return {
      repo_id: `id:cplieger/${name}`,
      owner: "cplieger",
      name,
      full_name: `cplieger/${name}`,
      clone_url: `https://github.com/cplieger/${name}.git`,
      affordances: NO_AFFORDANCES,
    };
  }

  it("names the one repo that failed", async () => {
    mocks.cloneDispatch.mockImplementation(({ url }: { url: string }) =>
      cloneAnswer({
        status: "success",
        value: url.includes("/loki.git") ? { error: "signal: killed" } : {},
      }),
    );
    const btn = document.createElement("button");
    const d = deps();
    const sentence = await cloneAllForAccount([repo("alpha"), repo("loki"), repo("beta")], btn, d);
    expect(sentence).toBe("Could not clone cplieger/loki (1 of 3 repos).");
    expect(note(repo("loki"))).toBe("Could not clone. signal: killed");
    expect(d.addCloned).toHaveBeenCalledTimes(2);
    expect(toastError).not.toHaveBeenCalled();
  });

  it("caps the named repos at three and counts the rest", () => {
    expect(batchFailure("clone", ["a", "b", "c", "d", "e"], 63)).toBe(
      "Could not clone a, b, c and 2 more (5 of 63 repos).",
    );
  });

  it("shows git's percent on the button while a repo transfers", async () => {
    const btn = document.createElement("button");
    let duringProgress = "";
    mocks.cloneDispatch.mockImplementation(
      ({ onProgress }: { onProgress?: (line: string) => void }) => {
        onProgress?.("Receiving objects:  42% (215/511)");
        duringProgress = btn.textContent;
        return cloneAnswer({ status: "success", value: {} });
      },
    );
    await cloneAllForAccount([repo("loki")], btn, deps());
    expect(duringProgress).toBe("Cloning 1/1 (42%)…");
  });

  it("says nothing when every clone lands", async () => {
    mocks.cloneDispatch.mockReturnValue(cloneAnswer({ status: "success", value: {} }));
    const btn = document.createElement("button");
    expect(await cloneAllForAccount([repo("alpha"), repo("beta")], btn, deps())).toBe("");
  });
});

describe("an account's repository listing", () => {
  function row(name: string): Repo {
    return {
      repo_id: `id:${name}`,
      owner: "cplieger",
      name,
      full_name: `cplieger/${name}`,
      affordances: NO_AFFORDANCES,
    };
  }

  function page(names: string[], next = "", partial?: RepoList["partial"]): RepoList {
    return { repos: names.map(row), next, ...(partial === undefined ? {} : { partial }) };
  }

  function names(repos: readonly Repo[]): string[] {
    return repos.map((r) => r.name);
  }

  it("holds a first page's rows and the cursor it answered", () => {
    const l = readFirstPage(undefined, page(["a", "b"], "c2"));
    expect(names(listedRepos(l))).toEqual(["a", "b"]);
    expect(l.next).toBe("c2");
    expect(l.unread).toBe(false);
  });

  it("adds a later page's rows once each, taking its cursor and its partial", () => {
    const l = readNextPage(
      readFirstPage(undefined, page(["a", "b"], "c2")),
      page(["b", "c"], "", { reason: "rate_limited", fetched: 2, omitted_at_least: 4 }),
    );
    expect(names(listedRepos(l))).toEqual(["a", "b", "c"]);
    expect(l.next).toBe("");
    expect(l.partial?.reason).toBe("rate_limited");
  });

  it("keeps the pages loaded past the first while a re-read first page names the cursor they continued from", () => {
    const loaded = readNextPage(
      readFirstPage(undefined, page(["a", "b"], "c2")),
      page(["c"], "c3"),
    );
    const l = readFirstPage(loaded, page(["a", "b2"], "c2"));
    expect(names(listedRepos(l))).toEqual(["a", "b2", "c"]);
    expect(l.next).toBe("c3");
  });

  it("starts over from a re-read first page that names another cursor", () => {
    const loaded = readNextPage(
      readFirstPage(undefined, page(["a", "b"], "c2")),
      page(["c"], "c3"),
    );
    const l = readFirstPage(loaded, page(["a", "d"], "x2"));
    expect(names(listedRepos(l))).toEqual(["a", "d"]);
    expect(l.next).toBe("x2");
  });

  it("keeps the rows it holds when a later read fails, and marks them stale until a read succeeds", () => {
    const held = readFirstPage(undefined, page(["a"], "c2"));
    const failed = readFirstPage(held, null);
    expect(names(listedRepos(failed))).toEqual(["a"]);
    expect(failed.next).toBe("c2");
    expect(failed.unread).toBe(false);
    expect(failed.stale).toBe(true);
    expect(readNextPage(failed, page(["b"])).stale).toBe(true);
    expect(readFirstPage(failed, page(["a"], "c2")).stale).toBe(false);
  });

  it("is unread with nothing held when a first read fails", () => {
    const none = readFirstPage(undefined, null);
    expect(none.unread).toBe(true);
    expect(none.stale).toBe(false);
    expect(listedRepos(none)).toEqual([]);
    expect(none.next).toBe("");
  });
});
