// The microphone in the composer's Send slot, painted on the real buttons: its face, its name and pressed state,
// where a press goes, and that it never sends or cancels by itself.

import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { signal } from "@cplieger/ui";
import { initPromptInput, sendComposer, setSendState } from "./prompt-input.js";
import { createDictation, type Dictation } from "./dictation.js";
import { ICON_MIC, ICON_SEND } from "./icons.js";
import { setActive, setSessions } from "./store.js";
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
  dictation = createDictation(speech.platform, input(), [sendBtn(), midTurn()]);
  initPromptInput(
    (text: string) => {
      submitted.push(text);
      return undefined;
    },
    cancelled,
    staged,
    () => undefined,
    dictation,
  );
});

beforeEach(async () => {
  dictation.stop();
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

function expectDictateFace(btn: HTMLButtonElement, pressed: boolean): void {
  expect(btn.hidden).toBe(false);
  expect(btn.getAttribute("aria-label")).toBe("Dictate");
  expect(btn.getAttribute("aria-pressed")).toBe(String(pressed));
  expect(btn.getAttribute("data-tooltip")).toBe(pressed ? "Stop dictation" : "Dictate");
  expect(btn.type).toBe("button");
  expect(btn.classList.contains("dictating")).toBe(pressed);
  expect(btn.querySelector(".send-btn-label")?.textContent).toBe("Dictate");
  expect(glyphMarkup(btn.querySelector("svg"))).toBe(registryMarkup(ICON_MIC));
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
