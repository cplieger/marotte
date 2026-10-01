// ---------------------------------------------------------------------------
// Prompt input: form submit, keydown (Enter, ↑/↓ history), send button, iOS
// viewport fix. send-state.ts drives three button faces: idle sends, streaming
// cancels, error retries; the error face means there is nothing to send TO, so
// every other failure reports through a toast and the turn's own divider.
//
// Nothing here disables the composer: a failed prompt leaves the server idle.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, getActiveId } from "./store.js";
import { payloadOf, turnOpenOf } from "./turns.js";
import { fixIOSViewport } from "./platform.js";
import { ICON_SEND, ICON_CANCEL, ICON_ALERT } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { collapseAll } from "./pill-expand.js";
import { setComposerValue } from "./composer-value.js";
import { signal, effect, touch } from "@cplieger/reactive";

/** The "context (nearly) full" state of the ACTIVE chat, written by
 *  context-ui.ts and rendered here as a placeholder and a tooltip. ADVISORY: it
 *  never refuses a send. Declared here rather than imported from context-ui so
 *  the send-state chain does not pull that module in. */
export const contextFull = signal(false);

/** Placeholder / tooltip shown while `contextFull` is true. Module-local:
 *  only prompt-input.ts renders it. */
const CONTEXT_FULL_REASON =
  "Context nearly full. kiro-cli will compact automatically on the next turn.";

/** Appended to every error tooltip. A fixed suffix rather than a test against
 *  the reason's wording, which is upstream prose. */
const RETRY_HINT = "Send again to retry.";

type Submit = (text: string) => void;
type Cancel = () => void;

export type SendState =
  { kind: "idle" } | { kind: "streaming" } | { kind: "error"; reason: string };

type SendKind = SendState["kind"];

const STATE_ICON: Record<SendKind, string> = {
  idle: ICON_SEND,
  streaming: ICON_CANCEL,
  error: ICON_ALERT,
};

const DEFAULT_TOOLTIP: Record<SendKind, string> = {
  idle: "Send",
  streaming: "Cancel this turn",
  // Unreachable: send-state only builds an error with a reason of its own.
  error: RETRY_HINT,
};

/** The reason a state carries, "" for the states that carry none. Doubles as
 *  the dedupe key: two errors differing only in reason are different states. */
function reasonOf(s: SendState): string {
  return s.kind === "error" ? s.reason : "";
}

let initialized = false;

/** How long after `compositionend` an Enter is still the IME's commit key rather
 *  than a send. Several IMEs deliver that Enter just AFTER composition ended,
 *  with `isComposing` false and no keyCode 229, so no other leg covers it. */
const IME_TAIL_MS = 50;

class PromptInputController {
  // History cycling state
  private idx = -1;
  private draft = "";
  private lastActiveID = "";

  // IME composition state; `imeTimer` holds `composing` true for the tail.
  private composing = false;
  private imeTimer: ReturnType<typeof setTimeout> | undefined;

  // Send-button state
  private state: SendState = { kind: "idle" };
  private onCancel: Cancel = () => undefined;
  private onSubmit: Submit = () => undefined;

  /** Send whatever is in the composer. THE send path: the form's submit handler,
   *  Enter and the keyboard shortcut all call this, and submit.ts decides what a
   *  send means. Neither of the two ways of going through the form works. A
   *  dispatched `new Event("submit")` is not cancelable, so preventDefault() is a
   *  no-op and the browser performs the form's native submission. And
   *  `requestSubmit()` runs constraint validation first, which the decision dock
   *  is inside: a half-typed elicitation field is `:invalid` under the MCP
   *  server's own schema, so the chat message would silently not be sent. */
  sendComposer(): void {
    const text = $.promptInput.value.trim();
    if (text === "") {
      return;
    }
    this.exitCycling();
    this.onSubmit(text);
    setComposerValue("");
  }

  private exitCycling(): void {
    this.idx = -1;
    this.draft = "";
  }

