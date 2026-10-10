// A control that starts a request is busy at once, stays busy through a repaint while it runs, and shows its
// outcome in place. Only the network is held.

import { describe, it, expect, vi, beforeEach, beforeAll, afterAll } from "vitest";

const H = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPost: vi.fn(),
  confirm: vi.fn(async (_message: string, _label?: string, _variant?: string) => true),
  dialogOpen: vi.fn(),
  dialogClose: vi.fn(),
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: H.apiGet,
  apiGetTyped: H.apiGetTyped,
  apiPost: H.apiPost,
}));
vi.mock("./toast.js", async (importOriginal) => {
  const { toastMock } = await import("./__test-helpers__/toast-mock.js");
  return { ...(await importOriginal<Record<string, unknown>>()), ...toastMock() };
});
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  confirm: H.confirm,
}));
// The forge store reads the list through an action; routed through the mocked client so each describe answers it.
vi.mock("./forge-store.js", async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>();
  const { apiGetTyped } = await import("./api-client.js");
  const read = (): unknown => apiGetTyped("/api/forges", (v: unknown) => v);
  return { ...orig, refreshForges: read, ensureForges: read };
});
vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  presentedTag: () => "profile-tag",
}));
vi.mock("./search-popup.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createSearchPopup: () => ({ open: vi.fn(), close: vi.fn(), toggle: vi.fn() }),
}));
vi.mock("./git-scroll.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));
// The real confirm's `ask` imports `openDialog` and `closeDialog`, so the originals stay.
vi.mock("@cplieger/ui-primitives/dialog", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  createDialog: () => ({ open: H.dialogOpen, close: H.dialogClose }),
}));

import * as toast from "./toast.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { initChangesTab, refreshChanges } from "./git-changes-tab.js";
import type { GitRepoStatus } from "./git-types.js";
import {
  heldFetch,
  json,
  outcomesOf,
  settle,
  expectBusy,
  expectIdle,
} from "./__test-helpers__/press-feedback.js";

beforeEach(() => {
  H.confirm.mockImplementation(async () => true);
});

