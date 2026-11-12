// Model-based: presses on Send's and Steer's slots (pointer presses that click, tap, drag off, are cancelled or open a
// context menu, right and middle presses, keys held with auto-repeat, a press of the other kind inside either, clicks
// with no press of their own), interleaved with everything that repaints them mid-press (the engine ending, results,
// focus moves, the turn starting or ending, typing, sends), never act on a face they did not begin on, and words
// heard before a send or a clear never reach the box after it. Under a finger or a mouse, a Space typed right after
// a session ends never sends, cancels or dictates.

import { afterAll, beforeAll, describe, expect, it } from "vitest";
import fc from "fast-check";
import { effect, signal } from "@cplieger/ui";
import { initPromptInput, sendComposer, setSendState } from "./prompt-input.js";
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
  | { readonly k: "type"; readonly text: string }
  /** The keyboard shortcut's send; a held one waits for `answer`. */
  | { readonly k: "send"; readonly held: boolean }
  | { readonly k: "answer" }
  /** A right or middle press and release, which makes no click; a right press's menu takes a held primary press's
   *  release (https://bugs.chromium.org/p/chromium/issues/detail?id=149091). */
  | { readonly k: "auxPress"; readonly slot: Slot; readonly button: 1 | 2 };

interface Gesture {
  readonly k: "gesture";
  readonly slot: Slot;
  readonly input: "pointer" | "space" | "enter";
  /** Pointer: the clicks of a multi-click. Key: the auto-repeats while held. */
  readonly count: number;
  /** How a pointer press ends: a mouse click, a finger's tap, a release dragged off, a cancel, or a context menu. */
  readonly end: "click" | "tap" | "dragOff" | "cancel" | "contextmenu";
  /** World events before each press start, click, repeat and key up, in order. */
  readonly gaps: readonly (readonly World[])[];
  /** A whole press of the other kind on the same button while this one is down: a key inside a pointer press, a
   *  mouse click inside a key press. */
  readonly inner: "none" | "enter" | "space" | "pointer";
}

/** A click with no press of its own: `click()`, or an assistive technology's simulated mouse click. */
interface Click {
  readonly k: "click";
  readonly slot: Slot;
  readonly detail: 0 | 1;
}

/** A live session's end, then a Space typed wherever focus is, as the browser delivers it. */
interface EndThenSpace {
  readonly k: "endThenSpace";
  readonly how: "finish" | "fail" | "escape" | "hide";
}

type Step = World | Gesture | Click | EndThenSpace;

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
/** Recognizers made before the last send or clear; their words must never reach the box again. */
let cut = 0;
/** What the box held when the send still waiting for its answer went. */
let sentRaw: string | null = null;
let worldSend: { readonly held: boolean } | null = null;
let answer: (() => void) | null = null;
let dictation: Dictation;
/** A right press's menu opened since the gesture's press went down. */
let menuOpened = false;
/** What the presses inside the gesture under test did, so the gesture's own count leaves them out. */
let byInner = { submitted: 0, cancelled: 0, starts: 0, made: 0 };

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
      cut = speech.made.length;
      const by = worldSend;
      if (by === null) {
        submitted++;
        return undefined;
      }
      if (!by.held) {
        return undefined;
      }
      sentRaw = box().value;
      return new Promise((resolve) => {
        answer = () => {
          resolve("sent");
        };
      });
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
  cut = speech.made.length;
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

/** Settles the send waiting for its answer, as the server's answer does. */
async function answerSend(): Promise<void> {
  const settle = answer;
  answer = null;
  settle?.();
  await new Promise((resolve) => setTimeout(resolve, 0));
  sentRaw = null;
}

async function apply(w: World): Promise<void> {
  const i = speech.made.length - 1;
  const rec = speech.made.at(-1);
  switch (w.k) {
    case "result":
      // Each recognizer's words carry its index, so the box shows whose words it holds.
      rec?.hear([`${w.text} r${String(i)}`, w.final]);
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
      // The reader cannot type into a held send's read-only box.
      if (!box().readOnly) {
        type(w.text);
      }
      break;
    case "send":
      worldSend = w;
      try {
        sendComposer();
      } finally {
        worldSend = null;
      }
      break;
    case "answer":
      await answerSend();
      break;
    case "auxPress": {
      const btn = button(w.slot);
      menuOpened ||= w.button === 2;
      pointer(btn, "pointerdown", true, w.button);
      pointer(btn, "pointerup", true, w.button);
      btn.dispatchEvent(
        new MouseEvent(w.button === 2 ? "contextmenu" : "auxclick", {
          bubbles: true,
          cancelable: true,
          button: w.button,
        }),
      );
      break;
    }
  }
  await flush();
  expectNoStaleWords();
}

