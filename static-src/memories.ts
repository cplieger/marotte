// kiro-cli's own store: the list is refetched after every mutation, and addressed by id since titles repeat.

import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { apiGetTypedOrError } from "./api-client.js";
import { asObject, decodeArray, optStr, reqNum, reqStr } from "./validators.js";
import { KEY_ATTR, reconcile } from "./reconcile.js";
import { entryDetail, entryList, entryRow } from "./entry-row.js";
import { iconEl } from "./icon-el.js";
import { ICON_EDIT, ICON_TRASH } from "./icons.js";
import { confirm as confirmDialog } from "./confirm.js";
import { deleteMemory, updateMemory } from "./actions/memory.js";
import type { MemoryEdit } from "./actions/memory.js";
import { loadSettings } from "./persist.js";
import { forceReflow } from "./dom.js";

interface MemoryRecord {
  readonly id: string;
  readonly memoryType: string;
  readonly title: string;
  readonly summary: string;
  readonly content: string | undefined;
  readonly updatedMs: number | undefined;
  readonly scope: string;
}

interface MemoryList {
  readonly memories: MemoryRecord[];
  readonly cap: number;
}

type LoadState =
  | { readonly kind: "loading" }
  | { readonly kind: "ready" }
  | { readonly kind: "not_enabled"; readonly message: string }
  | { readonly kind: "failed" };

/** How many memories exist and how many the filter shows, for the page's note. */
export interface MemoryCounts {
  total: number;
  shown: number;
}

const UNSCOPED = "Unscoped";

/** KAS sends epoch seconds, epoch milliseconds or an ISO string depending on the field; anything else is no time. */
function timeMs(v: unknown): number | undefined {
  if (typeof v === "number" && Number.isFinite(v) && v > 0) {
    return v < 1e12 ? v * 1000 : v;
  }
  if (typeof v === "string") {
    const ms = Date.parse(v);
    return Number.isNaN(ms) ? undefined : ms;
  }
  return undefined;
}

function decodeRecord(v: unknown): MemoryRecord {
  const o = asObject(v);
  const scopes = Array.isArray(o["scopes"])
    ? (o["scopes"] as unknown[]).filter((s): s is string => typeof s === "string" && s !== "")
    : [];
  return {
    id: reqStr(o, "id"),
    memoryType: optStr(o, "memory_type") ?? "",
    title: optStr(o, "title") ?? "",
    summary: optStr(o, "summary") ?? "",
    content: optStr(o, "content"),
    updatedMs: timeMs(o["updated_at"]) ?? timeMs(o["created_at"]),
    scope: scopes[0] ?? UNSCOPED,
  };
}

function decodeMemoryList(v: unknown): MemoryList {
  const o = asObject(v);
  return { memories: decodeArray(o["memories"], decodeRecord), cap: reqNum(o, "cap") };
}

function decodeMemoryOne(v: unknown): MemoryRecord {
  return decodeRecord(asObject(v)["memory"]);
}

let memories: MemoryRecord[] = [];
let cap = 1000;
let state: LoadState = { kind: "loading" };
let memoryOff = false;
let filterText = "";
let container: HTMLElement | null = null;
/** Pruned to the ids the last list still holds. */
const selected = new Set<string>();
let openID: string | null = null;
let editing = false;
/** Editing waits for `ready`, so a failed fetch never opens an editor over an empty body. */
type Detail =
  | { readonly kind: "loading" }
  | { readonly kind: "failed" }
  | { readonly kind: "ready"; readonly content: string };
const details = new Map<string, Detail>();
let onCounts: ((c: MemoryCounts) => void) | null = null;
/** Bumped per fetch, so a stale answer cannot overwrite a newer one. */
let generation = 0;

export function setMemoryCountsListener(fn: (c: MemoryCounts) => void): void {
  onCounts = fn;
}

function haystack(m: MemoryRecord): string {
  return [m.title, m.summary, m.memoryType, m.scope].join("\n").toLowerCase();
}

function visible(): MemoryRecord[] {
  return filterText === "" ? memories : memories.filter((m) => haystack(m).includes(filterText));
}

/** Render into the panel. An unchanged filter (a tab switch or refresh) refetches; a filter change only repaints. */
export function renderMemoriesPanel(panel: HTMLElement, filter = ""): void {
  container = panel;
  const next = filter.trim().toLowerCase();
  const filterMoved = next !== filterText;
  filterText = next;
  paint();
  if (!filterMoved || state.kind === "loading") {
    void refresh();
  }
}

