// Prompt input: form submit, keydown (Enter, ↑/↓ history), send button, iOS viewport fix.
// send-state.ts drives three button faces: idle sends, streaming cancels, error retries; the error
// face means there is nothing to send TO, so every other failure reports through a toast and the
// turn's own divider. While a turn streams and the composer holds a send (text or a staged file), a
// second button beside Cancel sends it, because a touch Return is a new line.

import { $, byId } from "./dom.js";
import { activeSession, getActive, getActiveId } from "./store.js";
import { payloadOf, turnOpenOf } from "./turns.js";
import { fixIOSViewport } from "./platform.js";
import { ICON_SEND, ICON_CANCEL, ICON_ALERT, ICON_HOURGLASS } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { collapseAll } from "./pill-expand.js";
import { composerText, isSendable, setComposerValue, syncComposerText } from "./composer-value.js";
import { touchComposer, wireTouchComposer } from "./composer-touch.js";
import { continueList } from "./list-continue.js";
import { applyEdit } from "./text-edit.js";
import { computed, effect, el, signal, touch, type ReadonlySignal } from "@cplieger/reactive";
import type { InterruptMode } from "./types.js";

/** The active chat's interrupt mode, value-deduped so an unrelated store write does not repaint
 *  the button. */
const busyMode = computed(() => activeSession.value?.interrupt_mode ?? "steer");

/** While a turn runs the placeholder names what Send will do with the text. */
const BUSY_PLACEHOLDER = {
  steer: "Steer the agent mid-turn...",
  queue: "Queue a follow-up for after this turn...",
} as const;

/** The mid-turn Send's face per mode. The word is a word of the name (WCAG 2.5.3). */
const MIDTURN_FACE: Record<InterruptMode, { label: string; name: string; icon: string }> = {
  steer: { label: "Steer", name: "Steer the running turn", icon: ICON_SEND },
  queue: { label: "Queue", name: "Queue for after this turn", icon: ICON_HOURGLASS },
};

/** Appended to every error tooltip. A fixed suffix rather than a test against the reason's
 *  wording, which is upstream prose. */
const RETRY_HINT = "Send again to retry.";

type Submit = (text: string) => void;
type Cancel = () => void;
type Staged = ReadonlySignal<boolean>;

export type SendState =
  { kind: "idle" } | { kind: "streaming" } | { kind: "error"; reason: string };

type SendKind = SendState["kind"];

const STATE_ICON: Record<SendKind, string> = {
  idle: ICON_SEND,
  streaming: ICON_CANCEL,
  error: ICON_ALERT,
};

/** The face's word, shown beside the glyph where the row has room. Each is a word of the
 *  state's accessible name, so the visible label stays inside it (WCAG 2.5.3). */
const STATE_LABEL: Record<SendKind, string> = {
  idle: "Send",
  streaming: "Cancel",
  error: "Retry",
};

const DEFAULT_TOOLTIP: Record<SendKind, string> = {
  idle: "Send",
  streaming: "Cancel this turn",
  // Unreachable: send-state only builds an error with a reason of its own.
  error: RETRY_HINT,
};

/** The reason a state carries, "" for the states that carry none. Doubles as the dedupe key: two
 *  errors differing only in reason are different states. */
function reasonOf(s: SendState): string {
  return s.kind === "error" ? s.reason : "";
}

let initialized = false;