/** A held send's read-only box shows exactly what was sent, and no other box shows words said before the cut. */
function expectNoStaleWords(): void {
  const b = box();
  if (b.readOnly) {
    expect(b.value, "words written into a held send's box").toBe(sentRaw);
    return;
  }
  const stale = [...b.value.matchAll(/r(\d+)/g)].filter((m) => Number(m[1]) < cut);
  expect(stale, `words said before the send or clear reached the box: ${b.value}`).toEqual([]);
}

async function applyAll(ws: readonly World[] | undefined): Promise<void> {
  for (const w of ws ?? []) {
    await apply(w);
  }
}

function click(btn: HTMLButtonElement, detail: number): void {
  btn.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, detail }));
}

function key(type: "keydown" | "keyup", name: string, repeat = false): KeyboardEvent {
  const e = new KeyboardEvent(type, { key: name, repeat, bubbles: true, cancelable: true });
  document.activeElement?.dispatchEvent(e);
  return e;
}

function pointer(btn: HTMLButtonElement, type: string, onButton = true, button = 0): void {
  const r = btn.getBoundingClientRect();
  const at = {
    clientX: onButton ? r.left + r.width / 2 : r.right + 40,
    clientY: r.top + r.height / 2,
  };
  (onButton ? btn : document.body).dispatchEvent(
    new PointerEvent(type, { bubbles: true, cancelable: true, button, ...at }),
  );
}

function counts(): typeof byInner {
  return { submitted, cancelled, starts, made: speech.made.length };
}

/** What happened since `before`, less what `credited` already accounts for. */
function did(before: typeof byInner, credited = byInner): string[] {
  const sent = submitted - credited.submitted > before.submitted;
  const cancel = cancelled - credited.cancelled > before.cancelled;
  const dictated =
    starts - credited.starts > before.starts || speech.made.length - credited.made > before.made;
  return [sent && "send", cancel && "cancel", dictated && "dictate"].filter((d) => d !== false);
}

/** A whole press of `kind` on `btn`, made as the browser makes it, checked against the face it began on. */
async function innerPress(btn: HTMLButtonElement, kind: Gesture["inner"]): Promise<void> {
  const face = began(btn);
  if (kind === "none" || face === null) {
    return;
  }
  const before = counts();
  if (kind === "pointer") {
    pointer(btn, "pointerdown");
    btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: 1 }));
    pointer(btn, "pointerup");
    click(btn, 1);
  } else {
    btn.focus();
    const name = kind === "space" ? " " : "Enter";
    const down = key("keydown", name);
    if (name === "Enter" && !down.defaultPrevented) {
      click(btn, 0);
    }
    await flush();
    const kept = document.activeElement === btn;
    key("keyup", name);
    if (name === " " && kept && !down.defaultPrevented) {
      click(btn, 0);
    }
  }
  await flush();
  const done = did(before, { submitted: 0, cancelled: 0, starts: 0, made: 0 });
  expect(MAY[face], `an inner ${kind} press that began on ${face} did ${done.join(", ")}`).toEqual(
    expect.arrayContaining(done),
  );
  const after = counts();
  byInner = {
    submitted: byInner.submitted + after.submitted - before.submitted,
    cancelled: byInner.cancelled + after.cancelled - before.cancelled,
    starts: byInner.starts + after.starts - before.starts,
    made: byInner.made + after.made - before.made,
  };
}

/** A key held on the focused button, its clicks made as the browser makes them: Enter at every key down the page
 *  did not cancel, on the element focused then; Space at key up, only on a button that kept focus throughout. */
async function holdKey(g: Gesture, btn: HTMLButtonElement): Promise<void> {
  const name = g.input === "space" ? " " : "Enter";
  let kept = true;
  const lost = (): void => {
    kept = false;
  };
  btn.addEventListener("blur", lost);
  const down = key("keydown", name);
  if (name === "Enter" && !down.defaultPrevented) {
    click(btn, 0);
  }
  await flush();
  if (g.inner === "pointer") {
    await innerPress(btn, "pointer");
  }
  for (let r = 0; r < g.count; r++) {
    await applyAll(g.gaps[r]);
    const target = document.activeElement;
    const again = key("keydown", name, true);
    if (name === "Enter" && target instanceof HTMLButtonElement && !again.defaultPrevented) {
      click(target, 0);
    }
    await flush();
  }
  await applyAll(g.gaps[g.count]);
  const target = document.activeElement;
  key("keyup", name);
  if (name === " " && target === btn && kept && !down.defaultPrevented) {
    click(btn, 0);
  }
  btn.removeEventListener("blur", lost);
  await flush();
}

