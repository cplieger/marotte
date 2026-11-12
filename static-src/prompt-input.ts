// Prompt input: form submit, keydown (Enter, ↑/↓ history, Ctrl/Cmd+↑ rewind, Escape cancels a turn),
// send button, iOS viewport fix.
// send-state.ts drives three button faces: idle sends, streaming cancels, error retries; the error
// face means there is nothing to send TO, so every other failure reports through a toast and the
// turn's own divider. While a turn streams and the composer holds a send (text or a staged file), a
// second button beside Cancel sends it, because a touch Return is a new line. With nothing to send,
// a microphone takes Send's or that button's slot (send-faces.ts decides which face shows).

import { $, byId, maybeEl, queryEl } from "./dom.js";
import { activeSession, getActive, getActiveId } from "./store.js";
import { afterWords } from "./run-order.js";
import { payloadOf, turnOpenOf } from "./turns.js";
import { fixIOSViewport } from "./platform.js";
import { ICON_SEND, ICON_CANCEL, ICON_ALERT, ICON_HOURGLASS, ICON_MIC } from "./icons.js";
import { sendFaces, type Faces } from "./send-faces.js";
import { NO_DICTATION, type Dictation } from "./dictation.js";
import { pressOwner, type Act } from "./send-press.js";
import { iconEl } from "./icon-el.js";
import { collapseAll } from "./pill-expand.js";
import {
  composerText,
  isSendable,
  onComposerWrite,
  recallComposerValue,
  setComposerValue,
  syncComposerText,
} from "./composer-value.js";
import { touchComposer, wireTouchComposer } from "./composer-touch.js";
import { flushComposerDraft, restoreRefusedEdit } from "./composer-state.js";
import { continueList } from "./list-continue.js";
import { applyEdit } from "./text-edit.js";
import { sigChanged } from "./paint-sig.js";
import { computed, effect, el, signal, touch, type ReadonlySignal } from "@cplieger/reactive";
import type { SendVerb } from "./wire/types.gen.js";

/** What Send does with the text now, from the server's run order (`RunView.send`). */
const sendVerb = computed<SendVerb>(() => activeSession.value?.run?.send ?? "prompt");

/** How long a message queued now would wait, in the dock's words, so the two agree. */
const queueWait = computed(() => {
  const wait = activeSession.value?.run?.next_wait;
  return wait === undefined ? "the next turn" : afterWords(wait);
});

/** The placeholder names what Send will do with the text. */
function placeholderFor(verb: SendVerb, wait: string): string {
  switch (verb) {
    case "prompt":
      return "Message Kiro…";
    case "steer":
      return "Steer the agent mid-turn…";
    case "queue":
      return wait === "the compaction"
        ? "Queue a message for after the compaction…"
        : `Queue a follow-up for after ${wait}…`;
  }
}

/** The mid-turn Send's face per verb. The word is a word of the name (WCAG 2.5.3). */
const MIDTURN_FACE: Record<
  SendVerb,
  { label: string; name: (wait: string) => string; icon: string }
> = {
  prompt: { label: "Send", name: () => "Send", icon: ICON_SEND },
  steer: { label: "Steer", name: () => "Steer the running turn", icon: ICON_SEND },
  queue: { label: "Queue", name: (wait) => `Queue for after ${wait}`, icon: ICON_HOURGLASS },
};

/** Appended to every error tooltip. A fixed suffix rather than a test against the reason's
 *  wording, which is upstream prose. */
const RETRY_HINT = "Send again to retry.";

/** Sends the text; a send that answers later returns its result, and the composer holds the text,
 *  read-only, until then. */
type Submit = (text: string) => Promise<unknown> | undefined;
type Cancel = () => void;
type EditLast = () => void;
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

/** Doubles as the dedupe key: two errors differing only in reason are different states. */
function reasonOf(s: SendState): string {
  return s.kind === "error" ? s.reason : "";
}

/** A toggle's name stays stable and `aria-pressed` carries the state; the tooltip says what a press does. */
function paintDictate(btn: HTMLButtonElement, pressed: boolean): void {
  btn.replaceChildren(el("span", { className: "send-btn-label" }, "Dictate"), iconEl(ICON_MIC));
  btn.setAttribute("aria-label", "Dictate");
  btn.setAttribute("aria-pressed", String(pressed));
  btn.setAttribute("data-tooltip", pressed ? "Stop dictation" : "Dictate");
  btn.classList.toggle("dictating", pressed);
  btn.type = "button";
}

