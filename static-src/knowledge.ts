// The list is kiro-cli's own store, so this module refetches and never holds an authoritative copy. Progress is polled:
// knowledge_indexing frames name a per-agent store this endpoint cannot read (handlers/knowledge-indexing.ts shows them).

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { byId } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { showToast } from "./toast.js";
import { confirm as confirmDialog } from "./confirm.js";
import { apiGetTyped, CancellableSlot, type Decoder } from "./api-client.js";
import { decodeEffectiveSettings } from "./wire/decoders.gen.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { paintIfChanged, sigChanged } from "./paint-sig.js";
import {
  addKnowledge,
  cancelKnowledgeIndexing,
  clearKnowledge,
  reindexKnowledge,
  removeKnowledge,
} from "./actions/knowledge.js";
import { ICON_CLOSE_UI, ICON_PLUS_UI, ICON_REFRESH, ICON_TRASH_UI } from "./icons.js";
import { asObject, decodeArray, optBool, optStr, reqNum, reqStr } from "./validators.js";

// Wire type and decoder, matching internal/hub/knowledge.go knowledgeContext.

interface KnowledgeContext {
  name: string;
  id: string;
  description?: string;
  path?: string;
  items_display?: string;
  item_count: number;
  indexing?: boolean;
}

const P = "$.knowledge.context";

const decodeContext: Decoder<KnowledgeContext> = (v) => {
  const o = asObject(v, P);
  const out: KnowledgeContext = {
    name: reqStr(o, "name", P),
    id: reqStr(o, "id", P),
    item_count: reqNum(o, "item_count", P),
  };
  const d = optStr(o, "description", P);
  if (d !== undefined) {
    out.description = d;
  }
  const p = optStr(o, "path", P);
  if (p !== undefined) {
    out.path = p;
  }
  const disp = optStr(o, "items_display", P);
  if (disp !== undefined) {
    out.items_display = disp;
  }
  const ix = optBool(o, "indexing", P);
  if (ix !== undefined) {
    out.indexing = ix;
  }
  return out;
};

const decodeList: Decoder<{ contexts: KnowledgeContext[] }> = (v) => {
  const o = asObject(v, "$.knowledge");
  return { contexts: decodeArray(o["contexts"], decodeContext, "$.knowledge.contexts") };
};

const POLL_MS = 1500;
/** Consecutive polls with no observable progress before giving up: a long index polls as long as it advances. */
const MAX_STALLED_POLLS = 30;

const listSlot = new CancellableSlot();
let pollTimer: ReturnType<typeof setTimeout> | null = null;
let stalledPolls = 0;
/** Progress signature from the previous tick, to tell advance from stall. */
let lastProgress = "";

registerCleanup(() => {
  listSlot.abort();
  clearPoll();
});

function clearPoll(): void {
  if (pollTimer !== null) {
    clearTimeout(pollTimer);
    pollTimer = null;
  }
}

/** During an add, `show` returns both the placeholder and the active operation under one name; the indexing entry wins. */
function mergeByName(contexts: KnowledgeContext[]): KnowledgeContext[] {
  const byName = new Map<string, KnowledgeContext>();
  for (const c of contexts) {
    const prev = byName.get(c.name);
    if (prev === undefined || (c.indexing === true && prev.indexing !== true)) {
      byName.set(c.name, c);
    }
  }
  return [...byName.values()];
}

/**
 * Items per indexing base, the only progress exposed. `keyenc.join` per component: a base name is free text, and a
 * separator in it could collapse two states and abandon a healthy index.
 */
function progressSignature(contexts: KnowledgeContext[]): string {
  return join(
    ...contexts
      .filter((c) => c.indexing === true)
      .map((c) => join(c.name, String(c.item_count), c.items_display ?? ""))
      .sort((a, b) => a.localeCompare(b)),
  );
}

/**
 * Fetch and render the knowledge list, rescheduling while any base indexes. `fromPoll` marks a poll tick; a user load
 * resets the stall budget.
 */
