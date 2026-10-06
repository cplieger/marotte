// Pure DOM factory: reactive state and reconcile specs are injected via RenderDeps.

import { el } from "@cplieger/reactive";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { chevronEl } from "./chevron.js";
import { ICON_DOWNLOAD, ICON_REPO, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";
import { partialWhy } from "./forge-types.js";
import type { ConfiguredForge, Repo } from "./wire/types.gen.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import {
  cloneAllForAccount,
  deleteAllForAccount,
  listedRepos,
  type RepoDeps,
  type RepoListing,
} from "./forge-auth-repos.js";

export interface ReposRenderDeps {
  lastLocalNames: Set<string>;
  expandOnNextPaint: Set<string>;
  bumpState: () => void;
  repoDeps: RepoDeps;
  repoSpec: ReconcileSpec<Repo>;
  /** Read the next page of an account's list; rejects when it was refused. */
  loadMore: (forgeId: string) => Promise<void>;
  /** Why an account's last list-wide press (Load more, Clone all, Delete all)
   *  fell short, by forge id, until the next one. */
  listNotes: Map<string, string>;
}

const disclosures = new WeakMap<HTMLElement, DisclosureController>();

/**
 * The toggle and batch buttons are siblings in one header row: a button inside a `<summary>` is flattened or
 * dropped by a screen reader.
 */
export function buildAccountReposDetails(
  a: ConfiguredForge,
  l: RepoListing,
  deps: ReposRenderDeps,
): HTMLElement {
  const block = el("div", { className: "forge-account-repos", "data-account-id": a.id });
  const chevron = chevronEl();
  chevron.classList.add("forge-account-repos-chevron");
  const toggle = el(
    "button",
    { type: "button", className: "forge-account-repos-summary" },
    chevron,
    el("span", { className: "forge-account-repos-icon", "aria-hidden": "true" }, iconEl(ICON_REPO)),
    el("span", { className: "forge-account-repos-label" }),
  );
  const body = el(
    "div",
    { className: "forge-account-repos-body" },
    el("div", { className: "forge-account-repos-more" }),
  );
  block.append(
    el(
      "div",
      { className: "forge-account-repos-head" },
      toggle,
      el("div", { className: "forge-account-repos-actions" }),
    ),
    body,
  );
  disclosures.set(block, createDisclosure(toggle, body));
  updateAccountReposDetails(block, a, l, deps);
  return block;
}

export function updateAccountReposDetails(
  block: HTMLElement,
  a: ConfiguredForge,
  l: RepoListing,
  deps: ReposRenderDeps,
): void {
  if (deps.expandOnNextPaint.has(a.id)) {
    disclosures.get(block)?.open();
    deps.expandOnNextPaint.delete(a.id);
  }
  const repos = listedRepos(l);
  const head = block.querySelector<HTMLElement>(":scope > .forge-account-repos-head");
  const actions = head?.querySelector<HTMLElement>(":scope > .forge-account-repos-actions");
  if (head !== null && actions !== null && actions !== undefined) {
    setAccountSummaryLabel(head, l, repos, deps);
    refreshAccountSummaryButtons(actions, a, repos, deps);
  }
  const body = block.querySelector<HTMLElement>(":scope > .forge-account-repos-body");
  const foot = body?.querySelector<HTMLElement>(":scope > .forge-account-repos-more") ?? null;
  if (body === null || foot === null) {
    return;
  }

  // The empty state is a whole list's answer; a list still paging, cut short or never read says so in the footer.
  const emptyEl = body.querySelector<HTMLElement>(":scope > .forge-account-repos-empty");
  let list = body.querySelector<HTMLElement>(":scope > .forge-account-repos-list");
  if (repos.length === 0) {
    list?.remove();
    const whole = !l.unread && !l.stale && l.next === "" && l.partial === undefined;
    if (!whole) {
      emptyEl?.remove();
    } else if (emptyEl === null) {
      foot.before(
        el(
          "div",
          { className: "forge-account-repos-empty" },
          "No repositories accessible to this account.",
        ),
      );
    }
  } else {
    emptyEl?.remove();
    if (list === null) {
      list = el("ul", { className: "forge-account-repos-list" });
      foot.before(list);
    }
    reconcile(list, sortRepos(repos, deps), deps.repoSpec);
  }
  refreshFooter(foot, a, l, deps);
}

/**
 * The footer beneath the rows: never read, why it stopped short, Load more, and why the last list-wide press fell
 * short. The status line and a Load more still reading are kept across paints.
 */
function refreshFooter(
  foot: HTMLElement,
  a: ConfiguredForge,
  l: RepoListing,
  deps: ReposRenderDeps,
): void {
  let status = foot.querySelector<HTMLElement>(":scope > .forge-account-repos-status");
  if (status === null) {
    status = el("span", {
      className: "forge-account-repos-status forge-account-error",
      role: "status",
    });
    foot.appendChild(status);
  }
  status.textContent = deps.listNotes.get(a.id) ?? "";

  for (const line of foot.querySelectorAll(":scope > p")) {
    line.remove();
  }
  const lines: HTMLElement[] = [];
  if (l.unread) {
    lines.push(
      el(
        "p",
        { className: "forge-account-error" },
        "Could not list the repositories on this account.",
      ),
    );
  } else if (l.stale) {
    lines.push(
      el(
        "p",
        { className: "forge-account-error" },
        "Could not refresh the repositories on this account. The list is from the last read.",
      ),
    );
  }
  const why = partialWhy(l.partial, l.next);
  if (why !== "") {
    lines.push(
      el(
        "p",
        { className: "section-hint" },
        `Not every repository on this account was read: ${why}.`,
      ),
    );
  }
  foot.prepend(...lines);

  let more = foot.querySelector<HTMLButtonElement>(":scope > .forge-account-repos-load-more");
  if (l.next === "" && more?.getAttribute("aria-busy") !== "true") {
    more?.remove();
    more = null;
  } else if (more === null) {
    more = makeLoadMoreButton(a.id, status, deps);
    status.before(more);
  }
  foot.classList.toggle("hidden", lines.length === 0 && more === null && status.textContent === "");
}