function clearDictate(btn: HTMLButtonElement): void {
  btn.removeAttribute("aria-pressed");
  btn.classList.remove("dictating");
}

let initialized = false;

/** How long after `compositionend` an Enter is still the IME's commit key rather than a send.
 *  Several IMEs deliver that Enter just AFTER composition ended, with `isComposing` false and no
 *  keyCode 229, so no other leg covers it. */
const IME_TAIL_MS = 50;

class PromptInputController {
  private idx = -1;
  private draft = "";
  private lastActiveID = "";

  // IME composition state; `imeTimer` holds `composing` true for the tail.
  private composing = false;
  private imeTimer: ReturnType<typeof setTimeout> | undefined;

  private state: SendState = { kind: "idle" };
  private onCancel: Cancel = () => undefined;
  private onSubmit: Submit = () => undefined;
  private onEditLast: EditLast = () => undefined;
  private staged: Staged = signal(false);
  private dictation: Dictation = NO_DICTATION;
  private midTurn: HTMLButtonElement | null = null;
  /** The `pointerType` of the last pointer press on a slot button. */
  private pressPointer = "";
  /** Whether the press that started the current or last dictation was a finger's, which never moves focus. */
  private fingerDictation = false;
  private readonly press = pressOwner((btn) => this.actOf(btn));
  /** Each chat whose send is in flight, by the token of that send. */
  private readonly held = new Map<string, object>();

  /** Send whatever is in the composer. THE send path: the form's submit handler, Enter, the
   *  mid-turn Send and the keyboard shortcut all call this, and the server's run order decides
   *  where the prompt goes. Neither of the two ways of going through the form works. */
  sendComposer(): void {
    const raw = $.promptInput.value;
    const text = raw.trim();
    if (this.heldHere() || !isSendable(text, this.staged.peek())) {
      return;
    }
    // Before the box clears, so no late words refill it.
    this.dictation.drop();
    this.exitCycling();
    const chatID = getActiveId();
    const sending = this.onSubmit(text);
    if (sending === undefined) {
      setComposerValue("");
      return;
    }
    this.holdForSend(chatID, raw, sending);
  }

  /** The send is in flight: the text stays, read-only, and the Send button shows it pending. The
   *  answer clears it in the render that applies the answer's `run`; a refusal leaves it there. */
  private holdForSend(chatID: string, raw: string, sending: Promise<unknown>): void {
    const token = {};
    this.held.set(chatID, token);
    this.applyHeld();
    void sending.then(
      (result) => {
        this.releaseHeld(chatID, token);
        if (result === "held" || result === "failed") {
          return;
        }
        if (getActiveId() === chatID && $.promptInput.value === raw) {
          setComposerValue("");
          // Saved now, behind any save of the sent text still in flight, so the record never ends on it.
          flushComposerDraft();
          return;
        }
        // The reader moved to another chat: the sent chat's saved draft goes, unless typed over.
        restoreRefusedEdit(chatID, raw, "");
      },
      () => {
        this.releaseHeld(chatID, token);
      },
    );
  }

  private releaseHeld(chatID: string, token: object): void {
    if (this.held.get(chatID) === token) {
      this.held.delete(chatID);
      this.applyHeld();
    }
  }

  /** Whether the active chat's send is in flight. */
  private heldHere(): boolean {
    return this.held.has(getActiveId());
  }

  /** Read-only and pending only on the chat whose send is in flight. */
  private applyHeld(): void {
    const on = this.heldHere();
    if ($.promptInput.readOnly === on) {
      return;
    }
    $.promptInput.readOnly = on;
    this.applyButtonState();
  }

  private exitCycling(): void {
    this.idx = -1;
    this.draft = "";
  }

  /** ONE method for both keys that end cycling (Escape, ArrowDown off the newest prompt), because
   *  exitCycling() zeroes `draft`: read after the exit it is "". */
  private restoreDraft(): void {
    const saved = this.draft;
    this.exitCycling();
    recallComposerValue(saved);
  }

  /** Whether this keystroke belongs to an IME composition, so Enter must reach the browser and
   *  commit the candidate instead of sending. Three legs, each covering a case the others miss: */
  private isComposing(e: KeyboardEvent): boolean {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- keyCode 229 is the whole reason to port this guard: the IME's commit Enter reports it when isComposing is already false.
    return this.composing || e.isComposing || e.keyCode === 229;
  }

  /** Some Android IMEs never deliver compositionend when the field loses focus mid-candidate, which
   *  would leave Enter dead for the rest of the page's life. */
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

