// The turn footer's open panel: titled sections of rows on one grid (`29-turns.css`), reconciled by
// key so a live repaint keeps every node, the lists the reader opened and focus.

import { el, reconcile } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { chevronEl } from "./chevron.js";
import { iconEl } from "./icon-el.js";
import { ICON_ARROW_RIGHT } from "./icons.js";

/** One entry of a disclosure's list. `sig` covers everything `build` renders: an entry keeps its
 *  node while its key and sig hold, and is rebuilt in place when the sig moves. */
export interface PanelItem {
  key: string;
  sig: string;
  className: string;
  build: () => (Node | string)[];
}

/** A plain value, or a wall-clock instant rendered as a `<time>`. */
export type PanelValue = string | { stamp: number };

export type PanelRow =
  | { kind: "fact"; key: string; label: string; value: PanelValue }
  | { kind: "list"; label: string; value: string; items: PanelItem[] }
  | { kind: "file"; path: string; added: number; removed: number; isNew: boolean; open: () => void }
  | { kind: "review"; count: number; open: () => void };

export interface PanelSection {
  title: string;
  /** Extra class on the section's row list. */
  listClass?: string;
  rows: PanelRow[];
}

/** Per panel, the labels of the disclosures the reader opened. A row that leaves and comes back
 *  reopens as the reader left it. */
const openLists = new WeakMap<HTMLElement, Set<string>>();
const sigs = new WeakMap<Element, string>();
let listSeq = 0;

/** Paint `sections` into `panel`, withholding a section with no row. `itemEpoch` is folded into
 *  every item's sig, for a fact outside the item data that changes how items render. */
export function renderPanel(
  panel: HTMLElement,
  sections: readonly PanelSection[],
  itemEpoch: string,
): void {
  let open = openLists.get(panel);
  if (open === undefined) {
    open = new Set();
    openLists.set(panel, open);
  }
  const ctx: Ctx = { open, itemEpoch };
  reconcile(
    panel,
    sections.filter((s) => s.rows.length > 0),
    {
      key: (s) => s.title,
      mount: (s) => {
        const section = el(
          "section",
          { className: "turn-info-section" },
          el("h4", { className: "turn-info-title" }, s.title),
          el("ul", { className: `turn-info-rows ${s.listClass ?? ""}`.trim() }),
        );
        fillRows(section, s, ctx);
        return section;
      },
      update: (section, s) => {
        fillRows(section, s, ctx);
      },
    },
  );
}

interface Ctx {
  open: Set<string>;
  itemEpoch: string;
}

function fillRows(section: HTMLElement, s: PanelSection, ctx: Ctx): void {
  const list = section.querySelector(":scope > .turn-info-rows");
  if (list === null) {
    return;
  }
  reconcile(list, s.rows, {
    key: rowKey,
    mount: (r) => {
      const li = mountRow(r, ctx);
      updateRow(li, r, ctx);
      return li;
    },
    update: (li, r) => {
      updateRow(li, r, ctx);
    },
  });
}

function rowKey(r: PanelRow): string {
  switch (r.kind) {
    case "fact":
      return join("fact", r.key);
    case "list":
      return join("list", r.label);
    case "file":
      return join("file", r.path);
    case "review":
      return "review";
  }
}

function mountRow(r: PanelRow, ctx: Ctx): HTMLElement {
  switch (r.kind) {
    case "fact":
      return el(
        "li",
        { className: "turn-info-row" },
        el("span", { className: "turn-info-label" }),
        el("span", { className: "turn-info-value" }),
      );
    case "list":
      return mountDisclosure(r.label, ctx);
    case "file":
      return mountFile(r);
    case "review":
      return mountReview(r);
  }
}

function updateRow(li: HTMLElement, r: PanelRow, ctx: Ctx): void {
  switch (r.kind) {
    case "fact":
      setText(li.querySelector(":scope > .turn-info-label"), r.label);
      writeValue(li.querySelector(":scope > .turn-info-value"), r.value);
      return;
    case "list":
      updateDisclosure(li, r, ctx);
      return;
    case "file":
      updateFile(li, r);
      return;
    case "review":
      setText(
        li.querySelector(".turn-review-label"),
        `Review all changes (${String(r.count)} files)`,
      );
      return;
  }
}

function setText(node: Element | null, text: string): void {
  if (node !== null && node.textContent !== text) {
    node.textContent = text;
  }
}

function writeValue(span: Element | null, v: PanelValue): void {
  if (span === null) {
    return;
  }
  if (typeof v === "string") {
    if (span.firstElementChild !== null || span.textContent !== v) {
      span.textContent = v;
    }
    return;
  }
  // The same pair, from the same value, as `turn-header.ts` writes, so "Started" and the header's
  // timestamp cannot disagree.
  const when = new Date(v.stamp);
  let t = span.querySelector("time");
  if (t === null) {
    t = el("time", {}) as HTMLTimeElement;
    span.replaceChildren(t);
  }
  setText(t, when.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }));
  t.dateTime = when.toISOString();
}

/** A full-row button that opens the list beneath it. The list is `hidden="until-found"` so
 *  find-in-page can open it: Chrome and Firefox scroll to the match, Safari opens the list without
 *  scrolling to it (WebKit bug 304174), and every engine opens it from the button. */
