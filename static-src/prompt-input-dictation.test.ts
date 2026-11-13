// The microphone in the composer's Send slot, painted on the real buttons: its face, its name and pressed state,
// where a press goes, and that it never sends or cancels by itself.

import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { signal } from "@cplieger/reactive";
import { initPromptInput, sendComposer, setSendState } from "./prompt-input.js";
import { createDictation, type Dictation } from "./dictation.js";
import { ICON_MIC, ICON_MIC_STOP, ICON_SEND } from "./icons.js";
import { setActive, setSessions } from "./store.js";
import { setComposerValue, writeComposerSilently } from "./composer-value.js";
import { BUS_TAB_CHANGED, emitBus } from "./bus.js";
import {
  _resetComposerStateForTest,
  composerDraft,
  initComposerState,
  restoreComposerState,
  saveComposerState,
} from "./composer-state.js";
import type { RunView, Session } from "./types.js";
import { fakeSpeech, settle } from "./__test-helpers__/fake-speech.js";
import { glyphMarkup, registryMarkup } from "./__test-helpers__/glyph.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";

const CHAT = "c1";
const submitted: string[] = [];
const cancelled = vi.fn();
const staged = signal(false);
const speech = fakeSpeech({ available: () => Promise.resolve("available") });
let dictation: Dictation;
/** What the next send answers with: undefined clears the box at once, a promise holds the send until it settles. */
let nextSend: Promise<unknown> | undefined;

function session(run?: RunView): Session {
  return {
    id: CHAT,
    name: "test",
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
    ...(run === undefined ? {} : { run }),
  };
}

/** `s` with one turn whose prompt is `text`, so ArrowUp recalls it. */
function withPrompt(s: Session, text: string): Session {
  const entry = {
    id: "t0-open",
    turn: "t0",
    lane: "",
    kind: "turn_open" as const,
    payload: { source: "prompt" as const, n: 1, prompt: { id: "p0", text } },
    seq: 0,
    ts: 0,
  };
  return {
    ...s,
    turns: new Map([["t0", { entries: [entry], openEntries: new Map() }]]),
    turn_order: ["t0"],
    turn_count: 1,
  };
}

const STEERING = runAt(1, [turnEntry("t1")], {
  send: "steer",
  cancel: "t1",
  next_wait: { after: "turns", turns: 1, this: true },
});

const QUEUEING = runAt(1, [turnEntry("t1", { steerable: false })], {
  send: "queue",
  cancel: "t1",
  next_wait: { after: "turns", turns: 1, this: true },
});

