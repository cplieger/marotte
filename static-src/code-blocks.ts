// Code-block decoration: title bar (language + actions), highlighting, and Run for short shell snippets (a callback,
// to avoid a static cycle with shell.ts). One idempotent path, `decorateBlock`, decorates a still-streaming fence
// provisionally and upgrades it on close. The provisional sweep is the only thing that decorates a fence the model
// never closed: the renderer's per-block callback fires only on close, and `parser_end` closes no open token.

import { el } from "@cplieger/reactive";
import { highlightByLang, normalizeLang } from "./highlight.js";
import { ICON_COPY, ICON_PLAY } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { CHROME_ATTR } from "./chrome-attr.js";

const SHELL_LANGS = new Set(["", "sh", "bash", "zsh", "shell", "console", "terminal"]);

/** On the wrapper so a second pass knows what is left: `streaming` has chrome, `final` adds highlighting. */
const STATE_ATTR = "data-code-state";

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

/** Decorate every code block under `root` as final. Also run when a stream ends, so an unclosed fence is finished. */
export function decorateCodeBlocks(root: HTMLElement): void {
  for (const pre of root.querySelectorAll("pre")) {
    decorateBlock(pre, false);
  }
}

/**
 * Decorate the block currently streaming in, if any: the last `<pre>` (the parser is append-only). A `final`
 * wrapper is left alone, so this is safe after every parse slice.
 */
export function decorateStreamingCodeTail(root: HTMLElement): void {
  const pres = root.querySelectorAll("pre");
  const last = pres[pres.length - 1];
  if (last === undefined) {
    return;
  }
  decorateBlock(last, true);
}

/**
 * `provisional`: chrome only. No highlight (a half-written statement highlights wrong) and no Run (an incomplete
 * command must not reach a shell).
 */
function decorateBlock(pre: HTMLElement, provisional: boolean): void {
  const existing = pre.parentElement;
  const wrapped = existing?.classList.contains("code-wrap") === true ? existing : null;
  if (wrapped?.getAttribute(STATE_ATTR) === "final") {
    return;
  }
  const wrap = wrapped ?? wrapBlock(pre);
  if (provisional) {
    wrap.setAttribute(STATE_ATTR, "streaming");
    return;
  }
  finalizeBlock(wrap, pre);
}

/** Copy reads the text at click time, so one button serves a streaming block and a finished one. */
function wrapBlock(pre: HTMLElement): HTMLElement {
  const wrap = el("div", { className: "code-wrap" });
  pre.parentElement?.insertBefore(wrap, pre);

  const actions = el(
    "div",
    { className: "code-actions" },
    makeCopyButton(() => blockText(pre)),
  );
  const head = el(
    "div",
    { className: "code-head", [CHROME_ATTR]: "" },
    el("span", { className: "code-lang" }, extractLang(pre, pre.querySelector("code"))),
    actions,
  );
  wrap.appendChild(head);
  wrap.appendChild(pre);
  return wrap;
}

function finalizeBlock(wrap: HTMLElement, pre: HTMLElement): void {
  wrap.setAttribute(STATE_ATTR, "final");
  const codeEl = pre.querySelector("code");
  const lang = extractLang(pre, codeEl);
  const text = blockText(pre);

  const label = wrap.querySelector(":scope > .code-head > .code-lang");
  if (label !== null) {
    label.textContent = lang;
  }

  // Unknown languages keep the already-escaped text; innerHTML is swapped only when highlighting succeeds.
  const hlLang = normalizeLang(lang);
  if (codeEl !== null && hlLang !== "") {
    codeEl.innerHTML = highlightByLang(text, hlLang);
  }

  const actions = wrap.querySelector(":scope > .code-head > .code-actions");
  if (actions !== null && isRunnableShell(lang, text)) {
    actions.appendChild(makeRunButton(text));
  }
}

function blockText(pre: HTMLElement): string {
  const codeEl = pre.querySelector("code");
  return (codeEl ?? pre).textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
}

/** The fence tag as written, lowercased: the `<pre>`'s class, else the `<code>`'s `language-*` (what smd-renderer writes). */
export function extractLang(pre: HTMLElement, code: HTMLElement | null): string {
  const preMatch = /(?:^|\s)code\s+(\S+)/.exec(pre.className);
  if (preMatch?.[1] !== undefined && preMatch[1] !== "") {
    return preMatch[1].toLowerCase();
  }
  if (code !== null) {
    const codeMatch = /language-(\S+)/.exec(code.className);
    if (codeMatch?.[1] !== undefined) {
      return codeMatch[1].toLowerCase();
    }
  }
  return "";
}

/**
 * Whether a finished fence earns Run: one command per click, so any `\n` or `\r` terminator refuses it
 * (a terminator test rather than a line count, which ignores blank lines and a trailing newline).
 */
export function isRunnableShell(lang: string, text: string): boolean {
  if (!SHELL_LANGS.has(lang)) {
    return false;
  }
  const trimmed = text.trim();
  if (trimmed === "") {
    return false;
  }
  if (trimmed.startsWith("#!")) {
    return false;
  }
  return !/[\r\n]/.test(trimmed);
}

function makeCopyButton(getText: () => string): HTMLButtonElement {
  const btn = el(
    "button",
    { className: "code-act-btn", "data-tooltip": "Copy", "aria-label": "Copy" },
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

function makeRunButton(text: string): HTMLButtonElement {
  const btn = el("button", { className: "code-act-btn" }, iconEl(ICON_PLAY)) as HTMLButtonElement;
  if (shellRunCb === null) {
    btn.setAttribute("data-tooltip", "Shell not available");
    btn.setAttribute("aria-label", "Shell not available");
    btn.disabled = true;
  } else {
    btn.setAttribute("data-tooltip", "Run in shell");
    btn.setAttribute("aria-label", "Run in shell");
    btn.addEventListener("click", () => {
      shellRunCb?.(text.trim());
    });
  }
  return btn;
}
