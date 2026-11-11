// Model-based: presses on Send's and Steer's slots, interleaved with everything that repaints them mid-press (the
// engine ending, results, focus moves, the turn starting or ending, typing), never act on a face they did not
// begin on.

import { beforeAll, describe, expect, it } from "vitest";
import fc from "fast-check";
import { effect, signal } from "@cplieger/ui";
import { initPromptInput, setSendState } from "./prompt-input.js";
import { createDictation, type Dictation } from "./dictation.js";
import { setActive, setSessions } from "./store.js";
import type { Session } from "./types.js";
import { fakeSpeech } from "./__test-helpers__/fake-speech.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";

type Slot = "main" | "side";

type World =
  | { readonly k: "result"; readonly text: string; readonly final: boolean }
  | { readonly k: "end" }
  | { readonly k: "begin" }
  | { readonly k: "focusBox" }
  | { readonly k: "focusButton"; readonly slot: Slot }
  | { readonly k: "blurBox" }
  | { readonly k: "turn"; readonly on: boolean }
  | { readonly k: "type"; readonly text: string };

interface Gesture {
  readonly k: "gesture";
  readonly slot: Slot;
  readonly input: "pointer" | "space" | "enter";
  readonly clicks: number;
  /** World events before each press start and each click, in order. */
  readonly gaps: readonly (readonly World[])[];
}

type Step = World | Gesture;

/** What the reader saw on the button when the press began. */
type Began = "send" | "cancel" | "start" | "stop";

/** What a press may end up doing, by the face it began on: the spec's oracle. */
const MAY: Record<Began, readonly string[]> = {
  send: ["send"],
  cancel: ["cancel"],
  start: ["dictate"],
  stop: [],
};

const CHAT = "c1";
const speech = fakeSpeech({ cloud: null });
const staged = signal(false);
let submitted = 0;
let cancelled = 0;
let starts = 0;
let dictation: Dictation;

const session: Session = {
  id: CHAT,
  name: "test",
  model: "",
  acp_session_id: "",
  current_mode_id: "",
  supervised_mode: false,
  usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
  turns: new Map(),
  turn_order: [],
  turn_count: 0,
  has_more: false,
  thinking: false,
  working_label: "Thinking",
  run: runAt(1, [turnEntry("t1")], {
    send: "steer",
    cancel: "t1",
    next_wait: { after: "turns", turns: 1, this: true },
  }),
};

