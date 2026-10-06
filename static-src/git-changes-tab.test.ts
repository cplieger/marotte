// The Changes tab's file list, grouped by side of the index, and its counts: entries are per side, a person counts
// files, and the two differ on a file staged and then edited again.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import type * as ModChanges from "./git-changes-tab.js";
import type { GitFileEntry, GitRepoStatus } from "./git-types.js";

// Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode (URL-keyed module map), and
// `refreshGeneration` and the abort controller are module state.
let bootSeq = 0;

const apiGet = vi.fn();
// Typed with the real signature so `mock.calls[0][0]` is the message.
const confirmDialog = vi.fn(
  async (_message: string, _label?: string, _variant?: "destructive" | "normal") => true,
);
const openChange = vi.fn();

const dispatches: { name: string; args: unknown }[] = [];

/** Set per test: everything the panel marks is derived from the verdicts. */
let pullAllResult: unknown = [];

interface FilterSeam {
  query: (q: string, ctx: unknown) => unknown;
  render: (result: unknown, q: string) => void;
}

let filterSeam: FilterSeam | null = null;

/** Exactly as the popup does: `query` records the text, `render` repaints. */
function applyFilter(q: string): void {
  if (filterSeam === null) {
    throw new Error("filter seam not captured — the module never built its popup");
  }
  const result = filterSeam.query(q, {});
  filterSeam.render(result, q);
}

function handle(value: unknown): Promise<unknown> & { outcome: Promise<unknown> } {
  return Object.assign(Promise.resolve(value), {
    outcome: Promise.resolve({ status: "success", value }),
  });
}

function recorder(name: string): { dispatch: (args: unknown) => Promise<unknown> } {
  return {
    dispatch: (args: unknown) => {
      dispatches.push({ name, args });
      return handle({ output: "" });
    },
  };
}

