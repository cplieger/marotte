import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModGitTabs from "./git-tabs.js";

/**
 * Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode (URL-keyed module map). The `.ts`
 * extension is load-bearing: written `.js`, coverage attributes every evaluation to a file that does not exist. Only
 * the module under test is busted, so `vi.mock` still intercepts its dependencies.
 */
let bootSeq = 0;

// setGitTab pushes a URL and syncs the git tab's route, so the suite runs under the real location and history; a
// fresh module per test resets the signal to "changes".
beforeEach(() => {
  vi.resetModules();
  bootSeq++;
  // resetModules clears module state, not the global location.
  history.replaceState(null, "", "/");
});

describe("git-tabs store", () => {
  it("onGitTabChange fires immediately with the current tab", async () => {
    const { onGitTabChange } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    const seen: GitTabName[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    expect(seen).toEqual(["changes"]);
    dispose();
  });

  it("setGitTab notifies subscribers with the new tab", async () => {
    const { onGitTabChange, setGitTab, getGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    const seen: GitTabName[] = [];
    const dispose = onGitTabChange((tab) => {
      seen.push(tab);
    });

    setGitTab("prs");

    expect(seen).toEqual(["changes", "prs"]);
    expect(getGitTab()).toBe("prs");
    dispose();
  });

  it("same-tab setGitTab is a no-op and does not re-notify", async () => {
    const { onGitTabChange, setGitTab, getGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    const fn = vi.fn<(tab: GitTabName) => void>();
    const dispose = onGitTabChange(fn);
    fn.mockClear(); // Drop the immediate fire; count change notifications only.

    setGitTab("changes"); // Already the default: deduped, no notify.

    expect(fn).not.toHaveBeenCalled();
    expect(getGitTab()).toBe("changes");
    dispose();
  });

  it("setGitTab pushes the matching URL; changes maps to the canonical /git", async () => {
    const { setGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    setGitTab("prs");
    expect(location.pathname).toBe("/git/prs");

    setGitTab("sources");
    expect(location.pathname).toBe("/git/sources");

    setGitTab("changes");
    expect(location.pathname).toBe("/git");
  });

  it("names the active Git section in the title bar subtitle", async () => {
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
    const { initGitTabs, setGitTab } = (await import(
      /* @vite-ignore */ `./git-tabs.ts?boot=${bootSeq}`
    )) as typeof ModGitTabs;

    // Two writers: `showView` owns the title, the tab module the subtitle, which paints only for the kind shown, so the
    // test performs the view switch as `tabs.ts` does.
    const { setPageTitle } = await import("./page-title.js");
    setPageTitle("Git", "git");

    initGitTabs();
    expect(document.getElementById("titlebar-subtitle")?.textContent).toBe("Changes");

    setGitTab("prs");
    expect(document.getElementById("titlebar-subtitle")?.textContent).toBe("Pull requests");
  });
});

// A local alias, since each test imports the module fresh inside its own body.
type GitTabName = "changes" | "prs" | "sources";
