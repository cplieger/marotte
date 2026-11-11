// Dictation into the composer through the browser's Web Speech API, on-device wherever the browser offers it:
// https://developer.mozilla.org/en-US/docs/Web/API/Web_Speech_API/Using_the_Web_Speech_API

import { computed, effect, signal, type ReadonlySignal } from "@cplieger/ui";
import { announce } from "@cplieger/ui/announce";
import { error as errorToast, info } from "./toast.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { watchActiveId } from "./store.js";
import { dictationCloudNoted, markDictationCloudNoted } from "./device-view.js";
import { applyEdit } from "./text-edit.js";
import { touchComposer } from "./composer-touch.js";

export type SpeechAvailability = "available" | "downloadable" | "downloading" | "unavailable";

export interface SpeechOptions {
  readonly langs: readonly string[];
  readonly processLocally: boolean;
  readonly quality: "dictation";
}

/** The slice of `SpeechRecognition` this module drives. */
export interface SpeechRecognizer extends EventTarget {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  processLocally: boolean;
  start(): void;
  stop(): void;
  abort(): void;
}

/** The constructor, with the on-device statics where the browser has them (Chrome 139, Firefox 160). */
export interface SpeechRecognizerClass {
  new (): SpeechRecognizer;
  available?: (o: SpeechOptions) => Promise<SpeechAvailability>;
  install?: (o: SpeechOptions) => Promise<boolean>;
}

/** The platform seam: production passes `browserSpeechPlatform()`, a test a fake recognizer. */
export interface SpeechPlatform {
  /** null where the browser has no speech recognition. */
  readonly Recognition: SpeechRecognizerClass | null;
  readonly lang: string;
  /** The service the cloud notice names, or null where the browser only recognizes on-device. */
  readonly cloud: string | null;
  /** Whether a result list repeats earlier final results inside later ones (Chrome on Android:
   *  https://issues.chromium.org/issues/40272768). */
  readonly cumulative: boolean;
}

export interface Dictation {
  /** Whether a press can start dictation: the browser recognizes speech, nothing refused it this page, and the box
   *  takes text. */
  readonly ready: ReadonlySignal<boolean>;
  /** From the press until the session stops. */
  readonly listening: ReadonlySignal<boolean>;
  /** Start, or stop so the words still being recognized land. */
  toggle(): void;
  /** Stop now: the words still being recognized are dropped, and a phrase waiting for the box's focus lands when
   *  it comes back. What is in the box stays. */
  stop(): void;
  /** Stop now and drop every word not yet in the box, because the box no longer holds the draft they were said
   *  into (a send, another chat). */
  drop(): void;
}

interface NavigatorShape {
  readonly userAgentData?: { readonly brands: readonly { readonly brand: string }[] };
  readonly vendor: string;
  readonly userAgent: string;
}

/** Whose servers a cloud recognition sends audio to. Firefox recognizes on-device only (Firefox 160 release notes). */
export function cloudServiceFor(nav: NavigatorShape): string | null {
  const brands = nav.userAgentData?.brands.map((b) => b.brand) ?? [];
  if (brands.includes("Microsoft Edge")) {
    return "Microsoft's speech service";
  }
  if (brands.includes("Google Chrome")) {
    return "Google's speech service";
  }
  if (/\bFirefox\//.test(nav.userAgent)) {
    return null;
  }
  if (nav.vendor.startsWith("Apple")) {
    return "Apple's speech service";
  }
  return "an online speech service";
}

export function browserSpeechPlatform(): SpeechPlatform {
  const g = globalThis as {
    readonly isSecureContext?: boolean;
    readonly SpeechRecognition?: SpeechRecognizerClass;
    readonly webkitSpeechRecognition?: SpeechRecognizerClass;
  };
  return {
    // Chromium exposes the constructor on an insecure origin too, where the microphone never opens.
    Recognition:
      g.isSecureContext === true
        ? (g.SpeechRecognition ?? g.webkitSpeechRecognition ?? null)
        : null,
    lang: navigator.language,
    cloud: cloudServiceFor(navigator),
    cumulative: /\bAndroid\b/.test(navigator.userAgent),
  };
}