/** How long after `compositionend` an Enter is still the IME's commit key rather than a send.
 *  Several IMEs deliver that Enter just AFTER composition ended, with `isComposing` false and no
 *  keyCode 229, so no other leg covers it. */
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
  private staged: Staged = signal(false);
  private midTurn: HTMLButtonElement | null = null;
  private midTurnFace: InterruptMode | "" = "";

  /** Send whatever is in the composer. THE send path: the form's submit handler, Enter, the
   *  mid-turn Send and the keyboard shortcut all call this, and submit.ts decides what a send
   *  means. Neither of the two ways of going through the form works. */
  sendComposer(): void {
    const text = $.promptInput.value.trim();
    if (!isSendable(text, this.staged.peek())) {
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

  /** Leave cycling and put the saved draft back in the box. ONE method for both keys that end
   *  cycling (Escape, ArrowDown off the newest prompt), because exitCycling() zeroes `draft`:
   *  read after the exit it is "". */
  private restoreDraft(el: HTMLTextAreaElement): void {
    const saved = this.draft;
    this.exitCycling();
    this.setInputValue(el, saved);
  }

  /** Whether this keystroke belongs to an IME composition, so Enter must reach the browser and
   *  commit the candidate instead of sending. Three legs, each covering a case the others miss: */
  private isComposing(e: KeyboardEvent): boolean {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- keyCode 229 is the whole reason to port this guard: the IME's commit Enter reports it when isComposing is already false.
    return this.composing || e.isComposing || e.keyCode === 229;
  }

  /** Drop composition state, on blur and on Escape. Some Android IMEs never deliver
   *  compositionend when the field loses focus mid-candidate, which would leave Enter dead for
   *  the rest of the page's life. */
  private resetIME(): void {
    clearTimeout(this.imeTimer);
    this.imeTimer = undefined;
    this.composing = false;
  }

  /** What the reader typed into THIS composer, newest first: each turn's prompt plus their own
   *  steers into it, so a steer whose `origin` is `agent` is a workflow's report rather than
   *  typed text. A turn's steers come after its prompt, so walking backwards keeps the list in
   *  position order. */
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

  /** Show a value in the box WITHOUT announcing it: cycling is navigation, so a displayed prompt
   *  is a preview and `draft` still holds the typed text. An `input` event would leave the cycle
   *  and be recorded as the new draft. Written as one edit, so Ctrl+Z takes a recalled prompt
   *  back. */
  private setInputValue(el: HTMLTextAreaElement, v: string): void {
    applyEdit(el, { start: 0, end: el.value.length, text: v, caret: v.length }, { silent: true });
    syncComposerText();
  }

  /** `hidden`, not opacity, so a hidden one leaves the tab order and the accessibility tree. */
  private applyMidTurnSend(): void {
    const btn = this.midTurn;
    if (btn === null) {
      return;
    }
    const hide =
      this.state.kind !== "streaming" || !isSendable(composerText.peek(), this.staged.peek());
    if (hide) {
      // Its own send or the turn's end hides it under a keyboard user, who keeps their place in
      // the composer rather than falling to <body>. A tap's focus is left to the platform.
      const keyboard = !btn.hidden && btn.matches(":focus-visible");
      btn.hidden = true;
      if (keyboard) {
        $.promptInput.focus();
      }
      return;
    }
    btn.hidden = false;
    const mode = busyMode.peek();
    if (this.midTurnFace === mode) {
      return;
    }
    this.midTurnFace = mode;
    const face = MIDTURN_FACE[mode];
    btn.replaceChildren(el("span", { className: "send-btn-label" }, face.label), iconEl(face.icon));
    btn.setAttribute("data-tooltip", face.name);
    btn.setAttribute("aria-label", face.name);
  }

  private applyButtonState(): void {
    const k = this.state.kind;
    const reason = reasonOf(this.state);
    $.sendBtn.replaceChildren(
      el("span", { className: "send-btn-label" }, STATE_LABEL[k]),
      iconEl(STATE_ICON[k]),
    );
    const tooltip = reason !== "" ? `${reason} ${RETRY_HINT}` : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", tooltip);
    $.sendBtn.setAttribute("aria-label", tooltip);
    $.sendBtn.classList.toggle("streaming", k === "streaming");
    $.sendBtn.classList.toggle("failed", k === "error");

    // type=button while streaming keeps a click out of the form: a click always cancels during a
    // turn, while Enter still sends.
    $.sendBtn.type = k === "streaming" ? "button" : "submit";
    // Unconditional, so nothing that ever set them can leave the composer off.
    $.sendBtn.disabled = false;
    $.promptInput.disabled = false;
    $.promptInput.placeholder =
      k === "streaming" ? BUSY_PLACEHOLDER[busyMode.peek()] : "Message Kiro...";
    this.applyMidTurnSend();
  }

  setSendState(next: SendState): void {
    if (this.state.kind === next.kind && reasonOf(this.state) === reasonOf(next)) {
      return;
    }
    this.state = next;
    this.applyButtonState();
  }

  init(onSubmit: Submit, onCancel: Cancel, staged: Staged): void {
    if (initialized) {
      return;
    }
    initialized = true;

    const form = $.promptForm;
    const input = $.promptInput;

    this.onCancel = onCancel;
    this.onSubmit = onSubmit;
    this.staged = staged;
    $.sendBtn.addEventListener("click", (e: MouseEvent) => {
      if (this.state.kind === "streaming") {
        e.preventDefault();
        e.stopPropagation();
        this.onCancel();
      }
    });

    this.midTurn = byId<HTMLButtonElement>("midturn-send-btn");
    this.midTurn.addEventListener("click", () => {
      this.sendComposer();
    });

    syncComposerText();
    // Follows the active chat's mode; setSendState() paints the other input directly, because
    // `state` is not a signal.
    effect(() => {
      touch(busyMode);
      this.applyButtonState();
    });
    effect(() => {
      touch(composerText);
      touch(staged);
      this.applyMidTurnSend();
    });

    form.addEventListener("submit", (e: Event) => {
      e.preventDefault();
      this.sendComposer();
    });

    // compositionstart clears any pending tail, so back-to-back compositions cannot have a stale
    // timer flip the flag false mid-candidate.
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

      // Ahead of the cycling branch, which is guarded on `idx !== -1`. No stopPropagation: a plain
      // Escape still has to reach the global handler that collapses pills and closes the dock.
      if (e.key === "Escape") {
        this.resetIME();
      }

      if (
        e.key === "Enter" &&
        e.shiftKey &&
        !e.ctrlKey &&
        !e.metaKey &&
        !e.altKey &&
        !this.isComposing(e) &&
        !menuOpen(input) &&
        input.selectionStart === input.selectionEnd
      ) {
        const edit = continueList(input.value, input.selectionStart);
        if (edit !== null) {
          e.preventDefault();
          applyEdit(input, edit);
        }
        return;
      }

      if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey) {
        // Inside this branch rather than an early return at the top, which would also break history
        // navigation during composition.
        if (this.isComposing(e)) {
          return;
        }
        // Under a finger Return is a new line; Cmd+Enter still sends through keys.ts.
        if (!e.metaKey && touchComposer()) {
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
      syncComposerText();
      if (this.idx !== -1) {
        this.exitCycling();
      }
    });
    input.addEventListener("focus", () => {
      syncEnterHint(input);
      collapseAll();
    });
    syncEnterHint(input);
    wireTouchComposer(input, () => menuOpen(input));
    // The draft layer keeps its own blur listener for its own concern.
    input.addEventListener("blur", () => {
      this.resetIME();
    });

    fixIOSViewport(input);
  }
}

/** Whether the popup this box names in `aria-controls` (the `/` and `#` menu) is open; that menu
 *  owns Enter while it is. Read off the element, because importing slash-menu.ts here would
 *  close an import cycle. */
function menuOpen(el: HTMLTextAreaElement): boolean {
  const id = el.getAttribute("aria-controls");
  const menu = id === null ? null : document.getElementById(id);
  return menu !== null && !menu.hidden;
}

/** The touch keyboard's Return label follows what Return does. */
function syncEnterHint(el: HTMLTextAreaElement): void {
  el.enterKeyHint = touchComposer() ? "enter" : "send";
}

const instance = new PromptInputController();

/** Called by send-state.ts whenever inputs change. */
export function setSendState(next: SendState): void {
  instance.setSendState(next);
}

/** `staged` tracks the pill row for `isSendable`. It is injected because importing
 *  attachments.ts here would close an import cycle. */
export function initPromptInput(
  onSubmit: Submit,
  onCancel: Cancel,
  staged: Staged = signal(false),
): void {
  instance.init(onSubmit, onCancel, staged);
}

/** Send the composer's contents: the keyboard shortcut's entry point, and the same path Enter
 *  and the send button take. */
export function sendComposer(): void {
  instance.sendComposer();
}
