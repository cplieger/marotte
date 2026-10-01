// ---------------------------------------------------------------------------
// The knowledge list is SERVER-canonical — kiro-cli's own store, not marotte's —
// so this module fetches and refetches it and never holds an authoritative copy.
// Indexing runs in the background and progress is POLLED, never pushed: the two
// indexing notifications that existed named a per-agent store this endpoint
// cannot read, so they were deleted rather than kept as a no-op.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { sigChanged } from "./paint-sig.js";
import { byId } from "./dom.js";
import { reconcile } from "./reconcile.js";
import { showToast } from "./toast.js";
import { confirm as confirmDialog } from "./confirm.js";
import { apiGetTyped, CancellableSlot, type Decoder } from "./api-client.js";
import { decodeEffectiveSettings } from "./wire/decoders.gen.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { addKnowledge, reindexKnowledge, removeKnowledge } from "./actions/knowledge.js";
import { ICON_PLUS_UI, ICON_REFRESH, ICON_TRASH_UI } from "./icons.js";
import { asObject, decodeArray, optBool, optStr, reqNum, reqStr } from "./validators.js";

// --- Wire type + decoder (matches internal/hub/knowledge.go knowledgeContext) ---

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

// --- Fetch + poll state ---

/** Poll cadence while any base is still indexing. */
const POLL_MS = 1500;
/**
 * Consecutive polls with NO OBSERVABLE PROGRESS before giving up.
 *
 * Stall-based rather than a flat tick cap: "taking a long time" and "wedged"
 * are different conditions and only the second is worth abandoning, so a big
 * index polls as long as it keeps advancing. The flat 200-tick cap this
 * replaced silently froze the UI at ~5 minutes while KAS carried on indexing.
 */
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

/** Collapse the show output to one row per base. During an add, `show` returns
 *  both the placeholder context (item_count 0, "(indexing...)") AND the active
 *  operation (indexing:true, progress) under the same name — the indexing
 *  entry wins so the row shows live progress. */
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

/** A signature of how far indexing has got, so a stall is distinguishable from
 *  slowness. Counts items per indexing base — the only progress this endpoint
 *  exposes.
 *
 *  `keyenc.join` per component because a base NAME is free text: a template
 *  literal lets a separator in one name collapse two progress states onto one
 *  signature, which abandons a healthy index early. */
function progressSignature(contexts: KnowledgeContext[]): string {
  return join(
    ...contexts
      .filter((c) => c.indexing === true)
      .map((c) => join(c.name, String(c.item_count), c.items_display ?? ""))
      .sort((a, b) => a.localeCompare(b)),
  );
}

/** Fetch + render the knowledge list. `fromPoll` distinguishes a poll tick from a
 *  user-triggered load, which resets the stall budget. Reschedules itself while
 *  any base is still indexing. */
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
      // Advanced, so the budget resets: a long index is not a wedged one.
      stalledPolls = 0;
      lastProgress = signature;
    }
    if (stalledPolls >= MAX_STALLED_POLLS) {
      // Say so: the row's last paint reads `Indexing… n%` and nothing will move
      // it, so leaving it claims the client is still watching an index it has
      // given up on. Re-activating the tab re-fires `loadKnowledge`, hence the copy.
      renderList(merged, true);
      return;
    }
    pollTimer = setTimeout(() => {
      loadKnowledge(true);
    }, POLL_MS);
  });
  // Read only on a user-triggered load: the setting cannot change under a poll tick.
  if (!fromPoll) {
    void refreshHint(signal);
  }
}

/** Show/hide the "knowledge is off" hint from marotte's own knowledge_enabled
 *  setting (the General toggle), which is what gates the agent's knowledge tool —
 *  kiro-cli's own knowledge flag drives its TUI and index builder, not a marotte
 *  chat. Management works either way; the hint only explains that the agent won't
 *  consult these bases during chats while it's off. */
async function refreshHint(signal: AbortSignal): Promise<void> {
  const s = await apiGetTyped("/api/settings", decodeEffectiveSettings, signal);
  if (s === null) {
    // Network, non-2xx, abort or a rejected payload — none of which means
    // "knowledge is off", so leave the hint as it stands.
    return;
  }
  byId<HTMLParagraphElement>("knowledge-hint").hidden = s.knowledge_enabled;
}

// --- Rendering ---

function renderError(): void {
  const container = byId<HTMLDivElement>("knowledge-list");
  container.replaceChildren(
    el("div", { className: "list-empty" }, "Couldn't load knowledge bases."),
  );
}

