import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";
import { page, userEvent } from "vitest/browser";
import type * as Chat from "./chat.js";
import type * as Dom from "./dom.js";
import type * as NoticeSubject from "./notice-subject.js";
import type * as Toast from "./toast.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { QUOTE_ATTRIBUTION } from "./message-quote.js";
import { buildRunCard } from "./fundamentals/run-card.js";
import { buildSteerNote } from "./fundamentals/steer-note.js";
import { buildTurnHeader } from "./fundamentals/turn-header.js";
import { wakeLine } from "./turn-wake.js";
import { namedWake } from "./turns.js";
import type { EntryTurnWake } from "./wire/types.gen.js";
import { setActive, setSessions } from "./store.js";
import type { Session } from "./types.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeSession, makeTurn } from "./__test-helpers__/model.js";

const { openTangentChat } = vi.hoisted(() => ({
  openTangentChat: vi.fn<(parentChatID: string) => Promise<string>>(),
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  openTangentChat,
}));
const { chatNotice, toastError } = vi.hoisted(() => ({ chatNotice: vi.fn(), toastError: vi.fn() }));
vi.mock("./notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubject>()),
  chatNotice,
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  error: toastError,
}));
// The real chat.js graph runs effects against the whole app shell; an element this file does not build is a
// detached stub, so only the shell below is real.
vi.mock("./dom.js", async (importOriginal) => {
  const actual = await importOriginal<typeof Dom>();
  const stubs = new Map<string, HTMLElement>();
  const stub = (key: string): HTMLElement => {
    const made = stubs.get(key) ?? document.createElement("div");
    stubs.set(key, made);
    return made;
  };
  const orStub = (key: string, read: () => unknown): unknown => {
    try {
      return read();
    } catch {
      return stub(key);
    }
  };
  return {
    ...actual,
    $: new Proxy(actual.$, { get: (t, k: string) => orStub(k, () => Reflect.get(t, k)) }),
    byId: (id: string) => orStub(id, () => actual.byId(id)),
  };
});

// The app shell this module reads through `$`, built once because the listeners bind at init.
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
const messages = document.createElement("div");
messages.id = "messages";
wrap.append(messages);
const input = document.createElement("textarea");
input.id = "prompt-input";
const runBarFold = document.createElement("div");
runBarFold.id = "run-bar-fold";
const runBar = document.createElement("ul");
runBar.id = "run-bar";
const sidebar = document.createElement("nav");
sidebar.id = "sidebar";
document.body.append(sidebar, wrap, runBarFold, runBar, input);

// Imported after the shell exists, so the chat.js graph's load-time reads find the real `#messages`.
const { initSelectionQuote } = await import("./selection-quote.js");

/** A chat as the store holds it: `turns` above zero is a conversation a tangent can inherit. */
function chat(id: string, turns: number): Session {
  return makeSession({
    id,
    name: id,
    turns: Array.from({ length: turns }, (_, i) => makeTurn({ id: `t${String(i)}`, n: i + 1 })),
  });
}

const NO_TANGENT = "Send a message first, then start a tangent from it";

beforeAll(() => {
  initSelectionQuote();
});

beforeEach(() => {
  setSessions([chat("c-active", 2)]);
  setActive("c-active");
  openTangentChat.mockReset();
  chatNotice.mockReset();
  toastError.mockReset();
});

afterEach(async () => {
  document.getSelection()?.removeAllRanges();
  await frames();
  messages.replaceChildren();
  runBar.replaceChildren();
  runBarFold.replaceChildren();
  sidebar.replaceChildren();
  input.value = "";
  setActive("");
  setSessions([]);
});

function frames(): Promise<void> {
  return new Promise((r) => {
    setTimeout(() => {
      requestAnimationFrame(() => requestAnimationFrame(() => r()));
    }, 0);
  });
}

function node<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  ...kids: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  e.className = className;
  e.append(...kids);
  return e;
}

function div(className: string, ...kids: (Node | string)[]): HTMLDivElement {
  return node("div", className, ...kids);
}

function span(className: string, text: string): HTMLSpanElement {
  return node("span", className, text);
}

function button(className: string, text: string): HTMLButtonElement {
  const b = node("button", className, text);
  b.type = "button";
  return b;
}

/** A `div[role=button]`, as run-card.ts and run-bar.ts render a control whose words are quotable. */
function roleButton(className: string, ...kids: (Node | string)[]): HTMLDivElement {
  const b = div(className, ...kids);
  b.setAttribute("role", "button");
  b.tabIndex = 0;
  return b;
}

function p(text: string): HTMLParagraphElement {
  return node("p", "", text);
}

