// ---------------------------------------------------------------------------
// A connection's owner scopes: the owners whose pull requests are read beside
// the account's own. Every add or remove sends the whole list, one write at a
// time, each built from the list the last one stored.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { buildChip } from "./chip.js";
import { iconEl } from "./icon-el.js";
import { ICON_EXEC_GROUP } from "./icons.js";
import { reconcile } from "./reconcile.js";
import { withAsyncFeedback } from "./async-button.js";
import { setOwners } from "./actions/forge.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

export interface OwnerScopesDeps {
  /** Record the list the server stored on the connection's row. */
  readonly stored: (id: string, owners: readonly string[]) => void;
}

interface Block {
  readonly id: string;
  owners: readonly string[];
  /** The write in flight, or a settled one: the next write starts after it. */
  chain: Promise<void>;
  readonly status: HTMLElement;
  readonly deps: OwnerScopesDeps;
}

const blocks = new WeakMap<HTMLElement, Block>();

export function buildOwnerScopes(a: ConfiguredForge, deps: OwnerScopesDeps): HTMLElement {
  const chevron = chevronEl();
  chevron.classList.add("forge-account-repos-chevron");
  const summary = el(
    "summary",
    { className: "forge-account-repos-summary" },
    chevron,
    el(
      "span",
      { className: "forge-account-repos-icon", "aria-hidden": "true" },
      iconEl(ICON_EXEC_GROUP),
    ),
    el("span", { className: "forge-account-repos-label" }),
  );

  const status = el("div", { className: "forge-card-status", "aria-live": "polite" });
  const input = el("input", {
    className: "rf-input",
    type: "text",
    autocomplete: "off",
    placeholder: a.kind === "gitlab" ? "my-group" : "an organization or user",
  }) as HTMLInputElement;
  const add = el(
    "button",
    { type: "submit", className: "btn rf-submit" },
    "Add",
  ) as HTMLButtonElement;
  const form = el(
    "form",
    { className: "rule-form", "data-rule-form": "owner", "aria-label": "Add an owner" },
    el(
      "label",
      { className: "rf-field rf-grow" },
      el("span", { className: "rf-label" }, "Owner"),
      input,
    ),
    add,
  ) as HTMLFormElement;

  const hint =
    a.kind === "gitlab"
      ? "Pull requests in these owners' projects are listed beside your own. On GitLab an owner is a group, such as my-group or my-group/subgroup."
      : "Pull requests in these owners' repositories are listed beside your own.";
  const details = el(
    "details",
    { className: "forge-account-owners", "data-account-id": a.id },
    summary,
    el(
      "div",
      { className: "forge-account-owners-body" },
      el("p", { className: "forge-help" }, hint),
      el("div", { className: "chip-list" }),
      form,
      status,
    ),
  );

  const block: Block = { id: a.id, owners: [], chain: Promise.resolve(), status, deps };
  blocks.set(details, block);

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const owner = input.value.trim();
    if (owner === "") {
      setLine(status, "Name an owner first.", "err");
      return;
    }
    setLine(status, "");
    void withAsyncFeedback(
      add,
      async () => {
        await write(block, (cur) => [...cur, owner], `Could not add ${owner}`);
        input.value = "";
      },
      { keepLabel: true },
    );
  });

  updateOwnerScopes(details, a);
  return details;
}

export function updateOwnerScopes(details: HTMLElement, a: ConfiguredForge): void {
  const block = blocks.get(details);
  if (block === undefined) {
    return;
  }
  block.owners = a.owner_scopes ?? [];
  const label = details.querySelector(".forge-account-repos-label");
  if (label !== null) {
    const n = block.owners.length;
    label.textContent = n === 0 ? "No other owners" : `${n} other owner${n === 1 ? "" : "s"}`;
  }
  const list = details.querySelector<HTMLElement>(".chip-list");
  if (list === null) {
    return;
  }
  reconcile(list, [...block.owners], {
    key: (owner) => owner,
    mount: (owner) => {
      const chip = buildChip({
        label: owner,
        removeTitle: `Remove ${owner}`,
        onRemove: () => {
          setLine(block.status, "");
          const remove = chip.querySelector<HTMLButtonElement>(".chip-remove");
          const run = (): Promise<void> =>
            write(block, (cur) => cur.filter((o) => o !== owner), `Could not remove ${owner}`);
          void (remove === null ? run() : withAsyncFeedback(remove, run));
        },
      });
      return chip;
    },
  });
}

/** Queue one whole-list write after the one in flight; `next` builds its list
 *  from what the server last stored. Rejects after writing the refusal. */
function write(
  block: Block,
  next: (cur: readonly string[]) => readonly string[],
  failure: string,
): Promise<void> {
  const run = block.chain.then(async () => {
    const o = await setOwners.dispatch({ forgeId: block.id, owners: next(block.owners) }).outcome;
    if (o.status === "cancelled") {
      return;
    }
    if (o.status === "error") {
      const refusal = `${failure}. ${o.error.message}`;
      setLine(block.status, refusal, "err");
      throw new Error(refusal);
    }
    block.owners = o.value.owners;
    block.deps.stored(block.id, o.value.owners);
  });
  block.chain = run.catch(() => undefined);
  return run;
}

function setLine(status: HTMLElement, text: string, kind: "err" | "" = ""): void {
  status.textContent = text;
  status.className = kind === "" ? "forge-card-status" : `forge-card-status ${kind}`;
}
