// A reply's prose runs to the turn body's content edge like every evidence card beside it: the turn
// card is the measure (marotte's transcript rule: no width cap inside the card). The editor's read view
// is the one document measure, shared by its front-matter block and its prose. Measured on real layout
// over the shipped stylesheet and the production renderer.
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { releaseMarkdownIn, renderMarkdownInto } from "./markdown.js";

const LONG =
  "A sentence long enough to wrap many times over at any measure a stylesheet might set. ".repeat(
    8,
  );
const DOC = `# Title\n\n${LONG}\n\n- ${LONG}\n\n1. ${LONG}\n\n> ${LONG}\n`;

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});

afterEach(() => {
  for (const h of hosts.splice(0)) {
    releaseMarkdownIn(h);
    h.remove();
  }
});

afterAll(() => {
  style.remove();
});

/** Every rendered block of `prose` whose width a prose measure could cap. */
function blocks(prose: HTMLElement): HTMLElement[] {
  return [
    ...prose.querySelectorAll<HTMLElement>(":scope > :is(h1, h2, h3, p, ul, ol, blockquote)"),
  ];
}

describe("the prose measure", () => {
  it("lets every block of a reply run the full width of the turn body", () => {
    const row = document.createElement("div");
    row.className = "msg-row";
    row.style.inlineSize = "1100px";
    const reply = document.createElement("div");
    reply.className = "message assistant";
    row.append(reply);
    document.body.append(row);
    hosts.push(row);
    renderMarkdownInto(reply, DOC);

    const width = reply.getBoundingClientRect().width;
    expect(width).toBeCloseTo(1100, 0);
    const found = blocks(reply);
    expect(found.map((b) => b.tagName)).toEqual(["H2", "P", "UL", "OL", "BLOCKQUOTE"]);
    for (const b of found) {
      expect(b.getBoundingClientRect().width, `${b.tagName} width`).toBeCloseTo(width, 0);
    }
  });

  it("ends the editor read view's prose on its front-matter block's edge", () => {
    const view = document.createElement("div");
    view.className = "editor-markdown";
    view.style.inlineSize = "1400px";
    const fm = document.createElement("dl");
    fm.className = "editor-fm";
    fm.textContent = "inclusion: always";
    const prose = document.createElement("div");
    prose.className = "message assistant";
    view.append(fm, prose);
    document.body.append(view);
    hosts.push(view);
    renderMarkdownInto(prose, DOC);

    const edge = fm.getBoundingClientRect().right;
    for (const b of blocks(prose)) {
      expect(b.getBoundingClientRect().right, `${b.tagName} right edge`).toBeCloseTo(edge, 0);
    }
  });
});
