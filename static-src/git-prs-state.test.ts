import { describe, it, expect, beforeEach } from "vitest";
import {
  applyInventoryEntry,
  applyInventoryList,
  bindPRPaint,
  cloneDirOf,
  cloneInDir,
  cycleAfter,
  getElsewherePRs,
  getPRGroups,
  heldEntry,
  inventoryHeld,
  reinsertPRInGroups,
  removePRFromGroups,
  settleRemoval,
  setPRForges,
  _resetForTest,
} from "./git-prs-state.js";
import type { ConfiguredForge, InventoryEntry, InventoryScope, PR } from "./wire/types.gen.js";

/** The canonical ids of org/repo and org/other. */
const REPO = "v1.6f72672f7265706f";
const OTHER = "v1.6f72672f6f74686572";

function row(number: number, repoID = REPO, path = "org/repo"): PR {
  return {
    repo_id: repoID,
    repo: path,
    number,
    title: `PR #${String(number)}`,
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    action: {
      mergeable: "yes",
      checks: "unknown",
      checks_passing: 0,
      checks_failing: 0,
      checks_pending: 0,
      checks_neutral: 0,
      checks_unknown: 0,
      checks_total: 0,
      auto_merge_armed: "no",
      queue_state: "none",
      queue_position: -1,
      merge_blocked: "none",
    },
  };
}

function scope(kind: string, rows: PR[], owner?: string): InventoryScope {
  return owner === undefined ? { scope: kind, rows } : { scope: kind, owner, rows };
}

function entry(
  forgeID: string,
  cycle: string,
  scopes: InventoryScope[],
  over: Partial<InventoryEntry> = {},
): InventoryEntry {
  return {
    forge_id: forgeID,
    state: "ready",
    cycle_id: cycle,
    credential: "valid",
    scopes,
    clones: [],
    fetched_at: 1,
    ...over,
  };
}

function forge(
  id: string,
  kind: ConfiguredForge["kind"],
  host: string,
  username: string,
): ConfiguredForge {
  return { id, kind, host, username, connected: true, reconnect_required: false };
}

const GH = forge("github:github.com", "github", "github.com", "me");
const GL = forge("gitlab:gitlab.com", "gitlab", "gitlab.com", "org");

/** The groups as `forge repo_id: numbers`, the shape every case reads. */
function shape(): string[] {
  return getPRGroups().map(
    (g) => `${g.forge_id} ${g.full_name}: ${g.prs.map((p) => p.number).join(",")}`,
  );
}

/** The contributions elsewhere as `forge path#number`, in their order. */
function elsewhere(): string[] {
  return getElsewherePRs().map((r) => `${r.forge_id} ${r.pr.repo}#${String(r.pr.number)}`);
}

let paints = 0;

beforeEach(() => {
  _resetForTest();
  paints = 0;
  bindPRPaint(() => {
    paints++;
  });
  setPRForges([GH, GL]);
});

describe("the groups an inventory derives", () => {
  it("groups the owner and added scopes' rows by repo_id under the display path", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [row(5), row(10), row(2, OTHER, "org/other")], "me"),
          scope("authored", [row(77, "v1.656c7365", "else/where")]),
          scope("added", [row(9, OTHER, "org/other")], "org"),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual([
      "github:github.com org/other: 9,2",
      "github:github.com org/repo: 10,5",
    ]);
    const g = getPRGroups()[1];
    expect(g).toMatchObject({
      repo_id: REPO,
      owner: "org",
      name: "repo",
      forge_kind: "github",
      forge_host: "github.com",
    });
  });

  it("stands the authored rows under the login in for the owner scope a connection lacks", () => {
    // GitLab's owner scope refuses the user's own namespace, so the login's
    // repositories are the authored rows under it; the rest are elsewhere.
    applyInventoryList({
      entries: [
        entry(GL.id, "4", [
          scope("authored", [row(11, REPO, "org/repo"), row(12, OTHER, "group/sub/other")]),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["gitlab:gitlab.com org/repo: 11"]);
    expect(elsewhere()).toEqual(["gitlab:gitlab.com group/sub/other#12"]);
  });

  it("counts a GitLab group's subgroups as its own", () => {
    applyInventoryList({
      entries: [
        entry(GL.id, "4", [
          scope("authored", [row(12, OTHER, "group/sub/other")]),
          scope("added", [], "group"),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["gitlab:gitlab.com group/sub/other: 12"]);
    expect(getPRGroups()[0]).toMatchObject({ owner: "group/sub", name: "other" });
    expect(elsewhere()).toEqual([]);
  });

  it("lists a row two scopes carry once", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [scope("owner", [row(5)], "me"), scope("added", [row(5)], "org")]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["github:github.com org/repo: 5"]);
  });

  it("keeps a row its owner scope carries in its group when the authored scope carries it too", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [scope("owner", [row(5)], "someone"), scope("authored", [row(5)])]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["github:github.com org/repo: 5"]);
    expect(elsewhere()).toEqual([]);
  });

  it("derives no group from a loading or a failed entry", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "0", [], { state: "loading" }),
        entry(GL.id, "2", [scope("authored", [row(1)])], { state: "failed" }),
      ],
      subject: [],
      viewing: false,
    });
    expect(getPRGroups()).toEqual([]);
    expect(getElsewherePRs()).toEqual([]);
    expect(inventoryHeld()).toBe(true);
  });
});