/** The controller where the browser cannot recognize speech: never ready, and every call does nothing. */
export const NO_DICTATION: Dictation = {
  ready: signal(false),
  listening: signal(false),
  toggle: () => undefined,
  stop: () => undefined,
  drop: () => undefined,
};

export function createDictation(platform: SpeechPlatform, box: HTMLTextAreaElement): Dictation {
  return platform.Recognition === null
    ? NO_DICTATION
    : new Controller(platform.Recognition, platform, box);
}

/** Where a session's words go: the selection at its first result, padded so they never run into a neighbour. */
interface Anchor {
  readonly start: number;
  readonly end: number;
  /** The box's value before the session wrote anything. */
  readonly base: string;
  readonly prefix: string;
  readonly suffix: string;
}

interface Session {
  rec: SpeechRecognizer | null;
  listening: boolean;
  /** Results land while true; a graceful stop keeps it until the engine's `end`. */
  accepting: boolean;
  begun: boolean;
  anchor: Anchor | null;
  /** What the session last wrote at its anchor, and the box's value right after. */
  written: string;
  after: string;
  /** The phrase heard while focus was outside the box, written when it comes back. */
  pending: string | null;
  /** Under a finger: the box's own `inputmode`, put away while the session keeps the box focused, and where focus
   *  goes back when it ends (null: it stays in the box). */
  keyboard: { readonly mode: string | null; readonly returnTo: HTMLElement | null } | null;
}

/** `cloud`: whether audio is known to go to the vendor's service, which the notice says. */
type Engine = { readonly local: boolean | null; readonly cloud: boolean } | "declined" | Refusal;

const REFUSED = {
  "not-allowed":
    "Dictation is off: the browser did not allow the microphone. Allow it for this site and reload the page to try again.",
  "service-not-allowed":
    "Dictation is off: this browser does not allow its speech service here. Reload the page to try again.",
  "audio-capture":
    "Dictation is off: no microphone was found. Connect one and reload the page to try again.",
  "language-not-supported":
    "Dictation is off: this browser cannot recognize the page's language. Reload the page to try again.",
  network:
    "Dictation is off: this browser's speech service did not answer. Reload the page to try again.",
} as const;

type Refusal = keyof typeof REFUSED;

function isRefused(code: string): code is Refusal {
  return Object.hasOwn(REFUSED, code);
}

/** Codes that end a session with nothing to report: silence, or our own abort. */
const QUIET = new Set(["no-speech", "aborted"]);

/** Where focus goes when a session that put the keyboard away ends: an edit of the box keeps it in the box, so the
 *  reader's next key never lands on the slot button; every other end returns it where the press took it from. */
const FOCUS_AFTER = {
  /** Typing, a paste, any write of the box that is not dictation's own. */
  edit: "box",
  /** Stop, Escape, the browser's end, an error or a refusal. */
  ended: "press",
  /** A send, a tab or chat switch, a page hide, a disabled or read-only box, the error face. */
  away: "press",
} as const satisfies Record<string, "box" | "press">;

type End = keyof typeof FOCUS_AFTER;

/** How long `install()` runs before the downloading toast shows: `available()` masks an installed pack as
 *  downloadable until this origin installs it, and that install resolves at once (Web Speech API, "Download Masking"). */
const DOWNLOAD_NOTICE_DELAY_MS = 400;

class Controller implements Dictation {
  readonly ready: ReadonlySignal<boolean>;
  readonly listening: ReadonlySignal<boolean>;
  private readonly Rec: SpeechRecognizerClass;
  private readonly platform: SpeechPlatform;
  private readonly box: HTMLTextAreaElement;
  private readonly off = signal(false);
  private readonly writable = signal(true);
  private readonly on = signal(false);
  /** The session whose results land; at most one. */
  private s: Session | null = null;
  /** A session that stopped while its last phrase waited for the box's focus. */
  private late: Session | null = null;
  /** Set around this module's own edits, so their `input` events are not read as the reader's. */
  private writing = false;
  /** A result arrived this page, so the speech service answers. */
  private answered = false;

