// Tests for the PR row's pure read-outs: the check chip, the merge-block
// reason, and the per-forge capability rules. No DOM needed, which is
// the reason these live outside git-prs-tab.ts.

import { describe, it, expect } from "vitest";

import {
  checkChip,
  mergeVerdict,
  movedRepository,
  rerunControl,
  rerunRefusal,
  queueText,
  canArmAutoMerge,
} from "./git-pr-status.js";
import type { GitPR, GitPRAction } from "./git-types.js";
import type { Affordance, FieldFill } from "./wire/types.gen.js";

function action(over: Partial<GitPRAction> = {}): GitPRAction {
  return {
    mergeable: "unknown",
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
    ...over,
  };
}

/** A row in the server's shape: open, pinned to a head, nothing blocking. */
function pr(act: Partial<GitPRAction> = {}, over: Partial<Omit<GitPR, "action">> = {}): GitPR {
  return {
    repo_id: "v1.6f2f72",
    repo: "o/r",
    number: 1,
    title: "T",
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    head_sha: "abc1234",
    ...over,
    action: action(act),
  };
}

function withoutHead(row: GitPR): GitPR {
  const copy = { ...row };
  delete copy.head_sha;
  return copy;
}

describe("mergeVerdict", () => {
  // Every named cause. The catch-all this replaced said the PR "isn't
  // mergeable" and told the reader to open it on the forge; no branch may
  // reintroduce that.
  const table: [string, string][] = [
    ["draft", "draft"],
    ["conflicts", "conflicts"],
    ["checks_failing", "check is failing"],
    ["checks_running", "still running"],
    ["behind", "behind its target"],
    ["blocked", "merge policy"],
  ];

  for (const [cause, fragment] of table) {
    it(`names the cause for ${cause}`, () => {
      const reason = mergeVerdict(pr({ merge_blocked: cause, mergeable: "yes" })).reason;
      expect(reason).toContain(fragment);
    });
  }

  it("enables the merge when the forge says nothing blocks it", () => {
    expect(mergeVerdict(pr({ merge_blocked: "none", mergeable: "unknown" })).reason).toBe("");
  });

  // The Gitea family names no cause on any row, and GitLab names none while it
  // is still computing. Reading `unknown` alone as blocked would disable Merge
  // on every Gitea and Codeberg row, so the draft flag and the verdict decide.
  describe("when the forge names no cause", () => {
    it("enables the merge when the verdict is yes", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "yes" })).reason).toBe("");
    });

    it("says the forge refuses without a reason when the verdict is no", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "no" })).reason).toBe(
        "the forge reports this PR is not mergeable and does not say why.",
      );
    });

    it("says the forge has not decided when the verdict is unknown", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "unknown" })).reason).toBe(
        "the forge has not said whether this can merge yet.",
      );
    });

    it("fails closed on a verdict this build does not know", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "maybe" })).reason).not.toBe(
        "",
      );
    });

    it("names the draft whatever the verdict", () => {
      for (const mergeable of ["yes", "no", "unknown"]) {
        expect(
          mergeVerdict(pr({ merge_blocked: "unknown", mergeable }, { draft: true })).reason,
        ).toContain("draft");
      }
    });
  });

  // Every family refuses an unpinned merge, so a row without a head commit
  // cannot merge however clear the forge says it is.
  it("blocks a row the forge reported no head commit for", () => {
    const want = "the forge did not report the head commit.";
    expect(mergeVerdict(withoutHead(pr({ merge_blocked: "none" }))).reason).toBe(want);
    expect(mergeVerdict(pr({ merge_blocked: "none" }, { head_sha: "" })).reason).toBe(want);
    expect(
      mergeVerdict(withoutHead(pr({ merge_blocked: "unknown", mergeable: "yes" }))).reason,
    ).toBe(want);
  });

  // Fail CLOSED on a cause this build does not know. merge_blocked is a plain
  // string so the server vocabulary can grow, and a fallback to "" would
  // recreate the exact defect the function exists to fix: the forge refuses
  // the merge while the row enables the button.
  it("blocks on an unrecognised cause and quotes it", () => {
    const reason = mergeVerdict(pr({ merge_blocked: "requires_two_approvals" })).reason;
    expect(reason).toContain("requires_two_approvals");
  });

  it("reserves none for mergeable, and nothing else", () => {
    // The old empty-string spelling of "nothing blocks" is a cause now.
    expect(mergeVerdict(pr({ merge_blocked: "" })).reason).not.toBe("");
    expect(mergeVerdict(pr({ merge_blocked: " " })).reason).not.toBe("");
  });

  // The unknown cause reaches a tooltip, so it is normalised to one bounded
  // line rather than trusted for its provenance.
  it("keeps an unrecognised cause to one short line", () => {
    const reason = mergeVerdict(pr({ merge_blocked: "a\nb".padEnd(120, "x") })).reason;
    expect(reason).not.toContain("\n");
    expect(reason.length).toBeLessThan(160);
  });

  it("never tells the reader to go use the forge instead", () => {
    for (const [cause] of table) {
      const reason = mergeVerdict(pr({ merge_blocked: cause })).reason;
      expect(reason.toLowerCase()).not.toContain("on the forge");
    }
  });

  it("no reason carries an em dash", () => {
    const rows = [
      ...table.map(([cause]) => pr({ merge_blocked: cause })),
      pr({ merge_blocked: "unknown", mergeable: "no" }),
      pr({ merge_blocked: "unknown", mergeable: "unknown" }),
      withoutHead(pr()),
    ];
    for (const row of rows) {
      expect(mergeVerdict(row).reason).not.toContain("\u2014");
    }
  });

  // An unknown verdict is its own state: the forge has not decided, which is not
  // the forge refusing, and the row says which of the two it is.
  describe("its state", () => {
    it("is ready when nothing blocks the merge", () => {
      expect(mergeVerdict(pr({ merge_blocked: "none" })).state).toBe("ready");
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "yes" })).state).toBe("ready");
    });

    it("is blocked when the forge refuses or names a cause", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "no" })).state).toBe("blocked");
      for (const [cause] of table) {
        expect(mergeVerdict(pr({ merge_blocked: cause })).state, cause).toBe("blocked");
      }
      expect(mergeVerdict(pr({ merge_blocked: "a_future_cause" })).state).toBe("blocked");
    });

    it("is unknown when the forge has not said, or named no head commit", () => {
      expect(mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "unknown" })).state).toBe(
        "unknown",
      );
      expect(mergeVerdict(withoutHead(pr({ merge_blocked: "none" }))).state).toBe("unknown");
    });

    it("is blocked, not unknown, for a draft whose verdict is unknown", () => {
      expect(
        mergeVerdict(pr({ merge_blocked: "unknown", mergeable: "unknown" }, { draft: true })).state,
      ).toBe("blocked");
    });
  });

  // A field the family's list does not carry is not an answer: the row says the
  // value has not been read, or that reading it failed, rather than that the forge
  // has not decided.
  describe("over a row's fill", () => {
    function filled(field: string, reason: string): readonly FieldFill[] {
      return [{ field, reason }];
    }

    it("says a GitLab block reason the list does not carry has not been read", () => {
      const row = pr(
        { merge_blocked: "unknown", mergeable: "unknown" },
        { fill: filled("merge_blocked", "not_on_list") },
      );
      expect(mergeVerdict(row)).toEqual({
        state: "unknown",
        reason:
          "this forge's list does not say whether it can merge. It is read while this list is shown.",
      });
    });

    it("says a Gitea verdict the list does not carry has not been read", () => {
      const row = pr(
        { merge_blocked: "unknown", mergeable: "unknown" },
        {
          fill: [
            { field: "mergeable", reason: "not_on_list" },
            { field: "merge_blocked", reason: "not_supplied" },
          ],
        },
      );
      expect(mergeVerdict(row).reason).toBe(
        "this forge's list does not say whether it can merge. It is read while this list is shown.",
      );
    });

    it("says the read of the row failed", () => {
      const row = pr(
        { merge_blocked: "unknown", mergeable: "unknown" },
        { fill: filled("mergeable", "unread") },
      );
      expect(mergeVerdict(row)).toEqual({
        state: "unknown",
        reason: "the last read of this pull request failed. The next cycle reads it again.",
      });
    });

    it("says a head commit the list does not carry has not been read", () => {
      const row = withoutHead(pr({ merge_blocked: "unknown", mergeable: "yes" }));
      expect(mergeVerdict({ ...row, fill: filled("head_sha", "not_on_list") })).toEqual({
        state: "unknown",
        reason: "the head commit has not been read yet. It is read while this list is shown.",
      });
      expect(mergeVerdict({ ...row, fill: filled("head_sha", "unread") }).reason).toBe(
        "the last read of this pull request failed. The next cycle reads it again.",
      );
    });

    it("reads a filled verdict as the forge's own", () => {
      const row = pr(
        { merge_blocked: "unknown", mergeable: "unknown" },
        { fill: filled("mergeable", "filled") },
      );
      expect(mergeVerdict(row).reason).toBe("the forge has not said whether this can merge yet.");
    });
  });
});

