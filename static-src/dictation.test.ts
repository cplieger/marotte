// The dictation controller against a fake recognizer injected at the platform seam: where words land, when it
// stops, what it tells the reader, which engine it picks, and that it never sends.

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { mountLiveRegions } from "@cplieger/ui/announce";
import {
  browserSpeechPlatform,
  cloudServiceFor,
  createDictation,
  type Dictation,
} from "./dictation.js";
import { fakeSpeech, settle, type FakeSpeech } from "./__test-helpers__/fake-speech.js";
import { _resetForTest as resetToasts } from "./toast.js";
import { BUS_TAB_CHANGED, emitBus } from "./bus.js";
import { setActive, setSessions } from "./store.js";
import { dictationCloudNoted } from "./device-view.js";
import type { Session } from "./types.js";

let box: HTMLTextAreaElement;
let releaseRegions: () => void = () => undefined;

beforeAll(() => {
  releaseRegions = mountLiveRegions(document.body);
  return () => {
    releaseRegions();
  };
});

beforeEach(() => {
  box = document.createElement("textarea");
  document.body.append(box);
  setSessions([session("c1"), session("c2")]);
  setActive("c1");
});

afterEach(() => {
  box.remove();
  document.documentElement.removeAttribute("data-pointer");
  resetToasts();
  vi.unstubAllGlobals();
  localStorage.clear();
});

function session(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** A browser that serves this language on-device, the common Chrome 139+ case. */
function onDevice(): FakeSpeech {
  return fakeSpeech({
    available: () => Promise.resolve("available"),
    install: () => Promise.resolve(true),
  });
}

/** Start dictating and let the engine open the microphone. */
async function listen(speech: FakeSpeech, d: Dictation): Promise<void> {
  d.toggle();
  await settle();
  speech.last().begin();
}

function toastTexts(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".uip-toast-msg")].map((n) => n.textContent);
}

function politeRegion(): HTMLElement {
  const r = document.querySelector<HTMLElement>('[aria-live="polite"]');
  if (r === null) {
    throw new Error("no polite live region");
  }
  return r;
}

/** Every text the polite region shows from now on, for an announcement that lands after a delay. */
function recordAnnouncements(): string[] {
  const seen: string[] = [];
  const region = politeRegion();
  new MutationObserver(() => {
    if (region.textContent !== "") {
      seen.push(region.textContent);
    }
  }).observe(region, { childList: true, characterData: true, subtree: true });
  return seen;
}

describe("support (rule 4)", () => {
  it("is off where the browser has neither SpeechRecognition nor webkitSpeechRecognition", () => {
    vi.stubGlobal("SpeechRecognition", undefined);
    vi.stubGlobal("webkitSpeechRecognition", undefined);
    const platform = browserSpeechPlatform();
    expect(platform.Recognition).toBeNull();
    expect(createDictation(platform, box).ready.value).toBe(false);
  });

  it("takes the prefixed constructor where only that exists", () => {
    const speech = fakeSpeech();
    vi.stubGlobal("SpeechRecognition", undefined);
    vi.stubGlobal("webkitSpeechRecognition", speech.platform.Recognition);
    expect(browserSpeechPlatform().Recognition).toBe(speech.platform.Recognition);
  });

  it("is off on an insecure origin, where the microphone never opens", () => {
    const speech = fakeSpeech();
    vi.stubGlobal("isSecureContext", false);
    vi.stubGlobal("SpeechRecognition", speech.platform.Recognition);
    vi.stubGlobal("webkitSpeechRecognition", speech.platform.Recognition);
    expect(browserSpeechPlatform().Recognition).toBeNull();
  });

  it("prefers the unprefixed constructor", () => {
    const a = fakeSpeech();
    const b = fakeSpeech();
    vi.stubGlobal("SpeechRecognition", a.platform.Recognition);
    vi.stubGlobal("webkitSpeechRecognition", b.platform.Recognition);
    expect(browserSpeechPlatform().Recognition).toBe(a.platform.Recognition);
  });

  it("is ready with a recognizer and a writable box", () => {
    expect(createDictation(onDevice().platform, box).ready.value).toBe(true);
  });

  it("makes no recognizer and does nothing when unsupported", () => {
    const d = createDictation(
      { Recognition: null, lang: "en-US", cloud: null, cumulative: false },
      box,
    );
    d.toggle();
    expect(d.listening.value).toBe(false);
  });
});

