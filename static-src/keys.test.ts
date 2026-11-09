// The keydown handler's gates: the modifier gate (a bare key never reaches the table), the two-sided Shift match,
// the text-entry carve-outs (only Ctrl+Enter and Ctrl+K/N survive a focused field, Ctrl+K not in the shell), and
// Escape (closes the top modal and stops). keys.ts listens once per module instance, so it is initialised once and
// stubs are cleared per test.

import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";

const { closeTop } = vi.hoisted(() => ({ closeTop: vi.fn(() => false) }));
vi.mock("./modals.js", () => ({ closeTopModal: () => closeTop() }));

import { initKeyboardShortcuts, registeredShortcuts } from "./keys.js";
import { trackShellPresses } from "./shell-keys.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";

const actions = {
  newChat: vi.fn(),
  toggleShell: vi.fn(),
  toggleFiles: vi.fn(),
  toggleGit: vi.fn(),
  toggleSettings: vi.fn(),
  sendMessage: vi.fn(),
  showShortcuts: vi.fn(),
};

function press(key: string, target?: Element, extra: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...extra });
  (target ?? document.body).dispatchEvent(e);
  return e;
}

function composer(): HTMLElement {
  return document.getElementById("prompt-input") as HTMLElement;
}

beforeAll(() => {
  initKeyboardShortcuts(actions);
});

beforeEach(() => {
  closeTop.mockReturnValue(false);
  for (const fn of Object.values(actions)) {
    fn.mockClear();
  }
  // The shell panel stays for every test: trackShellPresses' document listener, once installed,
  // reads #shell-terminal at each pointerdown for the rest of the file.
  document.body.innerHTML = `
    <textarea id="prompt-input"></textarea>
    <input id="a-text-field" type="text">
    <svg id="an-icon"></svg>
    <div id="shell-panel"><div id="shell-terminal"><textarea class="term-input"></textarea></div></div>`;
});

afterEach(() => {
  document.body.innerHTML = "";
});

describe("Escape", () => {
  it("closes the topmost modal and goes no further", () => {
    closeTop.mockReturnValue(true);
    let deselects = 0;
    const off = onBus(BUS_KEYS_ESCAPE, () => {
      deselects++;
    });
    try {
      const e = press("Escape");
      // The dialog consumed the key, so the page must not see it.
      expect(e.defaultPrevented).toBe(true);
      // Deselecting behind a dialog being dismissed loses a selection the user never touched.
      expect(deselects).toBe(0);
    } finally {
      off();
    }
  });

  it("clears the file browser selection when there is no modal to close", () => {
    closeTop.mockReturnValue(false);
    let deselects = 0;
    const off = onBus(BUS_KEYS_ESCAPE, () => {
      deselects++;
    });
    try {
      const e = press("Escape");
      expect(deselects).toBe(1);
      // Nothing was consumed, so Escape keeps the page's meaning (leaving fullscreen, cancelling an IME composition).
      expect(e.defaultPrevented).toBe(false);
    } finally {
      off();
    }
  });
});