  /** `hidden`, not opacity, so a hidden one leaves the tab order and the accessibility tree. */
  private applyMidTurnSend(): void {
    const btn = this.midTurn;
    if (btn === null) {
      return;
    }
    const side = this.faces().side;
    if (side === "hidden") {
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
    const pressed = this.dictation.listening.peek();
    const face = MIDTURN_FACE[sendVerb.peek()];
    const name = face.name(queueWait.peek());
    const parts = side === "dictate" ? [side, String(pressed)] : [side, face.label, name];
    if (!sigChanged(btn, parts)) {
      return;
    }
    if (side === "dictate") {
      paintDictate(btn, pressed);
      return;
    }
    clearDictate(btn);
    btn.replaceChildren(el("span", { className: "send-btn-label" }, face.label), iconEl(face.icon));
    btn.setAttribute("data-tooltip", name);
    btn.setAttribute("aria-label", name);
  }

  private faces(): Faces {
    return sendFaces({
      turn: this.state.kind,
      text: composerText.peek(),
      staged: this.staged.peek(),
      canDictate: this.dictation.ready.peek(),
      dictating: this.dictation.listening.peek(),
    });
  }

  /** What a press on `btn` would do on the face it shows now. */
  private actOf(btn: HTMLButtonElement): Act {
    const f = this.faces();
    const face = btn === this.midTurn ? f.side : f.main;
    switch (face) {
      case "dictate":
        return this.dictation.listening.peek() ? "stop" : "start";
      case "cancel":
        return "cancel";
      case "send":
      case "retry":
      case "hidden":
        return "send";
    }
  }

  /** A click from a touch or pen press; a key's click has detail 0. */
  private byFinger(e: MouseEvent): boolean {
    return e.detail > 0 && (this.pressPointer === "touch" || this.pressPointer === "pen");
  }

  /** The slot button `t` is, or null. */
  private slotOf(t: EventTarget | null): HTMLButtonElement | null {
    return t === $.sendBtn || t === this.midTurn ? (t as HTMLButtonElement) : null;
  }

  /** Every press listener below only feeds `press`, the one owner of a press; the buttons' keydown runs Escape. */
  private wirePresses(buttons: readonly HTMLButtonElement[]): void {
    for (const on of buttons) {
      on.addEventListener("pointerdown", (e: PointerEvent) => {
        this.pressPointer = e.pointerType;
        this.press({ k: "pointerdown", on, e });
      });
      for (const k of ["lostpointercapture", "pointercancel"] as const) {
        on.addEventListener(k, () => {
          this.press({ k });
        });
      }
      on.addEventListener("contextmenu", (e: MouseEvent) => {
        this.press({ k: "contextmenu", e });
      });
      on.addEventListener("blur", () => {
        this.press({ k: "blur" });
      });
      on.addEventListener("keydown", (e: KeyboardEvent) => {
        this.escapeStops(e);
      });
    }
    // On the document: a release dragged off the button never reaches it, and a key press's repeats and up follow
    // focus wherever its click moved it, ahead of the box's own listener.
    document.addEventListener(
      "pointerup",
      (e: PointerEvent) => {
        this.press({ k: "pointerup", e });
      },
      true,
    );
    document.addEventListener(
      "keydown",
      (e: KeyboardEvent) => {
        this.press({ k: "keydown", on: this.slotOf(e.target), e });
      },
      true,
    );
    document.addEventListener(
      "keyup",
      (e: KeyboardEvent) => {
        this.press({ k: "keyup", e });
      },
      true,
    );
    window.addEventListener("blur", () => {
      this.press({ k: "windowblur" });
    });
  }

  /** A session's end puts focus in the box from a slot button, whose face may now send or steer on the reader's next
   *  Space, or from nowhere; focus the reader moved elsewhere on the page stays there. A finger's session only takes
   *  focus off a slot button, since focusing the box would raise the keyboard. */
  private focusAfterDictation(): void {
    const at = document.activeElement;
    if (this.fingerDictation) {
      if (this.slotOf(at) !== null) {
        (at as HTMLElement).blur();
      }
      return;
    }
    if (at === null || at === document.body || this.slotOf(at) !== null) {
      $.promptInput.focus();
    }
  }

  /** Escape leaves dictation ahead of Cancel, so a keyboard reader keeps the turn. */
  private escapeStops(e: KeyboardEvent): boolean {
    if (
      e.key !== "Escape" ||
      e.shiftKey ||
      e.altKey ||
      e.ctrlKey ||
      e.metaKey ||
      !this.dictation.listening.peek() ||
      escapeOwnerOpen()
    ) {
      return false;
    }
    e.preventDefault();
    e.stopPropagation();
    this.dictation.toggle();
    return true;
  }

  /** `start` and `stop` do nothing once the session is already in that state, so a run's later clicks cannot
   *  undo its first. */
  private perform(act: Exclude<Act, "send">, finger: boolean): void {
    const listening = this.dictation.listening.peek();
    switch (act) {
      case "cancel":
        if (this.state.kind === "streaming") {
          this.onCancel();
        }
        // A press can take focus off the box, and a keyboard or mouse session's words land only in the focused box.
        if (listening && !this.fingerDictation) {
          $.promptInput.focus();
        }
        return;
      case "start":
      case "stop":
        if (listening === (act === "stop")) {
          if (act === "start") {
            this.fingerDictation = finger;
          }
          this.dictation.toggle({ finger });
        }
        return;
    }
  }

  /** Both buttons' faces only: the input effect runs this on every keystroke. */
  private applyFaces(): void {
    this.paintMain();
    this.applyMidTurnSend();
  }

  private paintMain(): void {
    const k = this.state.kind;
    const reason = reasonOf(this.state);
    const held = this.heldHere();
    const main = this.faces().main;
    const pressed = this.dictation.listening.peek();
    const parts = main === "dictate" ? [main, String(pressed)] : [main, reason, String(held)];
    if (!sigChanged($.sendBtn, parts)) {
      return;
    }
    if (main === "dictate") {
      paintDictate($.sendBtn, pressed);
      $.sendBtn.removeAttribute("aria-busy");
      $.sendBtn.classList.remove("streaming", "failed");
      return;
    }
    clearDictate($.sendBtn);
    $.sendBtn.replaceChildren(
      el("span", { className: "send-btn-label" }, STATE_LABEL[k]),
      held ? el("span", { className: "spinner-sm", "aria-hidden": "true" }) : iconEl(STATE_ICON[k]),
    );
    if (held) {
      $.sendBtn.setAttribute("aria-busy", "true");
    } else {
      $.sendBtn.removeAttribute("aria-busy");
    }
    const tooltip = reason !== "" ? `${reason} ${RETRY_HINT}` : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", k === "streaming" ? `${tooltip} (Esc)` : tooltip);
    $.sendBtn.setAttribute("aria-label", tooltip);
    $.sendBtn.classList.toggle("streaming", k === "streaming");
    $.sendBtn.classList.toggle("failed", k === "error");

    // type=button while streaming keeps a Cancel click out of the form, while Enter still sends.
    $.sendBtn.type = k === "streaming" ? "button" : "submit";
  }

  private applyButtonState(): void {
    this.paintMain();
    // Unconditional, so nothing that ever set them can leave the composer off.
    $.sendBtn.disabled = false;
    $.promptInput.disabled = false;
    $.promptInput.placeholder = placeholderFor(sendVerb.peek(), queueWait.peek());
    this.applyMidTurnSend();
  }

  setSendState(next: SendState): void {
    if (this.state.kind === next.kind && reasonOf(this.state) === reasonOf(next)) {
      return;
    }
    this.state = next;
    if (next.kind === "error") {
      this.dictation.stop();
    }
    this.applyButtonState();
  }

  init(
    onSubmit: Submit,
    onCancel: Cancel,
    staged: Staged,
    onEditLast: EditLast,
    dictation: Dictation,
  ): void {
    if (initialized) {
      return;
    }
    initialized = true;

    const form = $.promptForm;
    const input = $.promptInput;

    this.onCancel = onCancel;
    this.onSubmit = onSubmit;
    this.onEditLast = onEditLast;
    this.staged = staged;
    this.dictation = dictation;
    onComposerWrite(() => {
      dictation.drop();
    });
    const midTurn = byId<HTMLButtonElement>("midturn-send-btn");
    this.midTurn = midTurn;
    this.wirePresses([$.sendBtn, midTurn]);
    $.sendBtn.addEventListener("click", (e: MouseEvent) => {
      const act = this.press({ k: "click", on: $.sendBtn, e });
      if (act === "send") {
        // A face painted since the press (Dictate, Cancel) is type=button, so the form would not submit.
        if ($.sendBtn.type !== "submit") {
          e.preventDefault();
          this.sendComposer();
        }
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      if (act !== null) {
        this.perform(act, this.byFinger(e));
      }
    });
    midTurn.addEventListener("click", (e: MouseEvent) => {
      const act = this.press({ k: "click", on: midTurn, e });
      if (act === "send") {
        this.sendComposer();
      } else if (act !== null) {
        this.perform(act, this.byFinger(e));
      }
    });

    syncComposerText();
    // Follows the active chat's mode and queue; setSendState() paints the other input directly,
    // because `state` is not a signal.
    effect(() => {
      touch(sendVerb);
      touch(queueWait);
      this.applyButtonState();
    });
    effect(() => {
      touch(activeSession);
      this.applyHeld();
    });
    effect(() => {
      touch(composerText);
      touch(staged);
      touch(dictation.ready);
      touch(dictation.listening);
      this.applyFaces();
    });
    let listened = false;
    effect(() => {
      const listening = dictation.listening.value;
      if (listened && !listening) {
        this.focusAfterDictation();
      }
      listened = listening;
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

      // Read before Escape's reset: a key the IME owns writes nothing, so no history, list or Send.
      const composing = this.isComposing(e);
      // Ahead of the cycling branch, which is guarded on `idx !== -1`. The reset claims nothing, so
      // an Escape the arms below leave alone still reaches an open card.
      if (e.key === "Escape") {
        this.resetIME();
      }
      if (composing) {
        return;
      }

      if (
        e.key === "Enter" &&
        e.shiftKey &&
        !e.ctrlKey &&
        !e.metaKey &&
        !e.altKey &&
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
        // Under a finger Return is a new line; Cmd+Enter still sends through keys.ts.
        if (!e.metaKey && touchComposer()) {
          return;
        }
        e.preventDefault();
        this.sendComposer();
        return;
      }

      // Only from an empty box: with text, the chord is the browser's caret move.
      if (
        e.key === "ArrowUp" &&
        (e.ctrlKey || e.metaKey) &&
        !e.shiftKey &&
        !e.altKey &&
        !menuOpen(input) &&
        input.value.trim() === ""
      ) {
        e.preventDefault();
        this.onEditLast();
        return;
      }

      const bare = !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey;

      if (e.key === "ArrowUp" && bare && this.cursorOnFirstLine(input)) {
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
        recallComposerValue(prompts[this.idx]!);
        return;
      }

      if (e.key === "ArrowDown" && bare && this.cursorOnLastLine(input) && this.idx !== -1) {
        e.preventDefault();
        if (this.idx === 0) {
          this.restoreDraft();
          return;
        }
        this.idx -= 1;
        const prompts = this.userPrompts();
        recallComposerValue(prompts[this.idx] ?? "");
        return;
      }

      if (e.key === "Escape" && this.idx !== -1) {
        e.preventDefault();
        e.stopPropagation();
        this.restoreDraft();
        return;
      }

      if (this.escapeStops(e)) {
        return;
      }

      // The Cancel face's own action, and the last claimant of Escape here. Spent whole, so a
      // toast survives the press; a repeat does nothing, so a held Escape that closed a card or
      // left recall never cancels.
      if (e.key === "Escape" && bare && this.state.kind === "streaming" && !escapeOwnerOpen()) {
        e.preventDefault();
        e.stopPropagation();
        if (!e.repeat) {
          this.onCancel();
        }
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
  const menu = id === null ? null : maybeEl(id);
  return menu !== null && !menu.hidden;
}

/** Whether a surface that closes on Escape is open. Each closes from its own document listener
 *  without stopping the key, so it runs after the composer and must be asked. `is-open` is
 *  the open-state class of legacy-ui's popup and popover. */
function escapeOwnerOpen(): boolean {
  return queryEl(".uip-popup.is-open, .uip-popover.is-open, .turn-actions-more[open]") !== null;
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
 *  attachments.ts here would close an import cycle. `onEditLast` answers Ctrl/Cmd+Up in an empty
 *  composer. Without `dictation`, Send never becomes the microphone. */
export function initPromptInput(
  onSubmit: Submit,
  onCancel: Cancel,
  staged: Staged = signal(false),
  onEditLast: EditLast = () => undefined,
  dictation: Dictation = NO_DICTATION,
): void {
  instance.init(onSubmit, onCancel, staged, onEditLast, dictation);
}

/** Send the composer's contents: the keyboard shortcut's entry point, and the same path Enter
 *  and the send button take. */
export function sendComposer(): void {
  instance.sendComposer();
}