describe("language (rule 7)", () => {
  it("follows the page's language in the browser platform", () => {
    Object.defineProperty(navigator, "language", { configurable: true, value: "fr-FR" });
    try {
      expect(browserSpeechPlatform().lang).toBe("fr-FR");
    } finally {
      delete (navigator as { language?: string }).language;
    }
  });

  it("recognizes, checks and installs in the platform's language", async () => {
    const speech = fakeSpeech({
      lang: "fr-FR",
      available: () => Promise.resolve("downloadable"),
      install: () => Promise.resolve(true),
    });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    expect(speech.last().lang).toBe("fr-FR");
    expect(speech.availableCalls[0]?.langs).toEqual(["fr-FR"]);
    expect(speech.installCalls[0]?.langs).toEqual(["fr-FR"]);
  });
});

describe("listening and results (rule 3)", () => {
  it("listens continuously with interim results", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().started).toBe(1);
    expect(speech.last().continuous).toBe(true);
    expect(speech.last().interimResults).toBe(true);
  });

  it("is listening from the press, before the engine answers", () => {
    const d = createDictation(onDevice().platform, box);
    d.toggle();
    expect(d.listening.value).toBe(true);
  });

  it("shows interim words in the box and replaces them with the final result", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hel", false]);
    expect(box.value).toBe("hel");
    speech.last().hear(["hello wor", false]);
    expect(box.value).toBe("hello wor");
    speech.last().hear(["hello world", true]);
    expect(box.value).toBe("hello world");
    expect(d.listening.value, "words never end dictation").toBe(true);
  });

  it("joins the engine's results into one spaced phrase", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", true], [" world", false]);
    expect(box.value).toBe("hello world");
  });

  it("keeps a cumulative list (Chrome on Android) from repeating words", async () => {
    const speech = fakeSpeech({ cumulative: true, available: () => Promise.resolve("available") });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", true], ["hello world", true]);
    expect(box.value).toBe("hello world");
  });

  it("keeps distinct segments on a list that is not cumulative", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["no", true], ["no way", true]);
    expect(box.value).toBe("no no way");
  });

  it("reads a cumulative list from an Android user agent only", () => {
    const ua = (value: string): boolean => {
      Object.defineProperty(navigator, "userAgent", { configurable: true, value });
      try {
        return browserSpeechPlatform().cumulative;
      } finally {
        delete (navigator as { userAgent?: string }).userAgent;
      }
    };
    expect(ua("Mozilla/5.0 (Linux; Android 10; K) Chrome/150.0 Mobile Safari/537.36")).toBe(true);
    expect(ua("Mozilla/5.0 (X11; Linux x86_64) Chrome/150.0 Safari/537.36")).toBe(false);
  });

  it("stops on a second press and keeps the final result that follows", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello wor", false]);
    d.toggle();
    expect(speech.last().stopped).toBe(1);
    expect(d.listening.value).toBe(false);
    speech.last().hear(["hello world", true]);
    speech.last().finish();
    expect(box.value).toBe("hello world");
  });

  it("ends when the browser ends it", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(box.value).toBe("hello");
  });

  it("ends quietly when nobody spoke", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().fail("no-speech");
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(true);
    expect(toastTexts()).toEqual([]);
  });

  it("ends on a network error after words arrived and says so, staying available", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", false]);
    speech.last().fail("network");
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(true);
    expect(toastTexts()).toEqual(["Dictation stopped: the speech service did not answer."]);
  });

  it("turns off for the page on a network error before any result (Brave)", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().fail("network");
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(false);
    const [text, ...rest] = toastTexts();
    expect(rest).toEqual([]);
    expect(text?.toLowerCase()).toContain("did not answer");
    expect(text?.toLowerCase()).toContain("reload");
  });

  it("stays available on a network error before any result once an earlier session heard words", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    d.toggle();
    speech.last().finish();
    await listen(speech, d);
    speech.last().fail("network");
    speech.last().finish();
    expect(d.ready.value).toBe(true);
    expect(toastTexts()).toEqual(["Dictation stopped: the speech service did not answer."]);
  });

  it("starts a fresh recognizer on the next press", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    d.toggle();
    speech.last().finish();
    await listen(speech, d);
    expect(speech.made).toHaveLength(2);
    expect(speech.last().started).toBe(1);
  });

  it("stops listening when the reader edits the box mid-dictation, and never overwrites the edit", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", false]);
    box.value = "hello there";
    box.dispatchEvent(new Event("input", { bubbles: true }));
    speech.last().hear(["hello world", true]);
    expect(box.value).toBe("hello there");
    expect(d.listening.value).toBe(false);
  });
});