describe("checkChip", () => {
  it("is absent when the forge reported no verdict (the gitea case)", () => {
    expect(checkChip(pr({ checks: "unknown" }))).toBeNull();
    expect(checkChip(pr({ checks: "some_future_value" }))).toBeNull();
  });

  it("reports checks that finished with a neutral verdict", () => {
    expect(checkChip(pr({ checks: "neutral", checks_neutral: 2, checks_total: 2 }))).toEqual({
      text: "checks neutral",
      className: "git-pr-check-neutral",
      tooltip: "2 checks finished neutral",
    });
    expect(checkChip(pr({ checks: "neutral" }))?.tooltip).toBe(
      "The checks finished without a pass or a failure.",
    );
  });

  // A verdict the family's list does not carry is not "no checks": the chip says
  // it has not been read, so a row with failing CI cannot read as a quiet one.
  it("says a verdict the list does not carry has not been read", () => {
    const row = pr({ checks: "unknown" }, { fill: [{ field: "checks", reason: "not_on_list" }] });
    expect(checkChip(row)).toEqual({
      text: "checks not read",
      className: "git-pr-check-unknown",
      tooltip: "This forge's list does not carry checks. They are read while this list is shown.",
    });
  });

  it("says the read of the row's checks failed", () => {
    const row = pr({ checks: "unknown" }, { fill: [{ field: "checks", reason: "unread" }] });
    expect(checkChip(row)).toEqual({
      text: "checks unread",
      className: "git-pr-check-unknown",
      tooltip: "The last read of this pull request failed. The next cycle reads it again.",
    });
  });

  it("is absent when a read found no verdict", () => {
    const row = pr({ checks: "unknown" }, { fill: [{ field: "checks", reason: "filled" }] });
    expect(checkChip(row)).toBeNull();
  });

  // GitLab serves one pipeline status and no per-check counts, so a pass carries
  // no count rather than claiming zero checks passed.
  it("states no count the forge did not supply", () => {
    const row = pr(
      { checks: "passing" },
      { fill: [{ field: "check_counts", reason: "not_supplied" }] },
    );
    expect(checkChip(row)?.tooltip).toBe("The checks passed.");
  });

  it("reports a pass with its count", () => {
    const chip = checkChip(pr({ checks: "passing", checks_total: 7 }));
    expect(chip).toEqual({
      text: "checks passed",
      className: "git-pr-check-pass",
      tooltip: "7 checks passed",
    });
  });

  it("singularises a lone check", () => {
    expect(checkChip(pr({ checks: "passing", checks_total: 1 }))?.tooltip).toBe("1 check passed");
  });

  it("reports failures as a count out of the total", () => {
    const chip = checkChip(pr({ checks: "failing", checks_total: 7, checks_failing: 2 }));
    expect(chip).toEqual({
      text: "2 failing",
      className: "git-pr-check-fail",
      tooltip: "2 of 7 checks failing",
    });
  });

  it("still says something when a failing PR carries no counts", () => {
    const chip = checkChip(pr({ checks: "failing" }));
    expect(chip?.text).toBe("checks failing");
    expect(chip?.tooltip).toBe("A required check is failing.");
  });

  it("reports running checks", () => {
    const chip = checkChip(pr({ checks: "pending", checks_total: 3 }));
    expect(chip).toEqual({
      text: "checks running",
      className: "git-pr-check-pending",
      tooltip: "3 checks running",
    });
  });
});

