// ---------------------------------------------------------------------------
// Commit workflow rendering: commit message textarea + AI generate +
// recent commits collapsible section. Extracted from git-changes-tab.ts
// to isolate the commit-specific UI concern.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { commit as commitAction, generateCommitMessage } from "./actions/git-changes.js";
import type { ActionOutcome } from "./actions/index.js";
import { el } from "@cplieger/reactive";
import { isSafeURL } from "./url-safety.js";
import { iconEl } from "./icon-el.js";
import { ICON_SPARKLE } from "./icons.js";
import type { GitRepoStatus } from "./git-types.js";

type RepoStatus = GitRepoStatus;

/** The host a server-derived commit-URL prefix points at, or "" when there is
 *  no usable prefix. Doubles as the render gate below — no host, no link — and
 *  as the belt-and-braces scheme guard over a value the server built out of a
 *  repository's own origin remote, which is config we do not control. */
function commitLinkHost(prefix: string): string {
  if (prefix === "" || !isSafeURL(prefix)) {
    return "";
  }
  return new URL(prefix).host;
}

/** The commit hash: a link to its page on `host` when the server derived one,
 *  else the plain selectable text it has always been. The accessible name says
 *  where the link goes, because the hash alone does not. */
function renderSha(sha: string, prefix: string, host: string): HTMLElement {
  const code = el("code", { className: "git-recent-commits-sha" }, sha);
  if (host === "") {
    return code;
  }
  const label = `Open commit ${sha} on ${host}`;
  return el(
    "a",
    {
      className: "git-recent-commits-sha-link",
      href: prefix + sha,
      target: "_blank",
      rel: "noopener noreferrer",
      "aria-label": label,
      "data-tooltip": label,
    },
    code,
  );
}

/** Why a press on a repository did not land, as its section says it. */
export interface Refusal {
  lead: string;
  detail: string;
}

/** A press's request: the refusal it met, or null once it landed. */
export type PressRun = () => Promise<Refusal | null>;

export interface PressOpts {
  /** Asked before anything is sent; a "no" leaves the control as it was. */
  confirm?: () => Promise<boolean>;
  /** Read the repository's status again once the request lands; default true. */
  refresh?: boolean;
}

/** Dependencies injected from git-changes-tab module state. */
export interface CommitDeps {
  commitMessages: Map<string, string>;
  diffAbort: AbortController | null;
  /** Wire a control that runs one request on a repository, with its busy
   *  state and its refusal said in the section (git-changes-tab owns both). */
  press: (
    btn: HTMLButtonElement,
    repo: string,
    key: string,
    run: PressRun,
    opts?: PressOpts,
  ) => HTMLButtonElement;
}

/** The refusal an action's outcome leaves, null when it landed: what the press
 *  could not do, in git's words. */
export function refusalOf(o: ActionOutcome<unknown>, could: string): Refusal | null {
  if (o.status === "success") {
    return null;
  }
  return {
    lead: `Could not ${could}.`,
    detail: o.status === "error" ? o.error.message : "It was cancelled.",
  };
}

/** The commit message box on screen for `repo`: a repaint during a press
 *  replaces the one the press was built with. */
function liveBox(repo: string): HTMLTextAreaElement | null {
  return document.querySelector<HTMLTextAreaElement>(
    `.git-commit-input[data-repo="${CSS.escape(repo)}"]`,
  );
}

