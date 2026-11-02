import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import { CHROME_ATTR } from "./chrome-attr.js";
import { QUOTE_ATTRIBUTION } from "./message-quote.js";
import { initSelectionQuote } from "./selection-quote.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// The app shell this module reads through `$`, built once because the listeners bind at init.
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
const messages = document.createElement("div");
messages.id = "messages";
wrap.append(messages);
const input = document.createElement("textarea");
input.id = "prompt-input";

beforeAll(() => {
  document.body.append(wrap, input);
  initSelectionQuote();
});

afterEach(async () => {
  document.getSelection()?.removeAllRanges();
  await frames();
  messages.replaceChildren();
  input.value = "";
});

function frames(): Promise<void> {
  return new Promise((r) => {
    setTimeout(() => {
      requestAnimationFrame(() => requestAnimationFrame(() => r()));
    }, 0);
  });
}

function div(className: string, ...kids: (Node | string)[]): HTMLDivElement {
  const d = document.createElement("div");
  d.className = className;
  d.append(...kids);
  return d;
}

function p(text: string): HTMLParagraphElement {
  const el = document.createElement("p");
  el.textContent = text;
  return el;
}

/** A turn as the transcript draws it: header with the prompt, a body of reply rows and a tool card. */
function turn(): {
  prompt: HTMLElement;
  reply: HTMLElement;
  second: HTMLElement;
  tool: HTMLElement;
  codeHead: HTMLElement;
  code: HTMLElement;
} {
  const prompt = div("turn-req-text", "my question");
  const codeHead = div("code-head", "bash");
  codeHead.setAttribute(CHROME_ATTR, "");
  const code = document.createElement("pre");
  code.textContent = "echo hi";
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
    div("turn-header", div("turn-req", prompt)),
    div("turn-body", div("msg-row", reply), tool, div("msg-row", second)),
  );
  messages.append(card);
  return { prompt, reply, second, tool, codeHead, code };
}