describe("the contributions elsewhere", () => {
  it("lists an authored row outside every owner's repositories there and in no group", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [row(5)], "org"),
          scope("authored", [row(77, "v1.656c7365", "else/where"), row(5)]),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["github:github.com org/repo: 5"]);
    expect(elsewhere()).toEqual(["github:github.com else/where#77"]);
    expect(getElsewherePRs()[0]).toMatchObject({ forge_kind: "github", forge_host: "github.com" });
  });

  it("lists an authored row under a listed owner in its repository's group before that owner's walk reaches it", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [], "org"),
          scope("authored", [row(6)]),
          scope("added", [row(9, OTHER, "team/other")], "team"),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["github:github.com org/repo: 6", "github:github.com team/other: 9"]);
    expect(elsewhere()).toEqual([]);
  });

  it("compares an owner with a repository's path whatever their case", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [], "Org"),
          scope("authored", [row(6, REPO, "ORG/repo")]),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual(["github:github.com ORG/repo: 6"]);
    expect(elsewhere()).toEqual([]);
  });

  it("does not count a repository whose owner only begins with a listed one", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [], "org"),
          scope("authored", [row(6, REPO, "org2/repo")]),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(shape()).toEqual([]);
    expect(elsewhere()).toEqual(["github:github.com org2/repo#6"]);
  });

  it("orders them by repository, then newest first", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [
          scope("owner", [], "org"),
          scope("authored", [row(3, OTHER, "b/x"), row(2, REPO, "a/y"), row(4, OTHER, "b/x")]),
        ]),
      ],
      subject: [],
      viewing: false,
    });
    expect(elsewhere()).toEqual([
      "github:github.com a/y#2",
      "github:github.com b/x#4",
      "github:github.com b/x#3",
    ]);
  });
});

describe("which entry is held", () => {
  it("orders cycle ids as numbers", () => {
    expect(cycleAfter("10", "9")).toBe(true);
    expect(cycleAfter("9", "10")).toBe(false);
    expect(cycleAfter("12", "12")).toBe(false);
    expect(cycleAfter("13", "12")).toBe(true);
  });

  it("replaces one connection's entry with a later frame and ignores a late one", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "5", [scope("owner", [row(1)], "me")]),
        entry(GL.id, "5", [scope("authored", [row(2, OTHER, "org/other")])]),
      ],
      subject: [],
      viewing: false,
    });
    expect(applyInventoryEntry(entry(GH.id, "6", [scope("owner", [row(3)], "me")]))).toBe(true);
    expect(shape()).toEqual(["gitlab:gitlab.com org/other: 2", "github:github.com org/repo: 3"]);

    expect(applyInventoryEntry(entry(GH.id, "6", [scope("owner", [row(4)], "me")]))).toBe(false);
    expect(applyInventoryEntry(entry(GH.id, "4", [scope("owner", [row(4)], "me")]))).toBe(false);
    expect(heldEntry(GH.id)?.scopes[0]?.rows[0]?.number).toBe(3);
  });

  it("drops a connection the list no longer names and keeps an entry newer than the list's", () => {
    applyInventoryList({
      entries: [entry(GH.id, "5", [scope("owner", [row(1)], "me")]), entry(GL.id, "5", [])],
      subject: [],
      viewing: false,
    });
    applyInventoryEntry(entry(GH.id, "8", [scope("owner", [row(8)], "me")]));
    applyInventoryList({
      entries: [entry(GH.id, "7", [scope("owner", [row(7)], "me")])],
      subject: [],
      viewing: false,
    });
    expect(heldEntry(GL.id)).toBeUndefined();
    expect(heldEntry(GH.id)?.cycle_id).toBe("8");
  });

  it("replaces every entry on a reset, whatever its cycle", () => {
    applyInventoryList({
      entries: [entry(GH.id, "500", [scope("owner", [row(1)], "me")])],
      subject: [],
      viewing: false,
    });
    applyInventoryList(
      {
        entries: [entry(GH.id, "2", [scope("owner", [row(2)], "me")])],
        subject: [],
        viewing: false,
      },
      { reset: true },
    );
    expect(heldEntry(GH.id)?.cycle_id).toBe("2");
    expect(shape()).toEqual(["github:github.com org/repo: 2"]);
  });

  it("answers the clone a directory holds and the directory of a repository", () => {
    applyInventoryList({
      entries: [
        entry(GH.id, "1", [], { clones: [{ dir: "repo-clone", forge_id: GH.id, repo_id: REPO }] }),
      ],
      subject: [],
      viewing: false,
    });
    expect(cloneInDir("repo-clone")).toEqual({ dir: "repo-clone", forge_id: GH.id, repo_id: REPO });
    expect(cloneInDir("nothing")).toBeUndefined();
    expect(cloneDirOf(GH.id, REPO)).toBe("repo-clone");
    expect(cloneDirOf(GL.id, REPO)).toBeUndefined();
  });
});