function renderList(items: KnowledgeContext[], stalled = false): void {
  const container = byId<HTMLDivElement>("knowledge-list");
  // Drop any prior non-keyed placeholder (empty / error) before reconcile.
  for (const child of [...container.children]) {
    if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  if (items.length === 0) {
    container.replaceChildren();
    // Two CONCRETE examples, one of them a named repository (user ruling): the
    // shapes-only wording readers could not tell an entry from. The section hint
    // above already defines what a base IS; the README carries the longer list.
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

/** Rebuild a row's children only when its rendered state changed, so a stable row
 *  keeps its DOM identity (and any focus) across polls. Guard owned by
 *  `paint-sig.ts`. */
function fillRow(row: HTMLElement, c: KnowledgeContext, stalled: boolean): void {
  if (
    !sigChanged(row, [
      c.indexing === true ? "1" : "0",
      String(c.item_count),
      c.items_display ?? "",
      c.path ?? "",
      stalled ? "1" : "0",
    ])
  ) {
    return;
  }
  row.classList.toggle("knowledge-indexing", c.indexing === true);
  row.replaceChildren(...rowChildren(c, stalled));
}

function rowChildren(c: KnowledgeContext, stalled: boolean): HTMLElement[] {
  const name = el("span", { className: "list-row-name" }, c.name);
  if (c.indexing === true) {
    return [name, progressEl(c.items_display, stalled)];
  }
  const count = `${String(c.item_count)} item${c.item_count === 1 ? "" : "s"}`;
  const metaText = c.path !== undefined && c.path !== "" ? `${count} · ${c.path}` : count;
  const meta = el("span", { className: "list-row-meta knowledge-meta" }, metaText);
  return [name, meta, reindexBtn(c.name), removeBtn(c.name)];
}

/** Parse the leading integer percentage from an items_display string
 *  ("42%", "42% · ETA 3s", "0%"); null when there's no percentage
 *  ("Cancelled", "Failed"). */
function parsePct(display: string | undefined): number | null {
  const m = /^(\d+)%/.exec(display ?? "");
  return m ? Math.min(100, Number(m[1])) : null;
}

/** The in-flight indexing readout: a native <progress> plus its text.
 *
 *  <progress> rather than <meter>: this row exists only while `indexing` is true
 *  and moves toward completion, where a meter measures within a static range.
 *  `aria-label` rather than `aria-labelledby`: one row per indexing base, so an
 *  id-based name needs a unique id minted per row. The `pct !== null` guard is
 *  load-bearing — `items_display` can read "Cancelled" or "Failed", where a
 *  valueless <progress> animates a claim of work that is not happening. */
function progressEl(display: string | undefined, stalled: boolean): HTMLElement {
  const wrap = el("span", { className: "knowledge-progress" });
  if (stalled) {
    // No <progress> at all: the bar reports a value nothing is going to advance,
    // and an indeterminate one would claim work the client has stopped watching.
    wrap.appendChild(
      el(
        "span",
        { className: "knowledge-progress-text" },
        "Indexing stalled — reopen this tab to check again",
      ),
    );
    return wrap;
  }
  const pct = parsePct(display);
  if (pct !== null) {
    // `value`/`max` assigned on the typed element rather than passed to `el`,
    // which routes some names to a DOM property and the rest to setAttribute.
    const bar = el("progress", {
      className: "knowledge-bar",
      "aria-label": "Indexing",
    }) as HTMLProgressElement;
    bar.max = 100;
    bar.value = pct;
    wrap.appendChild(bar);
  }
  const text = display !== undefined && display !== "" ? `Indexing… ${display}` : "Indexing…";
  wrap.appendChild(el("span", { className: "knowledge-progress-text" }, text));
  return wrap;
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

/** Rebuild a base's index from its directory as it stands now. No confirm
 *  dialog: the base itself survives and only its index is rebuilt, so there is
 *  nothing to undo. The refetch is what picks up the new indexing row and
 *  restarts the progress poll — kiro-cli's `update` starts the build and returns,
 *  so the operation is already in `show`'s active list by the time we re-read. */
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

// --- Add form (inline, toggled by the + button) ---

function buildAddForm(): HTMLFormElement {
  const pathInput = el("input", {
    type: "text",
    id: "knowledge-add-path",
    className: "tool-form-input",
    placeholder: "Directory path (e.g. docs or /abs/path)…",
    "aria-label": "Knowledge base directory path",
  }) as HTMLInputElement;
  const nameInput = el("input", {
    type: "text",
    id: "knowledge-add-name",
    className: "tool-form-input",
    placeholder: "Name (optional)…",
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
  // `<output>` because this IS a result of the reader's submit: it carries an
  // implicit `status` live region, so a message written into it is announced
  // without an aria-live attribute of its own.
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
  // `.outcome` rather than the dispatch's own resolution: `error: false` keeps
  // this off the toast stack, so the field beside the input is the only surface
  // the server's message can reach, and only the typed outcome carries it.
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

// --- Init (once, at settings init) ---

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

  // There is no knowledge_indexing subscription. The notification fired only for
  // a non-builtin mode's declared bases, whose per-agent store is disjoint from
  // the default store this list reads, so the refetch it triggered could not show
  // what it announced. The poll is the whole progress channel.
}