describe("the Changes tab", () => {
  let host: HTMLElement;
  let repos: GitRepoStatus[] = [];
  let seq = 0;

  // One reset for the describe: the toolbar's loading binding is made once at init, and a later reset would leave it
  // reading a registry the actions do not write.
  beforeAll(() => {
    resetActionFramework();
    host = document.createElement("div");
    host.innerHTML = `
      <div class="git-tab-toolbar">
        <button type="button" id="git-pull-all-btn" class="icon-btn" aria-label="Pull every repo that can fast-forward"></button>
        <button type="button" id="git-refresh-all-btn" class="icon-btn" aria-label="Refresh all repos"></button>
      </div>
      <div id="git-changes-mount"></div>`;
    document.body.appendChild(host);
    initChangesTab();
  });

  afterAll(() => {
    host.remove();
  });

  beforeEach(() => {
    H.apiGet.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith("/api/git/status-all") ? { repos } : null),
    );
  });

  /** Behind, ahead, stashed and one change each side of the index, so every control renders. */
  function repoStatus(): GitRepoStatus {
    seq += 1;
    return {
      repo: `web${String(seq)}`,
      is_repo: true,
      branch: "main",
      ahead: 1,
      behind: 2,
      has_dirty: true,
      stashes: 1,
      files: [
        { path: "a.ts", status: "M", staged: false, display: "Modified" },
        { path: "b.ts", status: "M", staged: true, display: "Modified" },
      ],
    };
  }

  async function show(...rs: GitRepoStatus[]): Promise<void> {
    repos = rs;
    await refreshChanges();
  }

  function section(name: string): HTMLElement {
    return document.querySelector<HTMLElement>(`#git-changes-mount section[data-repo="${name}"]`)!;
  }

  function controls(name: string): HTMLButtonElement[] {
    return [
      ...section(name).querySelectorAll<HTMLButtonElement>(
        ".git-repo-action-bar button, .git-file-group-actions button, .git-file-actions button, .git-commit-row button",
      ),
    ];
  }

  function byLabel(name: string, label: string): HTMLButtonElement | undefined {
    return controls(name).find((b) => b.textContent.trim() === label);
  }

  function rowControl(name: string, path: string, label: string): HTMLButtonElement | undefined {
    const li = [...section(name).querySelectorAll<HTMLElement>(".git-file-row")].find(
      (r) => r.querySelector(".git-file-path")?.textContent === path,
    );
    return [...(li?.querySelectorAll<HTMLButtonElement>(".git-file-actions button") ?? [])].find(
      (b) => b.textContent.trim() === label,
    );
  }

  function note(name: string): string {
    return section(name).querySelector(".git-repo-note")?.textContent ?? "";
  }

  function commitBox(name: string): HTMLTextAreaElement {
    return section(name).querySelector<HTMLTextAreaElement>(".git-commit-input")!;
  }

  interface Press {
    name: string;
    route: string;
    /** What the refused sentence says the press could not do. */
    could: string;
    find: (repo: string) => HTMLButtonElement | undefined;
    arrange?: (repo: string) => void;
  }

  const PRESSES: Press[] = [
    {
      name: "Pull",
      route: "/api/git/pull",
      could: "pull",
      find: (r) => byLabel(r, "Pull ↓2"),
    },
    { name: "Push", route: "/api/git/push", could: "push", find: (r) => byLabel(r, "Push") },
    {
      name: "Stash",
      route: "/api/git/stash",
      could: "stash the changes",
      find: (r) => byLabel(r, "Stash"),
    },
    {
      name: "Pop",
      route: "/api/git/stash-pop",
      could: "pop the stash",
      find: (r) => byLabel(r, "Pop"),
    },
    {
      name: "Stage all",
      route: "/api/git/stage",
      could: "stage 1 file",
      find: (r) => byLabel(r, "Stage all"),
    },
    {
      name: "Unstage all",
      route: "/api/git/unstage",
      could: "unstage 1 file",
      find: (r) => byLabel(r, "Unstage all"),
    },
    {
      name: "a file's Stage",
      route: "/api/git/stage",
      could: "stage a.ts",
      find: (r) => rowControl(r, "a.ts", "Stage"),
    },
    {
      name: "a file's Unstage",
      route: "/api/git/unstage",
      could: "unstage b.ts",
      find: (r) => rowControl(r, "b.ts", "Unstage"),
    },
    {
      name: "Commit",
      route: "/api/git/commit",
      could: "commit",
      find: (r) => byLabel(r, "Commit 1 file"),
      arrange: (r) => {
        commitBox(r).value = "fix: a change";
      },
    },
    {
      name: "AI message",
      route: "/api/git/commit-message",
      could: "write a commit message",
      find: (r) => byLabel(r, "AI message"),
    },
  ];

  describe.each(PRESSES)("$name", (p) => {
    it("is busy from the press and through a repaint, holding only its own repository's other controls", async () => {
      const mine = repoStatus();
      const other = repoStatus();
      await show(mine, other);
      const calls = heldFetch();
      p.arrange?.(mine.repo);
      const btn = p.find(mine.repo)!;

      btn.click();

      expectBusy(btn);
      for (const c of controls(mine.repo).filter((c) => c !== btn)) {
        expect(c.disabled, `${c.textContent} waits`).toBe(true);
        expect(c.getAttribute("aria-busy"), `${c.textContent} is not the one working`).toBeNull();
      }
      for (const c of controls(other.repo)) {
        expectIdle(c);
      }
      await vi.waitFor(() => expect(calls).toHaveLength(1));
      expect(calls[0]!.url).toBe(p.route);

      await refreshChanges();

      expectBusy(p.find(mine.repo));
      for (const c of controls(mine.repo).filter((c) => c.getAttribute("aria-busy") === null)) {
        expect(c.disabled, `${c.textContent} still waits`).toBe(true);
      }
      calls[0]!.answer(json({ output: "" }));
      await vi.waitFor(() => {
        for (const c of controls(mine.repo)) {
          expectIdle(c);
          expect(c.querySelector(".btn-async-spinner")).toBeNull();
        }
      });
      expect(note(mine.repo)).toBe("");
    });

    it("says a refusal in its section in git's words until the repository's next press", async () => {
      const mine = repoStatus();
      await show(mine);
      const calls = heldFetch();
      p.arrange?.(mine.repo);

      p.find(mine.repo)!.click();
      await vi.waitFor(() => expect(calls).toHaveLength(1));
      calls[0]!.answer(json({ error: "fatal: index.lock exists" }));

      await vi.waitFor(() =>
        expect(note(mine.repo)).toBe(`Could not ${p.could}. fatal: index.lock exists`),
      );
      await vi.waitFor(() => expectIdle(p.find(mine.repo)), { timeout: 3000 });
      expect(toast.error).not.toHaveBeenCalled();
      await refreshChanges();
      expect(note(mine.repo)).toBe(`Could not ${p.could}. fatal: index.lock exists`);

      p.arrange?.(mine.repo);
      p.find(mine.repo)!.click();
      await vi.waitFor(() => expect(note(mine.repo)).toBe(""));
      await vi.waitFor(() => expect(calls).toHaveLength(2));
      calls[1]!.answer(json({ output: "" }));
      await vi.waitFor(() => expectIdle(p.find(mine.repo)), { timeout: 3000 });
      expect(note(mine.repo)).toBe("");
    });
  });

  it("Pull reads the section again once it lands, so a repository no longer behind offers no Pull", async () => {
    const mine = repoStatus();
    await show(mine);
    const calls = heldFetch();
    // By tooltip: the outcome glyph stands in for the label while it shows.
    const pullControl = (): Element | null =>
      section(mine.repo).querySelector('[data-tooltip="git pull --ff-only"]');
    expect(pullControl()).not.toBeNull();

    byLabel(mine.repo, "Pull ↓2")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    repos = [{ ...mine, behind: 0 }];
    calls[0]!.answer(json({ output: "Fast-forward" }));

    await vi.waitFor(() => expect(pullControl()).toBeNull());
    expectIdle(byLabel(mine.repo, "Push"));
  });

  // A read naming repositories is answered after a rescan; an unnamed one from the last scan, which predates the press.
  it("a landed press reads its own repository again", async () => {
    const mine = repoStatus();
    await show(mine, repoStatus());
    const calls = heldFetch();
    H.apiGet.mockClear();

    rowControl(mine.repo, "a.ts", "Stage")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(json({ output: "" }));

    await vi.waitFor(() => expect(H.apiGet).toHaveBeenCalled());
    expect(H.apiGet.mock.calls.map((c) => c[0])).toEqual([
      `/api/git/status-all?paths=${mine.repo}`,
    ]);
  });

  it("Pull all reads again the repositories its pass pulled or held", async () => {
    const pulled = repoStatus();
    const held = repoStatus();
    const current = repoStatus();
    await show(pulled, held, current);
    const calls = heldFetch();
    H.apiGet.mockClear();

    (document.getElementById("git-pull-all-btn") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    calls[0]!.answer(
      json({
        repos: [
          { repo: pulled.repo, verdict: "pulled" },
          { repo: held.repo, verdict: "blocked", reason: "dirty", detail: "Uncommitted changes." },
          { repo: current.repo, verdict: "skipped", reason: "up_to_date" },
        ],
      }),
    );

    await vi.waitFor(() => expect(H.apiGet).toHaveBeenCalled());
    expect(H.apiGet.mock.calls.map((c) => c[0])).toEqual([
      `/api/git/status-all?paths=${encodeURIComponent(`${pulled.repo},${held.repo}`)}`,
    ]);
  });

  it("a press on one repository leaves another repository's controls pressable", async () => {
    const mine = repoStatus();
    const other = repoStatus();
    await show(mine, other);
    const calls = heldFetch();

    byLabel(mine.repo, "Pull ↓2")!.click();
    byLabel(other.repo, "Pull ↓2")!.click();

    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls.map((c) => c.body)).toEqual(
      expect.arrayContaining([{ repo: mine.repo }, { repo: other.repo }]),
    );
    expectBusy(byLabel(other.repo, "Pull ↓2"));
    calls[0]!.answer(json({ output: "" }));
    calls[1]!.answer(json({ output: "" }));
    await vi.waitFor(() => {
      for (const c of [...controls(mine.repo), ...controls(other.repo)]) {
        expectIdle(c);
      }
    });
  });

  it("Commit clears its message once it lands, and AI message fills the box on screen", async () => {
    const mine = repoStatus();
    await show(mine);
    const calls = heldFetch();

    byLabel(mine.repo, "AI message")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    await refreshChanges();
    calls[0]!.answer(json({ output: "feat: drafted" }));
    await vi.waitFor(() => expect(commitBox(mine.repo).value).toBe("feat: drafted"));

    await vi.waitFor(() => expectIdle(byLabel(mine.repo, "Commit 1 file")), { timeout: 3000 });
    byLabel(mine.repo, "Commit 1 file")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1]!.body).toEqual({ repo: mine.repo, message: "feat: drafted" });
    calls[1]!.answer(json({ output: "[main abc1234] feat: drafted" }));
    await vi.waitFor(() => expect(commitBox(mine.repo).value).toBe(""));
  });

  it("Commit with no message says so in its section and sends nothing", async () => {
    const mine = repoStatus();
    await show(mine);
    const calls = heldFetch();

    byLabel(mine.repo, "Commit 1 file")!.click();

    await vi.waitFor(() =>
      expect(note(mine.repo)).toBe("Could not commit. Write a commit message first."),
    );
    expect(calls).toHaveLength(0);
  });

  it("Discard all is busy once confirmed and sends nothing when the confirm is cancelled", async () => {
    const mine = repoStatus();
    await show(mine);
    const calls = heldFetch();
    let decide: (ok: boolean) => void = () => undefined;
    H.confirm.mockImplementation(
      () =>
        new Promise<boolean>((r) => {
          decide = r;
        }),
    );
    const discard = byLabel(mine.repo, "Discard all")!;
    const outcomes = outcomesOf(discard);

    discard.click();
    discard.click();
    await vi.waitFor(() => expect(H.confirm).toHaveBeenCalledTimes(1));
    decide(false);
    await settle();
    expect(H.confirm).toHaveBeenCalledTimes(1);
    expect(calls).toHaveLength(0);
    expect(outcomes).toEqual([]);

    H.confirm.mockImplementation(async () => true);
    byLabel(mine.repo, "Discard all")!.click();
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.url).toBe("/api/git/discard");
    expectBusy(byLabel(mine.repo, "Discard all"));
    calls[0]!.answer(json({ error: "error: pathspec did not match" }));
    await vi.waitFor(() =>
      expect(note(mine.repo)).toBe("Could not discard 1 file. error: pathspec did not match"),
    );
  });

  it("a file's Discard sends nothing and reports no outcome when its confirm is cancelled", async () => {
    const mine = repoStatus();
    await show(mine);
    const calls = heldFetch();
    H.confirm.mockImplementation(async () => false);
    const discard = rowControl(mine.repo, "a.ts", "Discard")!;
    const outcomes = outcomesOf(discard);

    discard.click();

    await vi.waitFor(() => expect(H.confirm).toHaveBeenCalledTimes(1));
    await settle();
    expect(calls).toHaveLength(0);
    expect(outcomes).toEqual([]);
    expectIdle(rowControl(mine.repo, "a.ts", "Discard"));
  });

  it("Pull all is busy until its pass lands, never spinning while usable, and says a refusal beside it", async () => {
    await show(repoStatus());
    const calls = heldFetch();
    const pullAll = document.getElementById("git-pull-all-btn") as HTMLButtonElement;
    let release: () => void = () => undefined;
    H.apiGet.mockImplementation(
      (url: string) =>
        new Promise((r) => {
          release = () => {
            r(url.startsWith("/api/git/status-all") ? { repos } : null);
          };
        }),
    );

    pullAll.click();

    expectBusy(pullAll);
    await vi.waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]!.url).toBe("/api/git/pull-all");
    calls[0]!.answer(json({ repos: [] }));
    await settle();
    await settle();
    const spinning = pullAll.querySelector(".btn-async-spinner") !== null;
    expect(spinning).toBe(pullAll.disabled && pullAll.getAttribute("aria-busy") === "true");
    release();
    await vi.waitFor(() => expectIdle(pullAll), { timeout: 3000 });

    H.apiGet.mockImplementation((url: string) =>
      Promise.resolve(url.startsWith("/api/git/status-all") ? { repos } : null),
    );
    pullAll.click();
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    calls[1]!.answer(json({ error: "another pass is running" }, 409));
    await vi.waitFor(() =>
      expect(host.querySelector(".git-tab-toolbar-error")?.textContent).toBe(
        "Could not pull every repository. another pass is running",
      ),
    );
    await vi.waitFor(() => expectIdle(pullAll), { timeout: 3000 });
    expect(toast.error).not.toHaveBeenCalled();

    pullAll.click();
    expect(host.querySelector(".git-tab-toolbar-error")?.textContent).toBe("");
    await vi.waitFor(() => expect(calls).toHaveLength(3));
    calls[2]!.answer(json({ repos: [] }));
    await vi.waitFor(() => expectIdle(pullAll), { timeout: 3000 });
  });

  it("Refresh reports a read it could not make as a failure, beside the error the list shows", async () => {
    await show(repoStatus());
    const refresh = document.getElementById("git-refresh-all-btn") as HTMLButtonElement;
    const outcomes = outcomesOf(refresh);
    H.apiGet.mockImplementation(() => Promise.resolve(null));

    refresh.click();

    expectBusy(refresh);
    await vi.waitFor(() =>
      expect(document.getElementById("git-changes-mount")?.textContent).toContain(
        "Failed to load git status.",
      ),
    );
    await vi.waitFor(() => expect(outcomes).toContain("error"));
    expect(outcomes).not.toContain("success");
  });
});
