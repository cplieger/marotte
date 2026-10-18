// iOS can apply a smart quote or an autocorrection as an `insertReplacementText` behind the caret
// (WebKit `applyAutocorrection`, Source/WebKit/UIProcess/ios/WKContentViewInteraction.mm). A page
// write to the box's value or selection during that edit moves the rest of the word ("what' ups"
// for "what's up"). These cases simulate those edit sequences in Chromium with synthetic events
// and pin that no listener writes during a substitution or composition.

import { describe, it, expect, beforeAll, afterEach, vi } from "vitest";
import { cdp, userEvent } from "vitest/browser";
import type * as ApiClient from "./api-client.js";
import type * as Actions from "./actions/index.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: () => Promise.resolve(null),
}));
// No transport in this test; the draft save (`setDraft`) writes nothing back to the box.
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Actions>()),
  debouncedDispatch: () =>
    Object.assign(() => undefined, {
      isPending: () => false,
      flush: () => Promise.resolve(),
      cancel: () => undefined,
    }),
  registerCleanup: () => undefined,
}));

import { initPromptInput, setSendState } from "./prompt-input.js";
import {
  initComposerState,
  restoreComposerState,
  seedComposerState,
  adoptRemoteComposerState,
} from "./composer-state.js";
import { initSlashMenu, _setCatalogForTest } from "./slash-menu.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";
import type { SlashCommand } from "./wire/types.gen.js";

const CHAT = "c1";

/** Every write to the box's value or selection that the test itself did not make. */
const writes: string[] = [];
let driving = false;

function box(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}

/** The value with the caret as `|`, or the selection in brackets. */
function shown(el = box()): string {
  const v = el.value;
  const s = el.selectionStart;
  const e = el.selectionEnd;
  return s === e
    ? `${v.slice(0, s)}|${v.slice(s)}`
    : `${v.slice(0, s)}[${v.slice(s, e)}]${v.slice(e)}`;
}

function note(what: string): void {
  if (!driving) {
    writes.push(`${what} while ${shown()}`);
  }
}

function watch(el: HTMLTextAreaElement): void {
  const proto = HTMLTextAreaElement.prototype;
  for (const prop of ["value", "selectionStart", "selectionEnd", "selectionDirection"]) {
    const d = Object.getOwnPropertyDescriptor(proto, prop);
    Object.defineProperty(el, prop, {
      configurable: true,
      get: () => d?.get?.call(el),
      set: (v: unknown) => {
        note(`${prop}=${JSON.stringify(v)}`);
        d?.set?.call(el, v);
      },
    });
  }
  for (const method of ["setSelectionRange", "setRangeText", "select"] as const) {
    const real = proto[method] as (...args: unknown[]) => void;
    Object.defineProperty(el, method, {
      configurable: true,
      value: (...args: unknown[]) => {
        note(`${method}(${JSON.stringify(args)})`);
        Reflect.apply(real, el, args);
      },
    });
  }
  const exec = Document.prototype.execCommand;
  Object.defineProperty(document, "execCommand", {
    configurable: true,
    value: (...args: unknown[]) => {
      note(`execCommand(${JSON.stringify(args)})`);
      return Reflect.apply(exec, document, args) as boolean;
    },
  });
}

/** Run `fn` as the test's own write, which `note` does not count. */
function drive(fn: () => void): void {
  driving = true;
  try {
    fn();
  } finally {
    driving = false;
  }
}

function session(over: Partial<Session> = {}): Session {
  return {
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
    ...over,
  };
}

beforeAll(() => {
  document.body.innerHTML = `
    <form id="prompt-form" action="javascript:void 0">
      <ul id="slash-menu" hidden></ul>
      <textarea id="prompt-input" aria-controls="slash-menu"></textarea>
      <button id="midturn-send-btn" type="button" hidden></button>
      <button id="send-btn" type="submit"></button>
    </form>`;
  document.documentElement.setAttribute("data-pointer", "coarse");
  watch(box());
  initPromptInput(
    () => undefined,
    () => undefined,
  );
  initComposerState();
  initSlashMenu(() => undefined);
});

afterEach(() => {
  drive(() => {
    box().value = "";
  });
  writes.length = 0;
});

/** What else is happening in the app while the user types. */
const CONDITIONS = [
  "idle",
  "turn running, Steer",
  "turn running, Queue",
  "draft echoed back",
  "slash menu open",
] as const;
type Condition = (typeof CONDITIONS)[number];

let tick = 0;

async function between(c: Condition): Promise<void> {
  tick++;
  if (c.startsWith("turn running")) {
    setSessions([
      session({
        thinking: true,
        working_label: `Working ${String(tick)}`,
        ...(c.endsWith("Queue") ? { interrupt_mode: "queue" as const } : {}),
      }),
    ]);
    setSendState({ kind: "streaming" });
  }
  if (c === "draft echoed back") {
    setSessions([session({ draft: box().value })]);
    seedComposerState(CHAT);
    adoptRemoteComposerState(CHAT, box().value, []);
  }
  await new Promise((r) => requestAnimationFrame(r));
}

