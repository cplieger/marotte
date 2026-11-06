// Code-block decoration: title bar (language + actions), highlighting, and Run for shell blocks (a callback,
// to avoid a static cycle with shell.ts). One idempotent path, `decorateBlock`, decorates a still-streaming fence
// provisionally and upgrades it on close. The provisional sweep is the only thing that decorates a fence the model
// never closed: the renderer's per-block callback fires only on close, and `parser_end` closes no open token.

import { el } from "@cplieger/ui";
import { highlightByLang, normalizeLang } from "./highlight.js";
import { ICON_COPY, ICON_PLAY } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { confirm } from "./confirm.js";

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
 * wrapper is left alone, so this is safe after every parse slice. Returns that `<pre>` while it is still
 * provisional, else null.
 */
export function decorateStreamingCodeTail(root: HTMLElement): HTMLElement | null {
  const pres = root.querySelectorAll("pre");
  const last = pres[pres.length - 1];
  if (last === undefined) {
    return null;
  }
  return decorateBlock(last, true) ? last : null;
}

/**
 * `provisional`: chrome only. No highlight (a half-written statement highlights wrong) and no Run (an incomplete
 * command must not reach a shell). False when the block was already final.
 */
function decorateBlock(pre: HTMLElement, provisional: boolean): boolean {
  const existing = pre.parentElement;
  const wrapped = existing?.classList.contains("code-wrap") === true ? existing : null;
  if (wrapped?.getAttribute(STATE_ATTR) === "final") {
    return false;
  }
  const wrap = wrapped ?? wrapBlock(pre);
  if (provisional) {
    wrap.setAttribute(STATE_ATTR, "streaming");
    return true;
  }
  finalizeBlock(wrap, pre);
  return true;
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
    actions.appendChild(makeRunButton(pre, text));
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

/** Whether a finished fence earns Run: a non-empty shell block that is not a `#!` script, of any line count. */
export function isRunnableShell(lang: string, text: string): boolean {
  if (!SHELL_LANGS.has(lang)) {
    return false;
  }
  const trimmed = text.trim();
  if (trimmed === "") {
    return false;
  }
  return !trimmed.startsWith("#!");
}

/** Copy reads `getText` at click time; `className` is the button family of the row it joins. */
export function makeCopyButton(
  getText: () => string,
  className = "code-act-btn",
): HTMLButtonElement {
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

function makeRunButton(pre: HTMLElement, text: string): HTMLButtonElement {
  const btn = el("button", { className: "code-act-btn" }, iconEl(ICON_PLAY)) as HTMLButtonElement;
  const cmd = text.trim().replaceAll("\r\n", "\n");
  const refusal = runRefusal(cmd);
  if (refusal !== null) {
    btn.setAttribute("data-tooltip", refusal);
    btn.setAttribute("aria-label", refusal);
    btn.disabled = true;
  } else {
    btn.setAttribute("data-tooltip", "Run in shell");
    btn.setAttribute("aria-label", "Run in shell");
    btn.addEventListener("click", () => {
      void runSeen(pre, cmd);
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
 * A block of several lines, or a line wider than `pre`'s box, runs only after the dialog shows all of it: a
 * one-line click is consent to what it shows, a block's is not.
 */
async function runSeen(pre: HTMLElement, cmd: string): Promise<void> {
  const lines = cmd.includes("\n");
  if (
    (lines || pre.scrollWidth > pre.clientWidth) &&
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
