// One state plus hidden: amber is modified, the hue `.git-st-*` and VS Code use per file. `behind` is no state
// (nothing fetches, so it is stale by construction). Derivation and stylesheet are one decision, so both are pinned
// here: pinning one alone lets the other disagree.

import { describe, expect, it } from "vitest";
import { deriveState, deriveTooltip } from "./git-badge.js";
import { allRules, loadCSS, manifestSheets, ruleContaining } from "./__test-helpers__/css-rules.js";
import type { GitRepoStatusBadge } from "./git-types.js";

function repo(over: Partial<GitRepoStatusBadge> = {}): GitRepoStatusBadge {
  return {
    repo: "app",
    is_repo: true,
    branch: "main",
    ahead: 0,
    behind: 0,
    has_dirty: false,
    ...over,
  };
}

describe("git badge state", () => {
  it("hides when every repo is clean", () => {
    expect(deriveState({ repos: [repo(), repo({ repo: "lib" })] })).toEqual({
      kind: "none",
    });
  });

  it("is dirty when a repo has an uncommitted change", () => {
    expect(deriveState({ repos: [repo({ has_dirty: true })] })).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("is dirty when a repo's only change is an unpushed commit", () => {
    // An unpushed commit is local work the reader still owns, so `ahead` is dirty on a clean tree.
    expect(deriveState({ repos: [repo({ ahead: 2 })] })).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("stays HIDDEN for a repo that is only behind origin", () => {
    // `behind` never reaches the badge: nothing fetches, so the number is stale.
    expect(deriveState({ repos: [repo({ behind: 7 })] })).toEqual({ kind: "none" });
  });

  it("reports dirty-AND-behind as plain dirty, with no blended state", () => {
    // One repo dirty, another behind: the input that once produced a blended state.
    expect(
      deriveState({ repos: [repo({ has_dirty: true }), repo({ repo: "lib", behind: 3 })] }),
    ).toEqual({ kind: "dirty", dirtyCount: 1 });
  });

  it("counts each dirty repo once, however many ways it is dirty", () => {
    expect(deriveState({ repos: [repo({ has_dirty: true, ahead: 4, behind: 9 })] })).toEqual({
      kind: "dirty",
      dirtyCount: 1,
    });
  });

  it("skips a path that is not a repo", () => {
    expect(deriveState({ repos: [repo({ is_repo: false, has_dirty: true })] })).toEqual({
      kind: "none",
    });
  });

  it("tolerates an absent repo list", () => {
    expect(deriveState({})).toEqual({ kind: "none" });
  });
});

describe("git badge tooltip", () => {
  it("says local changes rather than uncommitted, because ahead is committed", () => {
    expect(deriveTooltip({ kind: "dirty", dirtyCount: 1 })).toBe("1 repo with local changes");
    expect(deriveTooltip({ kind: "dirty", dirtyCount: 4 })).toBe("4 repos with local changes");
  });

  it("says nothing when the badge is hidden", () => {
    expect(deriveTooltip({ kind: "none" })).toBe("");
  });
});

describe("git badge paint", () => {
  function badgeRules(): { selector: string; body: string }[] {
    return manifestSheets().flatMap((s) =>
      allRules(s.css).filter((r) => r.selector.includes(".git-badge")),
    );
  }

  it("declares no state arm, because dirty is its one state", () => {
    const arms = badgeRules()
      .map((r) => r.selector)
      .filter((s) => s.includes("[data-state="));
    expect(arms).toEqual([]);
  });

  it("paints amber", () => {
    const base = allRules(loadCSS("14-tools.css")).find((r) => r.selector === ".git-badge");
    expect(base?.body).toContain("background: var(--c-yellow)");
  });

  it("reaches for the state inks, never the destructive-action palette", () => {
    // --c-danger / --c-warning are the delete-confirm palette; a status mark takes the state ink --c-yellow.
    const offenders = badgeRules().filter(
      (r) => r.body.includes("--c-danger") || r.body.includes("--c-warning"),
    );
    expect(offenders.map((r) => r.selector)).toEqual([]);
  });

  it("agrees with the per-repo dirty mark it aggregates", () => {
    const dot = allRules(loadCSS("22-git-multirepo.css")).find(
      (r) => r.selector === ".git-repo-dirty-dot",
    );
    expect(dot?.body).toContain("background: var(--c-yellow)");
  });

  it("leaves the ahead and behind counts unhued", () => {
    // Counts stay colourless (as in VS Code): a tint would give amber a second meaning beside "modified".
    // `ruleContaining`, so the pair may be listed in either order.
    const sheet = loadCSS("22-git-multirepo.css");
    for (const member of [".git-repo-ahead", ".git-repo-behind"]) {
      expect(ruleContaining(sheet, member).body).toContain("color: var(--c-text-secondary)");
    }
  });

  it("carries no arm for a retired state in any stylesheet", () => {
    const retired = ['data-state="both"', 'data-state="remote"', 'data-state="local"'];
    const offenders: string[] = [];
    for (const s of manifestSheets()) {
      for (const r of allRules(s.css)) {
        if (!r.selector.includes(".git-badge")) {
          continue;
        }
        for (const name of retired) {
          if (r.selector.includes(name)) {
            offenders.push(`${s.name}: ${r.selector}`);
          }
        }
      }
    }
    expect(offenders).toEqual([]);
  });
});