function select(from: Node, fromOffset: number, to: Node, toOffset: number): void {
  document.getSelection()?.setBaseAndExtent(from, fromOffset, to, toOffset);
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

function button(): HTMLButtonElement | null {
  const panel = document.querySelector<HTMLElement>(".selection-quote");
  if (panel === null || panel.hidden || !panel.classList.contains("is-open")) {
    return null;
  }
  return panel.querySelector<HTMLButtonElement>("button");
}

describe("the Quote in reply button", () => {
  it("shows for a selection inside one reply", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 1), 6);
    await frames();
    expect(button()?.textContent).toBe("Quote in reply");
  });

  it("shows for a triple-click's selection, whose end lands at the next block's start", async () => {
    const { second } = turn();
    const next = div("tool-card", "ran another command");
    second.parentElement?.after(next);
    select(textOf(second, 0), 0, next, 0);
    await frames();
    expect(button()).not.toBeNull();
  });

  it("does not show for a selection reaching into the prompt", async () => {
    const { prompt, reply } = turn();
    select(textOf(prompt), 3, textOf(reply, 0), 5);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for a selection reaching into a tool card", async () => {
    const { reply, tool } = turn();
    select(textOf(reply, 1), 0, textOf(tool), 3);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for a selection spanning two replies", async () => {
    const { reply, second } = turn();
    select(textOf(reply, 1), 0, textOf(second, 0), 3);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for a selection ending in a code block's title bar", async () => {
    const { reply, codeHead } = turn();
    select(textOf(reply, 1), 0, textOf(codeHead), 2);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for a selection starting in a code block's title bar", async () => {
    const { codeHead, code } = turn();
    select(textOf(codeHead), 1, textOf(code), 4);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for a selection crossing a code block's title bar", async () => {
    const { reply, code } = turn();
    select(textOf(reply, 1), 0, textOf(code), 4);
    expect(document.getSelection()?.toString()).toContain("bash");
    await frames();
    expect(button()).toBeNull();
  });

  it("shows for a selection that ends at a title bar's start without covering its text", async () => {
    const { reply, codeHead } = turn();
    select(textOf(reply, 1), 0, textOf(codeHead), 0);
    await frames();
    expect(button()).not.toBeNull();
  });

  it("shows for a selection that starts at a title bar's end without covering its text", async () => {
    const { codeHead, code } = turn();
    select(textOf(codeHead), 4, textOf(code), 4);
    await frames();
    expect(button()).not.toBeNull();
  });

  it("shows for a selection inside a reply's code block", async () => {
    const { code } = turn();
    select(textOf(code), 0, textOf(code), 4);
    await frames();
    expect(button()).not.toBeNull();
  });

  it("does not show for a selection of two ranges whose first lies inside a reply", async () => {
    const { reply, prompt } = turn();
    const inReply = rangeOver(textOf(reply, 0), 0, textOf(reply, 0), 5);
    const inPrompt = rangeOver(textOf(prompt), 0, textOf(prompt), 2);
    await withTwoRanges(inReply, inPrompt, async () => {
      document.dispatchEvent(new Event("selectionchange"));
      await frames();
      expect(button()).toBeNull();
    });
  });

  it("does not show for a compaction summary, which shares the prose skin", async () => {
    const summary = div("compaction-body message assistant", p("summary text"));
    messages.append(div("turn", div("turn-body", summary)));
    select(textOf(summary, 0), 0, textOf(summary, 0), 7);
    await frames();
    expect(button()).toBeNull();
  });

  it("does not show for prose outside the transcript", async () => {
    const outside = div("message assistant", p("a page's markdown"));
    document.body.append(div("turn", outside));
    try {
      select(textOf(outside, 0), 0, textOf(outside, 0), 6);
      await frames();
      expect(button()).toBeNull();
    } finally {
      outside.parentElement?.remove();
    }
  });

  it("goes when the selection collapses", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(button()).not.toBeNull();
    document.getSelection()?.collapseToEnd();
    await frames();
    expect(button()).toBeNull();
  });

  it("waits for the pointer's release while a drag is selecting", async () => {
    const { reply } = turn();
    reply.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(button()).toBeNull();
    reply.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
    await frames();
    expect(button()).not.toBeNull();
  });

  it("goes when the transcript scrolls", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    expect(button()).not.toBeNull();
    wrap.dispatchEvent(new Event("scroll"));
    expect(button()).toBeNull();
  });

  it("keeps the selection when pressed", async () => {
    const { reply } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const press = new PointerEvent("pointerdown", { bubbles: true, cancelable: true });
    button()?.dispatchEvent(press);
    expect(press.defaultPrevented).toBe(true);
  });

  // The panel keeps its last left offset while hidden, so after the window narrows it is measured from
  // a point past the new right edge.
  it("keeps its label on one line inside the viewport after the window narrows", async () => {
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
      expect(button()).not.toBeNull();
      document.getSelection()?.removeAllRanges();
      await frames();
      await page.viewport(390, 800);
      expect(innerWidth, "viewport actually resized").toBe(390);
      select(end, 0, end, end.length);
      await frames();
      const b = button();
      expect(b).not.toBeNull();
      const label = document.createRange();
      label.selectNodeContents(b!);
      expect(label.getClientRects()).toHaveLength(1);
      expect(b!.closest(".selection-quote")!.getBoundingClientRect().right).toBeLessThanOrEqual(
        innerWidth,
      );
    } finally {
      await page.viewport(entry.width, entry.height);
      style.remove();
    }
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
      button()?.click();
      expect(input.value).toBe(`draft\n\n> paragraph\n>\n> ${QUOTE_ATTRIBUTION}\n\n`);
      expect(inputs).toBe(1);
      expect(document.activeElement).toBe(input);
      expect(input.selectionStart).toBe(input.value.length);
      expect(document.getSelection()?.isCollapsed ?? true).toBe(true);
      await frames();
      expect(button()).toBeNull();
    } finally {
      input.removeEventListener("input", onInput);
    }
  });

  it("quotes nothing when the selection left the reply before the click", async () => {
    const { reply, prompt } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = button();
    select(textOf(prompt), 0, textOf(reply, 0), 5);
    btn?.click();
    expect(input.value).toBe("");
  });

  it("quotes nothing when a second range joined the selection before the click", async () => {
    const { reply, prompt } = turn();
    select(textOf(reply, 0), 0, textOf(reply, 0), 5);
    await frames();
    const btn = button();
    expect(btn).not.toBeNull();
    const inReply = rangeOver(textOf(reply, 0), 0, textOf(reply, 0), 5);
    const inPrompt = rangeOver(textOf(prompt), 0, textOf(prompt), 2);
    await withTwoRanges(inReply, inPrompt, () => {
      btn?.click();
    });
    expect(input.value).toBe("");
  });
});
