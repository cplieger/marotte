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
}

export interface Dictation {
  /** Whether a press can start dictation: the browser recognizes speech, nothing refused it this page, and the box
   *  takes text. */
  readonly ready: ReadonlySignal<boolean>;
  /** From the press until the session stops. */
  readonly listening: ReadonlySignal<boolean>;
  /** Start, or stop so the words still being recognized land. */
  toggle(): void;
  /** Stop now and drop the words still on their way; what is in the box stays. */
  stop(): void;
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
    readonly SpeechRecognition?: SpeechRecognizerClass;
    readonly webkitSpeechRecognition?: SpeechRecognizerClass;
  };
  return {
    Recognition: g.SpeechRecognition ?? g.webkitSpeechRecognition ?? null,
    lang: navigator.language,
    cloud: cloudServiceFor(navigator),
  };
}

export function createDictation(platform: SpeechPlatform, box: HTMLTextAreaElement): Dictation {
  return new Controller(platform, box);
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
  /** Whether that write went through the browser's editing, so one undo takes it back. */
  native: boolean;
}

type Engine = { readonly local: boolean | null } | "off" | "declined";

const REFUSED: Readonly<Record<string, string>> = {
  "not-allowed":
    "Dictation is off: the browser did not allow the microphone. Allow it for this site and reload the page to try again.",
  "service-not-allowed":
    "Dictation is off: this browser does not allow its speech service here. Reload the page to try again.",
  "audio-capture":
    "Dictation is off: no microphone was found. Connect one and reload the page to try again.",
  "language-not-supported":
    "Dictation is off: this browser cannot recognize the page's language. Reload the page to try again.",
};

/** Codes that end a session with nothing to report: silence, or our own abort. */
const QUIET = new Set(["no-speech", "aborted"]);

class Controller implements Dictation {
  readonly ready: ReadonlySignal<boolean>;
  readonly listening: ReadonlySignal<boolean>;
  private readonly platform: SpeechPlatform;
  private readonly box: HTMLTextAreaElement;
  private readonly off = signal(false);
  private readonly writable = signal(true);
  private readonly on = signal(false);
  /** The session whose results land; at most one. */
  private s: Session | null = null;
  /** Set around this module's own edits, so their `input` events are not read as the reader's. */
  private writing = false;