export function loadKnowledge(fromPoll = false): void {
  if (!fromPoll) {
    stalledPolls = 0;
    lastProgress = "";
  }
  clearPoll();
  const signal = listSlot.start();
  void apiGetTyped("/api/knowledge", decodeList, signal).then((d) => {
    if (signal.aborted) {
      return;
    }
    if (d === null) {
      renderError();
      return;
    }
    const merged = mergeByName(d.contexts);
    renderList(merged);
    if (!merged.some((c) => c.indexing === true)) {
      return;
    }
    const signature = progressSignature(merged);
    if (signature === lastProgress) {
      stalledPolls++;
    } else {
      // Advanced, so the budget resets.
      stalledPolls = 0;
      lastProgress = signature;
    }
    if (stalledPolls >= MAX_STALLED_POLLS) {
      // Nothing will move the row's last paint, so say the client gave up; re-activating the tab re-fires the load.
      renderList(merged, true);
      return;
    }
    pollTimer = setTimeout(() => {
      loadKnowledge(true);
    }, POLL_MS);
  });
  // The setting cannot change under a poll tick.
  if (!fromPoll) {
    void refreshHint(signal);
  }
}

/**
 * The hint reads marotte's knowledge_enabled, which gates the agent's knowledge tool (kiro-cli's own flag drives its
 * TUI). Management works either way.
 */
async function refreshHint(signal: AbortSignal): Promise<void> {
  const s = await apiGetTyped("/api/settings", decodeEffectiveSettings, signal);
  if (s === null) {
    // None of these failures means "knowledge is off", so the hint stands.
    return;
  }
  byId<HTMLParagraphElement>("knowledge-hint").hidden = s.knowledge_enabled;
}

let listedCount = 0;

function setListed(n: number): void {
  listedCount = n;
  byId<HTMLButtonElement>("knowledge-clear-btn").classList.toggle("hidden", n === 0);
}

function renderError(): void {
  setListed(0);
  const container = byId<HTMLDivElement>("knowledge-list");
  container.replaceChildren(
    el("div", { className: "list-empty" }, "Could not load knowledge bases."),
  );
}

function renderList(items: KnowledgeContext[], stalled = false): void {
  setListed(items.length);
  const container = byId<HTMLDivElement>("knowledge-list");
  // Drop any prior non-keyed placeholder before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (items.length === 0) {
    container.replaceChildren();
    // Two concrete examples, one a named repository, so readers can tell what an entry looks like.
    container.appendChild(
      el(
        "div",
        { className: "list-empty" },
        "No knowledge bases yet. Add your own ",
        el("code", {}, "docs/"),
        " folder, or a documentation repository cloned into the workspace, such as ",
        el("code", {}, "refs/rust-book/src"),
        " from rust-lang/book.",
      ),
    );
    return;
  }
  reconcile(container, items, {
    key: (c: KnowledgeContext) => `kb:${c.name}`,
    mount: (c: KnowledgeContext) => mountRow(c, stalled),
    update: (row: HTMLElement, c: KnowledgeContext) => {
      fillRow(row, c, stalled);
    },
  });
}

function mountRow(c: KnowledgeContext, stalled: boolean): HTMLElement {
  const row = el("div", { className: "list-row knowledge-row" });
  fillRow(row, c, stalled);
  return row;
}

/**
 * Two guards, so a poll tick repaints only the progress readout and an indexing row's Stop keeps its identity and
 * focus. Guards owned by `paint-sig.ts`.
 */
function fillRow(row: HTMLElement, c: KnowledgeContext, stalled: boolean): void {
  const parts = c.indexing === true ? ["1"] : ["0", String(c.item_count), c.path ?? ""];
  if (sigChanged(row, parts)) {
    row.classList.toggle("knowledge-indexing", c.indexing === true);
    row.replaceChildren(...rowChildren(c));
  }
  const progress = row.querySelector(".knowledge-progress");
  if (progress !== null) {
    paintIfChanged(progress, [c.items_display ?? "", stalled ? "1" : "0"], () =>
      progressChildren(c.items_display, stalled),
    );
  }
}

