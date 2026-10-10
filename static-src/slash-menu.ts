// The composer's completion listbox, in two modes. `/` at the start lists the catalog the server
// keeps from KAS's `available_commands_update` (already narrowed to what KAS resolves before the
// model: saved prompts, steering docs, /goal) beside marotte's own verbs.

import { effect, el, signal, untracked } from "@cplieger/reactive";
import { ask } from "@cplieger/ui-primitives/ask";
import { apiGetTyped } from "./api-client.js";
import { onBus, onSSE, BUS_RECONCILE } from "./bus.js";
import { $ } from "./dom.js";
import { setComposerValue } from "./composer-value.js";
import { hashQuery, matchProviders, mentionToken, replaceSpan } from "./context-mentions.js";
import type { HashQuery, MentionProvider } from "./context-mentions.js";
import { itemsFor, resetMentionSources } from "./context-sources.js";
import type { MentionItem } from "./context-sources.js";
import { join as joinKey } from "@cplieger/keyenc";
import { getActiveId, isThinking, watchActiveId } from "./store.js";
import { chatNotice, subjectName } from "./notice-subject.js";
import { MAROTTE_COMMANDS } from "./typed-commands.js";
import { decodeSlashCommandsResponse } from "./wire/decoders.gen.js";
import type { SlashCommand, SlashCommandKind } from "./wire/types.gen.js";

/** One menu row. `kind` is `marotte` for a verb typed-commands.ts handles. */
interface MenuEntry {
  readonly name: string;
  readonly description: string;
  readonly kind: SlashCommandKind | "marotte";
  readonly hint: string;
}

/** Why a server-resolved row is greyed while a turn runs. */
export const BUSY_REASON = "Runs as a new turn, available when the agent is idle";

const catalog = signal<readonly SlashCommand[]>([]);

/** The upload picker, injected by the composition root: files-picker reaches chat.ts, which
 *  reaches this module through submit.ts, so importing it here is a cycle. */
let openFilePicker: () => void = () => undefined;

// False until a read returns a catalog KAS actually sent; `invokesCatalogCommand` fails closed
// while it is, because an unknown name might be a saved prompt a steer would leak.
let catalogReady = false;

/** Refetch the catalog. A failed read keeps the last good one. */
function loadSlashCatalog(): void {
  void apiGetTyped("/api/slash-commands", decodeSlashCommandsResponse).then((d) => {
    if (d !== null) {
      catalog.value = d.commands;
      catalogReady = d.ready;
    }
  });
}

/** The menu's rows: marotte's verbs first, then the catalog with any entry that shares a verb's
 *  name hidden, because typed-commands.ts claims that text first. */
export function menuEntries(commands: readonly SlashCommand[]): MenuEntry[] {
  const verbs = new Set(MAROTTE_COMMANDS.map((c) => c.name));
  const out: MenuEntry[] = MAROTTE_COMMANDS.map((c) => ({
    name: c.name,
    description: c.description,
    kind: "marotte",
    hint: "",
  }));
  for (const c of commands) {
    if (!verbs.has(c.name.toLowerCase())) {
      out.push({ name: c.name, description: c.description, kind: c.kind, hint: c.hint ?? "" });
    }
  }
  return out;
}

/** The query when the composer holds only a slash and a partial name, else null. */
export function slashQuery(text: string, caret: number): string | null {
  if (caret !== text.length) {
    return null;
  }
  const m = /^\/(\S*)$/.exec(text);
  return m === null ? null : (m[1] ?? "");
}

/** Prefix matches first, then substring matches, each in input order. */
export function matchEntries(query: string, entries: readonly MenuEntry[]): MenuEntry[] {
  const q = query.toLowerCase();
  const prefix: MenuEntry[] = [];
  const inner: MenuEntry[] = [];
  for (const e of entries) {
    const n = e.name.toLowerCase();
    if (n.startsWith(q)) {
      prefix.push(e);
    } else if (n.includes(q)) {
      inner.push(e);
    }
  }
  return [...prefix, ...inner];
}

/** A server-resolved row is a prompt-path command, so a steer cannot carry it. */
export function entryEnabled(entry: MenuEntry, thinking: boolean): boolean {
  return entry.kind === "marotte" || !thinking;
}

/** Whether `text` invokes a catalog command, which submit.ts holds mid-turn. Before a catalog
 *  has loaded, any command-shaped name that is not a marotte verb counts, so a saved prompt is
 *  never steered as prose. */
export function invokesCatalogCommand(text: string): boolean {
  const m = /^\s*\/(\S+)/.exec(text);
  if (m === null) {
    return false;
  }
  const name = (m[1] ?? "").toLowerCase();
  if (!catalogReady) {
    // A name with a `/` in it is a path, never a command.
    return !name.includes("/") && !MAROTTE_COMMANDS.some((c) => c.name === name);
  }
  return menuEntries(catalog.peek()).some(
    (e) => e.kind !== "marotte" && e.name.toLowerCase() === name,
  );
}

interface Row {
  readonly name: string;
  readonly hint: string;
  readonly desc: string;
  readonly enabled: boolean;
  readonly pick: () => void;
  readonly ref?: true;
}

