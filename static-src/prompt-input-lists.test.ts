// The composer's new-line keys: Shift+Enter continues a markdown list, plain Enter still sends, and
// under a finger Return is a new line that continues a list on its own. Real keystrokes, so the
// browser's own line break and undo are what is measured.

import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { initPromptInput } from "./prompt-input.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";
import type * as Platform from "./platform.js";

// Smart Punctuation is reverted on iOS only, so this file runs as an iPhone or iPad;
// `composer-touch.test.ts` holds the desktop side.
vi.mock("./platform.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Platform>()),
  isIOS: true,
}));

const CHAT = "c1";
const submitted: string[] = [];

function session(prompts: string[]): Session {
  return {
    id: CHAT,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    turns: new Map(
      prompts.map((text, i) => [
        `t${String(i)}`,
        {
          entries: [
            {
              id: `t${String(i)}-open`,
              turn: `t${String(i)}`,
              lane: "",
              kind: "turn_open" as const,
              payload: {
                source: "prompt" as const,
                n: i + 1,
                prompt: { id: `p${String(i)}`, text },
              },
              seq: 0,
              ts: i,
            },
          ],
          openEntries: new Map(),
        },
      ]),
    ),
    turn_order: prompts.map((_t, i) => `t${String(i)}`),
    turn_count: prompts.length,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

beforeAll(() => {
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <ul id="slash-menu" hidden></ul>
      <textarea id="prompt-input" aria-controls="slash-menu"></textarea>
      <button id="send-btn" type="submit"></button>
    </form>`;
  initPromptInput((text) => submitted.push(text), vi.fn());
});

function box(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** A focused box holding `text` with the caret at its end. */
function start(text: string): HTMLTextAreaElement {
  const el = box();
  el.focus();
  el.value = text;
  el.setSelectionRange(text.length, text.length);
  return el;
}

function touch(): void {
  document.documentElement.setAttribute("data-pointer", "coarse");
}

beforeEach(() => {
  setSessions([session(["oldest", "newest"])]);
  setActive(CHAT);
  submitted.length = 0;
  document.documentElement.setAttribute("data-pointer", "fine");
  (document.getElementById("slash-menu") as HTMLElement).hidden = true;
});

afterEach(() => {
  box().blur();
  box().value = "";
  document.documentElement.removeAttribute("data-pointer");
});

describe("Shift+Enter", () => {
  it("continues a bullet list", async () => {
    const el = start("- one");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("- one\n- ");
    expect(el.selectionStart).toBe(8);
  });

  it("continues a numbered list", async () => {
    const el = start("3) three");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("3) three\n4) ");
  });

  it("leaves the list on an empty item", async () => {
    const el = start("- one\n- ");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("- one\n");
  });

  it("is taken back by one undo", async () => {
    const el = start("- one");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    await userEvent.keyboard("{Control>}z{/Control}");
    expect(el.value).toBe("- one");
  });

  it("inserts a plain line break inside a code fence", async () => {
    const el = start("```\n- one");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("```\n- one\n");
  });

  it("inserts a plain line break while the / menu is open", async () => {
    const el = start("- one");
    (document.getElementById("slash-menu") as HTMLElement).hidden = false;
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("- one\n");
  });

  it("does nothing during an IME composition", () => {
    const el = start("- one");
    const ev = new KeyboardEvent("keydown", {
      key: "Enter",
      shiftKey: true,
      isComposing: true,
      bubbles: true,
      cancelable: true,
    });
    el.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    expect(el.value).toBe("- one");
  });

  it("does not send", async () => {
    start("- one");
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(submitted).toEqual([]);
  });
});

describe("Enter on a mouse-and-keyboard screen", () => {
  it("still sends a list item instead of continuing it", async () => {
    start("- one");
    await userEvent.keyboard("{Enter}");
    expect(submitted).toEqual(["- one"]);
  });

  it("labels the touch keyboard's Return as send", () => {
    start("");
    expect(box().enterKeyHint).toBe("send");
  });

  it("leaves a line break the browser inserted alone", () => {
    const el = start("- one\n");
    el.dispatchEvent(new InputEvent("input", { inputType: "insertLineBreak", bubbles: true }));
    expect(el.value).toBe("- one\n");
  });
});

describe("history recall", () => {
  it("is taken back by one undo", async () => {
    const el = start("");
    await userEvent.keyboard("draft");
    await userEvent.keyboard("{ArrowUp}");
    expect(el.value).toBe("newest");
    await userEvent.keyboard("{Control>}z{/Control}");
    expect(el.value).toBe("draft");
  });

  it("keeps cycling, because the recall is not announced as typing", async () => {
    const el = start("");
    await userEvent.keyboard("{ArrowUp}");
    await userEvent.keyboard("{ArrowUp}");
    expect(el.value).toBe("oldest");
  });
});

describe("Return under a finger", () => {
  it("inserts a line break and sends nothing", async () => {
    touch();
    const el = start("hello");
    await userEvent.keyboard("{Enter}");
    expect(el.value).toBe("hello\n");
    expect(submitted).toEqual([]);
  });

  it("continues a list from the inserted line break", async () => {
    touch();
    const el = start("1. one");
    await userEvent.keyboard("{Enter}");
    expect(el.value).toBe("1. one\n2. ");
  });

  it("leaves the list on an empty item", async () => {
    touch();
    const el = start("- one\n- ");
    await userEvent.keyboard("{Enter}");
    expect(el.value).toBe("- one\n");
  });

  it("does not continue a list when the break replaced a selection", async () => {
    touch();
    const el = start("- one two");
    el.setSelectionRange(6, 9);
    await userEvent.keyboard("{Enter}");
    expect(el.value).toBe("- one \n");
  });

  it("does not continue a list while the / menu is open", async () => {
    touch();
    const el = start("- one");
    (document.getElementById("slash-menu") as HTMLElement).hidden = false;
    await userEvent.keyboard("{Shift>}{Enter}{/Shift}");
    expect(el.value).toBe("- one\n");
  });

  it("labels the touch keyboard's Return as a new line", () => {
    touch();
    start("");
    box().blur();
    box().focus();
    expect(box().enterKeyHint).toBe("enter");
  });
});

/** The value an iOS Smart Punctuation substitution leaves, delivered the way the keyboard does:
 *  a replacement the page did not type. */
function substitute(el: HTMLTextAreaElement, before: string, after: string): void {
  el.value = before;
  el.dispatchEvent(new InputEvent("beforeinput", { inputType: "insertText", bubbles: true }));
  el.value = after;
  el.setSelectionRange(after.length, after.length);
  el.dispatchEvent(new InputEvent("input", { inputType: "insertReplacementText", bubbles: true }));
}

describe("smart punctuation", () => {
  it("turns an em dash back into the two hyphens typed under a finger", () => {
    touch();
    const el = start("");
    substitute(el, "git push -", "git push \u2014");
    expect(el.value).toBe("git push --");
  });

  it("straightens a curly quote typed under a finger", () => {
    touch();
    const el = start("");
    substitute(el, "don", "don\u2019");
    expect(el.value).toBe("don'");
  });

  it("still reverts on an iPad laid out for a trackpad", () => {
    const el = start("");
    substitute(el, "a -", "a \u2014");
    expect(el.value).toBe("a --");
  });
});
