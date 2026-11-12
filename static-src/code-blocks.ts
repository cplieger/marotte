// What marotte adds to @cplieger/ui/markdown's code block: Run for a shell fence (a callback, to avoid a static
// cycle with shell.ts), the closed state the streaming dot reads, and the bar's button family. The library builds
// the block, its label and its Copy.

import { el } from "@cplieger/reactive";
import { detectLanguage } from "@cplieger/ui/highlight";
import { ICON_COPY, ICON_PLAY } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { confirm } from "./confirm.js";

const SHELL_LANGS = new Set(["sh", "bash", "zsh", "shell", "console", "terminal"]);

/** `final` once the block's `code` hook ran; a block without it is still streaming (13-messages.css's dot). */
export const CODE_STATE_ATTR = "data-code-state";

type ShellRunCb = (cmd: string) => void;
let shellRunCb: ShellRunCb | null = null;

/** Wire the shell panel's run handler; called once at startup by the shell module. */
export function setShellRunCallback(cb: ShellRunCb): void {
  shellRunCb = cb;
}

type CopyCb = (text: string) => void;
let copyCb: CopyCb | null = null;

/** Wire the clipboard copy handler; a callback to avoid a dynamic import cycle. */
export function setCopyCallback(cb: CopyCb): void {
  copyCb = cb;
}

/** The fence tag of a `.cp-code` block as the library classed its `code`, lowercased; "" with none. */
export function codeLang(block: Element): string {
  const code = block.querySelector(":scope > pre > code");
  return /(?:^|\s)language-(\S+)/.exec(code?.className ?? "")?.[1]?.toLowerCase() ?? "";
}

/** The bar's actions slot of a `.cp-code` block. */
export function codeActions(block: Element): Element | null {
  return block.querySelector(':scope > [data-part="bar"] > [data-part="actions"]');
}

/** A button in the code bar's family (the library's Copy): quiet, small, its label in a tooltip. */
export function barButton(label: string, icon: string): HTMLButtonElement {
  return el(
    "button",
    {
      type: "button",
      className: "cp-button",
      "data-variant": "quiet",
      "data-size": "sm",
      "data-tooltip": label,
      "aria-label": label,
    },
    iconEl(icon),
  ) as HTMLButtonElement;
}

/** Mark the block closed and give a shell fence Run. Its text is final: the hook runs only at the close or the end. */
export function finishCodeBlock(block: HTMLElement, lang: string, text: string): void {
  block.setAttribute(CODE_STATE_ATTR, "final");
  if (isRunnableShell(lang, text)) {
    codeActions(block)?.append(makeRunButton(block, text));
  }
}

/**
 * Whether a finished fence earns Run: a non-empty shell block that is not a `#!` script, of any line count. An
 * untagged fence counts as shell unless it reads as another language, the one the library's bar then names.
 */
export function isRunnableShell(lang: string, text: string): boolean {
  const shell = lang === "" ? ["", "sh"].includes(detectLanguage(text)) : SHELL_LANGS.has(lang);
  if (!shell) {
    return false;
  }
  const trimmed = text.trim();
  if (trimmed === "") {
    return false;
  }
  return !trimmed.startsWith("#!");
}

/** Copy reads `getText` at click time; `className` is the button family of the row it joins. */
export function makeCopyButton(getText: () => string, className: string): HTMLButtonElement {
  const btn = el(
    "button",
    { className, "data-tooltip": "Copy", "aria-label": "Copy" },
    iconEl(ICON_COPY),
  ) as HTMLButtonElement;
  let timer: ReturnType<typeof setTimeout> | undefined;
  btn.addEventListener("click", () => {
    if (copyCb !== null) {
      copyCb(getText());
      btn.textContent = "✓";
      clearTimeout(timer);
      timer = setTimeout(() => {
        btn.replaceChildren(iconEl(ICON_COPY));
      }, 1500);
    }
  });
  return btn;
}

/**
 * Code points that do not show as themselves in a code block: controls (a tab renders as spaces), format
 * characters (bidi overrides and isolates, zero-width), private-use, lone surrogates, line and paragraph
 * separators, every space but U+0020, and the default-ignorables (variation selectors, tags, fillers).
 */
const HIDDEN_RUNE =
  /[\p{Cc}\p{Cf}\p{Co}\p{Cs}\p{Zl}\p{Zp}\p{Default_Ignorable_Code_Point}]|[^\P{Zs} ]/u;

function makeRunButton(block: HTMLElement, text: string): HTMLButtonElement {
  const cmd = text.trim().replaceAll("\r\n", "\n");
  const refusal = runRefusal(cmd);
  const btn = barButton(refusal ?? "Run in shell", ICON_PLAY);
  if (refusal !== null) {
    btn.disabled = true;
  } else {
    btn.addEventListener("click", () => {
      void runSeen(block, cmd);
    });
  }
  return btn;
}

/** A line feed is the one control a block may hold: it shows as the line break it sends. */
function runRefusal(cmd: string): string | null {
  // A space, not "": deleting LFs joins lone surrogates on adjacent lines into a pair \p{Cs} misses.
  if (HIDDEN_RUNE.test(cmd.replaceAll("\n", " "))) {
    return "Contains hidden characters. Copy it to inspect.";
  }
  return shellRunCb === null ? "Shell not available" : null;
}

/**
 * A block of several lines, or a line wider than its `pre`'s box, runs only after the dialog shows all of it: a
 * one-line click is consent to what it shows, a block's is not.
 */
async function runSeen(block: HTMLElement, cmd: string): Promise<void> {
  const lines = cmd.includes("\n");
  const pre = block.querySelector(":scope > pre");
  const clipped = pre !== null && pre.scrollWidth > pre.clientWidth;
  if (
    (lines || clipped) &&
    !(await confirm({
      title: lines ? "Run these lines in the shell?" : "Run this command?",
      body: lines ? cmd : `Part of it was out of view:\n\n${cmd}`,
      confirmLabel: "Run",
    }))
  ) {
    return;
  }
  shellRunCb?.(cmd);
}