let shown: Row[] = [];
let selected = 0;

function menuEl(): HTMLUListElement {
  return $.slashMenu;
}

function isOpen(): boolean {
  return !menuEl().hidden;
}

function close(): void {
  clearTimeout(searchTimer);
  menuSession++;
  const menu = menuEl();
  if (menu.hidden) {
    return;
  }
  menu.hidden = true;
  menu.replaceChildren();
  shown = [];
  items.clear();
  resetMentionSources();
  $.promptInput.removeAttribute("aria-activedescendant");
}

function thinkingNow(): boolean {
  const id = getActiveId();
  return id !== "" && isThinking(id);
}

function slashRows(query: string): Row[] {
  const thinking = thinkingNow();
  return matchEntries(query, menuEntries(catalog.value)).map((e) => {
    const enabled = entryEnabled(e, thinking);
    return {
      name: `/${e.name}`,
      hint: e.hint,
      desc: enabled ? e.description : BUSY_REASON,
      enabled,
      pick: () => {
        close();
        setComposerValue(`/${e.name} `);
      },
    };
  });
}

type ItemProvider = Exclude<MentionProvider, "attach">;

// null is a read that failed; it stays until the reader picks the retry row.
const items = new Map<string, MentionItem[] | null>();
const SEARCH_DEBOUNCE_MS = 150;
let searchTimer: ReturnType<typeof setTimeout> | undefined;
// Bumped by every close, so a read started for an earlier menu neither fills this menu's cache nor
// reopens a menu the reader closed.
let menuSession = 0;
// An MCP list belongs to one chat's pool at one moment, so its key carries both and a moved pool or
// chat misses the cache.
let mcpGen = 0;

function itemsKey(provider: ItemProvider, query: string): string {
  return provider === "mcp"
    ? joinKey(provider, getActiveId(), String(mcpGen), query)
    : joinKey(provider, query);
}

/** Re-render an open `#mcp:` list, which re-reads it under its new key. */
function refreshMcpRows(): void {
  if (isOpen() && currentHash()?.provider === "mcp") {
    render();
  }
}

function requestItems(provider: ItemProvider, query: string): void {
  const key = itemsKey(provider, query);
  const asked = menuSession;
  const run = (): void => {
    void itemsFor(provider, query).then((list) => {
      if (asked !== menuSession) {
        return;
      }
      items.set(key, list);
      const h = currentHash();
      if (h?.provider === provider && h.query === query) {
        render();
      }
    });
  };
  clearTimeout(searchTimer);
  if (provider === "file" || provider === "folder") {
    searchTimer = setTimeout(run, SEARCH_DEBOUNCE_MS);
  } else {
    run();
  }
}

function currentHash(): HashQuery | null {
  const input = $.promptInput;
  return hashQuery(input.value, input.selectionStart);
}

function writeOverSpan(h: HashQuery, insert: string): void {
  const input = $.promptInput;
  const next = replaceSpan(input.value, h.start, input.selectionStart, insert);
  setComposerValue(next.value);
  input.setSelectionRange(next.caret, next.caret);
}

function statusRow(desc: string): Row {
  return { name: "", hint: "", desc, enabled: false, pick: () => undefined };
}

function retryRow(key: string): Row {
  return {
    name: "Retry",
    hint: "",
    desc: "Couldn't load the list",
    enabled: true,
    pick: () => {
      items.delete(key);
      render();
    },
  };
}

function providerRows(h: HashQuery): Row[] {
  return matchProviders(h.head).map((p) => ({
    name: p.label,
    hint: "",
    desc: p.desc,
    enabled: true,
    pick: () => {
      if (p.id === "attach") {
        close();
        writeOverSpan(h, "");
        openFilePicker();
        return;
      }
      writeOverSpan(h, `#${p.id}:`);
      selected = 0;
      render();
    },
  }));
}

function itemRows(h: HashQuery, provider: ItemProvider): Row[] {
  const list = items.get(itemsKey(provider, h.query));
  if (list === undefined) {
    requestItems(provider, h.query);
    return [statusRow("Searching…")];
  }
  if (list === null) {
    return [retryRow(itemsKey(provider, h.query))];
  }
  if (list.length === 0) {
    const searching = provider === "file" && h.query === "";
    return [statusRow(searching ? "Type part of a file name" : "No matches")];
  }
  return list.map((it) => ({
    name: it.label,
    hint: it.hint,
    desc: "",
    enabled: true,
    ref: true,
    pick: () => {
      close();
      if (it.template === undefined) {
        const token = mentionToken(provider, it.query);
        if (token === null) {
          chatNotice(getActiveId(), UNREPRESENTABLE, "error");
          return;
        }
        writeOverSpan(h, `${token} `);
        return;
      }
      void fillTemplateRow(h, it.template);
    },
  }));
}

const UNREPRESENTABLE =
  "That address contains a ] or a line break, so it cannot be added as a context reference.";

