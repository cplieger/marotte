// The mid-turn split pill: the instance's default action with its word, then the other action as a glyph, each
// acting on one click, Enter doing the default, and a change of the setting flipping both at once.

import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { initPromptInput, setSendState } from "./prompt-input.js";
import { adoptInterruptDefault } from "./interrupt-default.js";
import { setSessions, setActive } from "./store.js";
import { ICON_QUEUE_UI, ICON_SEND } from "./icons.js";
import { iconEl } from "./icon-el.js";
import type { InterruptMode } from "./wire/types.gen.js";
import type { RunView, Session } from "./types.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";

const CHAT = "c1";
const sent: [string, InterruptMode][] = [];
const staged = signal(false);

function session(run: RunView): Session {
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
    run,
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
      <div id="midturn-send" class="send-split" role="group" aria-label="Send mid-turn" hidden>
        <button id="midturn-send-btn" type="button" class="send-btn" hidden></button>
        <button id="midturn-alt-btn" type="button" class="send-btn" hidden></button>
      </div>
      <button id="send-btn" type="submit"></button>
    </form>`;
  initPromptInput(
    (text, mode) => {
      sent.push([text, mode]);
      return undefined;
    },
    vi.fn(),
    staged,
  );
});

function box(): HTMLTextAreaElement {
  return document.getElementById("prompt-input") as HTMLTextAreaElement;
}
function group(): HTMLElement {
  return document.getElementById("midturn-send") as HTMLElement;
}
function main(): HTMLButtonElement {
  return document.getElementById("midturn-send-btn") as HTMLButtonElement;
}
function alt(): HTMLButtonElement {
  return document.getElementById("midturn-alt-btn") as HTMLButtonElement;
}

function type(text: string): void {
  box().value = text;
  box().dispatchEvent(new Event("input", { bubbles: true }));
}

function enter(): void {
  box().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
}

function glyphOf(btn: HTMLElement): string {
  return btn.querySelector("svg")?.outerHTML ?? "";
}

function glyph(icon: string): string {
  return iconEl(icon).querySelector("svg")?.outerHTML ?? iconEl(icon).outerHTML;
}

/** A turn runs, the run order says a plain send would steer, and the box holds text. */
function midTurnWithText(run: RunView = STEERING): void {
  setSessions([session(run)]);
  setActive(CHAT);
  setSendState({ kind: "streaming" });
  type("look at the tests first");
}

beforeEach(() => {
  document.documentElement.removeAttribute("data-pointer");
  adoptInterruptDefault({ interrupt_mode: "steer" });
  setSendState({ kind: "idle" });
  staged.value = false;
  type("");
  sent.length = 0;
});

describe("with Steer the default", () => {
  it("shows Steer with its word and glyph, then Queue instead as a glyph with its name and tooltip", () => {
    midTurnWithText();
    expect(group().hidden).toBe(false);
    expect(group().getAttribute("role")).toBe("group");
    expect(group().getAttribute("aria-label")).toBe("Send mid-turn");
    expect(main().hidden).toBe(false);
    expect(main().querySelector(".send-btn-label")?.textContent).toBe("Steer");
    expect(main().getAttribute("aria-label")).toBe("Steer the running turn");
    expect(main().getAttribute("data-tooltip")).toBe("Steer the running turn");
    expect(glyphOf(main())).toBe(glyph(ICON_SEND));
    expect(alt().hidden).toBe(false);
    expect(alt().querySelector(".send-btn-label")).toBeNull();
    expect(alt().getAttribute("aria-label")).toBe("Queue instead");
    expect(alt().getAttribute("data-tooltip")).toBe("Queue instead");
    expect(glyphOf(alt())).toBe(glyph(ICON_QUEUE_UI));
  });

  it("steers on one click of the main half and queues on one click of the icon half", () => {
    midTurnWithText();
    main().click();
    midTurnWithText();
    alt().click();
    expect(sent).toEqual([
      ["look at the tests first", "steer"],
      ["look at the tests first", "queue"],
    ]);
  });

  it("steers on Enter", () => {
    midTurnWithText();
    enter();
    expect(sent).toEqual([["look at the tests first", "steer"]]);
  });

  it("puts the main half before the icon half before Cancel in the focus order, all tabbable", () => {
    midTurnWithText();
    const cancel = document.getElementById("send-btn") as HTMLButtonElement;
    const order = [...document.querySelectorAll<HTMLButtonElement>("button:not([hidden])")];
    expect(order).toEqual([main(), alt(), cancel]);
    for (const b of order) {
      expect(b.tabIndex, b.id).toBe(0);
    }
  });
});

describe("with Queue the default", () => {
  beforeEach(() => {
    adoptInterruptDefault({ interrupt_mode: "queue" });
  });

  it("shows Queue with its word and glyph, then Steer instead as a glyph with its name and tooltip", () => {
    midTurnWithText();
    expect(main().querySelector(".send-btn-label")?.textContent).toBe("Queue");
    expect(main().getAttribute("aria-label")).toBe("Queue for after this turn");
    expect(main().getAttribute("data-tooltip")).toBe("Queue for after this turn");
    expect(glyphOf(main())).toBe(glyph(ICON_QUEUE_UI));
    expect(alt().querySelector(".send-btn-label")).toBeNull();
    expect(alt().getAttribute("aria-label")).toBe("Steer instead");
    expect(alt().getAttribute("data-tooltip")).toBe("Steer instead");
    expect(glyphOf(alt())).toBe(glyph(ICON_SEND));
    expect(box().placeholder).toBe("Queue a follow-up for after this turn…");
  });

  it("queues on one click of the main half and steers on one click of the icon half", () => {
    midTurnWithText();
    main().click();
    midTurnWithText();
    alt().click();
    expect(sent).toEqual([
      ["look at the tests first", "queue"],
      ["look at the tests first", "steer"],
    ]);
  });

  it("queues on Enter", () => {
    midTurnWithText();
    enter();
    expect(sent).toEqual([["look at the tests first", "queue"]]);
  });
});

describe("a change of the setting", () => {
  it("flips both halves and Enter in the open composer at once", () => {
    midTurnWithText();
    expect(main().getAttribute("aria-label")).toBe("Steer the running turn");

    adoptInterruptDefault({ interrupt_mode: "queue" });
    expect(main().querySelector(".send-btn-label")?.textContent).toBe("Queue");
    expect(alt().getAttribute("aria-label")).toBe("Steer instead");
    expect(box().placeholder).toBe("Queue a follow-up for after this turn…");
    enter();

    midTurnWithText();
    adoptInterruptDefault({ interrupt_mode: "steer" });
    expect(main().querySelector(".send-btn-label")?.textContent).toBe("Steer");
    expect(alt().getAttribute("aria-label")).toBe("Queue instead");
    expect(box().placeholder).toBe("Steer the agent mid-turn…");
    enter();

    expect(sent).toEqual([
      ["look at the tests first", "queue"],
      ["look at the tests first", "steer"],
    ]);
  });
});

describe("where a send cannot steer", () => {
  it("offers Queue alone beside a turn that takes no steer, whatever the default", () => {
    for (const mode of ["steer", "queue"] as const) {
      adoptInterruptDefault({ interrupt_mode: mode });
      midTurnWithText(QUEUEING);
      expect(main().querySelector(".send-btn-label")?.textContent, mode).toBe("Queue");
      expect(glyphOf(main()), mode).toBe(glyph(ICON_QUEUE_UI));
      expect(alt().hidden, mode).toBe(true);
    }
  });

  it("offers Queue alone for a staged file, which a steer cannot carry", () => {
    midTurnWithText();
    staged.value = true;
    expect(main().querySelector(".send-btn-label")?.textContent).toBe("Queue");
    expect(alt().hidden).toBe(true);
    main().click();
    expect(sent).toEqual([["look at the tests first", "queue"]]);
  });

  it("hides the whole pill while there is nothing to send", () => {
    midTurnWithText();
    type("");
    expect(group().hidden).toBe(true);
    expect([main().hidden, alt().hidden]).toEqual([true, true]);
  });
});