describe("the modifier gate", () => {
  it("ignores a registered key pressed with no modifier at all", () => {
    // Every table row needs Ctrl or Cmd; without the gate, `k` in the page body would open a new chat.
    press("k");
    press("/");
    press(",");
    expect(actions.newChat).not.toHaveBeenCalled();
    expect(actions.toggleShell).not.toHaveBeenCalled();
    expect(actions.toggleSettings).not.toHaveBeenCalled();
  });

  it("prevents the default of a chord it handled", () => {
    // Ctrl+K is the browser's search-bar focus on some platforms; the app claims it.
    const e = press("k", undefined, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("the shift match", () => {
  it("does not fire a no-shift chord while Shift is held", () => {
    // Shift chords belong to the browser; matching them would make Ctrl+Shift+K mean Ctrl+K.
    press("k", undefined, { ctrlKey: true, shiftKey: true });
    press(",", undefined, { ctrlKey: true, shiftKey: true });
    expect(actions.newChat).not.toHaveBeenCalled();
    expect(actions.toggleSettings).not.toHaveBeenCalled();
  });
});

describe("a focused text field", () => {
  it("sends on Ctrl+Enter from the composer", () => {
    const e = press("Enter", composer(), { ctrlKey: true });
    expect(actions.sendMessage).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("leaves every other chord to the field it was typed in", () => {
    // Ctrl+comma while typing belongs to the textarea and the browser.
    const e = press(",", composer(), { ctrlKey: true });
    expect(actions.toggleSettings).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps Ctrl+K working from a textarea", () => {
    const e = press("k", composer(), { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("keeps Ctrl+N working from a text input", () => {
    const field = document.getElementById("a-text-field") as HTMLElement;
    const e = press("n", field, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("treats a target that is not an HTML element as no field at all", () => {
    // An inline SVG in a button is a keydown target with no isContentEditable; reading the missing property as truthy
    // would suppress every bare-key binding.
    const icon = document.getElementById("an-icon") as unknown as Element;
    press("?", icon);
    expect(actions.showShortcuts).toHaveBeenCalledTimes(1);
  });
});

// web-terminal-ui docs/input.md: with a hardware keyboard on an iPad, a press in the terminal leaves
// focus on the body and the terminal types the keys the body gets while the last press was inside it.
describe("a key the shell's terminal takes from the body", () => {
  it("is left to the terminal after a press inside it, and handled after a press elsewhere", () => {
    const terminal = document.getElementById("shell-terminal") as HTMLElement;
    trackShellPresses();
    (document.activeElement as HTMLElement | null)?.blur();

    terminal.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    const typed = press("?");
    const chord = press("/", undefined, { ctrlKey: true });
    expect(actions.showShortcuts, "? after a press in the terminal").not.toHaveBeenCalled();
    expect(typed.defaultPrevented).toBe(false);
    expect(actions.toggleShell, "Ctrl+/ after a press in the terminal").not.toHaveBeenCalled();
    expect(chord.defaultPrevented).toBe(false);

    document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    press("?");
    press("/", undefined, { ctrlKey: true });
    expect(actions.showShortcuts, "? after a press outside it").toHaveBeenCalledTimes(1);
    expect(actions.toggleShell, "Ctrl+/ after a press outside it").toHaveBeenCalledTimes(1);
  });
});

// bash and emacs kill to end of line on Ctrl+K and nano cuts the line; a terminal never gets a
// Cmd chord, so Cmd+K stays the app's on macOS.
describe("the new-chat chords in the shell", () => {
  function terminalInput(): HTMLElement {
    return document.querySelector("#shell-terminal .term-input") as HTMLElement;
  }

  it("leaves Ctrl+K to the terminal's focused input", () => {
    const e = press("k", terminalInput(), { ctrlKey: true });
    expect(actions.newChat).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
  });

  it("leaves Ctrl+K to the terminal when it takes keys from the body", () => {
    trackShellPresses();
    (document.activeElement as HTMLElement | null)?.blur();
    terminalInput().dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    try {
      const e = press("k", undefined, { ctrlKey: true });
      expect(actions.newChat).not.toHaveBeenCalled();
      expect(e.defaultPrevented).toBe(false);
    } finally {
      // The last press is module state; a press elsewhere hands the body's keys back to the page.
      document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    }
  });

  it("opens a chat on Ctrl+N from the shell", () => {
    const e = press("n", terminalInput(), { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("opens a chat on Cmd+K from the shell", () => {
    const e = press("k", terminalInput(), { metaKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });

  it("still opens a chat on Ctrl+K from a field outside the shell", () => {
    const field = document.getElementById("a-text-field") as HTMLElement;
    const e = press("k", field, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("the init guard", () => {
  it("registers one listener and one table however often it is initialised", () => {
    // A second listener on the shared document would fire every chord twice (two new chats).
    const second = {
      newChat: vi.fn(),
      toggleShell: vi.fn(),
      toggleFiles: vi.fn(),
      toggleGit: vi.fn(),
      toggleSettings: vi.fn(),
      sendMessage: vi.fn(),
      showShortcuts: vi.fn(),
    };
    initKeyboardShortcuts(second);

    expect(registeredShortcuts()).toHaveLength(7);
    press("k", undefined, { ctrlKey: true });
    expect(actions.newChat).toHaveBeenCalledTimes(1);
    expect(second.newChat).not.toHaveBeenCalled();
  });
});