function rowChildren(c: KnowledgeContext): HTMLElement[] {
  const name = el("span", { className: "list-row-name" }, c.name);
  if (c.indexing === true) {
    return [name, el("span", { className: "knowledge-progress" }), cancelBtn(c.name)];
  }
  const count = `${String(c.item_count)} item${c.item_count === 1 ? "" : "s"}`;
  const metaText = c.path !== undefined && c.path !== "" ? `${count} · ${c.path}` : count;
  const meta = el("span", { className: "list-row-meta knowledge-meta" }, metaText);
  return [name, meta, reindexBtn(c.name), removeBtn(c.name)];
}

/** The leading integer percentage ("42%", "42% · ETA 3s"); null for "Cancelled" or "Failed". */
function parsePct(display: string | undefined): number | null {
  const m = /^(\d+)%/.exec(display ?? "");
  return m ? Math.min(100, Number(m[1])) : null;
}

/**
 * <progress>, not <meter>: the row moves toward completion. `aria-label`, since one row per base would need minted
 * ids. The `pct !== null` guard is load-bearing: a valueless <progress> animates work that is not happening.
 */
function progressChildren(display: string | undefined, stalled: boolean): HTMLElement[] {
  if (stalled) {
    // No bar: nothing will advance it, and an indeterminate one would claim work the client stopped watching.
    return [
      el(
        "span",
        { className: "knowledge-progress-text" },
        "Indexing stalled. Reopen this tab to check again",
      ),
    ];
  }
  const out: HTMLElement[] = [];
  const pct = parsePct(display);
  if (pct !== null) {
    // Set on the typed element: `el` routes some names to a property and the rest to setAttribute.
    const bar = el("progress", {
      className: "knowledge-bar",
      "aria-label": "Indexing",
    }) as HTMLProgressElement;
    bar.max = 100;
    bar.value = pct;
    out.push(bar);
  }
  const text = display !== undefined && display !== "" ? `Indexing… ${display}` : "Indexing…";
  out.push(el("span", { className: "knowledge-progress-text" }, text));
  return out;
}

function cancelBtn(name: string): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "list-row-btn knowledge-cancel",
    "data-tooltip": "Stop indexing",
    "aria-label": `Stop indexing knowledge base ${name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_CLOSE_UI;
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    void onCancel(name);
  });
  return btn;
}

/** No confirm: stopping keeps the base. The refetch runs whatever the answer: a refusal usually means it finished. */
async function onCancel(name: string): Promise<void> {
  await cancelKnowledgeIndexing.dispatch({ name }).outcome;
  loadKnowledge();
}

function reindexBtn(name: string): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "list-row-btn knowledge-reindex",
    "data-tooltip": "Re-index",
    "aria-label": `Re-index knowledge base ${name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_REFRESH;
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    onReindex(name);
  });
  return btn;
}

/**
 * No confirm: only the index is rebuilt. kiro-cli's `update` starts the build and returns, so the refetch finds the
 * indexing row and restarts the poll.
 */
function onReindex(name: string): void {
  void reindexKnowledge.dispatch(
    { name },
    {
      onSuccess: () => {
        showToast(`Re-indexing "${name}" in the background…`, "success");
        loadKnowledge();
      },
    },
  );
}

function removeBtn(name: string): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "list-row-btn knowledge-remove",
    "data-tooltip": "Remove",
    "aria-label": `Remove knowledge base ${name}`,
  }) as HTMLButtonElement;
  btn.innerHTML = ICON_TRASH_UI;
  btn.addEventListener("click", (e) => {
    e.stopPropagation();
    void onRemove(name);
  });
  return btn;
}