describe("an optimistic removal", () => {
  beforeEach(() => {
    applyInventoryList({
      entries: [
        entry(GH.id, "3", [scope("owner", [row(10), row(5), row(2)], "me")]),
        entry(GL.id, "3", [scope("authored", [row(5)])]),
      ],
      subject: [],
      viewing: false,
    });
  });

  it("hides the row by connection and repo_id and repaints", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    expect(r?.pr.number).toBe(5);
    expect(r?.group.forge_id).toBe(GH.id);
    expect(shape()).toEqual(["github:github.com org/repo: 10,2", "gitlab:gitlab.com org/repo: 5"]);
    expect(paints).toBe(1);
  });

  it("stays hidden when another connection's frame lands", () => {
    removePRFromGroups(GH.id, REPO, 5);
    applyInventoryEntry(entry(GL.id, "4", [scope("authored", [row(6)])]));
    expect(shape()).toEqual(["github:github.com org/repo: 10,2", "gitlab:gitlab.com org/repo: 6"]);
  });

  it("answers undefined and paints nothing for a row that is not listed", () => {
    expect(removePRFromGroups(GH.id, REPO, 999)).toBeUndefined();
    expect(removePRFromGroups("other", REPO, 5)).toBeUndefined();
    expect(removePRFromGroups(GH.id, OTHER, 5)).toBeUndefined();
    expect(paints).toBe(0);
  });

  it("shows the row again on a rollback, once", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    reinsertPRInGroups(r!);
    expect(shape()).toEqual([
      "github:github.com org/repo: 10,5,2",
      "gitlab:gitlab.com org/repo: 5",
    ]);
    expect(paints).toBe(2);
    const before = paints;
    reinsertPRInGroups(r!);
    expect(paints).toBe(before);
  });

  it("stays hidden through an entry read before the mutation answered", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    applyInventoryEntry(entry(GH.id, "4", [scope("owner", [row(10), row(5), row(2)], "me")]));
    expect(shape()).toEqual(["github:github.com org/repo: 10,2", "gitlab:gitlab.com org/repo: 5"]);

    reinsertPRInGroups(r!);
    expect(shape()).toEqual([
      "github:github.com org/repo: 10,5,2",
      "gitlab:gitlab.com org/repo: 5",
    ]);
  });

  it("stays hidden through a cycle older than the one the mutation named", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    settleRemoval(r!, "5");
    applyInventoryEntry(entry(GH.id, "4", [scope("owner", [row(10), row(5)], "me")]));
    expect(shape()).toEqual(["github:github.com org/repo: 10", "gitlab:gitlab.com org/repo: 5"]);
  });

  it("is decided by the cycle the mutation named, even when it still lists the row", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    settleRemoval(r!, "5");
    applyInventoryEntry(entry(GH.id, "5", [scope("owner", [row(10), row(5)], "me")]));
    expect(shape()).toEqual(["github:github.com org/repo: 10,5", "gitlab:gitlab.com org/repo: 5"]);
  });

  it("is decided at once when the named cycle's entry landed before the answer", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    applyInventoryEntry(entry(GH.id, "5", [scope("owner", [row(10), row(5)], "me")]));
    expect(shape()).toEqual(["github:github.com org/repo: 10", "gitlab:gitlab.com org/repo: 5"]);

    settleRemoval(r!, "5");
    expect(shape()).toEqual(["github:github.com org/repo: 10,5", "gitlab:gitlab.com org/repo: 5"]);
  });

  it("ends when an entry omits the row, which then decides it", () => {
    const r = removePRFromGroups(GH.id, REPO, 5);
    applyInventoryEntry(entry(GH.id, "4", [scope("owner", [row(10), row(2)], "me")]));
    const before = paints;
    reinsertPRInGroups(r!);
    expect(paints).toBe(before);

    applyInventoryEntry(entry(GH.id, "5", [scope("owner", [row(10), row(5)], "me")]));
    expect(shape()).toEqual(["github:github.com org/repo: 10,5", "gitlab:gitlab.com org/repo: 5"]);
  });
});