function makeLoadMoreButton(
  forgeId: string,
  status: HTMLElement,
  deps: ReposRenderDeps,
): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small forge-account-repos-load-more" },
    "Load more repositories",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    status.textContent = "";
    void withAsyncFeedback(btn, () => deps.loadMore(forgeId), { keepLabel: true }).then(() => {
      deps.bumpState();
    });
  });
  return btn;
}

function sortRepos(repos: Repo[], deps: ReposRenderDeps): Repo[] {
  // Cloned first, then by full_name. Reconcile preserves identity when one repo moves between groups.
  return [...repos].sort((x, y) => {
    const xc = deps.lastLocalNames.has(x.name);
    const yc = deps.lastLocalNames.has(y.name);
    if (xc !== yc) {
      return xc ? -1 : 1;
    }
    return x.full_name.localeCompare(y.full_name);
  });
}

function setAccountSummaryLabel(
  head: HTMLElement,
  l: RepoListing,
  repos: Repo[],
  deps: ReposRenderDeps,
): void {
  const label = head.querySelector<HTMLElement>(".forge-account-repos-label");
  if (label === null) {
    return;
  }
  if (l.unread) {
    label.textContent = "Repositories not read";
    return;
  }
  const total = repos.length;
  const cloned = repos.filter((r) => deps.lastLocalNames.has(r.name)).length;
  const sofar = l.next === "" ? "" : " so far";
  label.textContent = `${String(total)} repo${total === 1 ? "" : "s"}${sofar}, ${String(cloned)} cloned locally`;
}

/**
 * Skips a button mid-async (`aria-busy="true"`) so withAsyncFeedback's textContent updates are not clobbered; the
 * next bumpState after it completes refreshes it.
 */
function refreshAccountSummaryButtons(
  actions: HTMLElement,
  a: ConfiguredForge,
  repos: Repo[],
  deps: ReposRenderDeps,
): void {
  const cloneable = repos.filter(
    (r) =>
      !deps.lastLocalNames.has(r.name) && typeof r.clone_url === "string" && r.clone_url !== "",
  );
  const clonedRepos = repos.filter((r) => deps.lastLocalNames.has(r.name));

  const oldCloneAll = actions.querySelector<HTMLButtonElement>(".forge-account-repos-clone-all");
  if (oldCloneAll?.getAttribute("aria-busy") !== "true") {
    oldCloneAll?.remove();
    if (cloneable.length > 0) {
      actions.prepend(makeCloneAllButton(a, cloneable, deps));
    }
  }

  const oldDeleteAll = actions.querySelector<HTMLButtonElement>(".forge-account-repos-delete-all");
  if (oldDeleteAll?.getAttribute("aria-busy") !== "true") {
    oldDeleteAll?.remove();
    if (clonedRepos.length > 0) {
      actions.appendChild(makeDeleteAllButton(a, clonedRepos, deps));
    }
  }
}

/** Run one list-wide batch with `btn`'s feedback. A sentence it answers is the
 *  list's, and opens the list so the rows that fell short are in view. */
async function runBatch(
  a: ConfiguredForge,
  btn: HTMLButtonElement,
  deps: ReposRenderDeps,
  batch: () => Promise<string>,
): Promise<void> {
  deps.listNotes.delete(a.id);
  await withAsyncFeedback(btn, async () => {
    const sentence = await batch();
    if (sentence !== "") {
      deps.listNotes.set(a.id, sentence);
      deps.expandOnNextPaint.add(a.id);
      throw new Error(sentence);
    }
  });
  deps.bumpState();
}

function makeCloneAllButton(
  a: ConfiguredForge,
  cloneable: Repo[],
  deps: ReposRenderDeps,
): HTMLButtonElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small forge-account-repos-clone-all",
      "data-tooltip": `Clone every uncloned repo listed here (${cloneable.length})`,
      "aria-label": `Clone ${cloneable.length} uncloned repos`,
    },
    iconEl(ICON_DOWNLOAD),
    el("span", null, String(cloneable.length)),
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    void runBatch(a, btn, deps, () => cloneAllForAccount(cloneable, btn, deps.repoDeps));
  });
  return btn;
}

function makeDeleteAllButton(
  a: ConfiguredForge,
  clonedRepos: Repo[],
  deps: ReposRenderDeps,
): HTMLButtonElement {
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small btn-danger forge-account-repos-delete-all",
      "data-tooltip": `Remove every locally-cloned repo listed here (${clonedRepos.length})`,
      "aria-label": `Delete ${clonedRepos.length} local clones`,
    },
    iconEl(ICON_TRASH),
    el("span", null, String(clonedRepos.length)),
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    void (async () => {
      const n = clonedRepos.length;
      const ok = await confirmDialog(
        `Delete the local copy of ${String(n)} repo${n === 1 ? "" : "s"}? The remotes stay intact. You can re-clone any of them later.`,
        "Delete all",
        "destructive",
      );
      if (ok) {
        await runBatch(a, btn, deps, () => deleteAllForAccount(clonedRepos, btn, deps.repoDeps));
      }
    })();
  });
  return btn;
}
