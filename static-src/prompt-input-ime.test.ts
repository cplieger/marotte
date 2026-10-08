// The IME guard on the composer's Enter key.
import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import type * as ModPromptInput from "./prompt-input.js";

/** Cache-buster for the re-imports below. */
let bootSeq = 0;

vi.mock("./platform.js", () => ({ fixIOSViewport: vi.fn(), isIOS: false }));
vi.mock("./pill-expand.js", () => ({ collapseAll: vi.fn() }));
// The session carries ONE prior user prompt, which the ArrowUp case at the bottom of this file
// needs: against an empty session there is nothing to navigate to, so a handler that bailed at the
// top and one that ran correctly both leave the box empty and the assertion cannot fail.
const { PRIOR_PROMPT } = vi.hoisted(() => ({ PRIOR_PROMPT: "the prompt before this one" }));
vi.mock("./store.js", () => ({
  // No active chat: the busy placeholder reads Steer, which no case here observes.
  activeSession: { value: undefined, peek: () => undefined },
  getActive: () => ({
    // ONE prompt-opened turn, in the shape the appender writes it: the `turn_open` is `entries[0]`
    // and carries the prompt, which is where `userPrompts()` reads a past prompt from.
    turn_order: ["t1"],
    turns: new Map([
      [
        "t1",
        {
          entries: [
            {
              id: "t1-open",
              turn: "t1",
              lane: "",
              kind: "turn_open",
              seq: 0,
              ts: 1,
              payload: { source: "prompt", n: 1, prompt: { id: "m1", text: PRIOR_PROMPT } },
            },
          ],
          openEntries: new Map(),
        },
      ],
    ]),
  }),
  getActiveId: () => "c1",
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened this graph and these
  // names are imported somewhere in it. No case here calls them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

// Sends, counted at the controller's own onSubmit. The send is the subject; the event was only ever
// a proxy for it.
let submits = 0;

/** Press Enter with an explicit IME state. `keyCode` is set through the init dict, because a
 *  KeyboardEvent derives it from `key` otherwise. */
function pressEnter(opts: { isComposing?: boolean; keyCode?: number } = {}): KeyboardEvent {
  const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
  const e = new KeyboardEvent("keydown", {
    key: "Enter",
    bubbles: true,
    cancelable: true,
    ...opts,
  });
  input.dispatchEvent(e);
  return e;
}

function compose(type: "compositionstart" | "compositionend"): void {
  const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
  input.dispatchEvent(new CompositionEvent(type, { bubbles: true }));
}

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  bootSeq++;
  submits = 0;
  document.body.innerHTML = `
    <form id="prompt-form">
      <div id="prompt-box">
        <textarea id="prompt-input"></textarea>
      </div>
      <button id="midturn-send-btn" type="button" hidden></button>
      <button id="send-btn" type="submit"></button>
    </form>`;
  const mod = (await import(
    /* @vite-ignore */ `./prompt-input.ts?boot=${bootSeq}`
  )) as typeof ModPromptInput;
  mod.initPromptInput(
    () => {
      submits += 1;
    },
    () => undefined,
  );
  // A send needs text: every case below asks whether Enter reached the send, so an empty box would
  // make a blocked Enter and a delivered one indistinguishable.
  (document.getElementById("prompt-input") as HTMLTextAreaElement).value = "hello";
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Enter with no composition in flight", () => {
  it("submits", () => {
    const e = pressEnter();
    expect(submits).toBe(1);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("leg 1: the composition flag", () => {
  it("lets Enter through to the browser between compositionstart and end", () => {
    compose("compositionstart");
    const e = pressEnter();
    expect(submits).toBe(0);
    // Not prevented: the browser has to see the key to commit the candidate.
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps blocking for a 50ms tail after compositionend", () => {
    compose("compositionstart");
    compose("compositionend");
    expect(pressEnter().defaultPrevented).toBe(false);
    expect(submits).toBe(0);

    vi.advanceTimersByTime(49);
    expect(submits).toBe(0);

    vi.advanceTimersByTime(1);
    pressEnter();
    expect(submits).toBe(1);
  });

  it("does not let a stale tail timer end a composition that has restarted", () => {
    compose("compositionstart");
    compose("compositionend");
    vi.advanceTimersByTime(30);
    compose("compositionstart"); // second candidate, tail must be cancelled
    vi.advanceTimersByTime(100);
    expect(pressEnter().defaultPrevented).toBe(false);
    expect(submits).toBe(0);
  });
});

describe("leg 2: the native isComposing flag", () => {
  it("lets Enter through with no compositionstart seen at all", () => {
    const e = pressEnter({ isComposing: true });
    expect(submits).toBe(0);
    expect(e.defaultPrevented).toBe(false);
  });
});

describe("leg 3: keyCode 229", () => {
  it("blocks the commit Enter that reports 229 while isComposing is false", () => {
    // THE reason to port Crew's predicate rather than write `e.isComposing`: several IMEs report
    // the "processed by the IME" sentinel on the final Enter with isComposing already false and no
    // composition event outstanding.
    const e = pressEnter({ isComposing: false, keyCode: 229 });
    expect(e.defaultPrevented).toBe(false);
    expect(submits).toBe(0);
  });
});

describe("resetting a stuck composition", () => {
  it("clears it on blur, so a dropped compositionend cannot disable Enter", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart"); // no matching end: the Android focus-loss case
    input.dispatchEvent(new FocusEvent("blur"));
    pressEnter();
    expect(submits).toBe(1);
  });

  it("clears it on Escape, unconditionally rather than only while cycling history", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    pressEnter();
    expect(submits).toBe(1);
  });

  it("leaves a plain Escape propagating, so pills and the dock still close", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    let reachedDocument = false;
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        reachedDocument = true;
      }
    });
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(reachedDocument).toBe(true);
  });
});

describe("what the guard must NOT change", () => {
  it("keeps Shift+Enter inserting a newline", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    const e = new KeyboardEvent("keydown", {
      key: "Enter",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    input.dispatchEvent(e);
    expect(submits).toBe(0);
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps ArrowUp history navigation working during a composition", () => {
    // The guard sits INSIDE the Enter branch for this reason: an early return at the top of the
    // handler would break history navigation mid-candidate, which is not the bug being fixed.
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart");
    const e = new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true });
    input.dispatchEvent(e);
    expect(input.value).toBe(PRIOR_PROMPT);
    // Consumed: the key moved through history rather than reaching the browser's own caret
    // handling.
    expect(e.defaultPrevented).toBe(true);
  });
});