vi.mock("./api-client.js", () => ({
  apiGet,
  apiPost: vi.fn(),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("./bus.js", () => ({ onSSE: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: confirmDialog }));
vi.mock("./navigate.js", () => ({ openChange }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/git-changes.js", () => ({
  stage: recorder("stage"),
  unstage: recorder("unstage"),
  discard: recorder("discard"),
  pull: recorder("pull"),
  pullAll: {
    dispatch: (args: unknown) => {
      dispatches.push({ name: "pullAll", args });
      return handle(pullAllResult);
    },
  },
  push: recorder("push"),
  stash: recorder("stash"),
  stashPop: recorder("stashPop"),
  commit: recorder("commit"),
  generateCommitMessage: recorder("generateCommitMessage"),
}));
vi.mock("./search-popup.js", () => ({
  createSearchPopup: vi.fn((spec: unknown) => {
    // Capturing the spec lets a test drive the popup's query/render seam instead of reaching into the module.
    filterSeam = spec as FilterSeam;
    return { open: vi.fn(), close: vi.fn(), toggle: vi.fn() };
  }),
}));
// Pass-through: the ✓/✗ feedback is pinned in git-changes-press-feedback.test.ts, and a refused press rejects through it.
vi.mock("./async-button.js", () => ({
  withAsyncFeedback: async (_b: HTMLElement, fn: () => Promise<unknown>) => {
    try {
      return await fn();
    } catch {
      return undefined;
    }
  },
}));
vi.mock("./git-scroll.js", () => ({
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));
// The stub supplies what this suite reads: the trigger's aria-expanded, the body staying in the tree, and a trigger
// click reaching `onToggle` as the reader's own.
vi.mock("@cplieger/ui-primitives/disclosure", () => ({
  createDisclosure: (
    trigger: HTMLElement,
    _body: HTMLElement,
    opts: { open: boolean; onToggle?: (open: boolean, source: "user" | "api") => void },
  ) => {
    let open = opts.open;
    trigger.setAttribute("aria-expanded", String(open));
    trigger.addEventListener("click", () => {
      open = !open;
      trigger.setAttribute("aria-expanded", String(open));
      opts.onToggle?.(open, "user");
    });
    return { open: vi.fn(), close: vi.fn(), toggle: vi.fn() };
  },
}));
vi.mock("./chevron.js", () => ({
  chevronEl: () => document.createElement("span"),
}));

function file(path: string, status: string, staged = false, orig?: string): GitFileEntry {
  const labels: Record<string, string> = {
    M: "Modified",
    A: "Added",
    D: "Deleted",
    R: "Renamed",
    C: "Copied",
    T: "Typechange",
    "?": "Untracked",
    U: "Unmerged",
  };
  const e: GitFileEntry = { path, status, staged, display: labels[status] ?? "Unknown" };
  if (orig !== undefined) {
    e.orig_path = orig;
  }
  return e;
}

function repo(files: GitFileEntry[], over: Partial<GitRepoStatus> = {}): GitRepoStatus {
  return {
    repo: "demo",
    is_repo: true,
    branch: "main",
    remote: "https://example.invalid/demo.git",
    ahead: 0,
    behind: 0,
    files,
    has_dirty: files.length > 0,
    stashes: 0,
    ...over,
  };
}

async function load(): Promise<typeof ModChanges> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./git-changes-tab.ts?boot=${String(bootSeq)}`
  )) as typeof ModChanges;
}

async function paintRepos(repos: GitRepoStatus[]): Promise<HTMLElement> {
  apiGet.mockResolvedValue({ repos });
  const { refreshChanges } = await load();
  await refreshChanges();
  const mount = document.getElementById("git-changes-mount");
  if (mount === null) {
    throw new Error("mount missing");
  }
  return mount;
}

function group(mount: HTMLElement, kind: "staged" | "unstaged"): HTMLElement | null {
  return mount.querySelector<HTMLElement>(`.git-file-group-${kind}`);
}

function rowPaths(scope: HTMLElement | null): string[] {
  if (scope === null) {
    return [];
  }
  return [...scope.querySelectorAll(".git-file-path")].map((e) => e.textContent ?? "");
}

async function clickBtn(scope: HTMLElement, label: string): Promise<void> {
  const btn = [...scope.querySelectorAll("button")].find((b) => b.textContent === label);
  if (btn === undefined) {
    throw new Error(
      `no button "${label}" in scope; saw: ${[...scope.querySelectorAll("button")]
        .map((b) => JSON.stringify(b.textContent))
        .join(", ")}`,
    );
  }
  btn.click();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  apiGet.mockReset();
  confirmDialog.mockReset();
  confirmDialog.mockResolvedValue(true);
  dispatches.length = 0;
  filterSeam = null;
  pullAllResult = [];
  document.body.innerHTML = `<div id="git-view"><div id="git-changes-mount" class="git-multirepo-mount" aria-live="polite"></div></div>`;
});

describe("the file list is grouped by side of the index", () => {
  it("splits staged files from unstaged ones under their own headings", async () => {
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["a.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["b.ts", "c.ts"]);
    expect(group(mount, "staged")?.querySelector(".git-file-group-label")?.textContent).toBe(
      "Staged",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-label")?.textContent).toBe(
      "Changes",
    );
  });

  it("states each group's file count in its heading", async () => {
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(group(mount, "staged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "2 files",
    );
  });

  it("renders no empty second heading when everything is staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M", true)])]);

    expect(group(mount, "staged")).not.toBeNull();
    expect(group(mount, "unstaged")).toBeNull();
  });

  it("renders no staged heading when nothing is staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(group(mount, "staged")).toBeNull();
    expect(group(mount, "unstaged")).not.toBeNull();
  });

  it("no longer marks staged-ness with a class on the row", async () => {
    // Staged-ness is the group heading; a row class would be a second, weaker channel for the same fact.
    const mount = await paintRepos([repo([file("a.ts", "M", true)])]);

    expect(mount.querySelector(".git-file-row.staged")).toBeNull();
  });

  it("announces each group to a screen reader, with its size", async () => {
    // The heading is a sibling div, and a row's status cell reads the same on both sides of the index, so without a
    // label on the list the grouping is visual only.
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(group(mount, "staged")?.querySelector("ul")?.getAttribute("aria-label")).toBe(
      "Staged, 1 file",
    );
    expect(group(mount, "unstaged")?.querySelector("ul")?.getAttribute("aria-label")).toBe(
      "Changes, 2 files",
    );
  });

  it("sorts within a group rather than across the whole list", async () => {
    const mount = await paintRepos([
      repo([file("z.ts", "M", true), file("a.ts", "M", true), file("m.ts", "M")]),
    ]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["a.ts", "z.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["m.ts"]);
  });
});

describe("a file staged and then changed again", () => {
  // git's `MM p.ts` is split into one entry per side, so the same name appears in both groups and the panel must say why.
  const partial = () => repo([file("p.ts", "M", true), file("p.ts", "M"), file("q.ts", "M")]);

  it("appears in both groups", async () => {
    const mount = await paintRepos([partial()]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["p.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["p.ts", "q.ts"]);
  });

  it("carries a partially-staged mark on BOTH of its rows", async () => {
    const mount = await paintRepos([partial()]);

    const marked = [...mount.querySelectorAll(".git-file-row")].filter(
      (r) => r.querySelector(".git-file-partial") !== null,
    );
    expect(marked).toHaveLength(2);
    for (const r of marked) {
      expect(r.querySelector(".git-file-path")?.textContent).toBe("p.ts");
    }
  });

  it("leaves an ordinary file unmarked", async () => {
    const mount = await paintRepos([partial()]);

    const q = [...mount.querySelectorAll(".git-file-row")].find(
      (r) => r.querySelector(".git-file-path")?.textContent === "q.ts",
    );
    expect(q?.querySelector(".git-file-partial")).toBeNull();
  });

  it("counts as ONE file in each group, not as two changes", async () => {
    // Entries are per side of the index; a count a person reads is per file.
    const mount = await paintRepos([partial()]);

    expect(group(mount, "staged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "2 files",
    );
  });
});

describe("each group owns the bulk action that acts on it", () => {
  it("offers Unstage all on the staged group, scoped to it", async () => {
    // The fixture mixes both sides: with everything staged a group-scoped action and a whole-repo one are the same.
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M", true), file("c.ts", "?")]),
    ]);
    const head = group(mount, "staged")?.querySelector<HTMLElement>(".git-file-group-head");
    expect(head).not.toBeNull();

    await clickBtn(head as HTMLElement, "Unstage all");

    expect(dispatches).toEqual([
      { name: "unstage", args: { repo: "demo", files: ["a.ts", "b.ts"] } },
    ]);
  });

  it("offers Stage all and Discard all on the unstaged group", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M"), file("b.ts", "?")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    const labels = [...(head?.querySelectorAll("button") ?? [])].map((b) => b.textContent);
    expect(labels).toEqual(["Stage all", "Discard all"]);
  });

  it("keeps the repo action bar to sync operations", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M"), file("b.ts", "M", true)])]);
    const bar = mount.querySelector<HTMLElement>(".git-repo-action-bar");

    const labels = [...(bar?.querySelectorAll("button") ?? [])].map((b) => b.textContent);
    expect(labels).not.toContain("Stage all");
    expect(labels).not.toContain("Discard all");
  });

  it("stages exactly the unstaged group, deduping a partially-staged path", async () => {
    const mount = await paintRepos([
      repo([file("p.ts", "M", true), file("p.ts", "M"), file("q.ts", "M")]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Stage all");

    expect(dispatches).toEqual([
      { name: "stage", args: { repo: "demo", files: ["p.ts", "q.ts"] } },
    ]);
  });
});

describe("Discard all", () => {
  it("discards the unstaged group only, and sends each path once", async () => {
    // Only unstaged entries, so a partially-staged path goes out once.
    const mount = await paintRepos([
      repo([file("p.ts", "M", true), file("p.ts", "M"), file("s.ts", "A", true)]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(dispatches).toEqual([{ name: "discard", args: { repo: "demo", files: ["p.ts"] } }]);
  });

  it("names the unstaged file count in its confirm, not the entry count", async () => {
    const mount = await paintRepos([repo([file("p.ts", "M", true), file("p.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    const msg = String(confirmDialog.mock.calls[0]?.[0] ?? "");
    expect(msg).toContain("Discard 1 unstaged change in demo?");
    expect(msg).not.toContain("2 unstaged");
  });

  it("tells the reader the staged files survive it", async () => {
    // The scope must be stated: a reader expecting a clean tree afterwards will not get one.
    const mount = await paintRepos([
      repo([file("a.ts", "M"), file("s.ts", "A", true), file("t.ts", "A", true)]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(String(confirmDialog.mock.calls[0]?.[0] ?? "")).toContain(
      "Your 2 staged files stay untouched.",
    );
  });

  it("says nothing about staged files when none are staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(String(confirmDialog.mock.calls[0]?.[0] ?? "")).not.toContain("untouched");
  });

  it("dispatches nothing when the confirm is declined", async () => {
    confirmDialog.mockResolvedValue(false);
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(dispatches).toEqual([]);
  });
});

describe("the status cell", () => {
  it("is git's letter, carrying the shared per-letter colour class", async () => {
    // The file browser's `git-st-*` palette, so a change reads the same in both views.
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("M");
    expect(cell?.className).toContain("git-st-m");
  });

  it("keeps the word as the accessible name and the tooltip", async () => {
    // A letter, not a word, so every filename starts at the same x.
    const mount = await paintRepos([repo([file("a.ts", "?")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("?");
    expect(cell?.getAttribute("aria-label")).toBe("Status: Untracked");
    expect(cell?.getAttribute("data-tooltip")).toBe("Untracked");
  });

  it("prefers the server's label over the local table", async () => {
    // The server owns the status vocabulary; a letter this client does not know yet still reads as a word.
    const mount = await paintRepos([
      repo([{ path: "a.ts", status: "Z", staged: false, display: "Something New" }]),
    ]);

    expect(mount.querySelector(".git-file-status")?.getAttribute("aria-label")).toBe(
      "Status: Something New",
    );
  });

  it("gives its tooltip the name to point at", async () => {
    // The path button is `flex: 1`, so its centre is far from the name; the tooltip anchors on the label via
    // `data-tooltip-anchor` and stays on the button, which takes focus and carries the name.
    const mount = await paintRepos([repo([file("README.md", "M")])]);
    const btn = mount.querySelector<HTMLElement>(".git-file-path");

    expect(btn?.getAttribute("data-tooltip")).toBe("README.md");
    const mark = btn?.querySelector("[data-tooltip-anchor]");
    expect(mark?.textContent, "the mark is the name, not an empty wrapper").toBe("README.md");
    expect(btn?.hasAttribute("data-tooltip-anchor"), "on the ink, not on the box").toBe(false);
  });

  it("gives a typechange its word rather than a bare T", async () => {
    // ` T`/`T ` is a file swapped with a symlink.
    const mount = await paintRepos([repo([file("link.txt", "T")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("T");
    expect(cell?.getAttribute("aria-label")).toBe("Status: Typechange");
    expect(cell?.className).toContain("git-st-t");
  });
});

describe("a renamed or copied file says where it came from", () => {
  it("shows the origin path beside the new one", async () => {
    // A rename's meaning is the pair of paths, so the old path must render.
    const mount = await paintRepos([repo([file("new.ts", "R", true, "old.ts")])]);

    expect(mount.querySelector(".git-file-path")?.textContent).toBe("new.ts");
    expect(mount.querySelector(".git-file-orig")?.textContent).toBe("\u2190 old.ts");
    expect(mount.querySelector(".git-file-orig")?.getAttribute("data-tooltip")).toBe(
      "Renamed from old.ts",
    );
  });

  it("calls a copy a copy", async () => {
    const mount = await paintRepos([repo([file("dup.ts", "C", true, "src.ts")])]);

    expect(mount.querySelector(".git-file-orig")?.getAttribute("data-tooltip")).toBe(
      "Copied from src.ts",
    );
  });

  it("adds nothing to an ordinary change", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(mount.querySelector(".git-file-orig")).toBeNull();
  });
});

describe("the commit affordance", () => {
  it("names how many files it will commit", async () => {
    // The index is the selection, so Commit names what it will write.
    const mount = await paintRepos([repo([file("a.ts", "M", true), file("b.ts", "A", true)])]);

    const labels = [...mount.querySelectorAll(".git-commit-area button")].map((b) => b.textContent);
    expect(labels).toContain("Commit 2 files");
  });

  it("counts a partially-staged file once", async () => {
    const mount = await paintRepos([repo([file("p.ts", "M", true), file("p.ts", "M")])]);

    const labels = [...mount.querySelectorAll(".git-commit-area button")].map((b) => b.textContent);
    expect(labels).toContain("Commit 1 file");
  });

  it("does not exist with nothing staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(mount.querySelector(".git-commit-area")).toBeNull();
  });
});

describe("the shipped stylesheet, for the three facts the DOM cannot show", () => {
  // The test page links no app stylesheet, so these read the sheet as source.
  const multirepo = loadCSS("22-git-multirepo.css");
  const tools = loadCSS("14-tools.css");

  it("carries no per-row staged tint at all", () => {
    // A row tint measures about 1.1:1 against the pane, invisible, so staged-ness is a heading.
    expect(multirepo).not.toContain(".git-file-row.staged");
  });

  it("leaves hover as the one background a row row-top declares for a state", () => {
    // The deleted rule outscored this one, so a staged row could not respond to hover.
    const hover = ruleContaining(multirepo, ".git-repo-section-body .git-file-row-top:hover");
    expect(hover.body).toContain("background: var(--c-bg-secondary)");
  });

  it("reveals a row's actions on keyboard focus as well as on hover", () => {
    // `.git-file-actions` rests at `opacity: 0`; without a focus reveal, Tab lands on invisible Stage and Discard buttons
    // (WCAG 2.4.7).
    const reveal = ruleContaining(
      multirepo,
      ".git-repo-section-body .git-file-row:focus-within .git-file-actions",
    );
    expect(reveal.selector).toContain(":hover .git-file-actions");
    expect(reveal.body).toContain("opacity: 1");
  });

  it("gives the status cell a FIXED width so every filename starts at one x", () => {
    // A fixed width: sized to the status word, the column left a ragged edge where a reader scans.
    const cell = ruleContaining(multirepo, ".git-file-status");
    expect(cell.body).toContain("width: 1rem");
    expect(cell.body).not.toContain("min-width");
    // The per-letter class supplies the colour.
    expect(cell.body).not.toContain("color:");
  });

  it("has a colour for every status letter the server can emit", () => {
    // Every letter has a rule, or that status inherits the surrounding grey.
    for (const letter of ["m", "a", "d", "r", "c", "t", "u"]) {
      expect(tools, `.git-st-${letter} missing`).toContain(`.git-st-${letter}`);
    }
  });

  it("keeps no separator rule now that nothing emits one", () => {
    // The file ops moved onto their groups, so the bar holds one cluster and the separator has no emitter.
    expect(tools).not.toContain(".action-bar-sep");
  });
});

describe("the path filter", () => {
  it("keeps only the matching paths", async () => {
    const mount = await paintRepos([repo([file("src/a.ts", "M"), file("docs/b.md", "M")])]);
    expect(rowPaths(mount)).toEqual(["docs/b.md", "src/a.ts"]);

    applyFilter("docs/");

    expect(rowPaths(mount)).toEqual(["docs/b.md"]);
  });

  it("shows every file in a repo whose NAME matches", async () => {
    // Naming a repo keeps its section whole; filtering its paths too would empty the section the reader asked for.
    const mount = await paintRepos([
      repo([file("src/a.ts", "M"), file("src/b.ts", "M")], { repo: "marotte" }),
    ]);

    // Neither path contains "marotte".
    applyFilter("marotte");

    expect(rowPaths(mount)).toEqual(["src/a.ts", "src/b.ts"]);
  });

  it("says No matching changes when it drops every repo", async () => {
    const mount = await paintRepos([
      repo([], { repo: "quiet", has_dirty: false }),
      repo([file("a.ts", "M")], { repo: "busy" }),
    ]);

    applyFilter("zzz-matches-nothing");

    expect(mount.querySelector(".git-repo-row-clean")).toBeNull();
    expect(mount.querySelector(".git-multirepo-empty-title")?.textContent).toBe(
      "No matching changes",
    );
  });

  it("drops a repo that matches on neither its name nor any path", async () => {
    const mount = await paintRepos([
      repo([file("src/a.ts", "M")], { repo: "one" }),
      repo([file("docs/b.md", "M")], { repo: "two" }),
    ]);

    applyFilter("docs/");

    expect(mount.querySelector('[data-repo="one"]')).toBeNull();
    expect(mount.querySelector('[data-repo="two"]')).not.toBeNull();
  });

  it("groups the filtered survivors, and counts only them", async () => {
    const mount = await paintRepos([
      repo([file("src/a.ts", "M", true), file("src/b.ts", "M"), file("docs/c.md", "M")]),
    ]);

    applyFilter("src/");

    expect(rowPaths(group(mount, "staged"))).toEqual(["src/a.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["src/b.ts"]);
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
  });

  it("opens a section the reader had collapsed when it holds a matching path", async () => {
    // The filter outranks the reader's collapse latch while a query stands, or a match sits inside an aria-hidden,
    // inert region; the latch is read again once it clears.
    const mount = await paintRepos([repo([file("src/upload-policy.ts", "M")])]);
    const header = mount.querySelector<HTMLElement>(".git-repo-section-header");
    expect(header?.getAttribute("aria-expanded")).toBe("true");
    header?.click();
    expect(header?.getAttribute("aria-expanded")).toBe("false");

    applyFilter("upload-policy");
    expect(mount.querySelector(".git-repo-section-header")?.getAttribute("aria-expanded")).toBe(
      "true",
    );
    expect(rowPaths(mount)).toEqual(["src/upload-policy.ts"]);

    applyFilter("");
    expect(mount.querySelector(".git-repo-section-header")?.getAttribute("aria-expanded")).toBe(
      "false",
    );
  });

  it("does not trim the query itself: the popup already did", async () => {
    // The popup already trims per kind; case folding stays here, paired with this module's haystack.
    const mount = await paintRepos([repo([file("src/a.ts", "M"), file("docs/b.md", "M")])]);

    applyFilter("DOCS/");
    expect(rowPaths(mount)).toEqual(["docs/b.md"]);

    applyFilter("docs/ ");
    expect(rowPaths(mount)).toEqual([]);
  });
});

// A repo with no file changes is not a repo with nothing to do.
describe("a repo with nothing uncommitted", () => {
  it("scopes its sentence to the working tree", async () => {
    const mount = await paintRepos([
      repo([], { repo: "quiet", has_dirty: false }),
      repo([file("a.ts", "M")], { repo: "busy" }),
    ]);

    expect(mount.querySelector('[data-repo="quiet"] .git-repo-row-clean')?.textContent).toBe(
      "No uncommitted changes.",
    );
    expect(mount.querySelector('[data-repo="busy"] .git-repo-row-clean')).toBeNull();
  });

  it("keeps that sentence true beside Pull, Push and Pop", async () => {
    // The row sits under the sync actions, so it reaches a repo out of sync or holding a stash.
    const mount = await paintRepos([
      repo([], { repo: "behind", has_dirty: false, behind: 3 }),
      repo([], { repo: "ahead", has_dirty: false, ahead: 2 }),
      repo([], { repo: "stashed", has_dirty: false, stashes: 1 }),
    ]);

    for (const name of ["behind", "ahead", "stashed"]) {
      const section = mount.querySelector<HTMLElement>(`[data-repo="${name}"]`);
      expect(section?.querySelector(".git-repo-row-clean")?.textContent).toBe(
        "No uncommitted changes.",
      );
      expect(section?.querySelector(".git-repo-row-clean")?.textContent).not.toContain("Clean");
    }

    expect(
      mount.querySelector('[data-repo="behind"] .git-repo-action-bar button')?.textContent,
    ).toBe("Pull ↓3");
  });

  it("still gets a section on a workspace where every repo is quiet", async () => {
    // No aggregate empty state: a card replacing the list would hide a behind repo's Pull button and the branch chip.
    const mount = await paintRepos([
      repo([], { repo: "one", has_dirty: false }),
      repo([], { repo: "two", has_dirty: false, behind: 1 }),
    ]);

    expect(
      [...mount.querySelectorAll(".git-repo-section")].map((s) => s.getAttribute("data-repo")),
    ).toEqual(["one", "two"]);
    expect(mount.querySelector(".git-multirepo-empty-title")).toBeNull();
    expect(mount.querySelectorAll("[data-branch-trigger]").length).toBe(2);
    expect(mount.querySelector('[data-repo="two"] .git-repo-action-bar button')?.textContent).toBe(
      "Pull ↓1",
    );
  });
});

// The pass is the server's; the panel's half is that a repo it could not pull says so on its own block, and the mark
// expires with the fact it reports.
describe("Pull all", () => {
  async function bootToolbar(repos: GitRepoStatus[]): Promise<HTMLButtonElement> {
    document.body.innerHTML = `<div id="git-view">
      <div class="git-tab-toolbar">
        <button type="button" id="git-pull-all-btn" class="icon-btn"></button>
        <button type="button" id="git-refresh-all-btn" class="icon-btn"></button>
      </div>
      <div id="git-changes-mount" class="git-multirepo-mount" aria-live="polite"></div>
    </div>`;
    apiGet.mockResolvedValue({ repos });
    const mod = await load();
    mod.initChangesTab();
    await mod.refreshChanges();
    const btn = document.getElementById("git-pull-all-btn");
    if (!(btn instanceof HTMLButtonElement)) {
      throw new Error("the toolbar button was not wired");
    }
    return btn;
  }

  function mountEl(): HTMLElement {
    const mount = document.getElementById("git-changes-mount");
    if (mount === null) {
      throw new Error("mount missing");
    }
    return mount;
  }

  /** The click handler is fire-and-forget, so the settle is observed, not counted in microtasks. */
  async function pressPullAll(btn: HTMLButtonElement, after: GitRepoStatus[]): Promise<void> {
    apiGet.mockResolvedValue({ repos: after });
    btn.click();
    await vi.waitFor(() => {
      expect(dispatches.some((d) => d.name === "pullAll")).toBe(true);
      expect(apiGet.mock.calls.length).toBeGreaterThan(1);
    });
  }

  it("gives the toolbar a button that dispatches one pass", async () => {
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 1, has_dirty: false })]);
    // The toolbar renders its glyphs from TS.
    expect(btn.innerHTML).toContain("<svg");

    await pressPullAll(btn, [repo([], { repo: "demo", has_dirty: false })]);
    expect(dispatches.filter((d) => d.name === "pullAll").length).toBe(1);
  });

  it("marks a blocked repo in its header and names the reason in its body", async () => {
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 2, has_dirty: false })]);
    pullAllResult = [
      {
        repo: "demo",
        verdict: "blocked",
        reason: "diverged",
        detail: "main has 1 local commit the upstream does not, so there is no fast-forward.",
      },
    ];

    await pressPullAll(btn, [repo([], { repo: "demo", behind: 2, has_dirty: false })]);

    const flag = mountEl().querySelector<HTMLElement>(".git-repo-pull-flag");
    expect(flag?.textContent).toContain("diverged");
    expect(flag?.dataset["verdict"]).toBe("blocked");
    // Glyph as well as hue, so the state does not rest on colour alone.
    expect(flag?.querySelector("svg")).not.toBeNull();

    const note = mountEl().querySelector<HTMLElement>(".git-repo-note");
    expect(note?.textContent).toContain("Not pulled.");
    expect(note?.textContent).toContain("1 local commit");
    // Carried by `behind > 0` in the collapse default, not by the mark.
    expect(
      mountEl()
        .querySelector('[data-repo="demo"] .git-repo-section-header')
        ?.getAttribute("aria-expanded"),
    ).toBe("true");
  });

  it("distinguishes git refusing the pull from the pre-flight refusing to run it", async () => {
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 1, has_dirty: false })]);
    pullAllResult = [{ repo: "demo", verdict: "failed", detail: "fatal: could not read remote" }];

    await pressPullAll(btn, [repo([], { repo: "demo", behind: 1, has_dirty: false })]);

    expect(mountEl().querySelector<HTMLElement>(".git-repo-pull-flag")?.textContent).toContain(
      "pull failed",
    );
    const note = mountEl().querySelector<HTMLElement>(".git-repo-note");
    expect(note?.dataset["verdict"]).toBe("failed");
    expect(note?.textContent).toContain("Pull failed.");
  });

  it("marks nothing for a repo it pulled or had no reason to touch", async () => {
    const btn = await bootToolbar([
      repo([], { repo: "pulled", behind: 1, has_dirty: false }),
      repo([], { repo: "quiet", has_dirty: false }),
    ]);
    pullAllResult = [
      { repo: "pulled", verdict: "pulled" },
      { repo: "quiet", verdict: "skipped", reason: "up_to_date" },
    ];

    await pressPullAll(btn, [
      repo([], { repo: "pulled", has_dirty: false }),
      repo([], { repo: "quiet", has_dirty: false }),
    ]);

    expect(mountEl().querySelectorAll(".git-repo-pull-flag").length).toBe(0);
    expect(mountEl().querySelectorAll(".git-repo-note").length).toBe(0);
  });

  it("drops the mark once the repo stops being behind", async () => {
    // Once something else pulls the repo, the mark would be a stale warning.
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 1, has_dirty: false })]);
    pullAllResult = [
      {
        repo: "demo",
        verdict: "blocked",
        reason: "local_changes",
        detail: "Local changes to a.ts",
      },
    ];
    await pressPullAll(btn, [repo([], { repo: "demo", behind: 1, has_dirty: false })]);
    expect(mountEl().querySelectorAll(".git-repo-pull-flag").length).toBe(1);

    apiGet.mockResolvedValue({ repos: [repo([], { repo: "demo", has_dirty: false })] });
    document.getElementById("git-refresh-all-btn")?.click();
    await vi.waitFor(() => {
      expect(mountEl().querySelectorAll(".git-repo-pull-flag").length).toBe(0);
    });
  });

  it("keeps the mark while the repo is still behind, across a refresh", async () => {
    // A reader who walked away must still find why their repos did not move.
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 4, has_dirty: false })]);
    pullAllResult = [
      {
        repo: "demo",
        verdict: "blocked",
        reason: "in_progress",
        detail: "A merge is in progress.",
      },
    ];
    await pressPullAll(btn, [repo([], { repo: "demo", behind: 4, has_dirty: false })]);

    document.getElementById("git-refresh-all-btn")?.click();
    await vi.waitFor(() => {
      expect(apiGet.mock.calls.length).toBeGreaterThan(2);
    });
    expect(mountEl().querySelector(".git-repo-pull-flag")?.textContent).toContain("mid-merge");
  });

  it("marks a repo the server describes with a reason this build does not know", async () => {
    // An unrecognised reason still earns a mark: the repo was not pulled.
    const btn = await bootToolbar([repo([], { repo: "demo", behind: 1, has_dirty: false })]);
    pullAllResult = [
      { repo: "demo", verdict: "blocked", reason: "something_new", detail: "A newer reason." },
    ];

    await pressPullAll(btn, [repo([], { repo: "demo", behind: 1, has_dirty: false })]);

    expect(mountEl().querySelector(".git-repo-pull-flag")?.textContent).toContain("not pulled");
  });
});

// `status-all` is polled, so the skeleton arms on an empty mount, not on a request in flight, or it would paint
// over real content. Fake timers are scoped to this block: only the 150ms show delay needs them.
describe("the loading placeholder", () => {
  let settle: ((value: unknown) => void) | null = null;

  beforeEach(() => {
    vi.useFakeTimers();
    settle = null;
    apiGet.mockImplementation(
      async () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function mountEl(): HTMLElement {
    const m = document.getElementById("git-changes-mount");
    if (m === null) {
      throw new Error("mount missing");
    }
    return m;
  }

  it("paints per-repo placeholders while status-all is in flight", async () => {
    const { refreshChanges } = await load();
    const done = refreshChanges();

    // The show delay is 150ms, so a fast answer paints no placeholder.
    expect(mountEl().querySelector(".git-repo-skeleton")).toBeNull();
    await vi.advanceTimersByTimeAsync(150);

    const skel = mountEl().querySelector(".git-repo-skeleton");
    expect(
      skel,
      "an empty mount must stand in for the sections the paint will build",
    ).not.toBeNull();
    // The mount is aria-live="polite", so placeholder bars must not be announced.
    expect(skel?.getAttribute("aria-hidden")).toBe("true");
    expect(skel?.querySelectorAll(".skeleton").length).toBeGreaterThan(0);
    // Only the PRs tab names a connection on its skeleton.
    expect(mountEl().querySelector(".git-repo-skel-label")).toBeNull();

    settle?.({ repos: [] });
    await done;
    expect(
      mountEl().querySelector(".git-repo-skeleton"),
      "the placeholder must not outlive the answer it stood in for",
    ).toBeNull();
  });

  it("skips the placeholder when the mount already holds keyed rows", async () => {
    const row = document.createElement("section");
    row.setAttribute("data-reconcile-key", "demo");
    mountEl().appendChild(row);

    const { refreshChanges } = await load();
    void refreshChanges();
    await vi.advanceTimersByTimeAsync(150);

    expect(
      mountEl().querySelector(".git-repo-skeleton"),
      "status-all is polled, so a skeleton over populated rows would flash several times a minute",
    ).toBeNull();
    expect(mountEl().querySelector("[data-reconcile-key]")).not.toBeNull();
  });

  it("arms nothing on a CLEAN worktree once status-all has answered", async () => {
    // The empty-state row is unkeyed, so the arm keys on answered; a gap or resume refreshes without a tab switch, and a
    // clean worktree would otherwise flash a shimmer.
    const { refreshChanges } = await load();
    const first = refreshChanges();
    settle?.({ repos: [] });
    await first;

    const second = refreshChanges();
    await vi.advanceTimersByTimeAsync(150);

    expect(mountEl().querySelector(".git-repo-skeleton")).toBeNull();
    settle?.({ repos: [] });
    await second;
  });
});
