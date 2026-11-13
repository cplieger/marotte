import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { occurrenceKeys, type ItemPart, type PanelItem } from "./turn-info-panel.js";
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

/** An item's name as a list item part: the caller decides whether it links. */
type ItemName = (name: string, uri: string) => ItemPart;

const count = (n: number): string => Math.round(n).toLocaleString();
const share = (pct: number): string => `${String(Math.round(pct))}%`;

/** The request's size, the Context section's "Last request". */
export function requestLine(b: ContextBreakdown): string {
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

/** The Breakdown list: every category, largest first, with its parts, items and the rest indented
 *  beneath it. A category's identity is its key plus its occurrence: the wire states no uniqueness. */
export function breakdownItems(b: ContextBreakdown, itemName: ItemName): PanelItem[] {
  const ids = occurrenceKeys(b.categories.map((c) => c.key));
  return b.categories.flatMap((c, i) => categoryItems(c, ids[i] ?? c.key, itemName));
}

function categoryItems(c: ContextCategory, id: string, itemName: ItemName): PanelItem[] {
  const label = CATEGORY_LABEL.get(c.key) ?? c.key;
  const figure = `${share(c.percent)} \u00b7 ${count(c.chars)} characters`;
  const out: PanelItem[] = [
    {
      key: join("cat", id),
      className: "turn-info-item",
      parts: [textPart("name", label), textPart("share", figure, "turn-info-item-share")],
    },
  ];
  const parts = c.parts ?? [];
  if (parts.length > 0) {
    const text = parts
      .map((p) => `${PART_LABEL.get(p.key) ?? p.key} ${count(p.chars)}`)
      .join(" \u00b7 ");
    out.push({ key: join("parts", id), className: SUB, parts: [textPart("text", text)] });
  }
  const items = c.items ?? [];
  const keys = occurrenceKeys(items.map((it) => join(it.name, it.uri ?? "")));
  items.forEach((item, i) => {
    out.push(itemEntry(id, keys[i] ?? "", item, itemName));
  });
  const omitted = c.omitted_count ?? 0;
  if (omitted > 0) {
    const text = `${String(omitted)} more, ${count(c.omitted_chars ?? 0)} characters`;
    out.push({
      key: join("more", id),
      className: `${SUB} turn-info-more`,
      parts: [textPart("text", text)],
    });
  }
  return out;
}

const SUB = "turn-info-item turn-info-sub";

function textPart(key: string, text: string, className = ""): ItemPart {
  return { key, sig: text, build: () => el("span", className === "" ? {} : { className }, text) };
}

function itemEntry(cat: string, key: string, item: ContextItem, itemName: ItemName): PanelItem {
  const parts = [itemName(item.name, item.uri ?? "")];
  const badge = item.inclusion ?? "";
  if (badge !== "") {
    parts.push(textPart("badge", badge, "turn-info-badge"));
  }
  parts.push(textPart("share", share(item.percent), "turn-info-item-share"));
  return { key: join("item", cat, key), className: SUB, parts };
}
