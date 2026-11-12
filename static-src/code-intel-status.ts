// Settings > General > Code intelligence: a status row, not a switch. marotte always sends kiro-cli's
// codeIntelligence on (kascap setting.codeIntelligence), so installing a language server is the control.

import { byId } from "./dom.js";
import { onToolInventory, refreshToolInventory, showLanguageServers } from "./tools.js";
import type { Inventory } from "./types.js";

/** The language each bundled language server serves, keyed by its tool name. kascap
 *  TestBundledLanguageServersAreKiroServers holds every bundled server to a row here and one in
 *  kiro-cli's server table, reading this literal: keep one `["<tool>", "<language>"]` pair per line. */
const LANGUAGE_OF: ReadonlyMap<string, string> = new Map([
  ["clangd", "C/C++"],
  ["gopls", "Go"],
  ["jdtls", "Java"],
  ["kotlin-language-server", "Kotlin"],
  ["pyright", "Python"],
  ["rust-analyzer", "Rust"],
  ["typescript-language-server", "TypeScript/JavaScript"],
]);

const LIST = new Intl.ListFormat("en", { style: "long", type: "conjunction" });

interface CodeIntelStatus {
  text: string;
  action: "Install" | "Manage";
}

/** The row for one inventory read; null is a failed read. A server counts once it is installed and
 *  switched on; an unnamed one reads as its tool name. */
function codeIntelStatus(inv: Inventory | null): CodeIntelStatus {
  if (inv === null) {
    return {
      text: "Could not read the installed tools. Settings > Tools says why.",
      action: "Manage",
    };
  }
  const languages = new Set<string>();
  for (const t of inv.tools) {
    if (t.lsp === true && t.installed && t.disabled !== true) {
      languages.add(LANGUAGE_OF.get(t.name) ?? t.name);
    }
  }
  if (languages.size === 0) {
    return { text: "Off. Install a language server to turn it on.", action: "Install" };
  }
  const names = [...languages].sort((a, b) => a.localeCompare(b));
  return { text: `On for ${LIST.format(names)}.`, action: "Manage" };
}

function paint(s: CodeIntelStatus): void {
  byId("code-intel-status").textContent = s.text;
  const btn = byId<HTMLButtonElement>("code-intel-action");
  btn.textContent = s.action;
  btn.setAttribute(
    "aria-label",
    s.action === "Install" ? "Install a language server" : "Manage language servers",
  );
}

/** Wire the row once; it repaints on every inventory read the Tools tab makes. */
export function initCodeIntelStatus(): void {
  byId<HTMLButtonElement>("code-intel-action").addEventListener("click", () => {
    void showLanguageServers();
  });
  onToolInventory((inv) => {
    paint(codeIntelStatus(inv));
  });
}

/** The General panel's read: the row has no data of its own until an inventory read lands. */
export function loadCodeIntelStatus(): void {
  refreshToolInventory();
}