/** Run the gesture; `face` is read off the button at its first press start, after any world event before it, and
 *  `clicked` says whether the gesture made a click of its own. */
async function press(
  g: Gesture,
): Promise<{ readonly face: Began | null; readonly clicked: boolean }> {
  const btn = button(g.slot);
  if (g.input !== "pointer") {
    btn.focus();
    const face = began(btn);
    if (face !== null) {
      await holdKey(g, btn);
    }
    return { face, clicked: true };
  }
  const clicks = aborted(g) ? 1 : g.count;
  let face: Began | null = null;
  for (let i = 1; i <= clicks; i++) {
    await applyAll(g.gaps[2 * i - 2]);
    if (i === 1) {
      face = began(btn);
      if (face === null) {
        return { face, clicked: false };
      }
    }
    pointer(btn, "pointerdown");
    btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: i }));
    menuOpened = false;
    await applyAll(g.gaps[2 * i - 1]);
    if (i === 1 && g.inner !== "pointer") {
      await innerPress(btn, g.inner);
    }
    if (menuOpened) {
      return { face, clicked: i > 1 };
    }
    switch (g.end) {
      case "dragOff":
        pointer(btn, "pointerup", false);
        break;
      case "cancel":
        pointer(btn, "pointercancel");
        break;
      case "contextmenu":
        btn.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
        pointer(btn, "pointerup");
        break;
      case "tap":
        pointer(btn, "pointerup");
        pointer(btn, "lostpointercapture");
        click(btn, i);
        break;
      case "click":
        pointer(btn, "pointerup");
        click(btn, i);
        break;
    }
    await flush();
  }
  return { face, clicked: !aborted(g) };
}

function hidePage(): void {
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
  try {
    document.dispatchEvent(new Event("visibilitychange"));
  } finally {
    delete (document as { visibilityState?: unknown }).visibilityState;
  }
}

async function endThenSpace(e: EndThenSpace): Promise<void> {
  if (!dictation.listening.peek()) {
    return;
  }
  const rec = speech.made.at(-1);
  const before = counts();
  switch (e.how) {
    case "finish":
      rec?.finish();
      break;
    case "fail":
      rec?.fail("bad-grammar");
      break;
    case "escape":
      key("keydown", "Escape");
      key("keyup", "Escape");
      rec?.finish();
      break;
    case "hide":
      hidePage();
      break;
  }
  await flush();
  expect(dictation.listening.peek(), `${e.how} ended the session`).toBe(false);
  const target = document.activeElement;
  const down = key("keydown", " ");
  key("keyup", " ");
  if (target instanceof HTMLButtonElement && !down.defaultPrevented) {
    click(target, 0);
  }
  await flush();
  const done = did(before, { submitted: 0, cancelled: 0, starts: 0, made: 0 });
  expect(done, `a Space after ${e.how} did ${done.join(", ")}`).toEqual([]);
}

async function clickOnly(c: Click): Promise<Began | null> {
  const btn = button(c.slot);
  const face = began(btn);
  if (face === null) {
    return null;
  }
  if (c.detail === 0) {
    btn.click();
  } else {
    btn.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, detail: 1 }));
    click(btn, 1);
  }
  await flush();
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
  fc.record({ k: fc.constant("send" as const), held: fc.boolean() }),
  fc.constant({ k: "answer" as const }),
  fc.record({
    k: fc.constant("auxPress" as const),
    slot: fc.constantFrom<Slot>("main", "side"),
    button: fc.constantFrom<1 | 2>(1, 2),
  }),
);

const gesture: fc.Arbitrary<Gesture> = fc.record({
  k: fc.constant("gesture" as const),
  slot: fc.constantFrom<Slot>("main", "side"),
  input: fc.constantFrom("pointer" as const, "space" as const, "enter" as const),
  count: fc.integer({ min: 1, max: 3 }),
  end: fc.constantFrom(
    "click" as const,
    "tap" as const,
    "dragOff" as const,
    "cancel" as const,
    "contextmenu" as const,
  ),
  gaps: fc.array(fc.array(world, { maxLength: 2 }), { minLength: 6, maxLength: 6 }),
  inner: fc.oneof(
    { arbitrary: fc.constant("none" as const), weight: 3 },
    {
      arbitrary: fc.constantFrom("enter" as const, "space" as const, "pointer" as const),
      weight: 2,
    },
  ),
});

const clickStep: fc.Arbitrary<Click> = fc.record({
  k: fc.constant("click" as const),
  slot: fc.constantFrom<Slot>("main", "side"),
  detail: fc.constantFrom<0 | 1>(0, 1),
});

