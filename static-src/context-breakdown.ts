import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { occurrenceKeys, type PanelItem } from "./turn-info-panel.js";
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

type ItemName = (name: string, uri: string) => Node;

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
 *  beneath it. */
export function breakdownItems(b: ContextBreakdown, itemName: ItemName): PanelItem[] {
  return b.categories.flatMap((c) => categoryItems(c, itemName));
}

function categoryItems(c: ContextCategory, itemName: ItemName): PanelItem[] {
  const label = CATEGORY_LABEL.get(c.key) ?? c.key;
  const figure = `${share(c.percent)} \u00b7 ${count(c.chars)} characters`;
  const out: PanelItem[] = [
    {
      key: join("cat", c.key),
      sig: join(label, figure),
      className: "turn-info-item turn-info-cat",
      build: () => [
        el("span", { className: "turn-info-item-name" }, label),
        el("span", { className: "turn-info-item-share" }, figure),
      ],
    },
  ];
  const parts = c.parts ?? [];
  if (parts.length > 0) {
    const text = parts
      .map((p) => `${PART_LABEL.get(p.key) ?? p.key} ${count(p.chars)}`)
      .join(" \u00b7 ");
    out.push({
      key: join("parts", c.key),
      sig: text,
      className: SUB,
      build: () => [text],
    });
  }
  const items = c.items ?? [];
  const keys = occurrenceKeys(items.map((it) => join(it.name, it.uri ?? "")));
  items.forEach((item, i) => {
    out.push(itemEntry(c.key, keys[i] ?? "", item, itemName));
  });
  const omitted = c.omitted_count ?? 0;
  if (omitted > 0) {
    const text = `${String(omitted)} more, ${count(c.omitted_chars ?? 0)} characters`;
    out.push({
      key: join("more", c.key),
      sig: text,
      className: `${SUB} turn-info-more`,
      build: () => [text],
    });
  }
  return out;
}

const SUB = "turn-info-item turn-info-sub";

function itemEntry(cat: string, key: string, item: ContextItem, itemName: ItemName): PanelItem {
  const badge = item.inclusion ?? "";
  const pct = share(item.percent);
  return {
    key: join("item", cat, key),
    sig: join(item.name, item.uri ?? "", badge, pct),
    className: SUB,
    build: () => {
      const nodes: Node[] = [itemName(item.name, item.uri ?? "")];
      if (badge !== "") {
        nodes.push(el("span", { className: "turn-info-badge" }, badge));
      }
      nodes.push(el("span", { className: "turn-info-item-share" }, pct));
      return nodes;
    },
  };
}