beforeAll(() => {
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <textarea id="prompt-input"></textarea>
      <button id="midturn-send-btn" class="send-btn" type="button" hidden></button>
      <button id="send-btn" class="send-btn" type="submit"></button>
    </form>`;
  dictation = createDictation(speech.platform, box());
  initPromptInput(
    () => {
      submitted++;
      return undefined;
    },
    () => {
      cancelled++;
    },
    staged,
    () => undefined,
    dictation,
  );
  let was = false;
  effect(() => {
    const now = dictation.listening.value;
    if (now && !was) {
      starts++;
    }
    was = now;
  });
  setSessions([session]);
  setActive(CHAT);
});

function box(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

function button(slot: Slot): HTMLButtonElement {
  return document.getElementById(
    slot === "main" ? "send-btn" : "midturn-send-btn",
  ) as HTMLButtonElement;
}

function began(btn: HTMLButtonElement): Began | null {
  if (btn.hidden) {
    return null;
  }
  if (btn.getAttribute("aria-label") === "Dictate") {
    return btn.getAttribute("aria-pressed") === "true" ? "stop" : "start";
  }
  return btn.getAttribute("aria-label") === "Cancel this turn" ? "cancel" : "send";
}

function type(text: string): void {
  const el = box();
  el.value = text;
  el.setSelectionRange(text.length, text.length);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

async function flush(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }
}

async function apply(w: World): Promise<void> {
  const rec = speech.made.at(-1);
  switch (w.k) {
    case "result":
      rec?.hear([w.text, w.final]);
      break;
    case "end":
      rec?.finish();
      break;
    case "begin":
      rec?.begin();
      break;
    case "focusBox":
      box().focus();
      break;
    case "focusButton":
      button(w.slot).focus();
      break;
    case "blurBox":
      box().blur();
      break;
    case "turn":
      setSendState({ kind: w.on ? "streaming" : "idle" });
      break;
    case "type":
      type(w.text);
      break;
  }
  await flush();
}

async function applyAll(ws: readonly World[] | undefined): Promise<void> {
  for (const w of ws ?? []) {
    await apply(w);
  }
}

/** Run the gesture; `began` is read off the button at its first press start, after any world event before it. */
async function press(g: Gesture): Promise<Began | null> {
  const btn = button(g.slot);
  const click = (detail: number): void => {
    btn.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail }));
  };
  if (g.input !== "pointer") {
    const face = began(btn);
    if (face === null) {
      return null;
    }
    btn.dispatchEvent(
      new KeyboardEvent("keydown", { key: g.input === "space" ? " " : "Enter", bubbles: true }),
    );
    if (g.input === "space") {
      await applyAll(g.gaps[0]);
    }
    click(0);
    await flush();
    return face;
  }
  let face: Began | null = null;
  for (let i = 1; i <= g.clicks; i++) {
    await applyAll(g.gaps[2 * i - 2]);
    if (i === 1) {
      face = began(btn);
      if (face === null) {
        return null;
      }
    }
    btn.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, cancelable: true }));
    btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: i }));
    await applyAll(g.gaps[2 * i - 1]);
    click(i);
    await flush();
  }
  return face;
}

const world: fc.Arbitrary<World> = fc.oneof(
  fc.record({
    k: fc.constant("result" as const),
    text: fc.constantFrom("hello", "hello there", "try the other"),
    final: fc.boolean(),
  }),
  fc.constant({ k: "end" as const }),
  fc.constant({ k: "begin" as const }),
  fc.constant({ k: "focusBox" as const }),
  fc.record({
    k: fc.constant("focusButton" as const),
    slot: fc.constantFrom<Slot>("main", "side"),
  }),
  fc.constant({ k: "blurBox" as const }),
  fc.record({ k: fc.constant("turn" as const), on: fc.boolean() }),
  fc.record({ k: fc.constant("type" as const), text: fc.constantFrom("", "  ", "words") }),
);

const gesture: fc.Arbitrary<Gesture> = fc
  .record({
    slot: fc.constantFrom<Slot>("main", "side"),
    input: fc.constantFrom("pointer" as const, "space" as const, "enter" as const),
    clicks: fc.integer({ min: 1, max: 3 }),
  })
  .chain((g) =>
    fc
      .array(fc.array(world, { maxLength: 2 }), {
        minLength: 2 * g.clicks,
        maxLength: 2 * g.clicks,
      })
      .map((gaps) => ({ k: "gesture" as const, ...g, gaps })),
  );

const step: fc.Arbitrary<Step> = fc.oneof(
  { arbitrary: gesture, weight: 3 },
  { arbitrary: world, weight: 2 },
);

async function reset(): Promise<void> {
  dictation.stop();
  setSendState({ kind: "idle" });
  box().focus();
  type("");
  await flush();
  submitted = 0;
  cancelled = 0;
  starts = 0;
}

describe("a press on the composer's buttons (rule 3)", () => {
  it("never sends or steers from Dictate or Stop, never dictates from Send or Cancel, and Stop only stops", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(step, { minLength: 1, maxLength: 8 }), async (steps) => {
        await reset();
        for (const s of steps) {
          if (s.k !== "gesture") {
            await apply(s);
            continue;
          }
          const before = { submitted, cancelled, starts, made: speech.made.length };
          const face = await press(s);
          if (face === null) {
            continue;
          }
          const sent = submitted > before.submitted;
          const cancel = cancelled > before.cancelled;
          const dictated = starts > before.starts || speech.made.length > before.made;
          const did = [sent && "send", cancel && "cancel", dictated && "dictate"].filter(
            (d) => d !== false,
          );
          expect(MAY[face], `a press that began on ${face} did ${did.join(", ")}`).toEqual(
            expect.arrayContaining(did),
          );
        }
      }),
      { numRuns: 300 },
    );
  });
});
