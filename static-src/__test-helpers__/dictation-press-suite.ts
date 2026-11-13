// The composer's Send slot as index.html lays it out, under the shipped stylesheets, pressed with a real finger or
// mouse while a fake recognizer plays WebKit's events. Import it from a suite whose `vi.mock` block mocks
// `../toast.js` with `toast-mock.ts`; `dictationPressSuite` registers the cases.

import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { initPromptInput, setSendState } from "../prompt-input.js";
import { createDictation, type Dictation } from "../dictation.js";
import { ICON_MIC } from "../icons.js";
import { setActive, setSessions } from "../store.js";
import { error as errorToast, info } from "../toast.js";
import { emulateA11yMedia, resetA11yMedia } from "./a11y-media.js";
import { mountAppCSS } from "./css-rules.js";
import { fakeSpeech, settle } from "./fake-speech.js";
import { useSteppableClock, waitForOnClock } from "./fake-clock.js";
import { glyphMarkup, registryMarkup } from "./glyph.js";
import { makeSession } from "./model.js";
import { parkPointer } from "./park-pointer.js";
import { markDictationCloudNoted } from "../device-view.js";

export interface PressSuite {
  /** A real press on the element `selector` names: a finger's tap or a mouse click. */
  readonly press: (selector: string) => Promise<void>;
  /** Whether that press is a finger's. */
  readonly finger: boolean;
}

/** Chromium's own silence limit (`kNoSpeechTimeoutMs`, content/browser/speech/speech_recognizer_impl.h), which a
 *  WebKit session must match: WebKit has none, and on iOS 27 a session can stay deaf with its microphone open
 *  (https://bugs.webkit.org/show_bug.cgi?id=321436). */
const NO_RESULT_MS = 8000;

/** Past every engine's multi-click window: a tap inside it is a double tap (`detail` 2), which the press owner
 *  reads as the first tap's act again. */
const MULTI_CLICK_MS = 700;

/** Computed colours read mid-transition are neither face's. */
async function transitionsDone(el: Element): Promise<void> {
  await Promise.all(
    el
      .getAnimations()
      .filter((a) => a instanceof CSSTransition)
      .map((a) => a.finished.catch(() => undefined)),
  );
}