describe("where the words go (rule 9)", () => {
  it("inserts at the caret, spaced from the text on both sides", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    box.value = "before after";
    box.setSelectionRange(6, 6);
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    expect(box.value).toBe("before hello after");
  });

  it("adds no second space where the caret already sits after one", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    box.value = "before after";
    box.setSelectionRange(7, 7);
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    expect(box.value).toBe("before hello after");
  });

  it("replaces a selection", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    box.value = "keep this part";
    box.setSelectionRange(5, 9);
    await listen(speech, d);
    speech.last().hear(["that", true]);
    expect(box.value).toBe("keep that part");
  });

  it("puts focus in the box, so the words land as editing the reader can undo", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    const btn = document.createElement("button");
    document.body.append(btn);
    btn.focus();
    try {
      await listen(speech, d);
      expect(document.activeElement).toBe(box);
    } finally {
      btn.remove();
    }
  });

  it("is taken back by one undo", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    box.value = "before after";
    box.focus();
    box.setSelectionRange(7, 7);
    await listen(speech, d);
    speech.last().hear(["hel", false]);
    speech.last().hear(["hello wor", false]);
    speech.last().hear(["hello world", true]);
    expect(box.value).toBe("before hello world after");
    document.execCommand("undo");
    expect(box.value).toBe("before after");
  });

  describe("under a finger", () => {
    function pressFrom(): HTMLButtonElement {
      document.documentElement.setAttribute("data-pointer", "coarse");
      const btn = document.createElement("button");
      document.body.append(btn);
      btn.focus();
      return btn;
    }

    it("keeps the keyboard away while listening, and one undo takes the session back", async () => {
      const btn = pressFrom();
      try {
        const speech = onDevice();
        const d = createDictation(speech.platform, box);
        box.value = "before after";
        box.setSelectionRange(7, 7);
        await listen(speech, d);
        expect(box.inputMode).toBe("none");
        expect(document.activeElement).toBe(box);
        speech.last().hear(["hel", false]);
        speech.last().hear(["hello wor", false]);
        speech.last().hear(["hello world", true]);
        expect(box.value).toBe("before hello world after");
        d.toggle();
        speech.last().finish();
        expect(document.activeElement, "focus goes back to the button").toBe(btn);
        expect(box.hasAttribute("inputmode")).toBe(false);
        box.focus();
        document.execCommand("undo");
        expect(box.value).toBe("before after");
      } finally {
        btn.remove();
      }
    });

    it("gives back the box's own inputmode when the browser ends the session", async () => {
      const btn = pressFrom();
      try {
        box.setAttribute("inputmode", "text");
        const speech = onDevice();
        const d = createDictation(speech.platform, box);
        await listen(speech, d);
        speech.last().fail("no-speech");
        speech.last().finish();
        expect(box.getAttribute("inputmode")).toBe("text");
        expect(document.activeElement).toBe(btn);
      } finally {
        btn.remove();
      }
    });

    it("hands the keyboard back at once when the reader taps the box, and leaves focus there", async () => {
      const btn = pressFrom();
      try {
        const speech = onDevice();
        const d = createDictation(speech.platform, box);
        await listen(speech, d);
        box.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
        expect(box.hasAttribute("inputmode")).toBe(false);
        expect(d.listening.value, "a tap is not an edit").toBe(true);
        d.stop();
        expect(document.activeElement).toBe(box);
      } finally {
        btn.remove();
      }
    });

    it("keeps the keyboard the reader took back through Stop and the end", async () => {
      const btn = pressFrom();
      try {
        const speech = onDevice();
        const d = createDictation(speech.platform, box);
        await listen(speech, d);
        box.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
        d.toggle();
        expect(box.hasAttribute("inputmode")).toBe(false);
        speech.last().finish();
        expect(box.hasAttribute("inputmode")).toBe(false);
        expect(document.activeElement).toBe(box);
      } finally {
        btn.remove();
      }
    });

    it("keeps the box focused at the end when it was focused at the press", async () => {
      document.documentElement.setAttribute("data-pointer", "coarse");
      box.focus();
      const speech = onDevice();
      const d = createDictation(speech.platform, box);
      await listen(speech, d);
      expect(box.inputMode).toBe("none");
      d.stop();
      expect(document.activeElement).toBe(box);
      expect(box.hasAttribute("inputmode")).toBe(false);
    });
  });

  it("announces the committed text to the draft layer with an input event", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    const seen: string[] = [];
    box.addEventListener("input", () => {
      seen.push(box.value);
    });
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    expect(seen.at(-1)).toBe("hello");
  });
});

