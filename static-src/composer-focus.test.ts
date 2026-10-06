// The composer's accent border, tested as an event order. `:focus-within` flickers on a press on a non-focusable
// descendant (focus drops to <body> on mousedown and returns on click); `#context-card` and the pill padding are
// non-focusable too. The source half reads the shipped predicate (the page loads no app sheet); the event half asks it
// with `matches()` at every step. `:focus-within` is hand-modelled to show the round trip the predicate avoids.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import type * as Store from "./store.js";
import type * as ChatActions from "./actions/chat.js";
import type * as Toast from "./toast.js";

const { setSupervisedDispatch, collapseAll } = vi.hoisted(() => ({
  setSupervisedDispatch: vi.fn(),
  collapseAll: vi.fn(),
}));

let activeID = "";
let supervised: boolean | undefined;

vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  activeSession: {
    peek: () => (activeID === "" ? undefined : { id: activeID, supervised_mode: supervised }),
    get value() {
      return activeID === "" ? undefined : { id: activeID, supervised_mode: supervised };
    },
  },
  isThinking: () => false,
  // The tangent row is disabled on an empty chat; this chat has one, so every pressed row is live.
  isEmptyChat: () => false,
}));
vi.mock("./pill-expand.js", () => ({ makeExpandable: vi.fn(), collapseAll }));
vi.mock("./files-picker.js", () => ({ openFilePicker: vi.fn() }));
vi.mock("./chat.js", () => ({ openTangentChat: vi.fn() }));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  setSupervised: { dispatch: setSupervisedDispatch },
  setInterruptMode: { dispatch: vi.fn() },
  compactChat: { dispatch: vi.fn() },
  renameChat: { dispatch: vi.fn() },
  downloadKiroSession: { dispatch: vi.fn() },
}));
vi.mock("./submit.js", () => ({ submitPrompt: vi.fn() }));

function focusSelector(): string {
  const body = ruleBody(loadCSS("15-input.css"), ".prompt-box");
  const m = /&(:[^\s{]+)\s*\{[^}]*border-color:\s*var\(--c-accent\)/.exec(body);
  expect(
    m,
    "expected .prompt-box to light its border from ONE nested state selector",
  ).not.toBeNull();
  return `.prompt-box${m![1]}`;
}

/** `:focus-within` by definition (some descendant is the active element), so the rule is expressed, not queried. */
function focusWithinModel(box: Element): boolean {
  const active = document.activeElement;
  return active !== null && active !== document.body && box.contains(active);
}

async function mount(): Promise<{
  box: HTMLElement;
  input: HTMLTextAreaElement;
  card: HTMLElement;
}> {
  document.body.innerHTML = `
    <div id="prompt-box" class="prompt-box">
      <textarea id="prompt-input"></textarea>
      <div class="prompt-pills">
        <span class="pill-slot">
          <button id="chat-options-btn" class="pill pill-expandable" type="button"></button>
          <span id="chat-options-card" class="pill-expand-content chat-options-card"></span>
        </span>
      </div>
    </div>
  `;
  vi.resetModules();
  const mod = await import("./chat-options.js");
  mod.initChatOptions();
  return {
    box: document.getElementById("prompt-box") as HTMLElement,
    input: document.getElementById("prompt-input") as HTMLTextAreaElement,
    card: document.getElementById("chat-options-card") as HTMLElement,
  };
}

function supervisedLabel(card: HTMLElement): HTMLLabelElement {
  const label = [...card.querySelectorAll("label.chat-opt-row")].find((l) =>
    (l.textContent ?? "").includes("Supervised mode"),
  );
  if (label === undefined) {
    throw new Error("no Supervised mode row");
  }
  return label as HTMLLabelElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  activeID = "c-active";
  supervised = false;
});

describe("the composer's focus treatment", () => {
  it("keys on the message box, not on any descendant", () => {
    const selector = focusSelector();
    expect(
      selector,
      "the border must key on the message box's own focus. `:focus-within` is what it was, and it is " +
        "true for every focusable descendant — so every press on a NON-focusable one flickers it, " +
        "which is one bug for the label row, the context card's spans and the pill row's padding alike.",
    ).toBe('.prompt-box:has([id="prompt-input"]:focus)');
    expect(
      loadCSS("15-input.css").replace(/\/\*[\s\S]*?\*\//g, " "),
      "no rule may reintroduce :focus-within on the prompt box",
    ).not.toMatch(/\.prompt-box[^{]*:focus-within|&:focus-within/);
  });

  it("does not round-trip across mousedown -> blur -> mouseup -> click on the label", async () => {
    const { box, input, card } = await mount();
    const selector = focusSelector();
    const label = supervisedLabel(card);
    const checkbox = card.querySelector<HTMLInputElement>("#chat-opt-supervised");
    expect(checkbox, "the supervised row must carry its checkbox").not.toBeNull();

    input.focus();
    const lit: boolean[] = [];
    const litOld: boolean[] = [];
    const sample = (): void => {
      lit.push(box.matches(selector));
      litOld.push(focusWithinModel(box));
    };
    sample();

    // A synthetic pointer event moves no focus, so the drop to <body> on mousedown is modelled explicitly.
    label.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    input.blur();
    sample();

    label.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true }));
    checkbox!.checked = true;
    checkbox!.focus();
    checkbox!.dispatchEvent(new Event("change", { bubbles: true }));
    sample();

    expect(
      setSupervisedDispatch,
      "the click must still reach the supervised action",
    ).toHaveBeenCalledWith({
      chatID: "c-active",
      enabled: true,
    });

    // The border must be lit at the start, or a selector false throughout passes the round-trip check vacuously.
    expect(
      lit[0],
      "the border must be lit while the message box holds focus, or the round-trip check below is vacuous",
    ).toBe(true);

    expect(
      litOld,
      "the :focus-within model must reproduce the reported flicker, or this test is not exercising the bug",
    ).toEqual([true, false, true]);

    // The shipped selector goes false when the box really loses focus and stays there.
    const roundTrips = lit.some(
      (v, i) => i > 0 && i < lit.length - 1 && !v && lit[i - 1] && lit[i + 1],
    );
    expect(
      roundTrips,
      `the border's state across the gesture was ${JSON.stringify(lit)}; a false between two trues is ` +
        `the flicker, whatever the endpoints are`,
    ).toBe(false);
  });

  it("does not round-trip on a press inside the context card either", async () => {
    // Same class with no <label>: #context-card is spans, with no press affordance inviting the gesture.
    const { box, input } = await mount();
    const selector = focusSelector();
    const row = document.createElement("span");
    row.className = "pill-ctx-label";
    row.textContent = "Tokens used";
    box.querySelector(".prompt-pills")!.appendChild(row);

    input.focus();
    expect(box.matches(selector)).toBe(true);
    row.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
    input.blur();
    const during = box.matches(selector);
    row.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true }));
    row.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));

    expect(
      box.matches(selector),
      "a press on a span must not restore the border it just took away",
    ).toBe(during);
  });
});