describe("rerunControl", () => {
  function cap(support: string, detail = ""): Affordance {
    return { support, source: "swagger", detail };
  }

  it("is enabled where the connection says it can re-run checks", () => {
    for (const kind of ["github", "gitlab", "gitea", "codeberg"] as const) {
      expect(rerunControl(kind, cap("yes"), undefined), kind).toEqual({ offer: true, reason: "" });
    }
  });

  it("is not offered where the connection says it cannot", () => {
    for (const kind of ["github", "gitlab", "gitea", "codeberg"] as const) {
      expect(rerunControl(kind, cap("no"), undefined), kind).toEqual({ offer: false });
    }
  });

  // The user's ruling: GitHub decides re-runs per repository and no connection
  // read can say, so its control stays enabled whatever the capability reads,
  // read or not.
  it("stays enabled on GitHub when the capability is unknown or not read yet", () => {
    expect(rerunControl("github", cap("unknown"), undefined)).toEqual({ offer: true, reason: "" });
    expect(rerunControl("github", undefined, undefined)).toEqual({ offer: true, reason: "" });
  });

  // Elsewhere an unknown capability is one the server refuses the re-run on, so
  // the control says so rather than failing on the press.
  it("is disabled with the evidence where another family reads unknown", () => {
    expect(
      rerunControl("gitea", cap("unknown", "no swagger document on this instance"), undefined),
    ).toEqual({
      offer: true,
      reason:
        "the forge could not establish that it can re-run checks (no swagger document on this instance).",
    });
    expect(rerunControl("gitlab", cap("maybe"), undefined)).toEqual({
      offer: true,
      reason: "the forge could not establish that it can re-run checks.",
    });
  });

  it("waits for the capability before offering it on another family", () => {
    expect(rerunControl("gitlab", undefined, undefined)).toEqual({ offer: false });
  });

  // A failed read is no verdict at all: the press is offered and a refusal, if
  // one comes, disables it.
  it("is enabled when the capability could not be read", () => {
    expect(rerunControl("gitea", null, undefined)).toEqual({ offer: true, reason: "" });
  });

  it("is disabled with the refusal's reason, whatever the capability says", () => {
    for (const kind of ["github", "gitlab"] as const) {
      expect(rerunControl(kind, cap("yes"), "the token lacks a permission."), kind).toEqual({
        offer: true,
        reason: "the token lacks a permission.",
      });
    }
  });
});

