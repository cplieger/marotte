// Prompt-input history cycling: the draft round trip. The defect these pin (issue #956): ArrowDown
// read the saved draft AFTER exitCycling() had zeroed it, so returning to the draft emptied the box
// while Escape, which saved to a local first, worked.

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import { initPromptInput, sendComposer, setSendState } from "./prompt-input.js";
import { refreshContextUI } from "./context-ui.js";
import { setSessions, setActive, setChatInterruptMode } from "./store.js";
import type { Session } from "./types.js";

const CHAT = "c1";

const submitted: string[] = [];

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
let staged = false;

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
    vi.fn(),
    () => staged,
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
  // Newest prompt first is what userPrompts() produces, so ArrowUp reaches "newest" on the first
  // press and "oldest" on the second.
  setSessions([makeSession(["oldest", "newest"])]);
  setActive(CHAT);
  type("");
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
    staged = true;
    try {
      sendComposer();
    } finally {
      staged = false;
    }
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
});