  /** Leave cycling and put the saved draft back in the box. ONE method for both
   *  keys that end cycling (Escape, ArrowDown off the newest prompt), because
   *  exitCycling() zeroes `draft`: read after the exit it is "". */
  private restoreDraft(el: HTMLTextAreaElement): void {
    const saved = this.draft;
    this.exitCycling();
    this.setInputValue(el, saved);
  }

  /** Whether this keystroke belongs to an IME composition, so Enter must reach
   *  the browser and commit the candidate instead of sending. Three legs, each
   *  covering a case the others miss:
   *    - `composing`, the only one that survives an Enter delivered after
   *      composition ended;
   *    - `e.isComposing`, authoritative where the browser sets it, and sometimes
   *      false on exactly the committing Enter;
   *    - keyCode 229, what several Android and Windows IMEs report instead. */
  private isComposing(e: KeyboardEvent): boolean {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- keyCode 229 is the whole reason to port this guard: the IME's commit Enter reports it when isComposing is already false.
    return this.composing || e.isComposing || e.keyCode === 229;
  }

  /** Drop composition state, on blur and on Escape. Some Android IMEs never
   *  deliver compositionend when the field loses focus mid-candidate, which
   *  would leave Enter dead for the rest of the page's life. */
  private resetIME(): void {
    clearTimeout(this.imeTimer);
    this.imeTimer = undefined;
    this.composing = false;
  }

  /** What the reader typed into THIS composer, newest first: each turn's prompt
   *  plus their own steers into it, so a steer whose `origin` is `agent` is a
   *  workflow's report rather than typed text. A turn's steers come after its
   *  prompt, so walking backwards keeps the list in position order. */
  private userPrompts(): string[] {
    const s = getActive();
    if (s === undefined) {
      return [];
    }
    const out: string[] = [];
    for (let i = s.turn_order.length - 1; i >= 0; i--) {
      const turnID = s.turn_order[i];
      const t = turnID === undefined ? undefined : s.turns.get(turnID);
      if (t === undefined) {
        continue;
      }
      for (let j = t.entries.length - 1; j >= 0; j--) {
        const e = t.entries[j];
        const steer = e === undefined ? undefined : payloadOf(e, "steer");
        if (steer?.origin === "user" && steer.text !== "") {
          out.push(steer.text);
        }
      }
      const text = turnOpenOf(t)?.prompt?.text ?? "";
      if (text !== "") {
        out.push(text);
      }
    }
    return out;
  }