async function refresh(): Promise<void> {
  const gen = ++generation;
  const [res, settings] = await Promise.all([
    apiGetTypedOrError("/api/memory", decodeMemoryList),
    loadSettings(),
  ]);
  if (gen !== generation) {
    return;
  }
  if (settings !== null) {
    memoryOff = settings.memory_mode === "off";
  }
  if (res.ok && res.data !== null) {
    memories = res.data.memories;
    cap = res.data.cap;
    state = { kind: "ready" };
    const ids = new Set(memories.map((m) => m.id));
    for (const id of [...selected]) {
      if (!ids.has(id)) {
        selected.delete(id);
      }
    }
    if (openID !== null && !ids.has(openID)) {
      openID = null;
      editing = false;
    }
  } else if (res.status === 409) {
    state = { kind: "not_enabled", message: notEnabledText(res.body, res.error) };
  } else {
    state = { kind: "failed" };
  }
  paint();
}

function notEnabledText(body: unknown, error: string): string {
  const o = typeof body === "object" && body !== null ? (body as Record<string, unknown>) : {};
  return o["code"] === "not_enabled" && error !== ""
    ? error
    : "Memory is not available for this account.";
}

function paint(): void {
  if (container === null) {
    return;
  }
  const rows = visible();
  onCounts?.({ total: memories.length, shown: rows.length });
  const head = headStrip();
  if (state.kind !== "ready") {
    container.replaceChildren(head, statusBody());
    return;
  }
  if (rows.length === 0) {
    container.replaceChildren(
      head,
      el(
        "div",
        { className: "list-empty" },
        memories.length === 0 ? "No memories yet." : "No memories match the filter.",
      ),
    );
    return;
  }
  // The reconcile reads keyed sections only.
  for (const child of [...container.children]) {
    if (child.getAttribute(KEY_ATTR) === null) {
      child.remove();
    }
  }
  reconcile(container, sections(rows), {
    key: (s: Section) => joinKey("scope", s.scope),
    mount: (s: Section) => {
      const node = el(
        "div",
        { className: "docs-section" },
        el("div", { className: "entry-section-label" }, s.scope),
        entryList(),
      );
      fillSection(node, s);
      return node;
    },
    update: fillSection,
  });
  container.prepend(head);
  const detail = container.querySelector<HTMLElement>(".entry-detail:not(.open)");
  if (detail !== null) {
    forceReflow(detail);
    detail.classList.add("open");
  }
}

