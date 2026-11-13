import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModGitTabs from "./git-tabs.js";
import type { GitTab } from "./route-path.js";

/**
 * Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode (URL-keyed module map). The `.ts`
 * extension is load-bearing: written `.js`, coverage attributes every evaluation to a file that does not exist. Only
 * the module under test is busted, so `vi.mock` still intercepts its dependencies.
 */
let bootSeq = 0;

/**
 * The git row's route as the strip holds it, null while no row is open. A fresh signal per case: every re-imported
 * module instance stays subscribed to the cell it first read.
 */
const gitRoute = await vi.hoisted(async () => {
  const { signal } = await import("@cplieger/reactive");
  return { cell: signal<GitTab | null>(null) };
});
vi.mock("./tabs.js", () => ({
  subTabOf: (kind: string) => (kind === "git" ? gitRoute.cell.value : null),
  // The real setter's no-op while no row is open.
  setGitTab: vi.fn((tab: GitTab) => {
    if (gitRoute.cell.peek() !== null) {
      gitRoute.cell.value = tab;
    }
  }),
}));

beforeEach(async () => {
  const { signal } = await import("@cplieger/reactive");
  gitRoute.cell = signal<GitTab | null>("changes");
  vi.resetModules();
  bootSeq++;
});

async function boot(): Promise<typeof ModGitTabs> {
  return (await import(/* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`)) as typeof ModGitTabs;
}

describe("git-tabs reads the git row's sub-tab", () => {
  it("onGitTabChange fires immediately with the row's tab", async () => {
    const { onGitTabChange } = await boot();

    const seen: GitTab[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    expect(seen).toEqual(["changes"]);
    dispose();
  });

  it("a route write notifies subscribers with the new tab", async () => {
    const { onGitTabChange, getGitTab } = await boot();

    const seen: GitTab[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    gitRoute.cell.value = "prs";

    expect(seen).toEqual(["changes", "prs"]);
    expect(getGitTab()).toBe("prs");
    dispose();
  });

  it("a same-tab route write does not re-notify", async () => {
    const { onGitTabChange, getGitTab } = await boot();

    const fn = vi.fn<(tab: GitTab) => void>();
    const dispose = onGitTabChange(fn);
    fn.mockClear(); // Drop the immediate fire; count change notifications only.

    gitRoute.cell.value = "changes";

    expect(fn).not.toHaveBeenCalled();
    expect(getGitTab()).toBe("changes");
    dispose();
  });

  it("with no git row, getGitTab answers null and a close notifies no one", async () => {
    const { onGitTabChange, getGitTab, readGitTab } = await boot();

    const fn = vi.fn<(tab: GitTab) => void>();
    const dispose = onGitTabChange(fn);
    fn.mockClear();

    gitRoute.cell.value = null;

    expect(fn).not.toHaveBeenCalled();
    expect(getGitTab()).toBeNull();
    expect(readGitTab()).toBeNull();
    dispose();
  });

  it("names the active Git section in the title bar subtitle, and a pill click writes the row", async () => {
    document.body.innerHTML = `
      <nav id="git-tab-bar">
        <button data-git-tab="changes"></button>
        <button data-git-tab="prs"></button>
        <button data-git-tab="sources"></button>
      </nav>
      <span id="titlebar-title"></span>
      <span id="titlebar-subtitle"></span>
      <div data-git-panel="changes"></div>
      <div data-git-panel="prs"></div>
      <div data-git-panel="sources"></div>`;
    const { initGitTabs } = await boot();

    // Two writers: `showView` owns the title, the tab module the subtitle, which paints only for the kind shown, so the
    // test performs the view switch as `tabs.ts` does.
    const { setPageTitle } = await import("./page-title.js");
    setPageTitle("Git", "git");

    initGitTabs();
    expect(document.getElementById("titlebar-subtitle")?.textContent).toBe("Changes");

    document.querySelector<HTMLElement>('[data-git-tab="prs"]')?.click();
    expect(gitRoute.cell.peek()).toBe("prs");
    expect(document.getElementById("titlebar-subtitle")?.textContent).toBe("Pull requests");
    expect(document.querySelector('[data-git-panel="prs"]')?.classList.contains("hidden")).toBe(
      false,
    );
  });
});
