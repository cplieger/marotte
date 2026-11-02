// Every add or remove sends the whole list, one write at a time, each built from the list the last one stored.

import { join } from "@cplieger/keyenc";
import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { buildChip } from "./chip.js";
import { iconEl } from "./icon-el.js";
import { ICON_EXEC_GROUP } from "./icons.js";
import { reconcile } from "./reconcile.js";
import { withAsyncFeedback } from "./async-button.js";
import { setOwners } from "./actions/forge.js";
import { readForges } from "./forge-store.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

interface Block {
  readonly id: string;
  /** The list painted: the last durable answer, or the row's list when the row's list last changed. */
  owners: readonly string[];
  /** The row's list the block last adopted, so a repaint from rows older than a write keeps the write's list. */
  rowOwners: string;
  /** The write in flight, or a settled one: the next write starts after it. */
  chain: Promise<void>;
  readonly status: HTMLElement;
  readonly details: HTMLElement;
}

const blocks = new WeakMap<HTMLElement, Block>();

export function buildOwnerScopes(a: ConfiguredForge): HTMLElement {
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

  const block: Block = {
    id: a.id,
    owners: [],
    rowOwners: "",
    chain: Promise.resolve(),
    status,
    details,
  };
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

  block.rowOwners = join(...(a.owner_scopes ?? []));
  block.owners = a.owner_scopes ?? [];
  paintOwners(block);
  return details;
}

export function updateOwnerScopes(details: HTMLElement, a: ConfiguredForge): void {
  const block = blocks.get(details);
  const rowOwners = join(...(a.owner_scopes ?? []));
  if (block === undefined || rowOwners === block.rowOwners) {
    return;
  }
  block.rowOwners = rowOwners;
  block.owners = a.owner_scopes ?? [];
  paintOwners(block);
}

function paintOwners(block: Block): void {
  const details = block.details;
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
    paintOwners(block);
    void readForges();
  });
  block.chain = run.catch(() => undefined);
  return run;
}

function setLine(status: HTMLElement, text: string, kind: "err" | "" = ""): void {
  status.textContent = text;
  status.className = kind === "" ? "forge-card-status" : `forge-card-status ${kind}`;
}