/** Render the recent-commits collapsible section for a repo. */
export function renderRecentCommits(r: RepoStatus, deps: CommitDeps): HTMLElement {
  const body = el("div", { className: "git-recent-commits-body" }, "Loading…");
  const wrap = el(
    "details",
    { className: "git-recent-commits" },
    el("summary", { className: "git-recent-commits-summary" }, "Recent commits"),
    body,
  ) as HTMLDetailsElement;

  let loaded = false;
  wrap.addEventListener("toggle", () => {
    if (!wrap.open || loaded) {
      return;
    }
    loaded = true;
    void apiGet<{
      entries?: string[];
      remote?: string;
      behind?: number;
      commit_url_prefix?: string;
    }>(`/api/git/log?repo=${encodeURIComponent(r.repo)}`, deps.diffAbort?.signal).then((data) => {
      if (deps.diffAbort?.signal.aborted) {
        return;
      }
      if (data === null) {
        body.textContent = "Failed to load.";
        return;
      }
      const entries = data.entries ?? [];
      if (entries.length === 0) {
        body.textContent = "No commits.";
        return;
      }
      body.replaceChildren();
      const prefix = data.commit_url_prefix ?? "";
      const host = commitLinkHost(prefix);
      const list = el("ul", { className: "git-recent-commits-list" });
      for (const line of entries.slice(0, 20)) {
        const li = el("li", { className: "git-recent-commits-row" });
        // line shape: "<sha> <subject>"
        const sp = line.indexOf(" ");
        if (sp > 0) {
          li.appendChild(renderSha(line.slice(0, sp), prefix, host));
          li.appendChild(
            el("span", { className: "git-recent-commits-subject" }, line.slice(sp + 1)),
          );
        } else {
          li.textContent = line;
        }
        list.appendChild(li);
      }
      body.appendChild(list);
    });
  });

  return wrap;
}

/** Render the commit message textarea + AI generate + Commit button.
 *
 *  `stagedCount` is the number of staged FILES, and the Commit button
 *  names it. The button used to read a bare "Commit" sitting below the
 *  whole file list, so the one control that writes history said nothing
 *  about what it was about to write — and since the index is the
 *  selection, nothing else on the row did either. */
export function renderCommitArea(
  r: RepoStatus,
  deps: CommitDeps,
  stagedCount: number,
): HTMLElement {
  const wrap = el("div", { className: "git-commit-area" });

  const ta = el("textarea", {
    className: "git-commit-input",
    placeholder: "Commit message…",
    rows: 2,
    "data-repo": r.repo,
  }) as HTMLTextAreaElement;
  // Restore previously typed commit message.
  const saved = deps.commitMessages.get(r.repo);
  if (saved) {
    ta.value = saved;
  }
  wrap.appendChild(ta);

  const row = el("div", { className: "git-commit-row" });

  const ai = el(
    "button",
    {
      type: "button",
      className: "btn-small",
      "data-tooltip": "Generate commit message from staged changes",
    },
    iconEl(ICON_SPARKLE),
    "AI message",
  ) as HTMLButtonElement;
  row.appendChild(
    deps.press(
      ai,
      r.repo,
      "ai-message",
      async () => {
        const o = await generateCommitMessage.dispatch({ repo: r.repo }).outcome;
        if (o.status !== "success") {
          return refusalOf(o, "write a commit message");
        }
        // Server returns {output}; only fill when non-empty so a failed/empty
        // generation never wipes a message the user already typed.
        const generated = o.value.output ?? "";
        if (generated !== "") {
          deps.commitMessages.set(r.repo, generated);
          const box = liveBox(r.repo);
          if (box !== null) {
            box.value = generated;
          }
        }
        return null;
      },
      { refresh: false },
    ),
  );

  const commit = el(
    "button",
    { type: "button", className: "btn-small btn-primary" },
    `Commit ${String(stagedCount)} file${stagedCount === 1 ? "" : "s"}`,
  ) as HTMLButtonElement;
  row.appendChild(
    deps.press(commit, r.repo, "commit", async () => {
      const box = liveBox(r.repo) ?? ta;
      const message = box.value.trim();
      if (message === "") {
        return { lead: "Could not commit.", detail: "Write a commit message first." };
      }
      // A refused commit (a hook, an identity) leaves the typed message where it
      // is (18-F1); only a commit that landed clears it.
      const o = await commitAction.dispatch({ repo: r.repo, message }).outcome;
      const refused = refusalOf(o, "commit");
      if (refused !== null) {
        return refused;
      }
      box.value = "";
      deps.commitMessages.delete(r.repo);
      return null;
    }),
  );

  wrap.appendChild(row);
  return wrap;
}