async function onRemove(name: string): Promise<void> {
  const ok = await confirmDialog(
    `Remove knowledge base "${name}"? The indexed data is deleted and the agent loses access to it.`,
    "Remove",
    "destructive",
  );
  if (!ok) {
    return;
  }
  void removeKnowledge.dispatch(
    { name },
    {
      onSuccess: () => {
        loadKnowledge();
      },
    },
  );
}

function buildAddForm(): HTMLFormElement {
  const pathInput = el("input", {
    type: "text",
    id: "knowledge-add-path",
    className: "tool-form-input",
    placeholder: "Directory path, for example docs or /abs/path…",
    "aria-label": "Knowledge base directory path",
  }) as HTMLInputElement;
  const nameInput = el("input", {
    type: "text",
    id: "knowledge-add-name",
    className: "tool-form-input",
    placeholder: "Name, optional…",
    "aria-label": "Knowledge base name",
  }) as HTMLInputElement;
  const submit = el(
    "button",
    { type: "submit", className: "btn-small" },
    "Add",
  ) as HTMLButtonElement;
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", () => {
    hideAddForm();
  });
  // `<output>` carries an implicit `status` live region, so its message is announced without aria-live.
  const error = el("output", {
    className: "knowledge-add-error",
    id: "knowledge-add-error",
  }) as HTMLOutputElement;
  const form = el(
    "form",
    { className: "knowledge-add-form", id: "knowledge-add-form" },
    pathInput,
    nameInput,
    submit,
    cancel,
    error,
  ) as HTMLFormElement;
  form.hidden = true;
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    void onAdd(pathInput, nameInput, error);
  });
  registerCleanup(bindLoadingState("knowledge.add", submit, { preserveDisabled: true }));
  return form;
}

async function onAdd(
  pathInput: HTMLInputElement,
  nameInput: HTMLInputElement,
  error: HTMLOutputElement,
): Promise<void> {
  error.textContent = "";
  const path = pathInput.value.trim();
  if (path === "") {
    pathInput.focus();
    return;
  }
  const name = nameInput.value.trim();
  // `error: false` keeps this off the toasts, so the field is the only surface for the server's message, carried only by
  // the typed outcome.
  const out = await addKnowledge.dispatch({ path, name }).outcome;
  if (out.status !== "success") {
    error.textContent =
      out.status === "error" ? out.error.message : "The add was cancelled before it ran.";
    pathInput.focus();
    return;
  }
  showToast(`Indexing "${name !== "" ? name : path}" in the background…`, "success");
  pathInput.value = "";
  nameInput.value = "";
  hideAddForm();
  loadKnowledge();
}

function showAddForm(): void {
  byId<HTMLFormElement>("knowledge-add-form").hidden = false;
  byId<HTMLInputElement>("knowledge-add-path").focus();
}

function hideAddForm(): void {
  byId<HTMLFormElement>("knowledge-add-form").hidden = true;
}

export function initKnowledge(): void {
  const addBtn = byId<HTMLButtonElement>("knowledge-add-btn");
  addBtn.innerHTML = ICON_PLUS_UI;

  const form = buildAddForm();
  byId<HTMLDivElement>("knowledge-list").before(form);

  addBtn.addEventListener("click", () => {
    if (byId<HTMLFormElement>("knowledge-add-form").hidden) {
      showAddForm();
    } else {
      hideAddForm();
    }
  });

  const clearBtn = byId<HTMLButtonElement>("knowledge-clear-btn");
  clearBtn.innerHTML = ICON_TRASH_UI;
  clearBtn.addEventListener("click", () => {
    void onClearAll();
  });
}

/** The list is kiro-cli's own, so clearing it clears it for the TUI on this machine too; the confirm says so. */
async function onClearAll(): Promise<void> {
  const ok = await confirmDialog(
    `Remove all ${String(listedCount)} knowledge base${listedCount === 1 ? "" : "s"}? kiro-cli on this machine uses the same list.`,
    "Remove all",
    "destructive",
  );
  if (!ok) {
    return;
  }
  void clearKnowledge.dispatch(undefined, {
    onSuccess: () => {
      loadKnowledge();
    },
  });
}
