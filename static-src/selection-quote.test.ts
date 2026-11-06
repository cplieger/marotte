import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";
import { page } from "vitest/browser";
import { CHROME_ATTR } from "./chrome-attr.js";
import { QUOTE_ATTRIBUTION } from "./message-quote.js";
import { initSelectionQuote } from "./selection-quote.js";
import { setActive, setSessions } from "./store.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const { openTangentChat } = vi.hoisted(() => ({
  openTangentChat: vi.fn<(parentChatID: string) => Promise<string>>(),
}));
vi.mock("./chat.js", () => ({ openTangentChat }));

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

type Session = Parameters<typeof setSessions>[0][number];

/** A chat as the store holds it: `turns` above zero is a conversation a tangent can inherit. */
function chat(id: string, turns: number): Session {
  return {
    id,
    name: id,
    turn_count: turns,
    turn_order: Array.from({ length: turns }, (_, i) => `t${String(i)}`),
  } as unknown as Session;
}

const NO_TANGENT = "Send a message first, then start a tangent from it";

beforeAll(() => {
  document.body.append(sidebar, wrap, runBarFold, runBar, input);
  initSelectionQuote();
});

beforeEach(() => {
  setSessions([chat("c-active", 2)]);
  setActive("c-active");
  openTangentChat.mockReset();
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
): { card: HTMLElement; title: HTMLElement; output: HTMLElement; input: HTMLElement; badge: HTMLElement } {
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
  runBar.append(
    node("li", "run-bar-row", button("run-bar-open", "")),
  );
  runBar.lastElementChild?.firstElementChild?.append(node("span", "ev-sum", label, state));
  return { label, state };
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

  it("does not show for a compaction summary, which shares the prose skin", async () => {
    const summary = div("compaction-body message assistant", p("summary text"));
    messages.append(div("turn", div("turn-body", summary)));
    select(textOf(summary, 0), 0, textOf(summary, 0), 7);
    await frames();
    expect(action()).toBeNull();
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

  it("goes when the transcript scrolls", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(action()).not.toBeNull();
    wrap.dispatchEvent(new Event("scroll"));
    expect(action()).toBeNull();
  });

  it("keeps the selection when pressed", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const press = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    action()?.dispatchEvent(press);
    expect(press.defaultPrevented).toBe(true);
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
    runBarFold.append(button("run-bar-fold-btn", "2 workflows running"));
    selectAll(runBarFold);
    expect(await sourceLine()).toBe("> From the run list:");
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
    const press = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    action("Ask in a tangent")?.dispatchEvent(press);
    expect(press.defaultPrevented).toBe(true);
  });
});