  constructor(platform: SpeechPlatform, box: HTMLTextAreaElement) {
    this.platform = platform;
    this.box = box;
    this.listening = this.on;
    this.ready = computed(
      () => platform.Recognition !== null && !this.off.value && this.writable.value,
    );
    this.syncWritable();
    new MutationObserver(() => {
      this.syncWritable();
    }).observe(box, { attributes: true, attributeFilter: ["disabled", "readonly"] });
    box.addEventListener("input", () => {
      if (!this.writing) {
        this.stop();
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
        this.stop();
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
        this.halt(s);
        return;
      }
      s.rec.stop();
      this.focusBox();
      this.unlisten(s);
      return;
    }
    if (this.ready.peek()) {
      void this.begin();
    }
  }

  stop(): void {
    if (this.s !== null) {
      this.halt(this.s);
    }
  }

  private syncWritable(): void {
    this.writable.value = !this.box.disabled && !this.box.readOnly;
    if (!this.writable.peek()) {
      this.stop();
    }
  }

  /** Under a finger, focusing the box would raise the keyboard over the words. */
  private focusBox(): void {
    if (!touchComposer()) {
      this.box.focus();
    }
  }

  private halt(s: Session): void {
    s.accepting = false;
    s.rec?.abort();
    if (this.s === s) {
      this.s = null;
    }
    this.unlisten(s);
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
    const Rec = this.platform.Recognition;
    if (Rec === null) {
      return;
    }
    this.stop();
    const s: Session = {
      rec: null,
      listening: true,
      accepting: true,
      begun: false,
      anchor: null,
      written: "",
      after: "",
      native: false,
    };
    this.s = s;
    this.on.value = true;
    this.focusBox();
    const engine = await this.pickEngine(Rec);
    if (!s.listening) {
      return;
    }
    if (engine === "off" || engine === "declined") {
      if (engine === "off") {
        this.off.value = true;
        errorToast("Dictation is not available for the page's language in this browser.");
      } else {
        info("The speech model for this language was not installed. Press Dictate to try again.");
      }
      this.halt(s);
      return;
    }
    const rec = new Rec();
    rec.lang = this.platform.lang;
    rec.continuous = true;
    rec.interimResults = true;
    if (engine.local !== null) {
      rec.processLocally = engine.local;
    }
    this.wire(s, rec);
    s.rec = rec;
    try {
      rec.start();
    } catch (e) {
      if (e instanceof DOMException && e.name === "NotAllowedError") {
        this.refuse("not-allowed");
      } else {
        errorToast("Dictation could not start.");
      }
      this.halt(s);
      return;
    }
    const { cloud } = this.platform;
    if (engine.local !== true && cloud !== null && !dictationCloudNoted()) {
      markDictationCloudNoted();
      info(`Dictation in this browser sends audio to ${cloud}.`);
    }
  }

  /** On-device first; the cloud only where the browser has one and on-device cannot serve the language. A browser
   *  without `available()` (Safari) cannot say, so it gets its default engine (`local: null`). */
  private async pickEngine(Rec: SpeechRecognizerClass): Promise<Engine> {
    if (Rec.available === undefined) {
      return { local: null };
    }
    const opts = (processLocally: boolean): SpeechOptions => ({
      langs: [this.platform.lang],
      processLocally,
      quality: "dictation",
    });
    const here = await availability(Rec, opts(true));
    if (here === "available") {
      return { local: true };
    }
    if (here !== "unavailable" && Rec.install !== undefined) {
      info("Downloading speech recognition for this language. Dictation starts when it is ready.");
      if (await Rec.install(opts(true)).catch(() => false)) {
        return { local: true };
      }
      if (this.platform.cloud === null) {
        return "declined";
      }
    }
    if (this.platform.cloud === null) {
      return "off";
    }
    return (await availability(Rec, opts(false))) === "unavailable" ? "off" : { local: false };
  }

  private wire(s: Session, rec: SpeechRecognizer): void {
    rec.addEventListener("start", () => {
      s.begun = true;
      if (s.listening) {
        announce("Dictation started");
      }
    });
    rec.addEventListener("result", (e) => {
      if (s.accepting && this.s === s) {
        this.write(s, phraseOf(e as ResultEvent));
      }
    });
    rec.addEventListener("error", (e) => {
      const code = (e as Event & { readonly error?: unknown }).error;
      if (typeof code === "string" && QUIET.has(code)) {
        return;
      }
      if (typeof code === "string" && Object.hasOwn(REFUSED, code)) {
        this.refuse(code);
      } else {
        errorToast("Dictation stopped: the speech service did not answer.");
      }
      this.halt(s);
    });
    rec.addEventListener("end", () => {
      s.accepting = false;
      if (this.s === s) {
        this.s = null;
      }
      this.unlisten(s);
    });
  }

  private refuse(code: string): void {
    this.off.value = true;
    errorToast(REFUSED[code] ?? "Dictation is off. Reload the page to try again.");
  }

  /** Replace what the session wrote with `phrase`. Each write first takes the previous one back through the
   *  browser's undo, so the whole session is one undo step. */
  private write(s: Session, phrase: string): void {
    const box = this.box;
    if (phrase === "") {
      return;
    }
    if (s.written !== "" && box.value !== s.after) {
      this.halt(s);
      return;
    }
    s.anchor ??= anchorAt(box);
    const a = s.anchor;
    const text = a.prefix + phrase + a.suffix;
    if (text === s.written) {
      return;
    }
    const caret = a.start + a.prefix.length + phrase.length;
    this.writing = true;
    try {
      const back = s.written === "" || this.undoTo(s, a.base);
      applyEdit(box, {
        start: a.start,
        end: back ? a.end : a.start + s.written.length,
        text,
        caret,
      });
    } finally {
      this.writing = false;
    }
    s.native = document.activeElement === box;
    s.written = text;
    s.after = box.value;
  }

  /** Whether the browser's undo took the box back to `base`; redone when it did not land there. */
  private undoTo(s: Session, base: string): boolean {
    if (!s.native || document.activeElement !== this.box) {
      return false;
    }
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

/** The session's whole transcript: in continuous mode every result event carries the full list so far. */
function phraseOf(e: ResultEvent): string {
  return Array.from(e.results, (r) => r[0]?.transcript.trim() ?? "")
    .filter((t) => t !== "")
    .join(" ");
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

/** A refused check (Firefox's AI controls answer `NotAllowedError`) counts as no engine. */
function availability(Rec: SpeechRecognizerClass, o: SpeechOptions): Promise<SpeechAvailability> {
  return Rec.available === undefined
    ? Promise.resolve("unavailable")
    : Rec.available(o).catch(() => "unavailable" as const);
}
