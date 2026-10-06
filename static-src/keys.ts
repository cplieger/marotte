// One keydown handler on document. Escape and `?` carry no modifier, so they run before the Ctrl/Cmd gate; every
// table row needs Ctrl or Cmd.

import { emitBus, BUS_KEYS_ESCAPE } from "./bus.js";
import { closeTopModal } from "./modals.js";

interface ShortcutDef {
  key: string;
  shift?: boolean;
  action: () => void;
  /** In the reference sheet's words. */
  description: string;
  /** The sheet is generated from this table, so a binding declared here needs no second edit to appear on it. */
  group: string;
}

/** One registered chord, as the reference sheet reads it. No action: the sheet describes bindings. */
export interface ShortcutBinding {
  readonly key: string;
  readonly shift: boolean;
  readonly description: string;
  readonly group: string;
}

const shortcuts: ShortcutDef[] = [];
let initialized = false;

function register(def: ShortcutDef): void {
  shortcuts.push(def);
}

/**
 * The Ctrl/Cmd chords this module answers to, so the reference sheet is generated from the registry. Empty until
 * initKeyboardShortcuts has run.
 */
export function registeredShortcuts(): readonly ShortcutBinding[] {
  return shortcuts.map((s) => ({
    key: s.key,
    shift: s.shift === true,
    description: s.description,
    group: s.group,
  }));
}

function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target.isContentEditable
  );
}

export function initKeyboardShortcuts(actions: {
  newChat: () => void;
  toggleShell: () => void;
  toggleFiles: () => void;
  toggleGit: () => void;
  toggleSettings: () => void;
  sendMessage: () => void;
  showShortcuts: () => void;
}): void {
  if (initialized) {
    return;
  }
  initialized = true;

  register({ key: "k", action: actions.newChat, description: "New conversation", group: "Chats" });
  register({ key: "n", action: actions.newChat, description: "New conversation", group: "Chats" });
  register({ key: "/", action: actions.toggleShell, description: "Toggle shell", group: "Panels" });
  register({
    key: "f",
    shift: true,
    action: actions.toggleFiles,
    description: "Toggle file browser",
    group: "Panels",
  });
  register({
    key: "g",
    shift: true,
    action: actions.toggleGit,
    description: "Toggle git panel",
    group: "Panels",
  });
  register({
    key: ",",
    action: actions.toggleSettings,
    description: "Toggle settings",
    group: "Panels",
  });
  register({
    key: "Enter",
    action: actions.sendMessage,
    description: "Send message",
    group: "Composer",
  });

  document.addEventListener("keydown", (e: KeyboardEvent) => {
    const isInput = isTextEntry(e.target);

    if (e.key === "Escape") {
      // The shared helper runs confirm-dialog cleanup (clone-replaced buttons) on Escape dismissal too.
      if (closeTopModal()) {
        e.preventDefault();
        return;
      }
      // Deselect in the file browser.
      emitBus(BUS_KEYS_ESCAPE);
      return;
    }

    const mod = e.ctrlKey || e.metaKey;

    // `?` is Shift+/ with no Ctrl or Cmd, so it sits above the gate; never while typing. stopImmediatePropagation keeps it
    // from app.ts's focusComposerOnTyping on the same node, which ignores defaultPrevented.
    if (!mod && !e.altKey && e.key === "?" && !isInput) {
      e.preventDefault();
      e.stopImmediatePropagation();
      actions.showShortcuts();
      return;
    }

    if (!mod) {
      return;
    }

    for (const s of shortcuts) {
      if (s.key.toLowerCase() !== e.key.toLowerCase()) {
        continue;
      }
      if (s.shift === true && !e.shiftKey) {
        continue;
      }
      if (s.shift !== true && e.shiftKey) {
        continue;
      }

      // Ctrl+Enter sends from a textarea.
      if (s.key === "Enter" && isInput) {
        e.preventDefault();
        s.action();
        return;
      }

      if (isInput && s.key !== "Enter") {
        // Ctrl+K/N (new chat) work even in inputs.
        if (s.key === "k" || s.key === "n") {
          e.preventDefault();
          s.action();
          return;
        }
        continue;
      }

      e.preventDefault();
      s.action();
      return;
    }
  });
}