describe("rerunRefusal", () => {
  it("names what the instance lacks for an unsupported capability", () => {
    expect(rerunRefusal("capability_unsupported", "swagger declares no rerun verb")).toBe(
      "this forge cannot re-run checks for this repository (swagger declares no rerun verb).",
    );
    expect(rerunRefusal("capability_unsupported", "")).toBe(
      "this forge cannot re-run checks for this repository.",
    );
  });

  it("names the missing permission for an insufficient scope", () => {
    expect(rerunRefusal("scope_insufficient", "needs the workflow scope")).toBe(
      "the token is missing a permission this needs. Add it to the token on the forge. needs the workflow scope",
    );
  });

  // Only these two say the repository will refuse every later press; anything
  // else is the outcome of this one press.
  it("is no refusal for any other code", () => {
    for (const code of [undefined, "", "rate_limited", "head_moved", "not_supported"]) {
      expect(rerunRefusal(code, "nope"), String(code)).toBeUndefined();
    }
  });
});

describe("movedRepository", () => {
  const to = { repo_id: "v1.6e65772f7265706f", display_path: "new/repo" };

  // The successor is server-controlled text inside a refusal's body, so only a
  // whole one is taken.
  const cases = [
    {
      desc: "a stale refusal naming its successor",
      err: { code: "repo_ref_stale", cause: { successor: to } },
      want: { moved: true, to },
    },
    {
      desc: "a stale refusal naming none",
      err: { code: "repo_ref_stale", cause: { code: "repo_ref_stale" } },
      want: { moved: true, to: null },
    },
    {
      desc: "a stale refusal with no body",
      err: { code: "repo_ref_stale" },
      want: { moved: true, to: null },
    },
    {
      desc: "a successor with an empty id",
      err: { code: "repo_ref_stale", cause: { successor: { repo_id: "", display_path: "x/y" } } },
      want: { moved: true, to: null },
    },
    {
      desc: "a successor whose path is not text",
      err: { code: "repo_ref_stale", cause: { successor: { repo_id: "v1.61", display_path: 7 } } },
      want: { moved: true, to: null },
    },
    {
      desc: "another refusal carrying a successor",
      err: { code: "conflict", cause: { successor: to } },
      want: { moved: false },
    },
  ];

  for (const c of cases) {
    it(`reads ${c.desc}`, () => {
      expect(movedRepository(c.err)).toEqual(c.want);
    });
  }
});