const endStep: fc.Arbitrary<EndThenSpace> = fc.record({
  k: fc.constant("endThenSpace" as const),
  how: fc.constantFrom("finish" as const, "fail" as const, "escape" as const, "hide" as const),
});

const step: fc.Arbitrary<Step> = fc.oneof(
  { arbitrary: gesture, weight: 3 },
  { arbitrary: clickStep, weight: 1 },
  { arbitrary: world, weight: 2 },
  { arbitrary: endStep, weight: 1 },
);

async function reset(): Promise<void> {
  await answerSend();
  dictation.drop();
  setSendState({ kind: "idle" });
  box().focus();
  type("");
  await flush();
  submitted = 0;
  cancelled = 0;
  starts = 0;
}

/** A pointer press that makes no click. */
function aborted(g: Gesture): boolean {
  return g.end === "dragOff" || g.end === "cancel" || g.end === "contextmenu";
}

const quiet = (first: readonly World[] = []): Gesture["gaps"] => [first, [], [], [], [], []];

/** Space on the microphone while typing turns it to Send, then Enter on Stop with its repeat landing in the box. */
const spaceThenHeldEnter: readonly Step[] = [
  {
    k: "gesture",
    slot: "main",
    input: "space",
    count: 1,
    end: "click",
    gaps: quiet([{ k: "type", text: "words" }]),
    inner: "none",
  },
  {
    k: "gesture",
    slot: "main",
    input: "enter",
    count: 1,
    end: "click",
    gaps: quiet([{ k: "focusBox" }]),
    inner: "none",
  },
];

/** The mouse held on Stop while an Enter on it stops, then the mouse's own click once the box shows Send. */
const enterInsideMouseOnStop: readonly Step[] = [
  {
    k: "gesture",
    slot: "main",
    input: "pointer",
    count: 1,
    end: "click",
    gaps: quiet(),
    inner: "none",
  },
  { k: "begin" },
  { k: "result", text: "hello", final: true },
  {
    k: "gesture",
    slot: "main",
    input: "pointer",
    count: 1,
    end: "click",
    gaps: quiet(),
    inner: "enter",
  },
];

/** Space held on the microphone while a click on it starts dictation and typing ends it, so the key up lands in the
 *  box, then a `click()` on the Send face that leaves. */
const spaceUpOffTheButton: readonly Step[] = [
  {
    k: "gesture",
    slot: "main",
    input: "space",
    count: 1,
    end: "click",
    gaps: quiet([{ k: "type", text: "words" }]),
    inner: "pointer",
  },
  { k: "click", slot: "main", detail: 0 },
];

/** Dictation started from a focused Send, then the browser ends it and the reader types a Space. */
const endFromFocusedButton: readonly Step[] = [
  { k: "focusButton", slot: "main" },
  {
    k: "gesture",
    slot: "main",
    input: "pointer",
    count: 1,
    end: "click",
    gaps: quiet(),
    inner: "none",
  },
  { k: "begin" },
  { k: "result", text: "hello", final: true },
  { k: "endThenSpace", how: "finish" },
];

describe("a press on the composer's buttons (rule 3)", () => {
  afterAll(() => {
    document.documentElement.removeAttribute("data-pointer");
  });

  it("never sends or steers from Dictate or Stop, never dictates from Send or Cancel, and Stop only stops", async () => {
    await fc.assert(
      fc.asyncProperty(
        fc.boolean(),
        fc.array(step, { minLength: 1, maxLength: 8 }),
        async (finger, steps) => {
          await reset();
          if (finger) {
            document.documentElement.setAttribute("data-pointer", "coarse");
          } else {
            document.documentElement.removeAttribute("data-pointer");
          }
          for (const s of steps) {
            if (s.k === "endThenSpace") {
              await endThenSpace(s);
              continue;
            }
            if (s.k !== "gesture" && s.k !== "click") {
              await apply(s);
              continue;
            }
            const before = counts();
            byInner = { submitted: 0, cancelled: 0, starts: 0, made: 0 };
            const { face, clicked } =
              s.k === "gesture" ? await press(s) : { face: await clickOnly(s), clicked: true };
            expectNoStaleWords();
            if (face === null) {
              continue;
            }
            const done = did(before);
            expect(
              clicked ? MAY[face] : [],
              `a ${s.k} that began on ${face} did ${done.join(", ")}`,
            ).toEqual(expect.arrayContaining(done));
          }
        },
      ),
      {
        numRuns: 300,
        examples: [
          [false, spaceThenHeldEnter],
          [false, enterInsideMouseOnStop],
          [false, spaceUpOffTheButton],
          [true, endFromFocusedButton],
          [false, endFromFocusedButton],
        ],
      },
    );
  });
});