/** A turn as the transcript draws it: header with the prompt, a body of reply rows and a tool card. */
function turn(): {
  headRow: HTMLElement;
  turnN: HTMLElement;
  prompt: HTMLElement;
  reply: HTMLElement;
  second: HTMLElement;
  tool: HTMLElement;
  codeHead: HTMLElement;
  code: HTMLElement;
} {
  const turnN = span("turn-n", "#3");
  const fold = button("turn-fold-toggle", "");
  fold.setAttribute("aria-label", "Fold this turn");
  const headRow = div("turn-head-row", fold, turnN, span("turn-time", "13:50"));
  const prompt = div("turn-req-text", "my question");
  const header = div("turn-header", headRow, div("turn-req", prompt));
  header.dataset["trigger"] = "user";
  const codeHead = div("code-head", "bash");
  codeHead.setAttribute(CHROME_ATTR, "");
  const code = node("pre", "", "echo hi");
  const reply = div(
    "message assistant",
    p("first paragraph"),
    p("second paragraph"),
    div("code-wrap", codeHead, code),
  );
  const tool = div("tool-card", "ran a command");
  const second = div("message assistant", p("another reply"));
  const card = div(
    "turn",
    header,
    div("turn-body", div("msg-row", reply), tool, div("msg-row", second)),
  );
  messages.append(card);
  return { headRow, turnN, prompt, reply, second, tool, codeHead, code };
}

/** A tool card as `tool-card.ts` builds it: a header with the title and the disclosure, then the details. */
function toolCard(
  kind: string,
  titleText: string,
  outputText: string,
): {
  card: HTMLElement;
  title: HTMLElement;
  output: HTMLElement;
  input: HTMLElement;
  badge: HTMLElement;
} {
  const title = span("tool-title", titleText);
  const badge = span("tool-mcp-badge", "github");
  badge.setAttribute(CHROME_ATTR, "");
  const disclosure = button("tool-disclosure", "");
  disclosure.setAttribute("aria-label", "Toggle tool details");
  const output = node("pre", "", outputText);
  const toolInput = node("pre", "tool-input", '{ "path": "a.ts" }');
  const card = div(
    "tool-call",
    div("tool-header", title, badge, disclosure),
    div("tool-details", toolInput, div("tool-output", output)),
  );
  card.dataset["kind"] = kind;
  messages.append(div("turn", div("turn-body", card)));
  return { card, title, output, input: toolInput, badge };
}

function thinking(text: string): { summary: HTMLElement; body: HTMLElement } {
  const summary = node("summary", "", "Thinking");
  summary.setAttribute(CHROME_ATTR, "");
  const body = node("blockquote", "reasoning-body", p(text));
  const block = node("details", "reasoning-block", summary, body);
  block.open = true;
  messages.append(div("turn", div("turn-body", block)));
  return { summary, body };
}

function steerNote(origin: string, text: string): { text: HTMLElement; more: HTMLElement } {
  const textEl = div("steer-note-text", text);
  const more = button("steer-note-more", "Show more");
  const note = div(
    "steer-note",
    div("steer-note-head", span("steer-note-label", "Workflow result")),
    div("steer-note-body", textEl, more),
  );
  note.dataset["origin"] = origin;
  messages.append(div("turn", div("turn-body", div("steer-group", note))));
  return { text: textEl, more };
}

function runCard(name: string): { name: HTMLElement; capture: HTMLElement } {
  const nameEl = span("run-name", name);
  const head = div("run-head", nameEl, span("run-count", "2/5"));
  head.setAttribute("role", "button");
  const capture = div("run-step-capture", "built 3 files");
  const card = div("run-card", head, capture);
  card.dataset["run"] = "r-1";
  messages.append(div("turn", div("turn-body", card)));
  return { name: nameEl, capture };
}

function runBarRow(name: string): { label: HTMLElement; state: HTMLElement } {
  const label = span("ev-sum-label", name);
  const state = span("ev-sum-state", "Step 2 of 5");
  runBar.append(node("li", "run-bar-row", roleButton("run-bar-open", "")));
  runBar.lastElementChild?.firstElementChild?.append(node("span", "ev-sum", label, state));
  return { label, state };
}

/** A turn card holding `rows` in its body and `cardRows` beside the body, as notices and the footer sit. */
function turnWith(rows: readonly HTMLElement[], cardRows: readonly HTMLElement[] = []): void {
  messages.append(div("turn", div("turn-body", ...rows), ...cardRows));
}

/** A boundary row as `messages-events.ts` draws one. */
function boundaryRow(label: string): HTMLElement {
  const icon = span("boundary-icon", "↻");
  icon.setAttribute("aria-hidden", "true");
  return div("boundary boundary-reset", icon, span("boundary-label", label));
}

function select(from: Node, fromOffset: number, to: Node, toOffset: number): void {
  document.getSelection()?.setBaseAndExtent(from, fromOffset, to, toOffset);
}