describe("what stops it and keeps the words (rule 9)", () => {
  async function dictatingWithWords(): Promise<{ speech: FakeSpeech; d: Dictation }> {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["half a thought", false]);
    return { speech, d };
  }

  function expectStoppedKeeping(speech: FakeSpeech, d: Dictation): void {
    expect(d.listening.value).toBe(false);
    expect(speech.last().aborted + speech.last().stopped).toBeGreaterThan(0);
    expect(box.value).toBe("half a thought");
    speech.last().hear(["half a thought, late", true]);
    expect(box.value, "a late result never lands").toBe("half a thought");
  }

  it("a tab switch", async () => {
    const { speech, d } = await dictatingWithWords();
    emitBus(BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    expectStoppedKeeping(speech, d);
  });

  it("a chat switch", async () => {
    const { speech, d } = await dictatingWithWords();
    setActive("c2");
    expectStoppedKeeping(speech, d);
  });

  it("keeps a late result out of the next chat's box", async () => {
    const { speech } = await dictatingWithWords();
    setActive("c2");
    // The draft layer restores the next chat's own draft into the same box.
    box.value = "the other chat's draft";
    speech.last().hear(["half a thought, late", true]);
    expect(box.value).toBe("the other chat's draft");
  });

  it("a page hide", async () => {
    const { speech, d } = await dictatingWithWords();
    window.dispatchEvent(new PageTransitionEvent("pagehide", { persisted: true }));
    expectStoppedKeeping(speech, d);
  });

  it("keeps a phrase held for focus out of the next chat's box, even when both drafts are empty", async () => {
    const other = document.createElement("button");
    document.body.append(other);
    try {
      const speech = onDevice();
      const d = createDictation(speech.platform, box);
      await listen(speech, d);
      other.focus();
      speech.last().hear(["for the first chat", false]);
      setActive("c2");
      expect(d.listening.value).toBe(false);
      box.focus();
      expect(box.value).toBe("");
    } finally {
      other.remove();
    }
  });

  it("the page going to the background", async () => {
    const { speech, d } = await dictatingWithWords();
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    try {
      document.dispatchEvent(new Event("visibilitychange"));
    } finally {
      delete (document as { visibilityState?: unknown }).visibilityState;
    }
    expectStoppedKeeping(speech, d);
  });

  it("stop()", async () => {
    const { speech, d } = await dictatingWithWords();
    d.stop();
    expectStoppedKeeping(speech, d);
  });

  it("the composer being disabled, and it does not offer itself while disabled", async () => {
    const { speech, d } = await dictatingWithWords();
    box.disabled = true;
    await settle();
    expectStoppedKeeping(speech, d);
    expect(d.ready.value).toBe(false);
    d.toggle();
    expect(d.listening.value).toBe(false);
    box.disabled = false;
    await settle();
    expect(d.ready.value).toBe(true);
  });

  it("the composer going read-only", async () => {
    const { speech, d } = await dictatingWithWords();
    box.readOnly = true;
    await settle();
    expectStoppedKeeping(speech, d);
    expect(d.ready.value).toBe(false);
  });

  it("does not start in a disabled composer", () => {
    const speech = onDevice();
    box.disabled = true;
    const d = createDictation(speech.platform, box);
    expect(d.ready.value).toBe(false);
    d.toggle();
    expect(speech.made).toHaveLength(0);
  });

  it("a stop during the availability check starts nothing", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    d.toggle();
    d.toggle();
    await settle();
    expect(d.listening.value).toBe(false);
    expect(speech.made.every((r) => r.started === 0)).toBe(true);
  });
});