export function dictationPressSuite({ press: pressNow, finger }: PressSuite): void {
  let lastPress = -Infinity;
  /** Each press a single one, as a reader's start and stop taps seconds apart are. */
  async function press(selector: string): Promise<void> {
    const wait = lastPress + MULTI_CLICK_MS - performance.now();
    if (wait > 0) {
      await new Promise((resolve) => {
        setTimeout(resolve, wait);
      });
    }
    await pressNow(selector);
    // Every face is read with no pointer over it, as the resting face is (pointer-park-setup.ts).
    await parkPointer();
    lastPress = performance.now();
  }
  const speech = fakeSpeech({ cloud: "Apple's speech service" });
  let dictation: Dictation;
  let sheet: HTMLStyleElement;

  const input = (): HTMLTextAreaElement =>
    document.getElementById("prompt-input") as HTMLTextAreaElement;
  const sendBtn = (): HTMLButtonElement => document.getElementById("send-btn") as HTMLButtonElement;
  const toasts = (): unknown[][] => [
    ...vi.mocked(errorToast).mock.calls,
    ...vi.mocked(info).mock.calls,
  ];

  /** Press Dictate and let the engine open the microphone, as WebKit does: `start`, then `audiostart`. */
  async function startDictating(): Promise<void> {
    await press("#send-btn");
    await settle();
    speech.last().begin();
    speech.last().signal("audiostart");
  }

  beforeAll(() => {
    if (finger) {
      // A finger-driven load's tier (pointer-tier.ts `resolveTier`).
      document.documentElement.setAttribute("data-pointer", "coarse");
    }
    sheet = mountAppCSS();
    document.body.insertAdjacentHTML(
      "beforeend",
      `<form id="prompt-form" class="bottom-bar" action="javascript:void 0">
        <div id="prompt-box" class="prompt-box">
          <textarea id="prompt-input" rows="1" aria-label="Message"></textarea>
          <div class="prompt-pills">
            <div class="pill-spacer"></div>
            <button type="button" id="midturn-send-btn" class="send-btn" hidden></button>
            <button type="submit" id="send-btn" class="send-btn" data-tooltip="Send" aria-label="Send"></button>
          </div>
        </div>
      </form>`,
    );
    dictation = createDictation(speech.platform, input());
    initPromptInput(
      () => undefined,
      () => undefined,
      signal(false),
      () => undefined,
      dictation,
    );
    setSessions([makeSession({ id: "c1" })]);
    setActive("c1");
  });

  afterAll(() => {
    dictation.drop();
    document.getElementById("prompt-form")?.remove();
    sheet.remove();
    document.documentElement.removeAttribute("data-pointer");
  });

  beforeEach(async () => {
    // The first session's cloud notice is not what these cases count.
    markDictationCloudNoted();
    dictation.drop();
    setSendState({ kind: "idle" });
    input().value = "";
    input().dispatchEvent(new Event("input", { bubbles: true }));
    input().blur();
    await settle();
  });

  /** A working WebKit session in WebKit's order (WebCore/Modules/speech, SpeechRecognition.cpp and
   *  cocoa/WebSpeechRecognizerTask.mm): `speechstart` comes only with the first hypothesis, each list carries every
   *  final so far plus the newest result, and a stop sends `speechend` before the last final, then `audioend` and
   *  `end`. */
  async function speakInWebKitOrder(): Promise<void> {
    const rec = speech.last();
    rec.signal("speechstart");
    rec.hearFrom(0, ["Hello", false]);
    expect(input().value, "the first hypothesis").toBe("Hello");
    rec.hearFrom(0, ["Hello there", false]);
    rec.hearFrom(0, ["Hello there", true]);
    rec.hearFrom(1, ["Hello there", true], ["and more", false]);
    expect(input().value, "the second utterance's hypothesis").toBe("Hello there and more");
    await press("#send-btn");
    expect(rec.stopped + rec.aborted, "the second press stops the engine").toBeGreaterThan(0);
    rec.signal("speechend");
    rec.hearFrom(1, ["Hello there", true], ["and more", true]);
    rec.signal("audioend");
    rec.finish();
    expect(input().value, "the last final, after the stop").toBe("Hello there and more");
  }

  describe(`a ${finger ? "finger's tap" : "mouse click"} on Dictate (bug 1: words never reach the box)`, () => {
    if (finger) {
      it("leaves focus off the box, so no keyboard opens over the words, and the stop leaves it off too", async () => {
        await startDictating();
        expect(document.activeElement, "the tap that starts dictation").not.toBe(input());
        speech.last().hear(["hello", false]);
        await press("#send-btn");
        expect(dictation.listening.value, "the tap that stops it").toBe(false);
        speech.last().finish();
        await settle();
        expect(document.activeElement, "the tap that stops it").not.toBe(input());
        // Its face is Send again, which the reader's next Space on a hardware keyboard would press.
        expect(document.activeElement, "focus after the end").not.toBe(sendBtn());
        expect(input().value, "the words, written with the box unfocused").toBe("hello");
      });

      it("lands the words of a session in WebKit's event order with the box unfocused, as hiding the keyboard leaves it", async () => {
        await startDictating();
        input().blur();
        await speakInWebKitOrder();
      });
    } else {
      it("puts focus in the box, and the stop keeps it there", async () => {
        await startDictating();
        expect(document.activeElement, "the click that starts dictation").toBe(input());
        await press("#send-btn");
        expect(dictation.listening.value, "the click that stops it").toBe(false);
        speech.last().finish();
        await settle();
        expect(document.activeElement, "the click that stops it").toBe(input());
      });

      it("lands the words of a session in WebKit's event order", async () => {
        await startDictating();
        await speakInWebKitOrder();
      });
    }

    it(`stops a session that hears nothing ${String(NO_RESULT_MS / 1000)} s after its audio starts, and says so`, async () => {
      useSteppableClock();
      try {
        await startDictating();
        const rec = speech.last();
        await vi.advanceTimersByTimeAsync(NO_RESULT_MS - 500);
        expect(dictation.listening.value, "still listening before the limit").toBe(true);
        expect(toasts()).toEqual([]);
        await waitForOnClock(() => {
          expect(dictation.listening.value, "listening past the limit with no result").toBe(false);
        }, 1000);
        expect(rec.stopped + rec.aborted, "the engine is told to stop").toBeGreaterThan(0);
        expect(toasts(), "one message says why it stopped").toHaveLength(1);
      } finally {
        vi.useRealTimers();
      }
    });

    it("keeps a session that heard words listening through a later silence", async () => {
      useSteppableClock();
      try {
        await startDictating();
        speech.last().signal("speechstart");
        speech.last().hear(["hello", false]);
        await vi.advanceTimersByTimeAsync(NO_RESULT_MS * 3);
        expect(dictation.listening.value).toBe(true);
        expect(input().value).toBe("hello");
        expect(toasts()).toEqual([]);
      } finally {
        vi.useRealTimers();
      }
    });
  });

  describe(`the listening face after a ${finger ? "tap" : "click"} (bug 2: nothing shows it records)`, () => {
    /** The resting face's colours and glyph, read before any press. */
    async function restingFace(): Promise<{
      edge: string;
      fill: string;
      ink: string;
      width: number;
      glyph: string | null;
    }> {
      await transitionsDone(sendBtn());
      const s = getComputedStyle(sendBtn());
      return {
        edge: s.borderTopColor,
        fill: s.backgroundColor,
        ink: s.color,
        width: sendBtn().getBoundingClientRect().width,
        glyph: glyphMarkup(sendBtn().querySelector("svg")),
      };
    }

    it("is a stop control: its name, label, tooltip, glyph and colour all change", async () => {
      const rest = await restingFace();
      expect(rest.glyph, "the resting face is the microphone").toBe(registryMarkup(ICON_MIC));
      await startDictating();
      const btn = sendBtn();
      // APG Button pattern: a control whose name changes with its state is not a toggle, so no aria-pressed.
      expect.soft(btn.getAttribute("aria-label"), "accessible name").toBe("Stop dictation");
      expect.soft(btn.getAttribute("data-tooltip"), "tooltip").toBe("Stop dictation");
      expect.soft(btn.querySelector(".send-btn-label")?.textContent, "visible label").toBe("Stop");
      expect.soft(btn.hasAttribute("aria-pressed"), "aria-pressed").toBe(false);
      expect.soft(glyphMarkup(btn.querySelector("svg")), "glyph").not.toBe(rest.glyph);
      await transitionsDone(btn);
      expect.soft(getComputedStyle(btn).color, "ink colour").not.toBe(rest.ink);
      expect.soft(getComputedStyle(btn).backgroundColor, "fill colour").not.toBe(rest.fill);
      // motion.md "A row action keeps one footprint": the slot never changes width with its words.
      expect.soft(btn.getBoundingClientRect().width, "width").toBeCloseTo(rest.width, 1);
    });

    it("pulses while listening", async () => {
      await startDictating();
      const beat = getComputedStyle(sendBtn(), "::before");
      expect(beat.animationName).not.toBe("none");
      expect(beat.animationIterationCount).toBe("infinite");
      expect(beat.backgroundColor, "a swell of the fill's own colour cannot be seen").not.toBe(
        getComputedStyle(sendBtn()).backgroundColor,
      );
    });

    describe("under reduced motion", () => {
      beforeAll(async () => {
        await emulateA11yMedia({ reducedMotion: "reduce" });
      });

      afterAll(async () => {
        await resetA11yMedia();
      });

      it("holds still, and still differs from the resting face in glyph and colour", async () => {
        const rest = await restingFace();
        await startDictating();
        const btn = sendBtn();
        expect
          .soft(getComputedStyle(btn, "::before").animationIterationCount, "no endless pulse")
          .not.toBe("infinite");
        expect.soft(glyphMarkup(btn.querySelector("svg")), "glyph").not.toBe(rest.glyph);
        await transitionsDone(btn);
        expect.soft(getComputedStyle(btn).color, "ink colour").not.toBe(rest.ink);
        expect.soft(getComputedStyle(btn).backgroundColor, "fill colour").not.toBe(rest.fill);
      });
    });

    it("goes back to the resting microphone on a second press", async () => {
      const rest = await restingFace();
      await startDictating();
      await press("#send-btn");
      expect(dictation.listening.value, "the second press stops").toBe(false);
      speech.last().finish();
      await settle();
      const btn = sendBtn();
      expect(btn.getAttribute("aria-label")).toBe("Dictate");
      expect(btn.getAttribute("data-tooltip")).toBe("Dictate");
      expect(glyphMarkup(btn.querySelector("svg"))).toBe(rest.glyph);
      await transitionsDone(btn);
      expect(getComputedStyle(btn).borderTopColor).toBe(rest.edge);
      expect(getComputedStyle(btn).backgroundColor).toBe(rest.fill);
      expect(getComputedStyle(btn).color).toBe(rest.ink);
    });
  });
}