describe("queueText", () => {
  it("names a queued row's state and position", () => {
    expect(queueText(pr({ queue_state: "queued", queue_position: 3 }))).toBe(
      "in merge queue, position 3",
    );
    expect(queueText(pr({ queue_state: "awaiting_checks", queue_position: -1 }))).toBe(
      "in merge queue, awaiting checks",
    );
    expect(queueText(pr({ queue_state: "mergeable", queue_position: 0 }))).toBe(
      "in merge queue, ready to merge, position 0",
    );
    expect(queueText(pr({ queue_state: "unmergeable" }))).toBe("in merge queue, cannot merge");
    expect(queueText(pr({ queue_state: "locked" }))).toBe("in merge queue, locked");
  });

  it("quotes a queue state this build does not know", () => {
    expect(queueText(pr({ queue_state: "paused\nnow" }))).toBe("in merge queue (paused now)");
  });

  it("says nothing for a row in no queue, or one whose queue was not read", () => {
    expect(queueText(pr({ queue_state: "none", queue_position: 2 }))).toBe("");
    expect(queueText(pr({ queue_state: "unknown" }))).toBe("");
  });
});

describe("canArmAutoMerge", () => {
  it("offers arming while checks are unsettled", () => {
    expect(canArmAutoMerge(pr({ merge_blocked: "checks_running" }))).toBe(true);
    expect(canArmAutoMerge(pr({ checks: "pending" }))).toBe(true);
  });

  // The pending arm has to be gated on nothing ELSE blocking the merge. A
  // draft or conflicting PR with a check still running would not merge when
  // that check went green, so "Merge when green" is a promise the forge
  // cannot keep.
  it("does not offer arming when another cause blocks the merge", () => {
    for (const cause of [
      "draft",
      "conflicts",
      "behind",
      "blocked",
      "checks_failing",
      "a_future_cause",
    ]) {
      expect(canArmAutoMerge(pr({ checks: "pending", merge_blocked: cause }))).toBe(false);
    }
  });

  it("treats an unnamed cause with a yes verdict like nothing blocking", () => {
    expect(
      canArmAutoMerge(pr({ checks: "pending", merge_blocked: "unknown", mergeable: "yes" })),
    ).toBe(true);
    for (const mergeable of ["no", "unknown"]) {
      expect(canArmAutoMerge(pr({ checks: "pending", merge_blocked: "unknown", mergeable }))).toBe(
        false,
      );
    }
    expect(
      canArmAutoMerge(
        pr({ checks: "pending", merge_blocked: "unknown", mergeable: "yes" }, { draft: true }),
      ),
    ).toBe(false);
  });

  // checks_running is the one named cause that DOES earn the offer: it says
  // the checks are the only thing in the way.
  it("still offers arming when the checks are the stated cause", () => {
    expect(canArmAutoMerge(pr({ checks: "pending", merge_blocked: "checks_running" }))).toBe(true);
    expect(canArmAutoMerge(pr({ checks: "failing", merge_blocked: "checks_running" }))).toBe(true);
  });

  it("does not offer arming twice", () => {
    expect(canArmAutoMerge(pr({ checks: "pending", auto_merge_armed: "yes" }))).toBe(false);
    expect(canArmAutoMerge(pr({ checks: "pending", auto_merge_armed: "unknown" }))).toBe(true);
  });

  it("does not offer arming when the merge could happen now", () => {
    expect(canArmAutoMerge(pr({ checks: "passing" }))).toBe(false);
    expect(canArmAutoMerge(pr())).toBe(false);
  });

  it("does not offer arming on a closed PR", () => {
    expect(canArmAutoMerge(pr({ checks: "pending" }, { state: "closed" }))).toBe(false);
  });

  it("does not offer arming on a row with no head commit", () => {
    expect(canArmAutoMerge(withoutHead(pr({ checks: "pending" })))).toBe(false);
    expect(canArmAutoMerge(withoutHead(pr({ merge_blocked: "checks_running" })))).toBe(false);
  });
});
