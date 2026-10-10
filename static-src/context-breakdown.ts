import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import type { ContextBreakdown, ContextCategory, ContextItem } from "./wire/types.gen.js";

/** KAS's category keys in words; an unknown key is shown as sent. */
const CATEGORY_LABEL: ReadonlyMap<string, string> = new Map([
  ["kiroInstructions", "Kiro instructions"],
  ["toolSpecs", "Tool definitions"],
  ["mcpTools", "MCP tools"],
  ["agentsIndex", "Agents index"],
  ["skillsIndex", "Skills index"],
  ["powersIndex", "Powers index"],
  ["steering", "Steering"],
  ["skills", "Skills"],
  ["toolIO", "Tool input and output"],
  ["history", "Conversation"],
  ["workspace", "Workspace"],
  ["memory", "Memory"],
  ["attachedFiles", "Attached files"],
  ["other", "Other"],
]);

/** KAS's sub-total names (the `*Chars` members) in words; an unknown one is shown as sent. */
const PART_LABEL: ReadonlyMap<string, string> = new Map([
  ["user", "your messages"],
  ["assistant", "replies"],
  ["thinking", "thinking"],
  ["compactionSummary", "compaction summary"],
  ["input", "input"],
  ["output", "output"],
  ["fileTree", "file tree"],
  ["openFiles", "open files"],
  ["environment", "environment"],
  ["repositories", "repositories"],
  ["learnings", "learnings"],
  ["knowledge", "knowledge"],
]);

type InfoRow = (label: string, value: string | Node) => HTMLElement;

type ItemName = (name: string, uri: string) => Node;

const count = (n: number): string => Math.round(n).toLocaleString();
const share = (pct: number): string => `${String(Math.round(pct))}%`;

/** The breakdown's rows: the request's size first, then one row per category, largest first. */
export function contextBreakdownRows(
  b: ContextBreakdown,
  row: InfoRow,
  itemName: ItemName,
): HTMLElement[] {
  return [
    row("Last request", requestLine(b)),
    ...b.categories.map((c) => categoryRow(c, row, itemName)),
  ];
}

function requestLine(b: ContextBreakdown): string {
  const calls = b.model_calls > 1 ? ` over ${String(b.model_calls)} model calls` : "";
  const bits = [`${count(b.total_chars)} characters${calls}`];
  if (b.compacted === true) {
    bits.push("compacted");
  }
  const media = b.media;
  if (media !== undefined && media.images > 0) {
    bits.push(media.images === 1 ? "1 image" : `${String(media.images)} images`);
  }
  if (media !== undefined && media.documents > 0) {
    bits.push(media.documents === 1 ? "1 document" : `${String(media.documents)} documents`);
  }
  return bits.join(" \u00b7 ");
}

function categoryRow(c: ContextCategory, row: InfoRow, itemName: ItemName): HTMLElement {
  const label = CATEGORY_LABEL.get(c.key) ?? c.key;
  const figure = `${share(c.percent)} \u00b7 ${count(c.chars)} characters`;
  const items = c.items ?? [];
  const parts = c.parts ?? [];
  if (items.length === 0 && parts.length === 0) {
    return row(label, figure);
  }
  const body = el("ul", { className: "ctx-breakdown-items" });
  if (parts.length > 0) {
    body.appendChild(
      el(
        "li",
        { className: "ctx-breakdown-parts" },
        parts.map((p) => `${PART_LABEL.get(p.key) ?? p.key} ${count(p.chars)}`).join(" \u00b7 "),
      ),
    );
  }
  for (const item of items) {
    body.appendChild(itemRow(item, itemName));
  }
  if ((c.omitted_count ?? 0) > 0) {
    body.appendChild(
      el(
        "li",
        { className: "ctx-breakdown-more" },
        `${String(c.omitted_count ?? 0)} more, ${count(c.omitted_chars ?? 0)} characters`,
      ),
    );
  }
  const disclosure = el(
    "details",
    { className: "ctx-breakdown-category" },
    el("summary", { className: "ctx-breakdown-summary" }, chevronEl(), el("span", {}, figure)),
    body,
  );
  return row(label, disclosure);
}

function itemRow(item: ContextItem, itemName: ItemName): HTMLElement {
  const li = el("li", { className: "ctx-breakdown-item" }, itemName(item.name, item.uri ?? ""));
  if ((item.inclusion ?? "") !== "") {
    li.appendChild(el("span", { className: "ctx-breakdown-badge" }, item.inclusion ?? ""));
  }
  li.appendChild(el("span", { className: "ctx-breakdown-share" }, share(item.percent)));
  return li;
}
