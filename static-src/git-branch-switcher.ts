// Per-repo branch switcher: a singleton popover on a repo section's branch chip listing local branches, with a
// "Create new branch" form. Placement, anchor tracking, dismissal, aria-expanded and focus return are createPopover's;
// rovingFocus supplies the menu keyboard contract. This module keeps the content, the load and the checkout actions.

import { apiGet } from "./api-client.js";
import { checkoutBranch, suggestBranchName } from "./actions/git-branch.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { withAsyncFeedback } from "./async-button.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_GIT_BRANCH, ICON_SEND, ICON_SPARKLE, findGlyph } from "./icons.js";
import { createPopover, type PopoverController } from "@cplieger/ui-primitives/popover";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";

interface BranchEntry {
  name: string;
  current: boolean;
}
interface BranchesResponse {
  branches: BranchEntry[];
  current: string;
}

let openPopover: HTMLDivElement | null = null;
let popoverCtl: PopoverController | null = null;
let popoverNav: RovingFocusController | null = null;
let activeAnchor: HTMLElement | null = null;
let branchController: AbortController | null = null;
let popoverBindingCleanups: (() => void)[] = [];
/** Per-row checkout loading-state unbinds by branch, cleared by reconcile.onRemove and en masse by closePopover(). */
const rowUnbinds = new Map<string, () => void>();
registerCleanup(() => branchController?.abort());

/**
 * The icon is a sibling node rather than a background image, so it inherits `currentColor` and the CSP allows it;
 * the input reserves its room in CSS. Both fields use it so the two rows read as one kind of control.
 */
function branchField(glyph: string, input: HTMLInputElement): HTMLDivElement {
  return el("div", { className: "git-branch-field" }, iconEl(glyph), input) as HTMLDivElement;
}

/**
 * Open the branch switcher anchored to anchorEl for repo. A re-click on the same anchor closes it; another anchor
 * closes the previous popover and opens a new one.
 */