describe("focus outside the box (rule 9: undo works)", () => {
  let other: HTMLButtonElement;

  beforeEach(() => {
    other = document.createElement("button");
    document.body.append(other);
  });

  afterEach(() => {
    other.remove();
  });

  it("keeps listening, holds what it hears until focus comes back, then one undo takes the session back", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["hello", true]);
    other.focus();
    expect(d.listening.value).toBe(true);
    speech.last().hear(["hello", true], ["there", false]);
    expect(box.value, "nothing lands in an unfocused box").toBe("hello");
    d.toggle();
    expect(document.activeElement).toBe(box);
    expect(box.value).toBe("hello there");
    speech.last().hear(["hello", true], ["there friend", true]);
    speech.last().finish();
    expect(box.value).toBe("hello there friend");
    document.execCommand("undo");
    expect(box.value).toBe("");
  });

  it("keeps text typed before the session undoable", async () => {
    box.focus();
    document.execCommand("insertText", false, "typed");
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().hear(["one", false]);
    other.focus();
    speech.last().hear(["one two", true]);
    box.focus();
    expect(box.value).toBe("typed one two");
    d.stop();
    document.execCommand("undo");
    expect(box.value).toBe("typed");
    document.execCommand("undo");
    expect(box.value).toBe("");
  });

  it("lands the last phrase when focus comes back after the browser ended the session", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said while away", true]);
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(box.value).toBe("");
    box.focus();
    expect(box.value).toBe("said while away");
    document.execCommand("undo");
    expect(box.value).toBe("");
  });

  it("keeps a held phrase through a tab switch, landing it when focus comes back", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["before the switch", true]);
    emitBus(BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    expect(d.listening.value).toBe(false);
    box.focus();
    expect(box.value).toBe("before the switch");
  });

  it("drops the held phrase on drop(), the send path's call, and listens no more", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said before the send", true]);
    d.drop();
    expect(d.listening.value).toBe(false);
    box.focus();
    expect(box.value).toBe("");
  });

  it("drops a phrase held after the browser ended the session on drop()", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said while away", true]);
    speech.last().finish();
    d.drop();
    box.focus();
    expect(box.value).toBe("");
  });

  it("drops the held phrase when the composer is disabled meanwhile", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said before the bot draft", true]);
    box.disabled = true;
    await settle();
    box.disabled = false;
    await settle();
    box.focus();
    expect(box.value).toBe("");
  });

  it("never writes a held phrase into a read-only box, even before the box's change is seen", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said off the box", true]);
    speech.last().finish();
    box.readOnly = true;
    box.focus();
    expect(box.value).toBe("");
    box.readOnly = false;
    await settle();
    other.focus();
    box.focus();
    expect(box.value).toBe("");
  });

  it("drops the held phrase on a clear, even of a box that was already empty", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["said before the clear", true]);
    box.value = "";
    box.dispatchEvent(new Event("input", { bubbles: true }));
    expect(d.listening.value).toBe(false);
    box.focus();
    expect(box.value).toBe("");
  });

  it("drops the held phrase when the box changed meanwhile", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["stale words", true]);
    box.value = "a restored draft";
    box.focus();
    expect(box.value).toBe("a restored draft");
    expect(d.listening.value).toBe(false);
  });

  it("lands an earlier session's held phrase before the next session starts", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    other.focus();
    speech.last().hear(["first", true]);
    speech.last().finish();
    await listen(speech, d);
    expect(box.value).toBe("first");
    speech.last().hear(["second", true]);
    expect(box.value).toBe("first second");
  });
});