/** Selects the whole text of `el`'s first text node. */
function selectAll(el: Element): void {
  const t = textIn(el);
  select(t, 0, t, t.length);
}

function textIn(el: Element): Text {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  const t = walker.nextNode();
  if (!(t instanceof Text)) {
    throw new Error("no text node");
  }
  return t;
}

function textOf(el: Element, child = 0): Text {
  const t = el.children[child]?.firstChild ?? el.firstChild;
  if (!(t instanceof Text)) {
    throw new Error("no text node");
  }
  return t;
}

function rangeOver(from: Node, fromOffset: number, to: Node, toOffset: number): Range {
  const r = document.createRange();
  r.setStart(from, fromOffset);
  r.setEnd(to, toOffset);
  return r;
}

/** Runs `body` with `document.getSelection()` answering a two-range selection, as Firefox can hold: Chromium keeps
 *  at most one range, so a double stands in. Its text joins both ranges, as `Selection.toString()` does. */
async function withTwoRanges(
  first: Range,
  second: Range,
  body: () => Promise<void> | void,
): Promise<void> {
  const ranges = [first, second];
  const sel = {
    rangeCount: 2,
    isCollapsed: false,
    getRangeAt: (i: number): Range => {
      const r = ranges[i];
      if (r === undefined) {
        throw new DOMException("index out of range", "IndexSizeError");
      }
      return r;
    },
    toString: (): string => ranges.map((r) => r.toString()).join(""),
  } as unknown as Selection;
  Object.defineProperty(document, "getSelection", { configurable: true, value: () => sel });
  try {
    await body();
  } finally {
    Reflect.deleteProperty(document, "getSelection");
  }
}

function openPanel(): HTMLElement | null {
  const panel = document.querySelector<HTMLElement>(".selection-quote");
  if (panel === null || panel.hidden || !panel.classList.contains("is-open")) {
    return null;
  }
  return panel;
}

/** The open panel's button labelled `label`, or null while the panel is closed. */
function action(label = "Quote in reply"): HTMLButtonElement | null {
  const panel = openPanel();
  if (panel === null) {
    return null;
  }
  return [...panel.querySelectorAll("button")].find((b) => b.textContent === label) ?? null;
}

/** The first line of the quote "Quote in reply" writes for the current selection, or null when it is refused. */
async function sourceLine(): Promise<string | null> {
  await frames();
  const btn = action();
  if (btn === null) {
    return null;
  }
  btn.click();
  return input.value.split("\n")[0] ?? null;
}