export function openBranchSwitcher(repo: string, anchorEl: HTMLElement): void {
  if (openPopover !== null && activeAnchor === anchorEl) {
    closePopover();
    return;
  }
  closePopover();
  activeAnchor = anchorEl;

  // No role here: role="menu" on a panel holding an input and a form is an axe aria-required-children violation. The
  // menu role sits on the branch list, whose children are menuitems.
  const pop = el("div", { className: "git-branch-popover" }) as HTMLDivElement;
  const filter = el("input", {
    type: "search",
    className: "tool-form-input git-branch-popover-filter",
    placeholder: "Filter branches…",
    autocomplete: "off",
    "aria-label": "Filter branches",
  }) as HTMLInputElement;
  const list = el("div", {
    className: "git-branch-popover-list",
    role: "menu",
    "aria-label": "Branches",
  }) as HTMLDivElement;
  // Loading, empty and failure text is a status line beside the menu, never a child: a text node inside role="menu" is
  // the same violation, firing on every open. An empty menu is fine (measured with axe).
  const status = el(
    "div",
    { className: "git-branch-popover-status", role: "status" },
    "Loading…",
  ) as HTMLDivElement;
  const createInput = el("input", {
    type: "text",
    className: "tool-form-input git-branch-popover-create-input",
    placeholder: "Create new branch…",
    autocomplete: "off",
    "aria-label": "New branch name",
  }) as HTMLInputElement;
  // Fills the create input from the repo's work in progress; the user edits, then accepts.
  const suggestBtn = el(
    "button",
    {
      type: "button",
      className: "btn-small icon-only git-branch-popover-suggest",
      "data-tooltip": "Suggest a branch name for the work in progress",
      "aria-label": "Suggest a branch name",
    },
    iconEl(ICON_SPARKLE),
  ) as HTMLButtonElement;
  suggestBtn.addEventListener("click", () => {
    void withAsyncFeedback(suggestBtn, async () => {
      const o = await suggestBranchName.dispatch({ repo }).outcome;
      if (o.status !== "success") {
        // Reject so the feedback helper shows ✗ with the real failure (the framework already toasted it).
        throw new Error(o.status === "error" ? o.error.message : "suggestion cancelled");
      }
      const res = o.value;
      // Only while this popover is still the open one, and never over a name the user typed.
      if (res.output !== undefined && res.output !== "" && openPopover === pop) {
        createInput.value = res.output;
        createInput.focus();
        // Select the whole name and scroll to its head: a plain focus() leaves a long name showing its tail, and the
        // selection marks it a suggestion that typing replaces.
        createInput.setSelectionRange(0, res.output.length);
        createInput.scrollLeft = 0;
      }
    });
  });
  // Enter already submits, but after a suggestion a reader looks for a button that accepts it.
  const createBtn = el(
    "button",
    {
      type: "submit",
      className: "btn-small icon-only btn-primary git-branch-popover-go",
      "data-tooltip": "Create and check out this branch",
      "aria-label": "Create branch",
    },
    iconEl(ICON_SEND),
  ) as HTMLButtonElement;
  const createForm = el(
    "form",
    { className: "git-branch-popover-create" },
    branchField(ICON_GIT_BRANCH, createInput),
    suggestBtn,
    createBtn,
  ) as HTMLFormElement;
  pop.append(branchField(findGlyph("filter"), filter), list, status, createForm);
  const ctl = createPopover(anchorEl, pop, {
    placement: "bottom",
    align: "start",
    offset: 4,
    margin: 8,
    matchAnchorWidth: 340,
    haspopup: "menu",
    // The library guards against a detached anchor (git tab re-rendered mid-request).
    returnFocus: anchorEl,
    onClose: cleanupSwitcher,
  });
  openPopover = pop;
  popoverCtl = ctl;
  // Items are queried live, so rows arriving later just work; refresh() after each reconcile restores the
  // single-Tab-stop invariant.
  popoverNav = rovingFocus(pop, ".git-branch-popover-row");
  ctl.show();

  branchController?.abort();
  branchController = new AbortController();
  void apiGet<BranchesResponse>(
    `/api/git/branches?repo=${encodeURIComponent(repo)}`,
    branchController.signal,
  ).then((data) => {
    if (data === null) {
      status.textContent = "Failed to load branches.";
      return;
    }
    const render = (q: string): void => {
      const filtered = data.branches.filter((b) => b.name.toLowerCase().includes(q.toLowerCase()));
      const onRemoveRow = (_: HTMLElement, key: string): void => {
        const u = rowUnbinds.get(key);
        if (u !== undefined) {
          u();
          rowUnbinds.delete(key);
        }
      };
      // One reconcile for both outcomes: the empty-state text lives in the status line, so `textContent =` never wipes
      // keyed rows out from under reconcile.
      reconcile(list, filtered, {
        key: (b: BranchEntry) => b.name,
        mount: (b: BranchEntry) => {
          const row = el(
            "button",
            {
              type: "button",
              className: `git-branch-popover-row${b.current ? " current" : ""}`,
              role: "menuitem",
            },
            b.name,
          ) as HTMLButtonElement;
          if (b.current) {
            row.setAttribute("data-tooltip", "Current branch");
          }
          row.addEventListener("click", () => {
            void doCheckout(repo, b.name, false);
          });
          rowUnbinds.set(b.name, bindLoadingState("git.checkout_branch", row));
          return row;
        },
        update: (row, b: BranchEntry) => {
          // The current flag may flip while the popover stays open.
          row.className = `git-branch-popover-row${b.current ? " current" : ""}`;
          if (b.current) {
            row.setAttribute("data-tooltip", "Current branch");
          } else {
            row.removeAttribute("data-tooltip");
          }
        },
        onRemove: onRemoveRow,
      });
      status.textContent =
        filtered.length === 0 ? (q === "" ? "No branches." : "No matching branches.") : "";
    };
    render("");
    filter.addEventListener("input", () => {
      render(filter.value);
      if (openPopover === pop) {
        popoverNav?.refresh();
      }
    });
    if (openPopover === pop) {
      popoverNav?.refresh();
      // Content grew from "Loading…" to the rows: re-clamp.
      popoverCtl?.reposition();
    }
    filter.focus();
  });

  createForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const name = createInput.value.trim();
    if (name === "") {
      return;
    }
    void doCheckout(repo, name, true);
  });
  popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", createInput));
  popoverBindingCleanups.push(bindLoadingState("git.checkout_branch", createBtn));
}

/** Outside-click and Escape arrive here too, via the controller's dismissal wiring. */
function closePopover(): void {
  popoverCtl?.hide();
}

/**
 * Unbind loading states, abort the branches load, drop the panel. Focus return and aria-expanded are the popover
 * controller's.
 */
function cleanupSwitcher(): void {
  if (openPopover === null) {
    return;
  }
  for (const fn of popoverBindingCleanups) {
    fn();
  }
  popoverBindingCleanups = [];
  for (const unbind of rowUnbinds.values()) {
    unbind();
  }
  rowUnbinds.clear();
  branchController?.abort();
  branchController = null;
  popoverNav?.dispose();
  popoverNav = null;
  openPopover.remove();
  openPopover = null;
  popoverCtl = null;
  activeAnchor = null;
}

async function doCheckout(repo: string, branch: string, create: boolean): Promise<void> {
  // Anchor and optimistic state live in the closure, not the action args, so structuredClone-on-retry never sees a
  // DOM element.
  const anchor = activeAnchor;
  const prevText = anchor?.textContent ?? "";
  if (anchor !== null) {
    anchor.textContent = branch;
  }
  await checkoutBranch.dispatch(
    { repo, branch, create },
    {
      onSuccess: () => {
        void import("./git-changes-tab.js")
          .then((m) => m.refreshChanges())
          .catch((e: unknown) => {
            console.error("[git-branch] refresh import failed", e);
          });
      },
      onError: () => {
        if (anchor?.isConnected === true) {
          anchor.textContent = prevText;
        }
      },
      onSettled: () => {
        closePopover();
      },
    },
  );
}