describe("never sends (rule 3)", () => {
  it("leaves the text in the box through a final result, the end and an error", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    const form = document.createElement("form");
    form.append(box);
    document.body.append(form);
    const submits: Event[] = [];
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      submits.push(e);
    });
    const enters: KeyboardEvent[] = [];
    box.addEventListener("keydown", (e) => {
      enters.push(e);
    });
    try {
      await listen(speech, d);
      speech.last().hear(["send this", true]);
      speech.last().fail("network");
      speech.last().finish();
      expect(box.value).toBe("send this");
      expect(submits).toEqual([]);
      expect(enters).toEqual([]);
    } finally {
      form.remove();
    }
  });
});

describe("permission and microphone (rule 5)", () => {
  it.each([
    ["not-allowed", "microphone"],
    ["service-not-allowed", "speech"],
    ["audio-capture", "microphone"],
    ["language-not-supported", "language"],
  ])("on %s says so in plain words, names the reload, and stays off", async (code, word) => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    speech.last().fail(code);
    speech.last().finish();
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(false);
    const [text, ...rest] = toastTexts();
    expect(rest).toEqual([]);
    expect(text?.toLowerCase()).toContain(word);
    expect(text?.toLowerCase()).toContain("reload");
    d.toggle();
    expect(speech.made, "no second attempt this page").toHaveLength(1);
  });

  it("stays off when the engine refuses at start", async () => {
    const speech = fakeSpeech({ startThrows: new DOMException("blocked", "NotAllowedError") });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(false);
    expect(toastTexts()).toHaveLength(1);
  });
});