describe("the Quote in reply button", () => {
  it("shows for a selection inside one reply", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 1), 6);
    await frames();
    expect(action()?.textContent).toBe("Quote in reply");
  });

  it("offers Quote in reply and Ask in a tangent, and nothing else", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const labels = [...(openPanel()?.querySelectorAll("button") ?? [])].map((b) => b.textContent);
    expect(labels).toEqual(["Quote in reply", "Ask in a tangent"]);
  });

  it("shows for a triple-click's selection, whose end lands at the next block's start", async () => {
    const { second } = turn();
    const next = div("tool-card", "ran another command");
    second.parentElement?.after(next);
    select(textOf(second, 0), 0, next, 0);
    await frames();
    expect(action()).not.toBeNull();
  });

  it("does not show for a selection spanning two replies", async () => {
    const { reply, second } = turn();
    select(textOf(reply, 1), 0, textOf(second, 0), 3);
    await frames();
    expect(action()).toBeNull();
  });

  it("does not show for a selection ending in a code block's title bar", async () => {
    const { reply, codeHead } = turn();
    select(textOf(reply, 1), 0, textOf(codeHead), 2);
    await frames();
    expect(action()).toBeNull();
  });

  it("does not show for a selection starting in a code block's title bar", async () => {
    const { codeHead, code } = turn();
    select(textOf(codeHead), 1, textOf(code), 4);
    await frames();
    expect(action()).toBeNull();
  });

  it("does not show for a selection crossing a code block's title bar", async () => {
    const { reply, code } = turn();
    select(textOf(reply, 1), 0, textOf(code), 4);
    expect(document.getSelection()?.toString()).toContain("bash");
    await frames();
    expect(action()).toBeNull();
  });

  it("shows for a selection that ends at a title bar's start without covering its text", async () => {
    const { reply, codeHead } = turn();
    select(textOf(reply, 1), 0, textOf(codeHead), 0);
    await frames();
    expect(action()).not.toBeNull();
  });

  it("shows for a selection that starts at a title bar's end without covering its text", async () => {
    const { codeHead, code } = turn();
    select(textOf(codeHead), 4, textOf(code), 4);
    await frames();
    expect(action()).not.toBeNull();
  });

  it("shows for a selection inside a reply's code block", async () => {
    const { code } = turn();
    select(textOf(code), 0, textOf(code), 4);
    await frames();
    expect(action()).not.toBeNull();
  });

  it("does not show for a selection of two ranges whose first lies inside a reply", async () => {
    const { reply, prompt } = turn();
    const inReply = rangeOver(textOf(reply, 0), 0, textOf(reply, 0), 5);
    const inPrompt = rangeOver(textOf(prompt), 0, textOf(prompt), 2);
    await withTwoRanges(inReply, inPrompt, async () => {
      document.dispatchEvent(new Event("selectionchange"));
      await frames();
      expect(action()).toBeNull();
    });
  });

  it("does not show for prose outside the transcript", async () => {
    const outside = div("message assistant", p("a page's markdown"));
    document.body.append(div("turn", outside));
    try {
      select(textOf(outside, 0), 0, textOf(outside, 0), 6);
      await frames();
      expect(action()).toBeNull();
    } finally {
      outside.parentElement?.remove();
    }
  });

  it("goes when the selection collapses", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(action()).not.toBeNull();
    document.getSelection()?.collapseToEnd();
    await frames();
    expect(action()).toBeNull();
  });

  it("waits for the pointer's release while a drag is selecting", async () => {
    const { reply } = turn();
    reply.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(action()).toBeNull();
    reply.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    await frames();
    expect(action()).not.toBeNull();
  });

  // iOS hangs its end-of-selection drag handle below the last line box; the panel must sit clear of it.
  it("sits further below a selection a finger made than one a mouse made", async () => {
    const { reply } = turn();
    const gapAfter = async (pointerType: string): Promise<number> => {
      document.getSelection()?.removeAllRanges();
      await frames();
      reply.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType }));
      select(textOf(reply, 0), 0, textOf(reply, 0), 5);
      reply.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, pointerType }));
      await frames();
      const rects = document.getSelection()?.getRangeAt(0).getClientRects();
      const last = rects?.item(rects.length - 1);
      const panel = openPanel();
      expect(panel).not.toBeNull();
      return (panel?.getBoundingClientRect().top ?? 0) - (last?.bottom ?? 0);
    };
    expect(await gapAfter("mouse")).toBeLessThan(10);
    expect(await gapAfter("touch")).toBeGreaterThanOrEqual(20);
    expect(await gapAfter("mouse")).toBeLessThan(10);
  });

  it("goes when the transcript scrolls", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(action()).not.toBeNull();
    wrap.dispatchEvent(new Event("scroll"));
    expect(action()).toBeNull();
  });

  // A cancelled `pointerdown` would cost the tap its `click` in WebKit, so only the mouse event is cancelled.
  it("keeps the selection when pressed", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const pointer = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    const mouse = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    action()?.dispatchEvent(pointer);
    action()?.dispatchEvent(mouse);
    expect(pointer.defaultPrevented).toBe(false);
    expect(mouse.defaultPrevented).toBe(true);
  });

  // The panel keeps its last left offset while hidden, so after the window narrows it is measured from
  // a point past the new right edge.
  it("keeps its labels on one line inside the viewport after the window narrows", async () => {
    const style = mountAppCSS();
    const entry = { width: innerWidth, height: innerHeight };
    try {
      await page.viewport(1200, 800);
      const { reply } = turn();
      const para = reply.children[0] as HTMLElement;
      para.style.cssText =
        "position: fixed; inset-inline-end: 2px; inset-block-start: 100px; margin: 0";
      const end = textOf(reply, 0);
      select(end, 0, end, end.length);
      await frames();
      expect(action()).not.toBeNull();
      document.getSelection()?.removeAllRanges();
      await frames();
      await page.viewport(390, 800);
      expect(innerWidth, "viewport actually resized").toBe(390);
      select(end, 0, end, end.length);
      await frames();
      const panel = openPanel();
      expect(panel).not.toBeNull();
      for (const b of panel!.querySelectorAll("button")) {
        const label = document.createRange();
        label.selectNodeContents(b);
        expect(label.getClientRects(), b.textContent ?? "").toHaveLength(1);
      }
      expect(panel!.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth);
    } finally {
      await page.viewport(entry.width, entry.height);
      style.remove();
    }
  });
});