  constructor(Rec: SpeechRecognizerClass, platform: SpeechPlatform, box: HTMLTextAreaElement) {
    this.Rec = Rec;
    this.platform = platform;
    this.box = box;
    this.listening = this.on;
    this.ready = computed(() => !this.off.value && this.writable.value);
    this.syncWritable();
    new MutationObserver(() => {
      this.syncWritable();
    }).observe(box, { attributes: true, attributeFilter: ["disabled", "readonly"] });
    // The reader's edit or a clear (`setComposerValue`): the draft the waiting words were said into is gone.
    box.addEventListener("input", () => {
      if (!this.writing) {
        this.end("edit", true);
      }
    });
    box.addEventListener("pointerdown", () => {
      if (this.s !== null) {
        this.giveKeyboardBack(this.s, "box");
      }
    });
    box.addEventListener("focusin", () => {
      const late = this.late;
      this.late = null;
      for (const s of [late, this.s]) {
        const phrase = s?.pending ?? null;
        if (s !== null && phrase !== null) {
          s.pending = null;
          this.write(s, phrase);
        }
      }
    });
    onBus(BUS_TAB_CHANGED, () => {
      this.stop();
    });
    let chat = watchActiveId();
    effect(() => {
      const next = watchActiveId();
      if (next !== chat) {
        chat = next;
        this.drop();
      }
    });
    window.addEventListener("pagehide", () => {
      this.stop();
    });
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "hidden") {
        this.stop();
      }
    });
  }

  toggle(): void {
    const s = this.s;
    if (s?.listening === true) {
      if (s.rec === null) {
        this.halt(s, "ended");
        return;
      }
      s.rec.stop();
      this.box.focus();
      this.unlisten(s);
      return;
    }
    if (this.ready.peek()) {
      void this.begin();
    }
  }

  stop(): void {
    this.end("away", false);
  }

  drop(): void {
    this.end("away", true);
  }

  /** `dropWords`: the words not yet in the box go too. */
  private end(why: End, dropWords: boolean): void {
    if (dropWords) {
      this.late = null;
    }
    const s = this.s;
    if (s === null) {
      return;
    }
    if (dropWords) {
      s.pending = null;
    }
    this.halt(s, why);
  }

  private syncWritable(): void {
    this.writable.value = !this.box.disabled && !this.box.readOnly;
    if (!this.writable.peek()) {
      this.drop();
    }
  }

  /** The words land as one undo step only in a focused box. Under a finger the keyboard is put away for the
   *  session, so the focus does not raise it over the words. */
  private focusBox(s: Session): void {
    const box = this.box;
    if (touchComposer()) {
      const active = document.activeElement;
      s.keyboard = {
        mode: box.getAttribute("inputmode"),
        returnTo: active instanceof HTMLElement && active !== box ? active : null,
      };
      box.inputMode = "none";
    }
    box.focus();
  }

  /** "press" first moves focus back where the press took it from, so restoring the mode raises no keyboard. */
  private giveKeyboardBack(s: Session, to: (typeof FOCUS_AFTER)[End]): void {
    const k = s.keyboard;
    if (k === null) {
      return;
    }
    s.keyboard = null;
    const box = this.box;
    if (to === "press" && k.returnTo !== null && document.activeElement === box) {
      k.returnTo.focus();
      if (document.activeElement === box) {
        box.blur();
      }
    }
    if (k.mode === null) {
      box.removeAttribute("inputmode");
    } else {
      box.setAttribute("inputmode", k.mode);
    }
  }

  private halt(s: Session, why: End): void {
    s.accepting = false;
    s.rec?.abort();
    this.retire(s);
    this.unlisten(s);
    this.giveKeyboardBack(s, FOCUS_AFTER[why]);
  }

  /** The session's results stop landing; a phrase still waiting for focus lands on the box's next `focusin`. */
  private retire(s: Session): void {
    if (this.s !== s) {
      return;
    }
    this.s = null;
    this.late = s.pending === null ? null : s;
  }

  private unlisten(s: Session): void {
    if (!s.listening) {
      return;
    }
    s.listening = false;
    this.on.value = false;
    if (s.begun) {
      announce("Dictation stopped");
    }
  }

  private async begin(): Promise<void> {
    this.stop();
    const s: Session = {
      rec: null,
      listening: true,
      accepting: true,
      begun: false,
      anchor: null,
      written: "",
      after: "",
      pending: null,
      keyboard: null,
    };
    this.s = s;
    this.on.value = true;
    this.focusBox(s);
    const engine = await this.pickEngine(s);
    if (!s.listening) {
      return;
    }
    if (typeof engine === "string") {
      if (engine === "declined") {
        info("The speech model for this language was not installed. Press Dictate to try again.");
      } else {
        this.refuse(engine);
      }
      this.halt(s, "ended");
      return;
    }
    const rec = new this.Rec();
    rec.lang = this.platform.lang;
    rec.continuous = true;
    rec.interimResults = true;
    if (engine.local !== null) {
      rec.processLocally = engine.local;
    }
    this.wire(s, rec, engine.cloud);
    s.rec = rec;
    try {
      rec.start();
    } catch (e) {
      if (e instanceof DOMException && e.name === "NotAllowedError") {
        this.refuse("not-allowed");
      } else {
        errorToast("Dictation could not start.");
      }
      this.halt(s, "ended");
    }
  }

  /** On-device first; the cloud only where the browser has one and on-device cannot serve the language. A browser
   *  without `available()` (Safari) cannot say, so it gets its default engine (`local: null`). */
  private async pickEngine(s: Session): Promise<Engine> {
    const available = this.Rec.available?.bind(this.Rec);
    const install = this.Rec.install?.bind(this.Rec);
    if (available === undefined) {
      return { local: null, cloud: true };
    }
    const opts = (processLocally: boolean): SpeechOptions => ({
      langs: [this.platform.lang],
      processLocally,
      quality: "dictation",
    });
    const here = await availability(available, opts(true));
    if (here === "available") {
      return { local: true, cloud: false };
    }
    if ((here === "downloadable" || here === "downloading") && install !== undefined) {
      const notice = setTimeout(() => {
        if (s.listening) {
          info(
            "Downloading speech recognition for this language. Dictation starts when it is ready.",
          );
        }
      }, DOWNLOAD_NOTICE_DELAY_MS);
      const installed = await install(opts(true))
        .catch(() => false)
        .finally(() => {
          clearTimeout(notice);
        });
      if (installed) {
        return { local: true, cloud: false };
      }
      if (this.platform.cloud === null) {
        return "declined";
      }
    }
    if (this.platform.cloud === null) {
      return here === "refused" ? "service-not-allowed" : "language-not-supported";
    }
    const there = await availability(available, opts(false));
    if (there === "refused") {
      return "service-not-allowed";
    }
    if (there === "unavailable") {
      return "language-not-supported";
    }
    // An installed language pack serves even a processLocally false request (https://issues.chromium.org/issues/521896368),
    // so only "available" says the audio goes to the cloud.
    return { local: false, cloud: there === "available" };
  }

  /** The cloud notice waits for `start`, when audio really flows. */
  private wire(s: Session, rec: SpeechRecognizer, toCloud: boolean): void {
    rec.addEventListener("start", () => {
      s.begun = true;
      if (!s.listening) {
        return;
      }
      const { cloud } = this.platform;
      if (toCloud && cloud !== null && !dictationCloudNoted()) {
        markDictationCloudNoted();
        const notice = `Dictation in this browser sends audio to ${cloud}.`;
        // The toast announces its text, and a later polite announcement replaces a pending one: say both at once.
        info(notice);
        announce(`Dictation started. ${notice}`);
      } else {
        announce("Dictation started");
      }
    });
    rec.addEventListener("result", (e) => {
      this.answered = true;
      if (s.accepting && this.s === s) {
        this.write(s, phraseOf(e as ResultEvent, this.platform.cumulative));
      }
    });
    rec.addEventListener("error", (e) => {
      const code = (e as Event & { readonly error?: unknown }).error;
      if (typeof code === "string" && QUIET.has(code)) {
        return;
      }
      // A service that never answered (Brave: https://github.com/brave/brave-browser/issues/2802) never will.
      if (typeof code === "string" && isRefused(code) && !(code === "network" && this.answered)) {
        this.refuse(code);
      } else {
        errorToast("Dictation stopped: the speech service did not answer.");
      }
      this.halt(s, "ended");
    });
    rec.addEventListener("end", () => {
      s.accepting = false;
      this.retire(s);
      this.unlisten(s);
      this.giveKeyboardBack(s, FOCUS_AFTER.ended);
    });
  }

  private refuse(code: Refusal): void {
    this.off.value = true;
    errorToast(REFUSED[code]);
  }

  /** Replace what the session wrote with `phrase`. Each write first takes the previous one back through the
   *  browser's undo, so the whole session is one undo step. Only `execCommand` keeps that history, and it edits
   *  the focused element only, so a phrase heard while focus is elsewhere waits for the box's `focusin`. */
  private write(s: Session, phrase: string): void {
    const box = this.box;
    if (phrase === "") {
      return;
    }
    // A held send's box is read-only, and `setRangeText` would write into it anyway.
    if (box.readOnly || box.disabled) {
      s.pending = null;
      return;
    }
    s.anchor ??= anchorAt(box);
    const a = s.anchor;
    if (box.value !== (s.written === "" ? a.base : s.after)) {
      s.pending = null;
      if (this.s === s) {
        this.halt(s, "away");
      }
      return;
    }
    if (document.activeElement !== box) {
      s.pending = phrase;
      return;
    }
    const text = a.prefix + phrase + a.suffix;
    if (text === s.written) {
      return;
    }
    const caret = a.start + a.prefix.length + phrase.length;
    this.writing = true;
    try {
      const back = s.written === "" || this.undoTo(a.base);
      applyEdit(box, {
        start: a.start,
        end: back ? a.end : a.start + s.written.length,
        text,
        caret,
      });
    } finally {
      this.writing = false;
    }
    s.written = text;
    s.after = box.value;
  }

  /** Whether the browser's undo took the box back to `base`; redone when it did not land there. */
  private undoTo(base: string): boolean {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- the only undo that keeps the browser's history.
    document.execCommand("undo");
    if (this.box.value === base) {
      return true;
    }
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- the undo's own inverse.
    document.execCommand("redo");
    return false;
  }
}