describe("on-device first (rule 6)", () => {
  it("runs on-device where the language is installed, with no notice", async () => {
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.availableCalls[0]).toEqual({
      langs: ["en-US"],
      processLocally: true,
      quality: "dictation",
    });
    expect(speech.last().processLocally).toBe(true);
    expect(speech.installCalls).toEqual([]);
    expect(toastTexts()).toEqual([]);
  });

  it("installs a downloadable language, says it is downloading while the install runs, then runs on-device", async () => {
    let installed: (ok: boolean) => void = () => undefined;
    const speech = fakeSpeech({
      available: () => Promise.resolve("downloadable"),
      install: () =>
        new Promise((resolve) => {
          installed = resolve;
        }),
    });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await vi.waitFor(
      () => {
        expect(toastTexts().join(" ").toLowerCase()).toContain("download");
      },
      { timeout: 3000 },
    );
    expect(speech.made).toEqual([]);
    installed(true);
    await settle();
    speech.last().begin();
    expect(speech.installCalls).toEqual([
      { langs: ["en-US"], processLocally: true, quality: "dictation" },
    ]);
    expect(speech.last().processLocally).toBe(true);
    expect(speech.last().started).toBe(1);
    expect(toastTexts().join(" ")).not.toContain("Google");
  });

  it("says nothing of a download when the install resolves at once (an installed pack available() masks)", async () => {
    const speech = fakeSpeech({
      available: () => Promise.resolve("downloadable"),
      install: () => Promise.resolve(true),
    });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().processLocally).toBe(true);
    expect(speech.last().started).toBe(1);
    await new Promise((resolve) => {
      setTimeout(resolve, 1000);
    });
    expect(toastTexts()).toEqual([]);
  });

  it("shows no downloading toast once the reader stopped during the install", async () => {
    let installed: (ok: boolean) => void = () => undefined;
    const speech = fakeSpeech({
      available: () => Promise.resolve("downloadable"),
      install: () =>
        new Promise((resolve) => {
          installed = resolve;
        }),
    });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    d.toggle();
    await new Promise((resolve) => {
      setTimeout(resolve, 1000);
    });
    expect(toastTexts()).toEqual([]);
    installed(true);
    await settle();
    expect(speech.made).toEqual([]);
  });

  it("falls back to the cloud with a one-line notice when the install fails", async () => {
    const speech = fakeSpeech({
      available: (o) => Promise.resolve(o.processLocally ? "downloadable" : "available"),
      install: () => Promise.resolve(false),
    });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().processLocally).toBe(false);
    expect(speech.last().started).toBe(1);
    expect(toastTexts()).toContain(
      "Dictation in this browser sends audio to Google's speech service.",
    );
  });

  it("falls back to the cloud with the notice when on-device is unavailable", async () => {
    const speech = fakeSpeech({
      cloud: "Microsoft's speech service",
      available: (o) => Promise.resolve(o.processLocally ? "unavailable" : "available"),
    });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().processLocally).toBe(false);
    expect(toastTexts()).toEqual([
      "Dictation in this browser sends audio to Microsoft's speech service.",
    ]);
  });

  it.each(["downloadable", "downloading"] as const)(
    "gives no cloud notice when the fallback check answers %s, since an installed pack may still serve it",
    async (answer) => {
      const speech = fakeSpeech({
        available: (o) => Promise.resolve(o.processLocally ? "unavailable" : answer),
      });
      const d = createDictation(speech.platform, box);
      await listen(speech, d);
      expect(speech.last().processLocally).toBe(false);
      expect(speech.last().started).toBe(1);
      expect(toastTexts().join(" ")).not.toContain("sends audio");
      expect(dictationCloudNoted()).toBe(false);
    },
  );

  it("shows the notice only once the microphone opens, so a denied first press spends nothing", async () => {
    const shape = {
      available: (o: { processLocally: boolean }) =>
        Promise.resolve(o.processLocally ? ("unavailable" as const) : ("available" as const)),
    };
    const denied = fakeSpeech(shape);
    createDictation(denied.platform, box).toggle();
    await settle();
    expect(toastTexts(), "nothing before the start event").toEqual([]);
    denied.last().fail("not-allowed");
    denied.last().finish();
    expect(dictationCloudNoted()).toBe(false);
    expect(toastTexts().join(" ")).not.toContain("sends audio");
    resetToasts();
    const next = fakeSpeech(shape);
    await listen(next, createDictation(next.platform, box));
    expect(toastTexts()).toEqual([
      "Dictation in this browser sends audio to Google's speech service.",
    ]);
    expect(dictationCloudNoted()).toBe(true);
  });

  it("falls back to the cloud with the notice when the install rejects", async () => {
    const speech = fakeSpeech({
      available: (o) => Promise.resolve(o.processLocally ? "downloadable" : "available"),
      install: () => Promise.reject(new Error("install failed")),
    });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().processLocally).toBe(false);
    expect(speech.last().started).toBe(1);
    expect(toastTexts()).toContain(
      "Dictation in this browser sends audio to Google's speech service.",
    );
  });

  it("says the browser does not allow its speech service when an on-device-only check is refused", async () => {
    const speech = fakeSpeech({
      cloud: null,
      available: () => Promise.reject(new DOMException("blocked", "NotAllowedError")),
    });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    expect(speech.made).toHaveLength(0);
    expect(d.ready.value).toBe(false);
    expect(toastTexts()).toEqual([
      "Dictation is off: this browser does not allow its speech service here. Reload the page to try again.",
    ]);
  });

  it("treats a refused availability check as no on-device engine", async () => {
    const speech = fakeSpeech({
      available: (o) =>
        o.processLocally
          ? Promise.reject(new DOMException("blocked", "NotAllowedError"))
          : Promise.resolve("available"),
    });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().processLocally).toBe(false);
    expect(speech.last().started).toBe(1);
  });

  it("gives the cloud notice once per device", async () => {
    const shape = {
      available: (o: { processLocally: boolean }) =>
        Promise.resolve(o.processLocally ? ("unavailable" as const) : ("available" as const)),
    };
    const first = fakeSpeech(shape);
    const d = createDictation(first.platform, box);
    await listen(first, d);
    d.toggle();
    first.last().finish();
    resetToasts();
    await listen(first, d);
    const second = fakeSpeech(shape);
    await listen(second, createDictation(second.platform, box));
    expect(toastTexts()).toEqual([]);
  });

  it("takes the cloud with the notice where the browser cannot say (Safari)", async () => {
    const speech = fakeSpeech({ cloud: "Apple's speech service" });
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    expect(speech.last().started).toBe(1);
    expect(toastTexts()).toEqual([
      "Dictation in this browser sends audio to Apple's speech service.",
    ]);
  });

  it("stays off for the page when no engine serves the language, and names the reload", async () => {
    const speech = fakeSpeech({ available: () => Promise.resolve("unavailable") });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    expect(speech.made.every((r) => r.started === 0)).toBe(true);
    expect(d.listening.value).toBe(false);
    expect(d.ready.value).toBe(false);
    const [text, ...rest] = toastTexts();
    expect(rest).toEqual([]);
    expect(text?.toLowerCase()).toContain("language");
    expect(text?.toLowerCase()).toContain("reload");
  });

  it("never takes a cloud path on an on-device-only browser (Firefox), and lets a declined install retry", async () => {
    const speech = fakeSpeech({
      cloud: null,
      available: () => Promise.resolve("downloadable"),
      install: () => Promise.resolve(false),
    });
    const d = createDictation(speech.platform, box);
    d.toggle();
    await settle();
    expect(speech.made.every((r) => r.started === 0)).toBe(true);
    expect(speech.availableCalls.every((o) => o.processLocally)).toBe(true);
    expect(d.listening.value).toBe(false);
    expect(d.ready.value, "the next press asks again").toBe(true);
    expect(toastTexts().join(" ")).not.toContain("sends audio");
  });

  it("names each browser's cloud service", () => {
    const table: readonly [Parameters<typeof cloudServiceFor>[0], string | null][] = [
      [
        {
          userAgentData: { brands: [{ brand: "Not.A/Brand" }, { brand: "Google Chrome" }] },
          vendor: "Google Inc.",
          userAgent: "Mozilla/5.0 Chrome/150.0",
        },
        "Google's speech service",
      ],
      [
        {
          userAgentData: { brands: [{ brand: "Chromium" }, { brand: "Microsoft Edge" }] },
          vendor: "Google Inc.",
          userAgent: "Mozilla/5.0 Chrome/150.0 Edg/150.0",
        },
        "Microsoft's speech service",
      ],
      [
        {
          vendor: "Apple Computer, Inc.",
          userAgent: "Mozilla/5.0 (Macintosh) Version/26.0 Safari/605.1.15",
        },
        "Apple's speech service",
      ],
      [{ vendor: "", userAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox/160.0" }, null],
      [
        {
          userAgentData: { brands: [{ brand: "Chromium" }, { brand: "Not.A/Brand" }] },
          vendor: "Google Inc.",
          userAgent: "Mozilla/5.0 Chrome/150.0",
        },
        "an online speech service",
      ],
    ];
    for (const [nav, service] of table) {
      expect(cloudServiceFor(nav), nav.userAgent).toBe(service);
    }
  });
});

describe("announcements (rule 8)", () => {
  it("announces the start and the stop, and never the words", async () => {
    const seen = recordAnnouncements();
    const speech = onDevice();
    const d = createDictation(speech.platform, box);
    await listen(speech, d);
    await vi.waitFor(() => {
      expect(seen).toContain("Dictation started");
    });
    speech.last().hear(["secret words", false]);
    speech.last().hear(["secret words", true]);
    d.toggle();
    speech.last().finish();
    await vi.waitFor(() => {
      expect(seen).toContain("Dictation stopped");
    });
    expect(seen.join(" ")).not.toContain("secret");
  });

  it("announces the start with the cloud notice on first use, so neither replaces the other", async () => {
    const seen = recordAnnouncements();
    const speech = fakeSpeech({
      available: (o) => Promise.resolve(o.processLocally ? "unavailable" : "available"),
    });
    await listen(speech, createDictation(speech.platform, box));
    await vi.waitFor(() => {
      expect(seen.some((t) => t.includes("Dictation started"))).toBe(true);
    });
    expect(seen.join(" ")).toContain("sends audio to Google's speech service");
    expect(toastTexts()).toEqual([
      "Dictation in this browser sends audio to Google's speech service.",
    ]);
  });
});