describe("what a quote can come from", () => {
  it("names Kiro's reply", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    expect(await sourceLine()).toBe("> From Kiro's reply:");
  });

  it("names the reader's own prompt", async () => {
    const { prompt } = turn();
    selectAll(prompt);
    expect(await sourceLine()).toBe("> From your earlier message:");
  });

  it("names the turn header for its number and time", async () => {
    const { turnN } = turn();
    selectAll(turnN);
    expect(await sourceLine()).toBe("> From the turn header:");
  });

  it("names the turn header for a prompt another program sent", async () => {
    const { prompt } = turn();
    prompt.closest<HTMLElement>(".turn-header")!.dataset["trigger"] = "caller";
    selectAll(prompt);
    expect(await sourceLine()).toBe("> From the turn header:");
  });

  it("names the command whose output it is", async () => {
    const { output } = toolCard("execute", "go test ./...", "ok marotte 0.4s");
    selectAll(output);
    expect(await sourceLine()).toBe("> From the output of `go test ./...`:");
  });

  it("names the tool call for its title", async () => {
    const { title } = toolCard("execute", "go test ./...", "ok");
    selectAll(title);
    expect(await sourceLine()).toBe("> From the tool call `go test ./...`:");
  });

  it("names the tool call for its input", async () => {
    const { input: toolInput } = toolCard("read", "a.ts", "contents");
    selectAll(toolInput);
    expect(await sourceLine()).toBe("> From the tool call `a.ts`:");
  });

  it("names the hook", async () => {
    const { output } = toolCard("hook", "pre-commit", "lint passed");
    selectAll(output);
    expect(await sourceLine()).toBe("> From the hook `pre-commit`:");
  });

  it("names a steering box without repeating its title, which is the quoted content", async () => {
    const { title } = toolCard("steering", "Steering loaded: git-commits.md, testing.md", "");
    selectAll(title);
    expect(await sourceLine()).toBe("> From the steering box:");
  });

  it("names Kiro's thinking", async () => {
    const { body } = thinking("weighing two options");
    selectAll(body);
    expect(await sourceLine()).toBe("> From Kiro's thinking:");
  });

  it.each([
    ["agent", "> From a workflow result:"],
    ["step", "> From a workflow message:"],
    ["parent", "> From a workflow message:"],
    ["user", "> From your mid-turn message:"],
  ])("names a step note from origin %s", async (origin, line) => {
    const { text } = steerNote(origin, "all steps passed");
    selectAll(text);
    expect(await sourceLine()).toBe(line);
  });

  it("names a mid-turn message another program sent without calling it the reader's", async () => {
    const { text } = steerNote("user", "Agent Mail: 1 new message");
    text.closest(".steer-note")?.setAttribute("data-caller", "");
    selectAll(text);
    expect(await sourceLine()).toBe("> From a mid-turn message:");
  });

  it("names the run for a run card's name and its step output", async () => {
    const { name, capture } = runCard("marotte-touch-mode-consistency");
    selectAll(name);
    expect(await sourceLine()).toBe("> From run marotte-touch-mode-consistency:");
    input.value = "";
    selectAll(capture);
    expect(await sourceLine()).toBe("> From run marotte-touch-mode-consistency:");
  });

  it("names the run for a recap row's name and its status text", async () => {
    const { label, state } = runBarRow("marotte-touch-mode-consistency");
    selectAll(label);
    expect(await sourceLine()).toBe("> From run marotte-touch-mode-consistency:");
    input.value = "";
    selectAll(state);
    expect(await sourceLine()).toBe("> From run marotte-touch-mode-consistency:");
  });

  it("names the run list for the recap box's fold row", async () => {
    runBarFold.append(roleButton("run-bar-open run-bar-toggle", "2 workflows running"));
    selectAll(runBarFold);
    expect(await sourceLine()).toBe("> From the run list:");
  });

  it("names a run whose name holds backticks as code, which the sent quote's band draws as code", async () => {
    const { name } = runCard("deploy `prod`");
    selectAll(name);
    expect(await sourceLine()).toBe("> From run `` deploy `prod` ``:");
    const header = buildTurnHeader({
      n: 2,
      outcome: "completed",
      ts: 0,
      request: `${input.value}why?`,
      attachments: [],
    });
    const from = header.querySelector(".turn-req-quote-from");
    expect(from?.textContent).toBe("From run deploy `prod`:");
    expect(from?.querySelector("code")?.textContent).toBe("deploy `prod`");
  });

  it("names the turn's notice", async () => {
    const notice = div("turn-notice", "The agent stopped: the context window is full.");
    notice.setAttribute("role", "status");
    turnWith([div("msg-row", div("message assistant", p("partial reply")))], [notice]);
    selectAll(notice);
    expect(await sourceLine()).toBe("> From the turn's notice:");
  });

  it("names Kiro's plan for one of its entries", async () => {
    const entry = div("plan-entry", "🔄 Write the failing test");
    turnWith([div("plan-message", div("plan-header", "Plan"), div("plan-entries", entry))]);
    selectAll(entry);
    expect(await sourceLine()).toBe("> From Kiro's plan:");
  });

  it("names the turn details", async () => {
    const panel = div("turn-info-panel", "3 files changed");
    const ledger = button("turn-ledger-summary", "Turn details");
    turnWith([], [div("turn-footer", ledger, panel)]);
    selectAll(panel);
    expect(await sourceLine()).toBe("> From the turn details:");
  });

  it("names the compaction summary, which shares the reply's prose skin", async () => {
    const summary = div("compaction-body message assistant", p("summary text"));
    const head = node(
      "summary",
      "compaction-head",
      span("compaction-label", "Conversation compacted"),
    );
    const block = node("details", "compaction", head, summary);
    block.open = true;
    turnWith([block]);
    select(textOf(summary, 0), 0, textOf(summary, 0), 7);
    expect(await sourceLine()).toBe("> From the compaction summary:");
  });

  it("names a boundary row as the transcript", async () => {
    const row = boundaryRow("Context reset · an empty reply was retried in a new session");
    turnWith([row]);
    selectAll(row.querySelector(".boundary-label")!);
    expect(await sourceLine()).toBe("> From the transcript:");
  });

  it("names any other transcript row as the transcript", async () => {
    const callout = div("refusal-callout", "The model declined to continue");
    turnWith([], [callout]);
    selectAll(callout);
    expect(await sourceLine()).toBe("> From the transcript:");
  });
});