function statusBody(): HTMLElement {
  switch (state.kind) {
    case "loading":
      return el("div", { className: "list-empty" }, "Loading memories…");
    case "not_enabled":
      return el("div", { className: "list-empty" }, state.message);
    case "failed": {
      const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
      retry.addEventListener("click", () => {
        state = { kind: "loading" };
        paint();
        void refresh();
      });
      return el(
        "div",
        { className: "list-empty memories-failed" },
        "Couldn't load memories. ",
        retry,
      );
    }
    case "ready":
      return el("div");
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

/** Rebuilt each paint: it holds no reader state. */
function headStrip(): HTMLElement {
  const count = el(
    "span",
    { className: "memories-count" },
    `${memories.length.toLocaleString("en-US")} of ${cap.toLocaleString("en-US")}`,
  );
  const ready = state.kind === "ready";
  const delSel = el(
    "button",
    {
      type: "button",
      className: "btn-small memories-delete-selected",
      disabled: !ready || selected.size === 0,
    },
    selected.size === 0 ? "Delete selected" : `Delete selected (${String(selected.size)})`,
  );
  delSel.addEventListener("click", () => {
    void deleteMany([...selected]);
  });
  const delAll = el(
    "button",
    {
      type: "button",
      className: "btn-small memories-delete-all",
      disabled: !ready || memories.length === 0,
    },
    "Delete all",
  );
  delAll.addEventListener("click", () => {
    void deleteMany(
      memories.map((m) => m.id),
      true,
    );
  });
  return el(
    "div",
    { className: "memories-head" },
    el(
      "div",
      { className: "memories-head-row" },
      count,
      el("span", { className: "memories-head-actions" }, delSel, delAll),
    ),
    memoryOff
      ? el(
          "p",
          { className: "memories-off-note" },
          "Memory is off, so new chats neither read nor save these. Chats you already had keep the access they started with, except background learning, which stops when you reopen them. A chat from before this update stops using memory when you reopen it. Turn memory on in Settings > General.",
        )
      : null,
  );
}

interface Section {
  readonly scope: string;
  readonly rows: MemoryRecord[];
}

/** Scopes alphabetically with Unscoped last; newest first inside a scope. */
function sections(rows: MemoryRecord[]): Section[] {
  const by = new Map<string, MemoryRecord[]>();
  for (const m of rows) {
    const list = by.get(m.scope) ?? [];
    list.push(m);
    by.set(m.scope, list);
  }
  const scopes = [...by.keys()].sort((a, b) =>
    a === UNSCOPED ? 1 : b === UNSCOPED ? -1 : a.localeCompare(b),
  );
  return scopes.map((scope) => ({
    scope,
    rows: (by.get(scope) ?? []).sort((a, b) => (b.updatedMs ?? 0) - (a.updatedMs ?? 0)),
  }));
}

type ListEntry =
  | { readonly kind: "row"; readonly m: MemoryRecord }
  | { readonly kind: "detail"; readonly m: MemoryRecord };

function fillSection(node: HTMLElement, s: Section): void {
  const list = node.querySelector<HTMLElement>(".list-container");
  if (list === null) {
    return;
  }
  const entries: ListEntry[] = [];
  for (const m of s.rows) {
    entries.push({ kind: "row", m });
    if (m.id === openID) {
      entries.push({ kind: "detail", m });
    }
  }
  reconcile(list, entries, {
    key: entryKey,
    mount: (e: ListEntry) => (e.kind === "row" ? buildRow(e.m) : buildDetail(e.m)),
    update: (node: HTMLElement, e: ListEntry) => {
      if (e.kind === "row") {
        syncRow(node, e.m);
      }
    },
  });
}

/**
 * The key carries what a node renders, so a changed record or a view/edit switch remounts and an unchanged one keeps
 * its focus.
 */
function entryKey(e: ListEntry): string {
  const m = e.m;
  return e.kind === "row"
    ? joinKey("row", m.id, m.title, m.summary, m.memoryType, String(m.updatedMs ?? ""))
    : joinKey("detail", m.id, String(editing), details.get(m.id)?.kind ?? "loading");
}

function syncRow(row: HTMLElement, m: MemoryRecord): void {
  const box = row.querySelector<HTMLInputElement>(".memories-select");
  if (box !== null) {
    box.checked = selected.has(m.id);
  }
  row.querySelector(".entry-open")?.setAttribute("aria-expanded", String(m.id === openID));
}

function displayTitle(m: MemoryRecord): string {
  return m.title === "" ? "(untitled)" : m.title;
}

function buildRow(m: MemoryRecord): HTMLElement {
  const box = el("input", {
    type: "checkbox",
    className: "memories-select",
    "aria-label": `Select ${displayTitle(m)}`,
  }) as HTMLInputElement;
  box.checked = selected.has(m.id);
  box.addEventListener("change", () => {
    if (box.checked) {
      selected.add(m.id);
    } else {
      selected.delete(m.id);
    }
    syncHead();
  });
  const edit = el(
    "button",
    {
      type: "button",
      className: "icon-btn list-row-btn",
      "aria-label": `Edit ${displayTitle(m)}`,
      "data-tooltip": "Edit",
    },
    iconEl(ICON_EDIT),
  );
  edit.addEventListener("click", () => {
    openDetail(m.id, true);
  });
  const del = el(
    "button",
    {
      type: "button",
      className: "icon-btn list-row-btn entry-delete",
      "aria-label": `Delete ${displayTitle(m)}`,
      "data-tooltip": "Delete",
    },
    iconEl(ICON_TRASH),
  );
  del.addEventListener("click", () => {
    void deleteOne(m);
  });
  const row = entryRow({
    key: m.id,
    title: displayTitle(m),
    lead: el("span", { className: "memories-lead" }, box),
    badges:
      m.memoryType === "" ? undefined : [el("span", { className: "docs-badge" }, m.memoryType)],
    time: m.updatedMs === undefined ? undefined : { ms: m.updatedMs },
    sub: { kind: "clamp", text: m.summary },
    actions: [edit, del],
    open: {
      name: displayTitle(m),
      onOpen: () => {
        openDetail(m.id, false);
      },
    },
    data: { "data-memory-id": m.id },
  });
  syncRow(row, m);
  return row;
}

/** Repaints the head alone, so ticking a box keeps focus on it. */
function syncHead(): void {
  const old = container?.querySelector(":scope > .memories-head");
  old?.replaceWith(headStrip());
}

function openDetail(id: string, edit: boolean): void {
  if (openID === id && editing === edit) {
    openID = null;
    editing = false;
  } else {
    openID = id;
    editing = edit;
    if (!details.has(id)) {
      void loadContent(id);
    }
  }
  paint();
}

async function loadContent(id: string): Promise<void> {
  details.set(id, { kind: "loading" });
  const res = await apiGetTypedOrError(`/api/memory/${encodeURIComponent(id)}`, decodeMemoryOne);
  if (details.get(id)?.kind !== "loading") {
    return; // Deleted or re-requested meanwhile.
  }
  details.set(
    id,
    res.ok && res.data !== null
      ? { kind: "ready", content: res.data.content ?? "" }
      : { kind: "failed" },
  );
  paint();
}

function buildDetail(m: MemoryRecord): HTMLElement {
  const d = details.get(m.id) ?? { kind: "loading" };
  switch (d.kind) {
    case "loading":
      return entryDetail(el("div", { className: "memories-detail" }, "Loading…"));
    case "failed": {
      const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
      retry.addEventListener("click", () => {
        void loadContent(m.id);
        paint();
      });
      return entryDetail(
        el(
          "div",
          { className: "memories-detail memories-failed" },
          "Couldn't load this memory. ",
          retry,
        ),
      );
    }
    case "ready":
      return entryDetail(editing ? editForm(m, d.content) : viewBody(m, d.content));
    default: {
      const exhaustive: never = d;
      return exhaustive;
    }
  }
}

function viewBody(m: MemoryRecord, content: string): HTMLElement {
  const edit = el("button", { type: "button", className: "btn-small" }, "Edit");
  edit.addEventListener("click", () => {
    openDetail(m.id, true);
  });
  return el(
    "div",
    { className: "memories-detail" },
    el("div", { className: "memories-content" }, content === "" ? m.summary : content),
    el("div", { className: "memories-detail-actions" }, edit),
  );
}

function field(label: string, control: HTMLElement): HTMLElement {
  return el("label", { className: "recipe-input-label memories-field" }, label, control);
}

function editForm(m: MemoryRecord, content: string): HTMLElement {
  const title = el("input", {
    type: "text",
    className: "recipe-input",
    value: m.title,
  }) as HTMLInputElement;
  const summary = el("input", {
    type: "text",
    className: "recipe-input",
    value: m.summary,
  }) as HTMLInputElement;
  const body = el("textarea", { className: "recipe-input memories-textarea", rows: 8 });
  (body as HTMLTextAreaElement).value = content;
  const save = el("button", { type: "submit", className: "btn-small" }, "Save");
  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");
  cancel.addEventListener("click", () => {
    editing = false;
    paint();
  });
  const form = el(
    "form",
    { className: "memories-detail memories-form" },
    field("Title", title),
    field("Summary", summary),
    field("Content", body),
    el("div", { className: "memories-detail-actions" }, save, cancel),
  );
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const edit: MemoryEdit = {};
    if (title.value !== m.title) {
      edit.title = title.value;
    }
    if (summary.value !== m.summary) {
      edit.summary = summary.value;
    }
    const nextContent = (body as HTMLTextAreaElement).value;
    if (nextContent !== content) {
      edit.content = nextContent;
    }
    void saveEdit(m.id, edit);
  });
  return form;
}