interface ResultEvent extends Event {
  readonly results: ArrayLike<ArrayLike<{ readonly transcript: string }>>;
}

/** The session's whole transcript: in continuous mode every result event carries the full list so far. In a
 *  `cumulative` list an entry that extends the one before replaces it. */
function phraseOf(e: ResultEvent, cumulative: boolean): string {
  const kept: string[] = [];
  for (const r of Array.from(e.results)) {
    const t = r[0]?.transcript.trim() ?? "";
    const prev = kept.at(-1);
    if (t === "") {
      continue;
    }
    if (cumulative && prev !== undefined && t.startsWith(prev)) {
      kept[kept.length - 1] = t;
    } else {
      kept.push(t);
    }
  }
  return kept.join(" ");
}

function anchorAt(box: HTMLTextAreaElement): Anchor {
  const { selectionStart: start, selectionEnd: end, value } = box;
  const before = value.slice(0, start);
  const after = value.slice(end);
  return {
    start,
    end,
    base: value,
    prefix: before === "" || /\s$/.test(before) ? "" : " ",
    suffix: after === "" || /^\s/.test(after) ? "" : " ",
  };
}

/** A check the browser refuses (Firefox's AI controls answer `NotAllowedError`) is a refusal; any other failed
 *  check counts as no engine. */
function availability(
  check: (o: SpeechOptions) => Promise<SpeechAvailability>,
  o: SpeechOptions,
): Promise<SpeechAvailability | "refused"> {
  return check(o).catch((e: unknown) =>
    e instanceof DOMException && e.name === "NotAllowedError" ? "refused" : "unavailable",
  );
}