// A double-click selects as a reader does: neither Chromium nor WebKit selects a word inside a `<button>`. The app's
// CSS lays the names out, which decides where a word ends.
describe("a run's name the reader can open", () => {
  const RUN = "wf_5e1f0c2a9b7d4e36";
  let style: HTMLStyleElement | undefined;
  beforeEach(() => {
    style = mountAppCSS();
    // The app lays the empty sidebar over the transcript, where it would take the double-click.
    sidebar.style.setProperty("display", "none", "important");
  });
  // The pointer outlives the file: left on the page's middle, it hovers whatever a later file draws there.
  afterEach(async () => {
    await userEvent.hover(document.body, { position: { x: 0, y: 0 }, force: true });
    style?.remove();
    sidebar.style.removeProperty("display");
  });

  it("quotes a step note's run name selected on its own", async () => {
    const note = buildSteerNote({
      text: "all steps passed",
      origin: "agent",
      dropped: false,
      step: "review",
      run: RUN,
      runLabel: "nightly",
      onOpenRun: vi.fn(),
    });
    turnWith([div("steer-group", note)]);
    await userEvent.dblClick(note.querySelector(".steer-note-run")!);
    expect(document.getSelection()?.toString()).toBe("nightly");
    expect(await sourceLine()).toBe("> From a workflow result:");
  });

  it("quotes a woken turn's run name selected on its own", async () => {
    const wake: EntryTurnWake = {
      reason: "step_message",
      steers: [{ id: "wfmsg-1", run: RUN, step: "check-links", severity: "info", text: "done" }],
    };
    const header = buildTurnHeader({
      n: 2,
      outcome: "completed",
      ts: 0,
      request: undefined,
      attachments: [],
      wake: wakeLine(namedWake(wake, new Map([[RUN, "nightly"]]))),
      onOpenRun: vi.fn(),
    });
    messages.append(div("turn", header));
    await userEvent.dblClick(header.querySelector(".turn-wake-run")!);
    expect(document.getSelection()?.toString()).toBe("nightly");
    expect(await sourceLine()).toBe("> From the turn header:");
  });

  it("quotes a re-delivery row's run name selected on its own", async () => {
    const wake: EntryTurnWake = {
      reason: "rewake",
      steers: [{ id: "q", run: RUN, step: "draft", text: "Which branch?" }],
    };
    const header = buildTurnHeader({
      n: 2,
      outcome: "completed",
      ts: 0,
      request: undefined,
      attachments: [],
      wake: wakeLine(namedWake(wake, new Map([[RUN, "nightly"]]))),
      onOpenRun: vi.fn(),
    });
    messages.append(div("turn", header));
    await userEvent.dblClick(header.querySelector(".turn-wake-row-run")!);
    expect(document.getSelection()?.toString()).toBe("nightly");
    expect(await sourceLine()).toBe("> From the turn header:");
  });

  it("quotes a run card's step name selected on its own", async () => {
    const card = buildRunCard(RUN, "nightly", vi.fn());
    card.render({
      workflowId: RUN,
      status: "running",
      root: {
        nodeId: RUN,
        type: "sequence",
        status: "running",
        children: [{ nodeId: "lint", type: "step", status: "running" }],
      },
    });
    turnWith([card.root]);
    await userEvent.dblClick(card.root.querySelector(".run-step-name")!);
    expect(document.getSelection()?.toString()).toBe("lint");
    expect(await sourceLine()).toBe("> From run nightly:");
  });
});