/** Ask for each template variable in turn; Cancel at any step inserts nothing. */
async function fillTemplateRow(
  h: HashQuery,
  t: NonNullable<MentionItem["template"]>,
): Promise<void> {
  const input = $.promptInput;
  const chatID = getActiveId();
  const name = subjectName(chatID);
  const span = input.value.slice(h.start, input.selectionStart);
  const values: Record<string, string> = {};
  const vars = t.parsed.vars;
  for (const [i, v] of vars.entries()) {
    const answer = await ask(`${t.info.name}: ${v}`, {
      title: "MCP resource",
      confirmLabel: i === vars.length - 1 ? "Insert" : "Next",
      input: { placeholder: v },
    });
    if (answer === null) {
      input.focus();
      return;
    }
    values[v] = answer;
  }
  input.focus();
  const filled = mentionToken("mcp", `${t.server}:${t.parsed.expand(values)}`);
  if (filled === null) {
    chatNotice(chatID, UNREPRESENTABLE, "error", name);
    return;
  }
  const token = `${filled} `;
  // The dialog let the reader type meanwhile; replace the span only if it is still where it was,
  // else insert at the caret.
  if (input.value.slice(h.start, h.start + span.length) === span) {
    input.setSelectionRange(h.start + span.length, h.start + span.length);
    writeOverSpan(h, token);
  } else {
    const at = input.selectionStart;
    writeOverSpan({ ...h, start: at }, token);
  }
}

function hashRows(h: HashQuery): Row[] {
  return h.provider === null || h.provider === "attach" ? providerRows(h) : itemRows(h, h.provider);
}

function render(): void {
  const input = $.promptInput;
  const query = slashQuery(input.value, input.selectionStart);
  const hash = query === null ? currentHash() : null;
  let rows: Row[] = [];
  if (query !== null) {
    rows = slashRows(query);
  } else if (hash !== null) {
    rows = hashRows(hash);
  }
  if (rows.length === 0) {
    close();
    return;
  }
  shown = rows;
  selected = Math.min(selected, rows.length - 1);
  const menu = menuEl();
  menu.setAttribute("aria-label", query !== null ? "Commands" : "Context");
  menu.replaceChildren(
    ...rows.map((r, i) => {
      const li = el(
        "li",
        {
          id: `slash-opt-${String(i)}`,
          className: "slash-opt",
          role: "option",
          "aria-selected": i === selected ? "true" : "false",
        },
        r.name === ""
          ? ""
          : el(
              "span",
              { className: r.ref ? "slash-opt-name slash-opt-ref" : "slash-opt-name" },
              r.name,
            ),
        r.hint === "" ? "" : el("span", { className: "slash-opt-hint" }, r.hint),
        r.desc === "" ? "" : el("span", { className: "slash-opt-desc" }, r.desc),
      );
      if (!r.enabled) {
        li.setAttribute("aria-disabled", "true");
      }
      li.addEventListener("mousedown", (ev) => {
        // Keep the caret in the textarea.
        ev.preventDefault();
      });
      li.addEventListener("click", () => {
        choose(i);
      });
      return li;
    }),
  );
  menu.hidden = false;
  input.setAttribute("aria-activedescendant", `slash-opt-${String(selected)}`);
}

function choose(i: number): void {
  const r = shown[i];
  if (r?.enabled === true) {
    r.pick();
  }
}

function onKeydown(ev: KeyboardEvent): void {
  if (!isOpen() || ev.isComposing) {
    return;
  }
  switch (ev.key) {
    case "ArrowDown":
    case "ArrowUp": {
      const step = ev.key === "ArrowDown" ? 1 : -1;
      selected = (selected + step + shown.length) % shown.length;
      render();
      break;
    }
    case "Enter":
    case "Tab":
      if (ev.shiftKey) {
        return;
      }
      choose(selected);
      break;
    case "Escape":
      close();
      break;
    default:
      return;
  }
  // Ahead of prompt-input's own keydown, which would send or walk history.
  ev.preventDefault();
  ev.stopImmediatePropagation();
}

/** Wire the menu to the composer and keep the catalog fresh. `openPicker` is what the Attach
 *  file provider row opens. */
export function initSlashMenu(openPicker: () => void): void {
  openFilePicker = openPicker;
  const input = $.promptInput;
  input.setAttribute("aria-controls", "slash-menu");
  // Capture phase so this runs before prompt-input's listener on the same target.
  input.addEventListener("keydown", onKeydown, { capture: true });
  input.addEventListener("input", () => {
    selected = 0;
    render();
  });
  input.addEventListener("blur", close);
  onSSE("mcp_pool_changed", () => {
    mcpGen++;
    refreshMcpRows();
  });
  effect(() => {
    watchActiveId();
    untracked(refreshMcpRows);
  });
  onSSE("slash_commands_changed", loadSlashCatalog);
  onBus(BUS_RECONCILE, loadSlashCatalog);
  loadSlashCatalog();
}

// deadset:ignore DS1004 -- test seam: seeds the slash command catalog without a fetch
export function _setCatalogForTest(commands: readonly SlashCommand[]): void {
  catalog.value = commands;
  catalogReady = true;
}

// deadset:ignore DS1004 -- test seam: resets the slash command catalog
export function _resetCatalogForTest(): void {
  catalog.value = [];
  catalogReady = false;
}
