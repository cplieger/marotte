// The confirmation step of Merge and Merge-when-green, offering the repository's merge methods. Default: the last
// pick (`last_merge_method`, cross-device) where offered, else the first listed; a changed pick persists on confirm.

import { createDialog, type DialogController } from "@cplieger/ui-primitives/dialog";
import { el } from "@cplieger/reactive";

import { loadSettings } from "./persist.js";
import { patchAppSettings } from "./actions/settings.js";
import { readAffordances } from "./actions/git-prs.js";

/** What the caller renders into the dialog chrome, and the repository whose
 *  merge methods it offers. */
interface MergeDialogOpts {
  title: string;
  message: string;
  confirmLabel: string;
  forge_id: string;
  repo_id: string;
}

/** A spelling missing here is shown as the forge spells it. */
const METHOD_WORDS = new Map<string, { name: string; desc: string }>([
  [
    "merge",
    {
      name: "Create a merge commit",
      desc: "Keep every commit and join them to the base with a merge commit.",
    },
  ],
  ["squash", { name: "Squash and merge", desc: "Fold the branch into one commit on the base." }],
  [
    "rebase",
    {
      name: "Rebase and merge",
      desc: "Replay the branch's commits onto the base, no merge commit.",
    },
  ],
  [
    "rebase-merge",
    {
      name: "Rebase, then create a merge commit",
      desc: "Replay the commits onto the base, then join them with a merge commit.",
    },
  ],
  [
    "fast-forward-only",
    { name: "Fast-forward only", desc: "Move the base to the branch's head, no merge commit." },
  ],
  [
    "rebase_merge",
    {
      name: "Merge commit with semi-linear history",
      desc: "A merge commit, made only when the branch is rebased onto the base.",
    },
  ],
  [
    "ff",
    { name: "Fast-forward merge", desc: "Move the base to the branch's head, no merge commit." },
  ],
]);

// One controller per dialog element, so backdrop/Escape listeners do not stack. Keyed to the element: a stale
// controller drives a detached dialog whose close never reaches the live one.
const dialogCtls = new WeakMap<HTMLDialogElement, DialogController>();

function controllerFor(dlg: HTMLDialogElement): DialogController {
  let ctl = dialogCtls.get(dlg);
  if (ctl === undefined) {
    ctl = createDialog(dlg, { closeOnBackdrop: true, closeOnEscape: true });
    dialogCtls.set(dlg, ctl);
  }
  return ctl;
}

async function readMethods(opts: MergeDialogOpts): Promise<{ methods: string[]; error: string }> {
  const o = await readAffordances.dispatch({ forge_id: opts.forge_id, repo_id: opts.repo_id })
    .outcome;
  if (o.status !== "success") {
    const why = o.status === "error" ? o.error.message : "the read was cancelled";
    return { methods: [], error: `Could not read this repository's merge methods. ${why}` };
  }
  const methods = o.value.merge_strategies;
  return {
    methods,
    error: methods.length === 0 ? "The forge names no merge method for this repository." : "",
  };
}

function methodRow(method: string, checked: boolean): HTMLElement {
  const words = METHOD_WORDS.get(method);
  const text = el(
    "span",
    { className: "pr-merge-method-text" },
    el("span", { className: "pr-merge-method-name" }, words?.name ?? method),
  );
  if (words !== undefined) {
    text.appendChild(el("span", { className: "pr-merge-method-desc" }, words.desc));
  }
  const radio = el("input", {
    type: "radio",
    name: "pr-merge-method",
    value: method,
  }) as HTMLInputElement;
  radio.checked = checked;
  return el("label", { className: "pr-merge-method" }, radio, text);
}

function setStatus(status: HTMLElement, text: string, tone: "pending" | "error" | ""): void {
  status.textContent = text;
  status.className = tone === "" ? "forge-status" : `forge-status forge-status-${tone}`;
  status.hidden = text === "";
}

/**
 * Open the merge dialog. Resolves the chosen method in the forge's spelling, or null on cancel, Escape or backdrop;
 * persists a changed choice as the next default first.
 */
export async function openMergeMethodDialog(opts: MergeDialogOpts): Promise<string | null> {
  const dlg = document.getElementById("pr-merge-dialog") as HTMLDialogElement | null;
  if (dlg === null) {
    return null;
  }
  const ctl = controllerFor(dlg);

  const title = document.getElementById("pr-merge-title");
  const message = document.getElementById("pr-merge-message");
  const methods = document.getElementById("pr-merge-methods");
  const status = document.getElementById("pr-merge-status");
  const confirmBtn = document.getElementById("pr-merge-confirm-btn") as HTMLButtonElement | null;
  if (
    title === null ||
    message === null ||
    methods === null ||
    status === null ||
    confirmBtn === null
  ) {
    console.error("merge dialog missing required elements");
    return null;
  }

  title.textContent = opts.title;
  message.textContent = opts.message;
  for (const row of methods.querySelectorAll(".pr-merge-method")) {
    row.remove();
  }
  methods.setAttribute("aria-busy", "true");
  setStatus(status, "Reading this repository's merge methods…", "pending");

  return await new Promise<string | null>((resolve) => {
    let settled = false;
    let initial = "";
    const settle = (value: string | null): void => {
      if (settled) {
        return;
      }
      settled = true;
      resolve(value);
    };

    // Cloning drops a prior open's listeners.
    const freshConfirm = confirmBtn.cloneNode(true) as HTMLButtonElement;
    confirmBtn.replaceWith(freshConfirm);
    freshConfirm.textContent = opts.confirmLabel;
    freshConfirm.disabled = true;
    freshConfirm.addEventListener("click", () => {
      const picked =
        methods.querySelector<HTMLInputElement>('input[name="pr-merge-method"]:checked')?.value ??
        initial;
      if (picked !== initial) {
        // Fire-and-forget: a failed save costs the memory, never the merge.
        void patchAppSettings.dispatch({ body: { last_merge_method: picked } });
      }
      settle(picked);
      ctl.close();
    });
    for (const btn of dlg.querySelectorAll<HTMLButtonElement>("[data-pr-merge-close]")) {
      const fresh = btn.cloneNode(true) as HTMLButtonElement;
      btn.replaceWith(fresh);
      fresh.addEventListener("click", () => {
        ctl.close();
      });
    }
    // Any close path resolves null unless the confirm already settled.
    dlg.addEventListener(
      "close",
      () => {
        settle(null);
      },
      { once: true },
    );

    ctl.open();

    // A read answering after this open closed fills nothing: the element is the next open's.
    void Promise.all([readMethods(opts), loadSettings()]).then(([read, settings]) => {
      if (settled) {
        return;
      }
      methods.removeAttribute("aria-busy");
      if (read.error !== "") {
        setStatus(status, read.error, "error");
        return;
      }
      const remembered = settings?.last_merge_method ?? "";
      initial = read.methods.includes(remembered) ? remembered : (read.methods[0] ?? "");
      for (const m of read.methods) {
        methods.appendChild(methodRow(m, m === initial));
      }
      setStatus(status, "", "");
      freshConfirm.disabled = false;
    });
  });
}
