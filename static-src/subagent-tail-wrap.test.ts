// A DELEGATE'S TAIL READS AS PROSE: it wraps, and it shows the LAST lines written.
//
// Reported on the workflow creator's card: every tail line ended in an ellipsis and
// the next line did not follow from it, because each source line was one nowrap row
// cut at the card's width — the reader saw the start of three paragraphs and the end
// of none. The fixture is that card's shape: a delegate streaming paragraphs much
// wider than the card, driven through the real store, the real lane read and the
// real stylesheet.
import { vi, describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

import { buildSubagentCard, type SubagentCard } from "./fundamentals/subagent-block.js";
import { bindSubagentTail, TAIL_LINES } from "./subagent-tail.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { defaultUsage, openEntry, openTurn, setActive, setSessions } from "./store.js";
import type { Session } from "./types.js";

const CHAT = "c-creator";
const SUB = "u-creator";
const TURN = "t1";

// Paragraphs in the creator's own register (taken from a real wf-workflow-creator
// stage), each several times wider than the card.
const FIRST =
  "I need to read the brief first so every step prompt can point at it, then check which agents exist in this workspace before I commit to a shape for the implement-and-review loop.";
const SECOND =
  "The brief asks for an implementation step followed by a semantic review step, and the reviewer has to run last so its verdict gates the stop condition of the repeat node.";
const NEWEST =
  "Opening words of the newest paragraph come first, then the plan settles on a repeat of two steps with the reviewer gating the loop: the coder implements the brief and writes its report, the reviewer reads the uncommitted diff and writes a verdict file, and the repeat stops once that verdict is APPROVED or after five iterations, whichever comes first, so the run cannot loop forever on a reviewer that never approves, and it ends with the closing word zebrafinch";

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;
const stops: (() => void)[] = [];

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  stage.className = "turn-body";
  stage.style.inlineSize = "760px";
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

afterEach(() => {
  for (const stop of stops.splice(0)) {
    stop();
  }
  stage.replaceChildren();
});

function session(): Session {
  return {
    id: CHAT,
    name: "creator",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The creator's card, mounted, with its tail bound to the lane's open text. */
function streamingCreator(text: string): SubagentCard {
  setSessions([session()]);
  setActive(CHAT);
  openTurn(CHAT, {
    id: `${TURN}-open`,
    turn: TURN,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: "m-1", text: "make a workflow" } },
  });
  openEntry(CHAT, { turn: TURN, id: `${TURN}-live`, lane: SUB, kind: "text", text, n: 1 });
  const card = buildSubagentCard("wf-workflow-creator", "in_progress");
  stage.appendChild(card.root);
  stops.push(bindSubagentTail(CHAT, SUB, card.setTail));
  return card;
}

function tailOf(card: SubagentCard): HTMLElement {
  const tail = card.root.querySelector<HTMLElement>(".subagent-tail");
  if (tail === null) {
    throw new Error("the card has no tail element");
  }
  return tail;
}

/** The painted box of `word` inside the tail's text, or null when it is absent. */
function wordRect(tail: HTMLElement, word: string): DOMRect | null {
  const walker = document.createTreeWalker(tail, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
    const at = (n.textContent ?? "").indexOf(word);
    if (at >= 0) {
      const range = document.createRange();
      range.setStart(n, at);
      range.setEnd(n, at + word.length);
      return range.getBoundingClientRect();
    }
  }
  return null;
}

/** Whether a word's painted box lies inside the region the tail shows. The clip is
 *  the window holding the lines (or the tail itself), never the padding band. */
function shown(tail: HTMLElement, word: string): boolean {
  const r = wordRect(tail, word);
  if (r === null) {
    return false;
  }
  const clip = (tail.querySelector(".subagent-tail-window") ?? tail).getBoundingClientRect();
  const slack = 0.5;
  return (
    r.top >= clip.top - slack &&
    r.bottom <= clip.bottom + slack &&
    r.left >= clip.left - slack &&
    r.right <= clip.right + slack
  );
}

/** One line's rendered height, measured on a one-character clone of a real line so
 *  the probe inherits exactly the cascade a tail line gets. */
function lineHeightOf(tail: HTMLElement): number {
  const line = tail.querySelector<HTMLElement>(".subagent-tail-line");
  if (line === null) {
    throw new Error("the tail painted no line");
  }
  const probe = line.cloneNode(false) as HTMLElement;
  probe.textContent = "x";
  line.after(probe);
  const h = probe.getBoundingClientRect().height;
  probe.remove();
  return h;
}

describe("the workflow creator's tail on paragraphs wider than the card", () => {
  const prose = `${FIRST}\n\n${SECOND}\n\n${NEWEST}`;

  it("clips no line at the card's edge", () => {
    const tail = tailOf(streamingCreator(prose));
    const lines = [...tail.querySelectorAll<HTMLElement>(".subagent-tail-line")];
    expect(lines.length, "the tail painted its lines").toBeGreaterThan(0);
    for (const line of lines) {
      expect(line.scrollWidth, `"${line.textContent ?? ""}" overflows`).toBeLessThanOrEqual(
        line.clientWidth + 1,
      );
    }
  });

  it("shows the end of the newest paragraph", () => {
    const tail = tailOf(streamingCreator(prose));
    expect(shown(tail, "zebrafinch"), "the newest word is on screen").toBe(true);
  });

  // Tail-follow: the newest paragraph wraps past the window, so its opening words
  // scroll out ABOVE — the reader sees how it ends, which is what the tail is for.
  it("lets the newest paragraph's opening words leave the window", () => {
    const tail = tailOf(streamingCreator(prose));
    expect(shown(tail, "Opening"), "the start of the newest paragraph").toBe(false);
  });

  it(`holds the window at ${String(TAIL_LINES)} lines however much text arrives`, () => {
    const tail = tailOf(streamingCreator(prose));
    const window = tail.querySelector<HTMLElement>(".subagent-tail-window") ?? tail;
    const lh = lineHeightOf(tail);
    expect(window.getBoundingClientRect().height).toBeLessThanOrEqual(TAIL_LINES * lh + 0.5);
    // Whole lines only: a window that cut a line through its middle would show a
    // half-painted row at the top.
    expect(window.getBoundingClientRect().height / lh).toBeCloseTo(TAIL_LINES, 1);
  });

  it("sizes to a short delegate's one line rather than reserving three", () => {
    const tail = tailOf(streamingCreator("Reading the brief."));
    const window = tail.querySelector<HTMLElement>(".subagent-tail-window") ?? tail;
    expect(window.getBoundingClientRect().height / lineHeightOf(tail)).toBeCloseTo(1, 1);
    expect(shown(tail, "brief"), "the whole short line is shown").toBe(true);
  });
});