beforeAll(() => {
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <textarea id="prompt-input"></textarea>
      <button id="midturn-send-btn" class="send-btn" type="button" hidden></button>
      <button id="send-btn" class="send-btn" type="submit"></button>
    </form>`;
  dictation = createDictation(speech.platform, input());
  initPromptInput(
    (text: string) => {
      submitted.push(text);
      const answer = nextSend;
      nextSend = undefined;
      return answer;
    },
    cancelled,
    staged,
    () => undefined,
    dictation,
  );
});

beforeEach(async () => {
  // Every press a case left down ends, as a real one does.
  release(sendBtn(), false);
  window.dispatchEvent(new Event("blur"));
  input().focus();
  dictation.drop();
  setSessions([session()]);
  setActive(CHAT);
  setSendState({ kind: "idle" });
  type("");
  staged.value = false;
  submitted.length = 0;
  cancelled.mockClear();
  await settle();
});

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

function sendBtn(): HTMLButtonElement {
  return document.getElementById("send-btn") as HTMLButtonElement;
}

function midTurn(): HTMLButtonElement {
  return document.getElementById("midturn-send-btn") as HTMLButtonElement;
}

function type(text: string): void {
  const el = input();
  el.value = text;
  el.setSelectionRange(text.length, text.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function press(key: string, mods: KeyboardEventInit = {}): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...mods });
  input().dispatchEvent(e);
  return e;
}

/** Press the button and let the engine open the microphone. */
async function startFrom(btn: HTMLButtonElement): Promise<void> {
  btn.click();
  await settle();
  speech.last().begin();
}

/** One click of a run, `detail` being its place in the run as the browser counts it (0 for a keyboard press). */
function clickAt(btn: HTMLButtonElement, detail: number): void {
  btn.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail }));
}

/** A pointer press's start, as every engine fires it before the click. */
function pressDown(btn: HTMLButtonElement, detail = 1): void {
  btn.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
  btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail }));
}

/** A pointer press's release, on the button or dragged off it; `button` 2 is a right press's. */
function release(btn: HTMLButtonElement, onButton: boolean, button = 0): void {
  const r = btn.getBoundingClientRect();
  const at = {
    clientX: onButton ? r.left + r.width / 2 : r.right + 40,
    clientY: r.top + r.height / 2,
  };
  (onButton ? btn : document.body).dispatchEvent(
    new PointerEvent("pointerup", { bubbles: true, cancelable: true, button, ...at }),
  );
}

/** A click with no pointer press of its own, as an extension's simulated click makes it. */
function simulatedClick(btn: HTMLButtonElement): void {
  btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: 1 }));
  btn.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true, detail: 1 }));
  clickAt(btn, 1);
}

/** Lay the page out for a finger, as a tablet with a hardware keyboard is. */
async function underFinger(body: () => Promise<void>): Promise<void> {
  document.documentElement.setAttribute("data-pointer", "coarse");
  try {
    await body();
  } finally {
    document.documentElement.removeAttribute("data-pointer");
  }
}

function streaming(): void {
  setSessions([session(STEERING)]);
  setSendState({ kind: "streaming" });
}

/** Listening is a Stop control in its own words, so it carries no `aria-pressed` (APG Button pattern). */
function expectDictateFace(btn: HTMLButtonElement, listening: boolean): void {
  const name = listening ? "Stop dictation" : "Dictate";
  expect(btn.hidden).toBe(false);
  expect(btn.getAttribute("aria-label")).toBe(name);
  expect(btn.hasAttribute("aria-pressed")).toBe(false);
  expect(btn.getAttribute("data-tooltip")).toBe(name);
  expect(btn.type).toBe("button");
  expect(btn.classList.contains("dictating")).toBe(listening);
  expect(btn.querySelector(".send-btn-label")?.textContent).toBe(listening ? "Stop" : "Dictate");
  expect(glyphMarkup(btn.querySelector("svg"))).toBe(
    registryMarkup(listening ? ICON_MIC_STOP : ICON_MIC),
  );
}

function expectSendFace(btn: HTMLButtonElement): void {
  expect(btn.getAttribute("aria-label")).toBe("Send");
  expect(btn.hasAttribute("aria-pressed")).toBe(false);
  expect(btn.type).toBe("submit");
  expect(btn.classList.contains("dictating")).toBe(false);
  expect(glyphMarkup(btn.querySelector("svg"))).toBe(registryMarkup(ICON_SEND));
}

describe("the microphone in Send's slot (rule 1)", () => {
  it("shows on an empty box with nothing staged", () => {
    expectDictateFace(sendBtn(), false);
  });

  it("shows on a whitespace-only box", () => {
    type("  \n ");
    expectDictateFace(sendBtn(), false);
  });

  it("gives way to Send as soon as anything is typed, and comes back when it is cleared", () => {
    type("h");
    expectSendFace(sendBtn());
    type("");
    expectDictateFace(sendBtn(), false);
  });

  it("gives way to Send for a staged file with no text", () => {
    staged.value = true;
    expectSendFace(sendBtn());
  });

  it("is the same element as Send, so it keeps Send's target size and focus ring", () => {
    const before = sendBtn();
    type("h");
    type("");
    expect(sendBtn()).toBe(before);
    expect(sendBtn().classList.contains("send-btn")).toBe(true);
  });

  it("does not show while the composer is disabled", async () => {
    input().disabled = true;
    await settle();
    try {
      expectSendFace(sendBtn());
    } finally {
      input().disabled = false;
      await settle();
    }
  });
});

describe("pressing it (rules 3 and 8)", () => {
  it("starts dictation, and the button becomes the pressed stop control", async () => {
    const btn = sendBtn();
    await startFrom(btn);
    expect(dictation.listening.value).toBe(true);
    expect(sendBtn()).toBe(btn);
    expectDictateFace(btn, true);
    expect(submitted).toEqual([]);
  });

  it("stays the stop control after words arrive", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hello", false]);
    expect(input().value).toBe("hello");
    expectDictateFace(sendBtn(), true);
    speech.last().hear(["hello there", true]);
    expectDictateFace(sendBtn(), true);
  });

  it("stops on a second press and then follows the box: Send with text", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hello", true]);
    sendBtn().click();
    expect(dictation.listening.value).toBe(false);
    speech.last().finish();
    expectSendFace(sendBtn());
    expect(submitted, "dictation never sends").toEqual([]);
  });

  it("follows the box when the browser ends it with nothing heard: the microphone again", async () => {
    await startFrom(sendBtn());
    speech.last().finish();
    expectDictateFace(sendBtn(), false);
  });

  it("never sends on its own, through results, an error and the end", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["do not send", true]);
    speech.last().fail("network");
    speech.last().finish();
    await settle();
    expect(submitted).toEqual([]);
    expect(input().value).toBe("do not send");
  });

  it("sends on Enter as today, stopping dictation first so no late words refill the box", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["send this", false]);
    press("Enter");
    expect(submitted).toEqual(["send this"]);
    expect(dictation.listening.value).toBe(false);
    speech.last().hear(["send this now", true]);
    expect(input().value).toBe("");
  });

  it("sends through the keyboard shortcut's entry point as today, stopping dictation first", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["shortcut send", false]);
    sendComposer();
    expect(submitted).toEqual(["shortcut send"]);
    expect(dictation.listening.value).toBe(false);
  });

  it("stops on Escape in the box, keeping the words and cancelling nothing", async () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    await startFrom(midTurn());
    speech.last().hear(["keep me", false]);
    const e = press("Escape");
    expect(e.defaultPrevented).toBe(true);
    expect(dictation.listening.value).toBe(false);
    expect(cancelled).not.toHaveBeenCalled();
    expect(input().value).toBe("keep me");
  });
});

describe("a double click on the dictation button (rule 3)", () => {
  it("sends nothing from Send's slot, and the next single click sends", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hello wor", false]);
    clickAt(sendBtn(), 1);
    clickAt(sendBtn(), 2);
    expect(submitted).toEqual([]);
    expectSendFace(sendBtn());
    clickAt(sendBtn(), 1);
    expect(submitted).toEqual(["hello wor"]);
  });

  it("steers nothing from the mid-turn slot, and a keyboard press after it steers", async () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    await startFrom(midTurn());
    speech.last().hear(["try the other", false]);
    clickAt(midTurn(), 1);
    clickAt(midTurn(), 2);
    expect(submitted).toEqual([]);
    expect(cancelled).not.toHaveBeenCalled();
    expect(midTurn().getAttribute("aria-label")).toBe("Steer the running turn");
    clickAt(midTurn(), 0);
    expect(submitted).toEqual(["try the other"]);
  });

  it("keeps listening when the click that started it is doubled", async () => {
    clickAt(sendBtn(), 1);
    clickAt(sendBtn(), 2);
    await settle();
    expect(dictation.listening.value).toBe(true);
    expectDictateFace(sendBtn(), true);
  });
});

describe("a press acts on the face it began on (rule 3)", () => {
  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])(
    "stops, and sends nothing, when the browser ends the session between press and click on %s",
    async (_, mid) => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["almost done", false]);
      pressDown(btn);
      speech.last().finish();
      expect(btn.getAttribute("aria-label"), "the end repainted the button").not.toBe("Dictate");
      clickAt(btn, 1);
      expect(submitted).toEqual([]);
      expect(cancelled).not.toHaveBeenCalled();
      expect(input().value).toBe("almost done");
    },
  );

  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])(
    "stops in WebKit's press order, where the press takes focus out of the box, on %s",
    async (_, mid) => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["hello wor", false]);
      pressDown(btn);
      input().blur();
      clickAt(btn, 1);
      expect(dictation.listening.value).toBe(false);
      expect(submitted).toEqual([]);
      expect(cancelled).not.toHaveBeenCalled();
      expect(document.activeElement, "Stop puts focus back in the box").toBe(input());
      expect(input().value).toBe("hello wor");
    },
  );

  it("sends nothing from a Space press on Stop whose session the browser ended before the key up", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["by keyboard", false]);
    sendBtn().focus();
    sendBtn().dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true }));
    speech.last().finish();
    expect(document.activeElement, "the session's end takes focus off the button").toBe(input());
    // The key up reaches the box, and a button that lost focus mid-press never clicks (https://crbug.com/945854).
    input().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    expect(submitted).toEqual([]);
    expect(input().value).toBe("by keyboard");
  });

  it("never opens the microphone on the second click of a double click on Send that emptied the box", () => {
    const made = speech.made.length;
    type("hello");
    pressDown(sendBtn(), 1);
    clickAt(sendBtn(), 1);
    expectDictateFace(sendBtn(), false);
    pressDown(sendBtn(), 2);
    clickAt(sendBtn(), 2);
    expect(dictation.listening.value).toBe(false);
    expect(speech.made).toHaveLength(made);
    expect(submitted).toEqual(["hello"]);
  });

  it("never opens the microphone from a Send press whose box emptied before the click", () => {
    type("hello");
    pressDown(sendBtn());
    type("");
    clickAt(sendBtn(), 1);
    expect(dictation.listening.value).toBe(false);
    expect(submitted).toEqual([]);
  });

  it("never sends from a Cancel press whose turn ended before the click", () => {
    streaming();
    type("not yet");
    pressDown(sendBtn());
    setSendState({ kind: "idle" });
    expectSendFace(sendBtn());
    clickAt(sendBtn(), 1);
    expect(submitted).toEqual([]);
    expect(cancelled).not.toHaveBeenCalled();
  });

  it("keeps dictating into the box after Cancel takes the press's focus", async () => {
    streaming();
    await startFrom(midTurn());
    sendBtn().focus();
    clickAt(sendBtn(), 1);
    expect(cancelled).toHaveBeenCalledOnce();
    expect(dictation.listening.value).toBe(true);
    expect(document.activeElement).toBe(input());
    speech.last().hear(["still here", false]);
    expect(input().value).toBe("still here");
  });

  it("acts on the face it lands on for a press that never went down here", async () => {
    pressDown(midTurn());
    await startFrom(sendBtn());
    expect(dictation.listening.value).toBe(true);
  });
});

describe("a held key is one press, on the face it began on (rule 3)", () => {
  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])("only stops on Enter held on Stop, sending and steering nothing, on %s", async (_, mid) => {
    if (mid) {
      streaming();
    }
    const btn = mid ? midTurn() : sendBtn();
    await startFrom(btn);
    speech.last().hear(["hello", true]);
    btn.focus();
    await userEvent.keyboard("{Enter>3}{/Enter}");
    expect(dictation.listening.value).toBe(false);
    expect(submitted).toEqual([]);
    expect(cancelled).not.toHaveBeenCalled();
    expect(input().value).toBe("hello");
  });

  it("cancels once on Enter held on Cancel while dictating, and sends nothing", async () => {
    streaming();
    await startFrom(midTurn());
    speech.last().hear(["hello", true]);
    sendBtn().focus();
    await userEvent.keyboard("{Enter>3}{/Enter}");
    expect(cancelled).toHaveBeenCalledOnce();
    expect(submitted).toEqual([]);
    expect(input().value).toBe("hello");
  });

  it("sends once on Enter held on Send, and never opens the microphone on the face the send left", async () => {
    const made = speech.made.length;
    type("hi there");
    sendBtn().focus();
    await userEvent.keyboard("{Enter>3}{/Enter}");
    expect(submitted).toEqual(["hi there"]);
    expect(dictation.listening.value).toBe(false);
    expect(speech.made).toHaveLength(made);
  });

  it("ignores every other click until the key comes up, then a click acts on the face painted", () => {
    type("hi");
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
    clickAt(sendBtn(), 0);
    expect(submitted).toEqual(["hi"]);
    expectDictateFace(sendBtn(), false);
    clickAt(sendBtn(), 0);
    expect(dictation.listening.value, "a click during the hold").toBe(false);
    sendBtn().dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
    clickAt(sendBtn(), 0);
    expect(dictation.listening.value).toBe(true);
  });

  it("ends a Space press at a key up off the button, so a click right after acts on the face painted", () => {
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    input().focus();
    type("hi");
    input().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    sendBtn().click();
    expect(dictation.listening.value).toBe(false);
    expect(submitted).toEqual(["hi"]);
  });

  it("ends a Space press when focus leaves its button, so a click right after a key up back on it acts on the face painted", () => {
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    input().focus();
    type("hi");
    sendBtn().focus();
    sendBtn().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    sendBtn().click();
    expect(dictation.listening.value).toBe(false);
    expect(submitted).toEqual(["hi"]);
  });

  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])(
    "stops on Escape with focus on Stop, keeping the words and cancelling nothing, on %s",
    async (_, mid) => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["keep me", false]);
      btn.focus();
      await userEvent.keyboard("{Escape}");
      expect(dictation.listening.value).toBe(false);
      expect(cancelled).not.toHaveBeenCalled();
      expect(input().value).toBe("keep me");
    },
  );
});

describe("a press of one kind inside a press of the other (rule 3)", () => {
  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])(
    "only stops on an Enter on Stop while the mouse is held there, and the release sends nothing, on %s",
    async (_, mid) => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["hello", true]);
      pressDown(btn);
      btn.focus();
      await userEvent.keyboard("{Enter}");
      expect(dictation.listening.value).toBe(false);
      speech.last().finish();
      release(btn, true);
      clickAt(btn, 1);
      expect(submitted).toEqual([]);
      expect(input().value).toBe("hello");
    },
  );

  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])(
    "only stops on a mouse click on Stop while a Space is held there, and the Space sends nothing, on %s",
    async (_, mid) => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["hello", true]);
      btn.focus();
      btn.dispatchEvent(
        new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
      );
      pressDown(btn);
      release(btn, true);
      clickAt(btn, 1);
      expect(dictation.listening.value).toBe(false);
      speech.last().finish();
      expect(document.activeElement, "the stop takes focus off the button").toBe(input());
      // The key up reaches the box, and a button that lost focus mid-press never clicks (https://crbug.com/945854).
      input().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
      expect(submitted).toEqual([]);
      expect(input().value).toBe("hello");
    },
  );
});

describe("a held Enter under a finger, with a hardware keyboard (rule 3)", () => {
  it.each([
    ["Send's slot", false],
    ["the mid-turn slot", true],
  ])("only stops on Stop, writing no new line into the box, on %s", async (_, mid) => {
    await underFinger(async () => {
      if (mid) {
        streaming();
      }
      const btn = mid ? midTurn() : sendBtn();
      await startFrom(btn);
      speech.last().hear(["hello", true]);
      btn.focus();
      await userEvent.keyboard("{Enter>3}{/Enter}");
      expect(input().value).toBe("hello");
      expect(dictation.listening.value).toBe(false);
      expect(submitted).toEqual([]);
      expect(cancelled).not.toHaveBeenCalled();
    });
  });

  it("cancels once on Cancel while dictating, and keeps listening with the words unchanged", async () => {
    await underFinger(async () => {
      streaming();
      await startFrom(midTurn());
      speech.last().hear(["hello", true]);
      sendBtn().focus();
      await userEvent.keyboard("{Enter>3}{/Enter}");
      expect(cancelled).toHaveBeenCalledOnce();
      expect(input().value).toBe("hello");
      expect(dictation.listening.value).toBe(true);
    });
  });

  it("starts once on Dictate, and keeps listening into an empty box", async () => {
    await underFinger(async () => {
      const made = speech.made.length;
      sendBtn().focus();
      await userEvent.keyboard("{Enter>3}{/Enter}");
      await settle();
      expect(input().value).toBe("");
      expect(dictation.listening.value).toBe(true);
      expect(speech.made).toHaveLength(made + 1);
    });
  });

  it("still writes a new line for every repeat of an Enter held in the box", async () => {
    await underFinger(async () => {
      input().focus();
      await userEvent.keyboard("{Enter>3}{/Enter}");
      expect(input().value).toBe("\n\n\n");
      expect(submitted).toEqual([]);
    });
  });
});

describe("a press's face lasts only as long as the press (rule 3)", () => {
  it("never records a right press, so a later simulated click acts on the face painted", () => {
    sendBtn().dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, cancelable: true, button: 2 }),
    );
    release(sendBtn(), true, 2);
    sendBtn().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, button: 2 }));
    type("hello");
    simulatedClick(sendBtn());
    expect(submitted).toEqual(["hello"]);
    expect(dictation.listening.value).toBe(false);
  });

  it("dictates from a simulated click on the microphone after a right press on Send", () => {
    type("hello");
    sendBtn().dispatchEvent(
      new PointerEvent("pointerdown", { bubbles: true, cancelable: true, button: 2 }),
    );
    release(sendBtn(), true, 2);
    type("");
    simulatedClick(sendBtn());
    expect(dictation.listening.value).toBe(true);
    expect(submitted).toEqual([]);
  });

  it("keeps a primary press through a middle press's auxclick, for its click", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["chord", false]);
    pressDown(sendBtn());
    sendBtn().dispatchEvent(new MouseEvent("auxclick", { bubbles: true, button: 1 }));
    speech.last().finish();
    release(sendBtn(), true);
    clickAt(sendBtn(), 1);
    expect(submitted).toEqual([]);
    expect(input().value).toBe("chord");
  });

  it("ends a primary press when a right press chorded into it opens the menu, so a later simulated click acts on the face painted", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["chord", false]);
    pressDown(sendBtn());
    sendBtn().dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2 }),
    );
    speech.last().finish();
    expectSendFace(sendBtn());
    simulatedClick(sendBtn());
    expect(submitted).toEqual(["chord"]);
  });

  it("ends a primary press that opened a context menu (a Ctrl-click, a long press)", () => {
    pressDown(sendBtn());
    sendBtn().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    release(sendBtn(), true);
    type("hello");
    simulatedClick(sendBtn());
    expect(submitted).toEqual(["hello"]);
    expect(dictation.listening.value).toBe(false);
  });

  it("ends a key press whose key up went to another window", () => {
    type("hi");
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
    clickAt(sendBtn(), 0);
    expect(submitted).toEqual(["hi"]);
    window.dispatchEvent(new Event("blur"));
    clickAt(sendBtn(), 0);
    expect(dictation.listening.value).toBe(true);
  });

  it("swallows the repeats of a Space begun on a button after focus leaves it, and clicks nothing", () => {
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    input().focus();
    const again = new KeyboardEvent("keydown", {
      key: " ",
      repeat: true,
      bubbles: true,
      cancelable: true,
    });
    input().dispatchEvent(again);
    expect(again.defaultPrevented).toBe(true);
    input().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    expect(dictation.listening.value, "a Space released off its button clicks nothing").toBe(false);
  });

  it("ends a Space press at its click, so an Enter pressed right after it sends", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hello", true]);
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    sendBtn().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    clickAt(sendBtn(), 0);
    speech.last().finish();
    expect(dictation.listening.value).toBe(false);
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    sendBtn().dispatchEvent(enter);
    expect(enter.defaultPrevented, "the Enter makes its click").toBe(false);
    clickAt(sendBtn(), 0);
    expect(submitted).toEqual(["hello"]);
  });

  it("swallows the repeats of a second key pressed on the button during a key press, wherever focus moved", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hello", true]);
    sendBtn().focus();
    sendBtn().dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    sendBtn().dispatchEvent(enter);
    expect(enter.defaultPrevented, "a second key clicks nothing").toBe(true);
    sendBtn().dispatchEvent(new KeyboardEvent("keyup", { key: " ", bubbles: true }));
    clickAt(sendBtn(), 0);
    expect(dictation.listening.value).toBe(false);
    input().focus();
    const again = press("Enter", { repeat: true });
    expect(again.defaultPrevented).toBe(true);
    expect(submitted).toEqual([]);
    expect(input().value).toBe("hello");
    input().dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", bubbles: true }));
  });

  it("sends from a click() after a press dragged off the microphone, never dictating", () => {
    const made = speech.made.length;
    pressDown(sendBtn());
    release(sendBtn(), false);
    type("hello");
    sendBtn().click();
    expect(submitted).toEqual(["hello"]);
    expect(dictation.listening.value).toBe(false);
    expect(speech.made).toHaveLength(made);
  });

  it("dictates from a click() on the microphone after a press on Send dragged off", () => {
    type("hello");
    pressDown(sendBtn());
    release(sendBtn(), false);
    type("");
    sendBtn().click();
    expect(dictation.listening.value).toBe(true);
    expect(submitted).toEqual([]);
  });

  it("acts on the face painted for a click() while a pointer press is still down", () => {
    pressDown(sendBtn());
    type("hello");
    sendBtn().click();
    expect(submitted).toEqual(["hello"]);
    expect(dictation.listening.value).toBe(false);
  });

  it("acts on the face painted for a click after a press that lost its capture before the release", () => {
    pressDown(sendBtn());
    sendBtn().dispatchEvent(new PointerEvent("lostpointercapture", { bubbles: true }));
    type("hello");
    clickAt(sendBtn(), 1);
    expect(submitted).toEqual(["hello"]);
    expect(dictation.listening.value).toBe(false);
  });

  it("keeps a finger's press through its release and the capture's end, for the click that follows", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["by finger", false]);
    pressDown(sendBtn());
    speech.last().finish();
    release(sendBtn(), true);
    sendBtn().dispatchEvent(new PointerEvent("lostpointercapture", { bubbles: true }));
    clickAt(sendBtn(), 1);
    expect(submitted).toEqual([]);
    expect(input().value).toBe("by finger");
  });
});

describe("where focus goes when dictation ends (rule 9)", () => {
  /** Each way a session ends, the box's value after it, and what it sent. */
  const ENDS: readonly (readonly [
    name: string,
    end: () => Promise<void> | void,
    value: string,
    sent: readonly string[],
  ])[] = [
    ["typing", () => userEvent.keyboard("x "), "hellox ", []],
    ["a paste", () => void document.execCommand("insertText", false, "x "), "hellox ", []],
    [
      "Stop",
      () => {
        sendBtn().click();
        speech.last().finish();
      },
      "hello",
      [],
    ],
    [
      "Escape",
      () => {
        press("Escape");
        speech.last().finish();
      },
      "hello",
      [],
    ],
    ["the browser's end", () => speech.last().finish(), "hello", []],
    ["an engine error", () => speech.last().fail("bad-grammar"), "hello", []],
    ["a page hide", hide, "hello", []],
    ["a send", sendComposer, "", ["hello"]],
    [
      "a tab switch",
      () => emitBus(BUS_TAB_CHANGED, { to: "__files__", kind: "files" }),
      "hello",
      [],
    ],
    ["a chat switch", () => setActive("c2"), "hello", []],
    [
      "the error face",
      () => setSendState({ kind: "error", reason: "The agent is starting." }),
      "hello",
      [],
    ],
    ["a history recall", () => void press("ArrowUp", {}), "an earlier prompt", []],
    [
      "a write that announces itself (a quote, a slash command)",
      () => setComposerValue("> q\n"),
      "> q\n",
      [],
    ],
    [
      "a silent write (a seed, another chat's draft)",
      () => writeComposerSilently("a draft"),
      "a draft",
      [],
    ],
  ];

  function hide(): void {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    try {
      document.dispatchEvent(new Event("visibilitychange"));
    } finally {
      delete (document as { visibilityState?: unknown }).visibilityState;
    }
  }

  /** Dictate from a focused button, as a tap or a click focuses it, and hear one phrase, with one earlier prompt
   *  in the chat's history and a second chat to switch to. */
  async function dictateHello(): Promise<void> {
    setSessions([withPrompt(session(), "an earlier prompt"), { ...session(), id: "c2" }]);
    setActive(CHAT);
    sendBtn().focus();
    await startFrom(sendBtn());
    speech.last().hear(["hello", true]);
    expect(document.activeElement).toBe(input());
  }

  /** The end leaves focus in the box, never on a button, and the reader's next Space types there. */
  async function expectBoxKeepsFocus(value: string, sent: readonly string[]): Promise<void> {
    expect(document.activeElement).toBe(input());
    expect(dictation.listening.value).toBe(false);
    expect(input().value).toBe(value);
    expect(submitted).toEqual(sent);
    speech.last().hear(["hello", true], ["late", true]);
    expect(input().value, "a late result after the end").toBe(value);
    await userEvent.keyboard(" ");
    expect(input().value).toBe(`${value} `);
    expect(submitted, "a Space after the end").toEqual(sent);
    expect(dictation.listening.value, "a Space after the end").toBe(false);
  }

  it.each(ENDS)("under a finger, %s leaves focus in the box", async (_, end, value, sent) => {
    await underFinger(async () => {
      await dictateHello();
      await end();
      expect(input().hasAttribute("inputmode")).toBe(false);
      await expectBoxKeepsFocus(value, sent);
    });
  });

  it.each(ENDS)("with a mouse, %s leaves focus in the box", async (_, end, value, sent) => {
    await dictateHello();
    await end();
    await expectBoxKeepsFocus(value, sent);
  });

  it.each([
    ["under a finger", underFinger],
    ["with a mouse", (body: () => Promise<void>) => body()],
  ] as const)(
    "%s, the browser's end with focus on the button moves it to the box",
    async (_, tier) => {
      await tier(async () => {
        await dictateHello();
        sendBtn().focus();
        speech.last().finish();
        await expectBoxKeepsFocus("hello", []);
      });
    },
  );

  it("leaves focus the reader moved elsewhere on the page where it is", async () => {
    const other = document.createElement("input");
    document.body.append(other);
    try {
      await dictateHello();
      other.focus();
      speech.last().finish();
      expect(document.activeElement).toBe(other);
    } finally {
      other.remove();
    }
  });

  it.each([
    ["under a finger", underFinger],
    ["with a mouse", (body: () => Promise<void>) => body()],
  ] as const)("%s, a disabled box ends it with focus on no button", async (_, tier) => {
    await tier(async () => {
      await dictateHello();
      input().disabled = true;
      await settle();
      try {
        expect(dictation.listening.value).toBe(false);
        expect([sendBtn(), midTurn()]).not.toContain(document.activeElement);
        await userEvent.keyboard(" ");
        expect(submitted).toEqual([]);
      } finally {
        input().disabled = false;
        await settle();
      }
    });
  });
});

describe("words heard while focus is off the box (rule 9)", () => {
  it("land after a tab switch and the return to the chat, which restores its own draft", async () => {
    initComposerState();
    restoreComposerState(CHAT);
    const other = document.createElement("input");
    document.body.append(other);
    try {
      await startFrom(sendBtn());
      speech.last().hear(["hello", true]);
      expect(composerDraft(CHAT)).toBe("hello");
      other.focus();
      speech.last().hear(["hello", true], ["there", true]);
      emitBus(BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
      expect(dictation.listening.value).toBe(false);
      // The chat tab's return, as activateChatView runs it.
      emitBus(BUS_TAB_CHANGED, { to: CHAT, kind: "chat" });
      saveComposerState();
      setActive(CHAT);
      restoreComposerState(CHAT);
      expect(input().value).toBe("hello");
      await userEvent.click(input());
      expect(document.activeElement).toBe(input());
      expect(input().value).toBe("hello there");
      expect(submitted).toEqual([]);
    } finally {
      other.remove();
      _resetComposerStateForTest();
    }
  });

  it("never reach the box of a send held for its answer, nor the box the answer clears", async () => {
    let answer: (v: unknown) => void = () => undefined;
    nextSend = new Promise((resolve) => {
      answer = resolve;
    });
    await startFrom(sendBtn());
    speech.last().hear(["hello", true]);
    sendBtn().focus();
    speech.last().hear(["hello", true], ["world", false]);
    expect(input().value, "the second phrase waits for the box").toBe("hello");
    sendComposer();
    expect(submitted).toEqual(["hello"]);
    expect(input().readOnly).toBe(true);
    input().focus();
    expect(input().value).toBe("hello");
    answer("sent");
    await settle();
    expect(input().value).toBe("");
    input().blur();
    input().focus();
    expect(input().value).toBe("");
  });
});

describe("during a turn (rule 2)", () => {
  it("keeps Cancel and puts the microphone in Steer's slot while the box is empty", () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    expect(sendBtn().getAttribute("aria-label")).toBe("Cancel this turn");
    expectDictateFace(midTurn(), false);
  });

  it("swaps to Steer once there is text", () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    type("look here");
    expect(midTurn().getAttribute("aria-label")).toBe("Steer the running turn");
    expect(midTurn().hasAttribute("aria-pressed")).toBe(false);
    expect(midTurn().classList.contains("dictating")).toBe(false);
    type("");
    expectDictateFace(midTurn(), false);
  });

  it("swaps to Queue once there is text under a queueing run", () => {
    setSessions([session(QUEUEING)]);
    setSendState({ kind: "streaming" });
    type("later");
    expect(midTurn().querySelector(".send-btn-label")?.textContent).toBe("Queue");
  });

  it("dictates a steer from the mid-turn slot without cancelling or steering", async () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    await startFrom(midTurn());
    expect(dictation.listening.value).toBe(true);
    expect(cancelled).not.toHaveBeenCalled();
    expect(submitted).toEqual([]);
    expectDictateFace(midTurn(), true);
    expect(sendBtn().getAttribute("aria-label")).toBe("Cancel this turn");
    speech.last().hear(["try the other file", true]);
    expectDictateFace(midTurn(), true);
    midTurn().click();
    expect(midTurn().getAttribute("aria-label")).toBe("Steer the running turn");
    expect(submitted).toEqual([]);
  });

  it("keeps Cancel cancelling while dictating", async () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    await startFrom(midTurn());
    sendBtn().click();
    expect(cancelled).toHaveBeenCalledOnce();
  });

  it("moves the stop control to Send's slot when the turn ends mid-dictation", async () => {
    setSessions([session(STEERING)]);
    setSendState({ kind: "streaming" });
    await startFrom(midTurn());
    setSendState({ kind: "idle" });
    expect(midTurn().hidden).toBe(true);
    expectDictateFace(sendBtn(), true);
  });

  it("hides with Send where dictation cannot run", async () => {
    setSessions([session(STEERING)]);
    input().readOnly = true;
    await settle();
    try {
      setSendState({ kind: "streaming" });
      expect(midTurn().hidden).toBe(true);
    } finally {
      input().readOnly = false;
      await settle();
    }
  });
});

describe("the error face (rule 9)", () => {
  it("keeps Retry on an empty box, and stops dictation when the chat cannot take a send", async () => {
    await startFrom(sendBtn());
    speech.last().hear(["hold on", false]);
    setSendState({ kind: "error", reason: "The agent is starting." });
    expect(dictation.listening.value).toBe(false);
    expect(sendBtn().getAttribute("aria-pressed")).toBeNull();
    expect(sendBtn().querySelector(".send-btn-label")?.textContent).toBe("Retry");
    expect(input().value).toBe("hold on");
  });
});