/** Commands matching every query the slash cases type, so the menu stays open through each edit. */
const MENU: SlashCommand[] = ["whats", "what's", "what\u2019s"].map((name): SlashCommand => ({
  name,
  description: "",
  kind: "prompt",
}));

async function begin(c: Condition, prefix: string): Promise<HTMLTextAreaElement> {
  _setCatalogForTest(c === "slash menu open" ? MENU : []);
  setSessions([session(c.endsWith("Queue") ? { interrupt_mode: "queue" } : {})]);
  setActive(CHAT);
  const el = box();
  drive(() => {
    restoreComposerState(CHAT);
    setSendState({ kind: c.startsWith("turn running") ? "streaming" : "idle" });
    el.focus();
    el.value = "";
  });
  writes.length = 0;
  if (prefix !== "") {
    await userEvent.keyboard(prefix);
  }
  if (c === "slash menu open") {
    expect(document.getElementById("slash-menu")?.hidden).toBe(false);
  }
  return el;
}

/** Real keystrokes. */
async function keys(c: Condition, text: string): Promise<void> {
  for (const ch of text) {
    await userEvent.keyboard(ch);
    await between(c);
  }
}

/** A trusted `insertText` of a character no US key types, as the soft keyboard sends it. */
async function insertText(c: Condition, text: string): Promise<void> {
  await cdp().send("Input.insertText", { text });
  await between(c);
}

/** A keyboard replacement of `[start, end)` as synthetic (untrusted) events with the range,
 *  inputType and final caret WebKit gives one. */
async function replace(c: Condition, start: number, end: number, text: string): Promise<void> {
  const el = box();
  const init = { inputType: "insertReplacementText", data: text, bubbles: true };
  drive(() => {
    el.setSelectionRange(start, end);
  });
  el.dispatchEvent(new InputEvent("beforeinput", { ...init, cancelable: true }));
  drive(() => {
    el.setRangeText(text, start, end, "end");
  });
  el.dispatchEvent(new InputEvent("input", init));
  await between(c);
}

const prefix = (c: Condition): string => (c === "slash menu open" ? "/what" : "what");

describe("typing on an iPhone keyboard", () => {
  it.each(CONDITIONS)("keeps a smart quote that replaced the typed one (%s)", async (c) => {
    const el = await begin(c, prefix(c));
    const at = prefix(c).length;
    await keys(c, "'");
    await replace(c, at, at + 1, "\u2019");
    await keys(c, "s up");
    expect(shown(el)).toBe(`${prefix(c)}\u2019s up|`);
    expect(writes).toEqual([]);
  });

  it.each(CONDITIONS)("keeps a smart quote the keyboard typed (%s)", async (c) => {
    const el = await begin(c, prefix(c));
    await insertText(c, "\u2019");
    await keys(c, "s up");
    expect(shown(el)).toBe(`${prefix(c)}\u2019s up|`);
    expect(writes).toEqual([]);
  });

  it.each(CONDITIONS)("keeps the caret after a word autocorrected behind it (%s)", async (c) => {
    const el = await begin(c, prefix(c));
    const at = prefix(c).length;
    await keys(c, "s");
    await replace(c, at - 4, at + 1, "what\u2019s");
    await keys(c, " up");
    expect(shown(el)).toBe(`${prefix(c)}\u2019s up|`);
    expect(writes).toEqual([]);
  });

  it.each(CONDITIONS)(
    "keeps the caret after a quoted word re-corrected behind it (%s)",
    async (c) => {
      const el = await begin(c, prefix(c));
      const at = prefix(c).length;
      await insertText(c, "\u2019");
      await keys(c, "s");
      await replace(c, at - 4, at + 2, "what\u2019s");
      await keys(c, " up");
      expect(shown(el)).toBe(`${prefix(c)}\u2019s up|`);
      expect(writes).toEqual([]);
    },
  );

  it.each(CONDITIONS)("leaves an IME composition to the IME (%s)", async (c) => {
    const lead = c === "slash menu open" ? "/" : "";
    const el = await begin(c, lead);
    let composing = 0;
    const onInput = (ev: Event): void => {
      if (ev instanceof InputEvent && ev.isComposing) {
        composing++;
      }
    };
    el.addEventListener("input", onInput);
    for (const text of ["w", "wh", "wha", "what", "what\u2019", "what\u2019s"]) {
      await cdp().send("Input.imeSetComposition", {
        text,
        selectionStart: text.length,
        selectionEnd: text.length,
      });
      await between(c);
    }
    await insertText(c, "what\u2019s");
    el.removeEventListener("input", onInput);
    await keys(c, " up");
    expect(composing).toBeGreaterThan(0);
    expect(shown(el)).toBe(`${lead}what\u2019s up|`);
    expect(writes).toEqual([]);
  });
});