function mountDisclosure(label: string, ctx: Ctx): HTMLElement {
  listSeq += 1;
  const id = `turn-info-list-${String(listSeq)}`;
  const btn = el(
    "button",
    { className: "turn-info-dbtn", type: "button", "aria-controls": id },
    el("span", { className: "turn-info-twist" }, chevronEl()),
    el("span", { className: "turn-info-label" }),
    el("span", { className: "turn-info-value" }),
  );
  const list = el("ul", { className: "turn-info-list", id });
  const li = el("li", { className: "turn-info-row turn-info-drow" }, btn, list);
  btn.addEventListener("click", () => {
    if (!ctx.open.delete(label)) {
      ctx.open.add(label);
    }
    syncDisclosure(li, ctx.open.has(label));
  });
  // Find-in-page drops `hidden` itself; recording it keeps the next repaint from re-closing it.
  list.addEventListener("beforematch", () => {
    ctx.open.add(label);
    syncDisclosure(li, true);
  });
  return li;
}

function updateDisclosure(li: HTMLElement, r: Extract<PanelRow, { kind: "list" }>, ctx: Ctx): void {
  setText(li.querySelector(".turn-info-dbtn > .turn-info-label"), r.label);
  setText(li.querySelector(".turn-info-dbtn > .turn-info-value"), r.value);
  const list = li.querySelector(":scope > .turn-info-list");
  if (list !== null) {
    reconcile(list, r.items, {
      key: (it) => it.key,
      mount: (it) => {
        const item = el("li", {});
        paintItem(item, it, ctx.itemEpoch);
        return item;
      },
      update: (item, it) => {
        paintItem(item, it, ctx.itemEpoch);
      },
    });
  }
  syncDisclosure(li, ctx.open.has(r.label));
}

function paintItem(item: HTMLElement, it: PanelItem, epoch: string): void {
  const sig = join(it.sig, it.className, epoch);
  if (sigs.get(item) === sig) {
    return;
  }
  sigs.set(item, sig);
  item.replaceChildren(...it.build());
  item.className = it.className;
  // A document link's target is its whole row (the expander in `29-turns.css`).
  item.classList.toggle("turn-info-doc", item.querySelector(".turn-info-link") !== null);
}

function syncDisclosure(li: HTMLElement, open: boolean): void {
  const btn = li.querySelector(":scope > .turn-info-dbtn");
  const list = li.querySelector(":scope > .turn-info-list");
  btn?.setAttribute("aria-expanded", open ? "true" : "false");
  if (open) {
    list?.removeAttribute("hidden");
  } else if (list?.getAttribute("hidden") !== "until-found") {
    list?.setAttribute("hidden", "until-found");
  }
}

/** One changed file: `path`, a `new` badge, `+N −M`. The path is the row's ink and what the hover
 *  text is about, so the tooltip anchors to it rather than to the row's middle. */
function mountFile(r: Extract<PanelRow, { kind: "file" }>): HTMLElement {
  // `data-tooltip`, not `title`: the app's styled tooltip, never the UA's.
  const btn = el("button", {
    className: "turn-file-row",
    type: "button",
    "data-tooltip": `Open the diff for ${r.path}`,
  });
  btn.addEventListener("click", r.open);
  return el("li", { className: "turn-ledger-file" }, btn);
}

function updateFile(li: HTMLElement, r: Extract<PanelRow, { kind: "file" }>): void {
  const btn = li.querySelector(":scope > .turn-file-row");
  const sig = join(String(r.added), String(r.removed), r.isNew ? "1" : "");
  if (btn === null || sigs.get(btn) === sig) {
    return;
  }
  sigs.set(btn, sig);
  const parts: Node[] = [
    el("span", { className: "turn-file-path", "data-tooltip-anchor": "" }, r.path),
  ];
  if (r.isNew) {
    parts.push(el("span", { className: "turn-file-badge" }, "new"));
  }
  const delta = el("span", { className: "turn-file-delta" });
  if (r.added > 0) {
    delta.appendChild(el("span", { className: "turn-file-add" }, `+${String(r.added)}`));
  }
  if (r.removed > 0) {
    delta.appendChild(el("span", { className: "turn-file-del" }, `\u2212${String(r.removed)}`));
  }
  parts.push(delta);
  btn.replaceChildren(...parts);
}

/** The multi-file seam beneath the file rows, shaped like one of them. */
function mountReview(r: Extract<PanelRow, { kind: "review" }>): HTMLElement {
  const btn = el(
    "button",
    {
      className: "turn-review-all",
      type: "button",
      "data-tooltip": "Review every changed file in the git view",
    },
    el("span", { className: "turn-review-label" }),
    el("span", { className: "turn-review-arrow", "aria-hidden": "true" }, iconEl(ICON_ARROW_RIGHT)),
  );
  btn.addEventListener("click", r.open);
  return el("li", { className: "turn-ledger-file turn-ledger-review" }, btn);
}

/** Keys for a list whose values may repeat: the value, plus its occurrence when it repeats. */
export function occurrenceKeys(values: readonly string[]): string[] {
  const seen = new Map<string, number>();
  return values.map((v) => {
    const n = seen.get(v) ?? 0;
    seen.set(v, n + 1);
    return join(v, String(n));
  });
}