describe("what a quote cannot come from", () => {
  it("refuses a turn action's button label", async () => {
    const { reply } = turn();
    const copy = button("btn-small", "Copy");
    reply.parentElement?.append(copy);
    selectAll(copy);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a button inside a reply", async () => {
    const { reply } = turn();
    const rewind = button("btn-small refusal-btn", "Rewind");
    reply.append(rewind);
    selectAll(rewind);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a selection reaching from a reply into a button inside it", async () => {
    const { reply } = turn();
    const rewind = button("btn-small refusal-btn", "Rewind");
    reply.append(rewind);
    select(textOf(reply, 1), 0, textIn(rewind), 3);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a step note's Show more", async () => {
    const { more } = steerNote("agent", "all steps passed");
    selectAll(more);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a menu item in the transcript", async () => {
    const item = div("", "Rename");
    item.setAttribute("role", "menuitem");
    const menu = div("", item);
    menu.setAttribute("role", "menu");
    messages.append(menu);
    selectAll(item);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses editable text in the transcript", async () => {
    const editable = div("", "an inline edit");
    editable.contentEditable = "true";
    messages.append(editable);
    selectAll(editable);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a tool card's chrome label", async () => {
    const { badge } = toolCard("execute", "gh pr list", "1 open");
    selectAll(badge);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a selection crossing a tool card's chrome label", async () => {
    const { title, output } = toolCard("execute", "gh pr list", "1 open");
    select(textIn(title), 0, textIn(output), 2);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses the thinking block's summary", async () => {
    const { summary } = thinking("weighing two options");
    selectAll(summary);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a disclosure's summary row", async () => {
    const head = node(
      "summary",
      "compaction-head",
      span("compaction-label", "Conversation compacted"),
    );
    const block = node(
      "details",
      "compaction",
      head,
      div("compaction-body message assistant", p("kept")),
    );
    turnWith([block]);
    selectAll(head);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses the sidebar", async () => {
    sidebar.append(span("chat-name", "My chat"));
    selectAll(sidebar);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses a popover's text", async () => {
    const pop = div("uip-popover", "Rename this chat");
    document.body.append(pop);
    try {
      selectAll(pop);
      expect(await sourceLine()).toBeNull();
    } finally {
      pop.remove();
    }
  });

  it("refuses text selected in the composer", async () => {
    input.value = "my own draft";
    input.focus();
    input.setSelectionRange(0, 6);
    await frames();
    expect(action()).toBeNull();
  });
});

describe("a selection across two sources", () => {
  it("refuses one reaching from the prompt into the reply", async () => {
    const { prompt, reply } = turn();
    select(textOf(prompt), 3, textOf(reply, 0), 5);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one reaching from a reply into a tool card", async () => {
    const { reply, tool } = turn();
    select(textOf(reply, 1), 0, textOf(tool), 3);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one reaching from the turn header's number into the prompt", async () => {
    const { turnN, prompt } = turn();
    select(textIn(turnN), 0, textOf(prompt), 2);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one reaching from a tool's output into the thinking after it", async () => {
    const { output } = toolCard("execute", "make", "done");
    const { body } = thinking("next step");
    select(textIn(output), 0, textIn(body), 4);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one reaching from the turn's notice back into the reply", async () => {
    const reply = div("message assistant", p("partial reply"));
    const notice = div("turn-notice", "The agent stopped.");
    turnWith([div("msg-row", reply)], [notice]);
    select(textOf(reply, 0), 3, textIn(notice), 5);
    expect(await sourceLine()).toBeNull();
    select(textIn(notice), 5, textOf(reply, 0), 3);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one spanning two rows that no kind names", async () => {
    const first = boundaryRow("Mode: Plan");
    const second = boundaryRow("Mode: Execute");
    turnWith([first, second]);
    select(
      textIn(first.querySelector(".boundary-label")!),
      0,
      textIn(second.querySelector(".boundary-label")!),
      4,
    );
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one reaching from a delegate's card into a reply inside it", async () => {
    const { card, title, badge } = toolCard("delegate", "review the diff", "done");
    badge.remove();
    const inner = div("message assistant", p("the subagent's reply"));
    card.append(inner);
    select(textIn(title), 0, textOf(inner, 0), 4);
    expect(await sourceLine()).toBeNull();
  });

  it("refuses one spanning two recap rows", async () => {
    const first = runBarRow("run-a");
    const second = runBarRow("run-b");
    select(textIn(first.label), 0, textIn(second.label), 3);
    expect(await sourceLine()).toBeNull();
  });
});