  private cursorOnFirstLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) {
      return false;
    }
    return !el.value.slice(0, pos).includes("\n");
  }

  private cursorOnLastLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) {
      return false;
    }
    return !el.value.slice(pos).includes("\n");
  }

  /** Show a value in the box WITHOUT announcing it: cycling is navigation, so a
   *  displayed prompt is a preview and `draft` still holds the typed text. An
   *  `input` event would leave the cycle and be recorded as the new draft. */
  private setInputValue(el: HTMLTextAreaElement, v: string): void {
    el.value = v;
    el.setSelectionRange(v.length, v.length);
  }

  private applyButtonState(): void {
    const k = this.state.kind;
    // Only while idle: text typed mid-turn is a steer and does not size the next
    // prompt, and after a failure the error is the more useful thing to report.
    const ctxFull = k === "idle" && contextFull.value;
    const reason = reasonOf(this.state);
    $.sendBtn.replaceChildren(iconEl(STATE_ICON[k]));
    const tooltip =
      reason !== ""
        ? `${reason} ${RETRY_HINT}`
        : ctxFull
          ? CONTEXT_FULL_REASON
          : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", tooltip);
    $.sendBtn.setAttribute("aria-label", tooltip);
    $.sendBtn.classList.toggle("streaming", k === "streaming");
    $.sendBtn.classList.toggle("failed", k === "error");

    // type=button while streaming keeps a click out of the form: a click always
    // cancels during a turn, while Enter still sends.
    $.sendBtn.type = k === "streaming" ? "button" : "submit";
    // Unconditional, so nothing that ever set them can leave the composer off.
    $.sendBtn.disabled = false;
    $.promptInput.disabled = false;
    $.promptInput.placeholder = ctxFull ? CONTEXT_FULL_REASON : "Message Kiro...";
  }

  setSendState(next: SendState): void {
    if (this.state.kind === next.kind && reasonOf(this.state) === reasonOf(next)) {
      return;
    }
    this.state = next;
    this.applyButtonState();
  }

  init(onSubmit: Submit, onCancel: Cancel): void {
    if (initialized) {
      return;
    }
    initialized = true;

    const form = $.promptForm;
    const input = $.promptInput;

    this.onCancel = onCancel;
    this.onSubmit = onSubmit;
    $.sendBtn.addEventListener("click", (e: MouseEvent) => {
      if (this.state.kind === "streaming") {
        e.preventDefault();
        e.stopPropagation();
        this.onCancel();
      }
    });

    // Follows the context-full signal; setSendState() paints the other input
    // directly, because `state` is not a signal.
    effect(() => {
      touch(contextFull);
      this.applyButtonState();
    });

    form.addEventListener("submit", (e: Event) => {
      e.preventDefault();
      this.sendComposer();
    });

    // compositionstart clears any pending tail, so back-to-back compositions
    // cannot have a stale timer flip the flag false mid-candidate.
    input.addEventListener("compositionstart", () => {
      clearTimeout(this.imeTimer);
      this.imeTimer = undefined;
      this.composing = true;
    });
    input.addEventListener("compositionend", () => {
      this.composing = true;
      clearTimeout(this.imeTimer);
      this.imeTimer = setTimeout(() => {
        this.composing = false;
        this.imeTimer = undefined;
      }, IME_TAIL_MS);
    });

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (getActiveId() !== this.lastActiveID) {
        this.lastActiveID = getActiveId();
        this.exitCycling();
      }

      // Ahead of the cycling branch, which is guarded on `idx !== -1`. No
      // stopPropagation: a plain Escape still has to reach the global handler
      // that collapses pills and closes the dock.
      if (e.key === "Escape") {
        this.resetIME();
      }

      if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey) {
        // Inside this branch rather than an early return at the top, which would
        // also break history navigation during composition.
        if (this.isComposing(e)) {
          return;
        }
        e.preventDefault();
        this.sendComposer();
        return;
      }

      if (e.key === "ArrowUp" && this.cursorOnFirstLine(input)) {
        const prompts = this.userPrompts();
        if (prompts.length === 0) {
          return;
        }
        if (this.idx === -1) {
          this.draft = input.value;
        }
        const next = Math.min(this.idx + 1, prompts.length - 1);
        if (next === this.idx) {
          return;
        }
        this.idx = next;
        e.preventDefault();
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        this.setInputValue(input, prompts[this.idx]!);
        return;
      }

      if (e.key === "ArrowDown" && this.cursorOnLastLine(input) && this.idx !== -1) {
        e.preventDefault();
        if (this.idx === 0) {
          this.restoreDraft(input);
          return;
        }
        this.idx -= 1;
        const prompts = this.userPrompts();
        this.setInputValue(input, prompts[this.idx] ?? "");
        return;
      }

      if (e.key === "Escape" && this.idx !== -1) {
        e.preventDefault();
        e.stopPropagation();
        this.restoreDraft(input);
        return;
      }
    });

    input.addEventListener("input", () => {
      if (this.idx !== -1) {
        this.exitCycling();
      }
    });
    input.addEventListener("focus", () => {
      collapseAll();
    });
    // The draft layer keeps its own blur listener for its own concern.
    input.addEventListener("blur", () => {
      this.resetIME();
    });

    fixIOSViewport(input);
  }
}

const instance = new PromptInputController();

/** Called by send-state.ts whenever inputs change. */
export function setSendState(next: SendState): void {
  instance.setSendState(next);
}

export function initPromptInput(onSubmit: Submit, onCancel: Cancel): void {
  instance.init(onSubmit, onCancel);
}

/** Send the composer's contents: the keyboard shortcut's entry point, and the
 *  same path Enter and the send button take. */
export function sendComposer(): void {
  instance.sendComposer();
}
