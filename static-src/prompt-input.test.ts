// Prompt-input history cycling: the draft round trip. The defect these pin (issue #956): ArrowDown
// read the saved draft AFTER exitCycling() had zeroed it, so returning to the draft emptied the box
// while Escape, which saved to a local first, worked.

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { initPromptInput, sendComposer, setSendState } from "./prompt-input.js";
import { refreshContextUI } from "./context-ui.js";
import { setSessions, setActive, setChatInterruptMode } from "./store.js";
import type { Session } from "./types.js";

const CHAT = "c1";

const submitted: string[] = [];
const cancelled = vi.fn();

function makeSession(prompts: string[]): Session {
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
    // One turn per prompt, oldest first, each carrying the reader's own text on its `turn_open`.
    // `userPrompts` walks `turn_order` backwards, so this is what puts "newest" at the top of the
    // history.
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
    turn_order: prompts.map((_text, i) => `t${String(i)}`),
    turn_count: prompts.length,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** Whether the composer reports staged attachments; reset per test. */
const staged = signal(false);

/** The prompt bar is a module singleton bound to these three elements at init, so the DOM is
 *  staged once and every test reuses it. Replacing the nodes between tests would leave the
 *  keydown listener on the departed textarea. */
beforeAll(() => {
  // The action attribute is the real markup's backstop against a native form submission, and it is
  // staged here so a regression shows up as a navigation attempt in this suite rather than only as
  // a browser console violation.
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <textarea id="prompt-input"></textarea>
      <button id="midturn-send-btn" type="button" hidden></button>
      <button id="send-btn" type="submit"></button>
    </form>
    <button id="switch-model-btn">
      <span id="ctx-model-pill"></span><span id="ctx-effort-pill" class="hidden"></span>
    </button>
    <span id="context-indicator"></span>
    <span id="context-ring-fill"></span>
    <span id="context-ring-wedge"></span>
    <span id="context-label"></span>
    <span id="ctx-tokens"></span>
    <span id="ctx-credits"></span>
    <span id="ctx-turns"></span>
    <span id="ctx-last-turn"></span>
    <span id="ctx-entries"></span>
    <span id="ctx-tools"></span>
    <span id="ctx-metering"></span>`;
  initPromptInput(
    (text: string) => {
      submitted.push(text);
    },
    cancelled,
    staged,
  );
});

function input(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** Type into the box the way a user does: text, a caret at its end, and the `input` event a
 *  keystroke fires. That event is what ends cycling, so a helper that skips it leaves the
 *  controller's index pointing into history and the next test starts mid-cycle. */
function type(text: string): void {
  const el = input();
  el.value = text;
  el.setSelectionRange(text.length, text.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function press(key: string): void {
  input().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
}

beforeEach(() => {
  setSendState({ kind: "idle" });
  cancelled.mockClear();
  // Newest prompt first is what userPrompts() produces, so ArrowUp reaches "newest" on the first
  // press and "oldest" on the second.
  setSessions([makeSession(["oldest", "newest"])]);
  setActive(CHAT);
  type("");
  staged.value = false;
  submitted.length = 0;
});

describe("prompt history cycling", () => {
  it("gives the draft back on ArrowDown off the newest prompt", () => {
    type("half-written thought");
    press("ArrowUp");
    expect(input().value).toBe("newest");

    press("ArrowDown");
    expect(input().value).toBe("half-written thought");
  });

  it("gives the draft back on Escape", () => {
    type("half-written thought");
    press("ArrowUp");
    press("ArrowUp");
    expect(input().value).toBe("oldest");

    press("Escape");
    expect(input().value).toBe("half-written thought");
  });

  it("steps to the newer prompt when ArrowDown is not at the newest", () => {
    type("half-written thought");
    press("ArrowUp");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("newest");
  });

  it("restores an empty draft without falling back to a prompt", () => {
    press("ArrowUp");
    expect(input().value).toBe("newest");

    press("ArrowDown");
    expect(input().value).toBe("");
  });

  it("re-captures the draft on a second cycle", () => {
    type("first draft");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("first draft");

    type("second draft");
    press("ArrowUp");
    press("ArrowDown");
    expect(input().value).toBe("second draft");
  });
});

// The send path. Three gestures (the form's submit, Enter, the keyboard shortcut) reach ONE
// function, and nothing fakes a DOM event to get there.
describe("send", () => {
  it("sends the trimmed text on Enter", () => {
    expect.assertions(1);
    type("  hello  ");
    press("Enter");
    expect(submitted).toEqual(["hello"]);
  });

  it("clears the box after sending", () => {
    expect.assertions(1);
    type("hello");
    press("Enter");
    expect(input().value).toBe("");
  });

  it("sends once per Enter, not once per gesture-plus-event", () => {
    // A synthetic submit event dispatched from the keydown handler ran the send AND then let the
    // native submission proceed; one keystroke has to mean one send and nothing else.
    expect.assertions(1);
    type("hello");
    press("Enter");
    expect(submitted).toHaveLength(1);
  });

  it("refuses an empty or whitespace-only box", () => {
    expect.assertions(1);
    type("   ");
    press("Enter");
    expect(submitted).toEqual([]);
  });

  it("does not dispatch a non-cancelable submit event at the form", () => {
    // The cause, asserted directly: a `cancelable: false` submit event is one whose preventDefault
    // cannot work, so the native submission follows it.
    expect.assertions(1);
    const uncancelable = vi.fn();
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    const listener = (e: Event): void => {
      if (!e.cancelable) {
        uncancelable();
      }
    };
    form.addEventListener("submit", listener);
    type("hello");
    press("Enter");
    form.removeEventListener("submit", listener);
    expect(uncancelable).not.toHaveBeenCalled();
  });

  it("attempts no native form submission on Enter", () => {
    // The consequence, asserted through the DOM's own submit machinery: requestSubmit/submit is
    // what a native submission would reach.
    expect.assertions(1);
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    const native = vi.fn();
    const realSubmit = form.submit.bind(form);
    form.submit = native;
    try {
      type("hello");
      press("Enter");
    } finally {
      form.submit = realSubmit;
    }
    expect(native).not.toHaveBeenCalled();
  });

  it("sends through the form's own submit event too", () => {
    // The send button is type=submit, so a click arrives this way.
    expect.assertions(1);
    type("hello");
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(submitted).toEqual(["hello"]);
  });

  it("sends through the exported shortcut entry point", () => {
    expect.assertions(1);
    type("hello");
    sendComposer();
    expect(submitted).toEqual(["hello"]);
  });

  it("sends an empty box when an attachment is staged, and not otherwise", () => {
    expect.assertions(2);
    type("");
    sendComposer();
    expect(submitted).toEqual([]);
    staged.value = true;
    sendComposer();
    expect(submitted).toEqual([""]);
  });

  it("ends history cycling when it sends", () => {
    expect.assertions(3);
    type("draft");
    press("ArrowUp");
    expect(input().value).toBe("newest");
    press("Enter");
    expect(submitted).toEqual(["newest"]);
    // Cycling ended, so ArrowUp is a fresh walk from the top rather than a step deeper into the
    // previous cycle.
    press("ArrowUp");
    expect(input().value).toBe("newest");
  });
});

// The ring is the only context surface: a context past every threshold, on the active chat, leaves
// the composer's placeholder and Send's name as they are.
describe("a full context", () => {
  it("changes nothing on the composer", async () => {
    const s = makeSession([]);
    s.usage = { ...s.usage, context_pct: 99, context_size: 200_000 };
    setSessions([s]);
    setActive(CHAT);
    refreshContextUI(s);
    // The context bar paints in a frame; awaiting it keeps that paint, and any throw, in this test.
    await new Promise((resolve) => {
      requestAnimationFrame(resolve);
    });
    expect(document.getElementById("context-label")?.textContent).toBe("99%");
    expect(input().placeholder).toBe("Message Kiro...");
    const send = document.getElementById("send-btn");
    expect(send?.getAttribute("data-tooltip")).toBe("Send");
    expect(send?.getAttribute("aria-label")).toBe("Send");
  });
});

// While a turn runs the placeholder names what Send will do with the text, and it follows the
// active chat's mode without a send-state change.
describe("the busy placeholder", () => {
  it("names the chat's mode while a turn streams, and drops it once idle", () => {
    const box = input();
    setSendState({ kind: "streaming" });
    expect(box.placeholder).toBe("Steer the agent mid-turn...");

    setChatInterruptMode(CHAT, "queue");
    expect(box.placeholder).toBe("Queue a follow-up for after this turn...");

    setSendState({ kind: "idle" });
    expect(box.placeholder).toBe("Message Kiro...");
  });
});

// Send never disables and nothing disables the composer: mid-turn the button becomes
// Cancel (a `type="button"` click, so it cannot submit the form) and a failure keeps
// both controls live so the reader can retry at once.
describe("the send button's state", () => {
  function sendBtn(): HTMLButtonElement {
    return document.getElementById("send-btn") as HTMLButtonElement;
  }

  it("is an enabled Cancel button mid-turn, with the textarea still enabled", () => {
    expect.assertions(4);
    setSendState({ kind: "streaming" });
    expect(sendBtn().disabled).toBe(false);
    expect(sendBtn().getAttribute("aria-label")).toBe("Cancel this turn");
    expect(sendBtn().type).toBe("button");
    expect(input().disabled).toBe(false);
  });

  it("stays enabled after a failure, with the textarea still enabled", () => {
    expect.assertions(2);
    setSendState({ kind: "error", reason: "The bridge exited." });
    expect(sendBtn().disabled).toBe(false);
    expect(input().disabled).toBe(false);
  });

  it("is an enabled submit button again once the turn is over", () => {
    expect.assertions(3);
    setSendState({ kind: "streaming" });
    setSendState({ kind: "idle" });
    expect(sendBtn().disabled).toBe(false);
    expect(sendBtn().type).toBe("submit");
    expect(sendBtn().getAttribute("aria-label")).toBe("Send");
  });

  // The visible word sits left of the glyph and is a word of the accessible name (matched
  // case-insensitively, as WCAG 2.5.3 does), so speech input can say what the reader sees.
  it.each([
    [{ kind: "idle" } as const, "Send"],
    [{ kind: "streaming" } as const, "Cancel"],
    [{ kind: "error", reason: "The bridge exited." } as const, "Retry"],
  ])("labels the %o face with its word before the glyph", (state, word) => {
    setSendState(state);
    const [first, second] = sendBtn().children;
    expect(first?.classList.contains("send-btn-label")).toBe(true);
    expect(first?.textContent).toBe(word);
    expect(second?.tagName.toLowerCase()).toBe("svg");
    expect(sendBtn().getAttribute("aria-label")?.toLowerCase()).toContain(word.toLowerCase());
  });
});

// Under a finger Return is a new line, so while a turn runs this button is the only way to send
// what the box holds. Cancel keeps its own button beside it.
describe("the mid-turn Send", () => {
  function midTurn(): HTMLButtonElement {
    return document.getElementById("midturn-send-btn") as HTMLButtonElement;
  }

  function sendBtn(): HTMLButtonElement {
    return document.getElementById("send-btn") as HTMLButtonElement;
  }

  it("stays hidden mid-turn while the box is empty or blank", () => {
    setSendState({ kind: "streaming" });
    expect(midTurn().hidden).toBe(true);
    type("   \n ");
    expect(midTurn().hidden).toBe(true);
  });

  it("appears mid-turn for a staged attachment in a blank box, and leaves when it is unstaged", () => {
    setSendState({ kind: "streaming" });
    staged.value = true;
    expect(midTurn().hidden).toBe(false);
    staged.value = false;
    expect(midTurn().hidden).toBe(true);
  });

  it("stays hidden when idle whatever is staged", () => {
    staged.value = true;
    expect(midTurn().hidden).toBe(true);
    type("hello");
    expect(midTurn().hidden).toBe(true);
  });

  it("appears mid-turn once the box holds text, and leaves when it is cleared", () => {
    setSendState({ kind: "streaming" });
    type("look at the tests first");
    expect(midTurn().hidden).toBe(false);
    type("");
    expect(midTurn().hidden).toBe(true);
  });

  it("appears for text that was already in the box when the turn started", () => {
    type("queued thought");
    expect(midTurn().hidden).toBe(true);
    setSendState({ kind: "streaming" });
    expect(midTurn().hidden).toBe(false);
  });

  it("follows a recalled prompt, which fires no input event", () => {
    setSendState({ kind: "streaming" });
    press("ArrowUp");
    expect(input().value).toBe("newest");
    expect(midTurn().hidden).toBe(false);
    press("Escape");
    expect(midTurn().hidden).toBe(true);
  });

  it("is hidden when idle and leaves when the turn ends, giving Send back to the one button", () => {
    type("hello");
    expect(midTurn().hidden).toBe(true);
    setSendState({ kind: "streaming" });
    expect(midTurn().hidden).toBe(false);
    setSendState({ kind: "idle" });
    expect(midTurn().hidden).toBe(true);
    expect(sendBtn().getAttribute("aria-label")).toBe("Send");
  });

  it("is hidden on the error face", () => {
    type("hello");
    setSendState({ kind: "error", reason: "The bridge exited." });
    expect(midTurn().hidden).toBe(true);
  });

  it("names a steer in steer mode, apart from Cancel", () => {
    setChatInterruptMode(CHAT, "steer");
    setSendState({ kind: "streaming" });
    type("hello");
    expect(midTurn().getAttribute("aria-label")).toBe("Steer the running turn");
    expect(midTurn().getAttribute("data-tooltip")).toBe("Steer the running turn");
    expect(midTurn().querySelector(".send-btn-label")?.textContent).toBe("Steer");
    expect(sendBtn().getAttribute("aria-label")).toBe("Cancel this turn");
  });

  it("names a queued follow-up in queue mode, and repaints when the mode changes", () => {
    setChatInterruptMode(CHAT, "queue");
    setSendState({ kind: "streaming" });
    type("hello");
    expect(midTurn().getAttribute("aria-label")).toBe("Queue for after this turn");
    expect(midTurn().getAttribute("data-tooltip")).toBe("Queue for after this turn");
    expect(midTurn().querySelector(".send-btn-label")?.textContent).toBe("Queue");

    setChatInterruptMode(CHAT, "steer");
    expect(midTurn().getAttribute("aria-label")).toBe("Steer the running turn");
  });

  it("sends the box through the composer's send path, then empties it and hides", () => {
    setSendState({ kind: "streaming" });
    type("  steer this  ");
    midTurn().click();
    expect(submitted).toEqual(["steer this"]);
    expect(cancelled).not.toHaveBeenCalled();
    expect(input().value).toBe("");
    expect(midTurn().hidden).toBe(true);
  });

  it("leaves Cancel cancelling while it is shown", () => {
    setSendState({ kind: "streaming" });
    type("not yet");
    expect(midTurn().hidden).toBe(false);
    sendBtn().click();
    expect(cancelled).toHaveBeenCalledOnce();
    expect(submitted).toEqual([]);
    expect(input().value).toBe("not yet");
  });

  it("hands keyboard focus back to the box when its own send hides it", () => {
    setSendState({ kind: "streaming" });
    type("hello");
    input().focus();
    midTurn().focus();
    expect(midTurn().matches(":focus-visible"), "precondition: keyboard-style focus").toBe(true);
    midTurn().click();
    expect(document.activeElement).toBe(input());
  });
});