describe("quoting a selection", () => {
  it("appends exactly the selected text as a quote, announces it and gives the composer focus", async () => {
    const { reply } = turn();
    input.value = "draft";
    let inputs = 0;
    const onInput = (): void => {
      inputs++;
    };
    input.addEventListener("input", onInput);
    try {
      select(textOf(reply, 0), 6, textOf(reply, 0), 15);
      await frames();
      action()?.click();
      expect(input.value).toBe(
        `draft\n\n> From Kiro's reply:\n>\n> paragraph\n>\n> ${QUOTE_ATTRIBUTION}\n\n`,
      );
      expect(inputs).toBe(1);
      expect(document.activeElement).toBe(input);
      expect(input.selectionStart).toBe(input.value.length);
      expect(document.getSelection()?.isCollapsed ?? true).toBe(true);
      await frames();
      expect(action()).toBeNull();
    } finally {
      input.removeEventListener("input", onInput);
    }
  });

  it("quotes nothing when the selection left the reply before the click", async () => {
    const { reply, prompt } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = action();
    select(textOf(prompt), 0, textOf(reply, 0), 5);
    btn?.click();
    expect(input.value).toBe("");
  });

  it("quotes nothing when a second range joined the selection before the click", async () => {
    const { reply, prompt } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = action();
    expect(btn).not.toBeNull();
    const inReply = rangeOver(textOf(reply, 0), 0, textOf(reply, 0), 5);
    const inPrompt = rangeOver(textOf(prompt), 0, textOf(prompt), 2);
    await withTwoRanges(inReply, inPrompt, () => {
      btn?.click();
    });
    expect(input.value).toBe("");
  });
});

describe("Ask in a tangent", () => {
  /** The tangent opening as `openTangentChat` does: the new chat becomes active and the composer is its own. */
  function opensTangent(): void {
    openTangentChat.mockImplementation((parent) => {
      setSessions([chat(parent, 2), chat("c-tangent", 2)]);
      setActive("c-tangent");
      input.value = "";
      return Promise.resolve("c-tangent");
    });
  }

  it("opens a tangent off the active chat with the quote focused in its composer", async () => {
    opensTangent();
    const { output } = toolCard("execute", "go test ./...", "FAIL x");
    input.value = "parent draft";
    selectAll(output);
    await frames();
    action("Ask in a tangent")?.click();
    await vi.waitFor(() => {
      expect(input.value).not.toBe("");
    });
    expect(openTangentChat).toHaveBeenCalledWith("c-active");
    expect(input.value).toBe(
      `> From the output of \`go test ./...\`:\n>\n> FAIL x\n>\n> ${QUOTE_ATTRIBUTION}\n\n`,
    );
    expect(document.activeElement).toBe(input);
    expect(input.selectionStart).toBe(input.value.length);
    expect(document.getSelection()?.isCollapsed ?? true).toBe(true);
  });

  it("writes nothing when the reader moved away before the tangent opened", async () => {
    openTangentChat.mockResolvedValue("");
    const { reply } = turn();
    input.value = "parent draft";
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    action("Ask in a tangent")?.click();
    await vi.waitFor(() => {
      expect(openTangentChat).toHaveBeenCalledOnce();
    });
    await frames();
    expect(input.value).toBe("parent draft");
  });

  it("is disabled with the tangent's reason while the chat has no messages", async () => {
    setSessions([chat("c-active", 0)]);
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = action("Ask in a tangent");
    expect(btn?.getAttribute("aria-disabled")).toBe("true");
    expect(btn?.dataset["tooltip"]).toBe(NO_TANGENT);
    btn?.click();
    await frames();
    expect(openTangentChat).not.toHaveBeenCalled();
    expect(
      chatNotice,
      "a tap shows the reason touch has no tooltip for",
    ).toHaveBeenCalledExactlyOnceWith("c-active", NO_TANGENT, "error");
    expect(input.value).toBe("");
    expect(action()?.getAttribute("aria-disabled")).not.toBe("true");
  });

  it("is disabled with the same reason when no chat is open", async () => {
    setActive("");
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = action("Ask in a tangent");
    expect(btn?.getAttribute("aria-disabled")).toBe("true");
    expect(btn?.dataset["tooltip"]).toBe(NO_TANGENT);
    btn?.click();
    await frames();
    expect(openTangentChat).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledExactlyOnceWith(NO_TANGENT);
    expect(chatNotice).not.toHaveBeenCalled();
  });

  it("enables once the chat has a message", async () => {
    setSessions([chat("c-active", 0)]);
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(action("Ask in a tangent")?.getAttribute("aria-disabled")).toBe("true");
    setSessions([chat("c-active", 1)]);
    await frames();
    const btn = action("Ask in a tangent");
    expect(btn?.getAttribute("aria-disabled")).not.toBe("true");
    expect(btn?.dataset["tooltip"]).toBeUndefined();
  });

  it("keeps the selection when pressed", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const pointer = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    const mouse = new MouseEvent("mousedown", { bubbles: true, cancelable: true });
    action("Ask in a tangent")?.dispatchEvent(pointer);
    action("Ask in a tangent")?.dispatchEvent(mouse);
    expect(pointer.defaultPrevented).toBe(false);
    expect(mouse.defaultPrevented).toBe(true);
  });
});