async function saveEdit(id: string, edit: MemoryEdit): Promise<void> {
  if (Object.keys(edit).length === 0) {
    editing = false;
    paint();
    return;
  }
  const res = await updateMemory.dispatch({ id, edit });
  if (res === null) {
    return; // The action reported it; the form keeps what was typed.
  }
  details.delete(id);
  editing = false;
  await refresh();
  if (openID === id) {
    void loadContent(id);
  }
}

async function deleteOne(m: MemoryRecord): Promise<void> {
  const ok = await confirmDialog(
    `Delete the memory "${displayTitle(m)}"? This cannot be undone.`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return;
  }
  const res = await deleteMemory.dispatch({ id: m.id });
  if (res === null) {
    return;
  }
  selected.delete(m.id);
  details.delete(m.id);
  await refresh();
}

/** One request each; a failure stops the run and the refetch shows what is left. */
async function deleteMany(ids: string[], all = false): Promise<void> {
  if (ids.length === 0) {
    return;
  }
  const n = ids.length.toLocaleString("en-US");
  const noun = ids.length === 1 ? "memory" : "memories";
  const ok = await confirmDialog(
    all
      ? `Delete all ${n} ${noun}? The agent will forget everything it saved. This cannot be undone.`
      : `Delete ${n} selected ${noun}? This cannot be undone.`,
    all ? "Delete all" : "Delete",
    "destructive",
  );
  if (!ok) {
    return;
  }
  for (const id of ids) {
    const res = await deleteMemory.dispatch({ id });
    if (res === null) {
      break;
    }
    selected.delete(id);
    details.delete(id);
  }
  await refresh();
}

/** Test seam: forget module state between cases. */
export function _resetMemoriesForTest(): void {
  memories = [];
  cap = 1000;
  state = { kind: "loading" };
  memoryOff = false;
  filterText = "";
  container = null;
  selected.clear();
  openID = null;
  editing = false;
  details.clear();
  onCounts = null;
  generation++;
}
