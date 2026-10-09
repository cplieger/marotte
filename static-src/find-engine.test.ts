// The shared DOM walker on the shapes its consumers render: a diff pane with highlighter spans and gutter chrome,
// and streamed adjacent text nodes.
import { describe, it, expect, afterEach } from "vitest";
import { FindEngine } from "./find-engine.js";
import { lineDiff } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";

afterEach(() => {
  document.body.replaceChildren();
});

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

function marks(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>("mark.find-hit")];
}

function hitOf(mark: HTMLElement): string | null {
  return mark.getAttribute("data-hit");
}

/** The editor's diff pane with editor-diff.ts's options, so rows carry token spans and gutter chrome. */
function goPane(): HTMLElement {
  const oldText = ["func alpha() {", '\treturn "old"', "}", ""].join("\n");
  const newText = ["func alpha() {", '\treturn "new"', "}", ""].join("\n");
  const pane = renderDiffPane(lineDiff(oldText, newText), {
    oldLabel: "before",
    newLabel: "after",
    lineNumbers: true,
    syncScroll: true,
    lang: "x.go",
    source: { oldText, newText },
  });
  const host = document.createElement("div");
  host.appendChild(pane);
  document.body.appendChild(host);
  return host;
}

describe("a match is found in a run, not in a text node", () => {
  it("matches a phrase whose words sit in different highlighter token spans", () => {
    const host = goPane();
    // The highlighter splits the line into several spans; it reads as one line.
    expect(new FindEngine(host).search("func alpha")).toBe(2);
    expect(new FindEngine(host).search("() {")).toBe(2);
    expect(new FindEngine(host).search('return "new"')).toBe(1);
  });

  it("matches a phrase crossing an inline element boundary", () => {
    const host = mount(`<p>call <code>foo bar</code> now</p>`);
    const eng = new FindEngine(host);
    expect(eng.search("call foo")).toBe(1);
    const pieces = marks(host);
    expect(pieces.map((m) => m.textContent)).toEqual(["call ", "foo"]);
    expect(pieces.map(hitOf)).toEqual(["0", "0"]);
    expect(host.textContent).toBe("call foo bar now");
  });

  it("counts hits rather than pieces, and steps between hits", () => {
    const host = mount(`<p>ab<b>c</b> abc</p>`);
    const eng = new FindEngine(host);
    expect(eng.search("abc")).toBe(2);
    expect(eng.total).toBe(2);
    const pieces = marks(host);
    expect(pieces.map(hitOf)).toEqual(["0", "0", "1"]);
    // The current hit is the first, on every piece; the scroll target is the piece it starts in.
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      true,
      true,
      false,
    ]);
    expect(eng.currentMark()).toBe(pieces[0]);
    eng.next();
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      true,
    ]);
    expect(eng.currentMark()).toBe(pieces[2]);
  });

  it("drops the current hit without dropping the highlight, and takes it back on setCurrent", () => {
    // Conflict mode steps one cursor through these marks then the buffer's, so only one may be current.
    const host = mount(`<p>ab<b>c</b> abc</p>`);
    const eng = new FindEngine(host);
    eng.search("abc");
    eng.clearCurrent();
    const pieces = marks(host);
    expect(pieces).toHaveLength(3);
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      false,
    ]);
    expect(eng.currentIndex).toBe(-1);
    expect(eng.currentMark()).toBeNull();
    eng.setCurrent(1);
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      true,
    ]);
  });

  it("does not join text across a block boundary or a <br>", () => {
    expect(new FindEngine(mount(`<div>func </div><div>alpha</div>`)).search("func alpha")).toBe(0);
    expect(new FindEngine(mount(`<p>func <br>alpha</p>`)).search("func alpha")).toBe(0);
    expect(
      new FindEngine(mount(`<ul><li>func </li><li>alpha</li></ul>`)).search("func alpha"),
    ).toBe(0);
    // Control: the same words in inline siblings are one line.
    expect(new FindEngine(mount(`<span>func </span><span>alpha</span>`)).search("func alpha")).toBe(
      1,
    );
  });

  it("restores the original text and answers the same count on a second run", () => {
    const p = document.createElement("p");
    // Adjacent text nodes, as the streaming renderer leaves them: a per-node walker missed the split word.
    p.append(document.createTextNode("al"), document.createTextNode("pha alpha"));
    const host = document.createElement("div");
    host.appendChild(p);
    document.body.appendChild(host);
    const eng = new FindEngine(host);
    expect(eng.search("alpha")).toBe(2);
    eng.clear();
    expect(host.textContent).toBe("alpha alpha");
    expect(eng.search("alpha")).toBe(2);
  });

  it("answers the same count on two consecutive runs over a real diff pane", () => {
    const host = goPane();
    const eng = new FindEngine(host);
    const first = { alpha: eng.search("alpha"), phrase: eng.search("func alpha") };
    const second = { alpha: eng.search("alpha"), phrase: eng.search("func alpha") };
    expect(first).toEqual({ alpha: 2, phrase: 2 });
    expect(second).toEqual(first);
  });
});

describe("both diff columns", () => {
  it("counts a context line rendered in both columns as two hits", () => {
    // One hit per column: text hits, unfiltered.
    const host = goPane();
    expect(new FindEngine(host).search("alpha")).toBe(2);
    expect(marks(host).map((m) => m.closest(".diff-col")?.className)).toEqual([
      "diff-col diff-col-old",
      "diff-col diff-col-new",
    ]);
    expect(new FindEngine(host).search("old")).toBe(1);
    expect(new FindEngine(host).search("new")).toBe(1);
  });
});

describe("UI chrome", () => {
  it("skips a diff pane's line numbers and markers", () => {
    const host = goPane();
    expect(host.querySelector(".diff-gutter")?.textContent).toBe("1");
    expect(new FindEngine(host).search("1")).toBe(0);
    expect(new FindEngine(host).search("-")).toBe(0);
    expect(new FindEngine(host).search("+")).toBe(0);
  });

  it("marks the refused resource inside .tool-denial while skipping the card's other chrome", () => {
    const host = mount(
      `<div class="tool-call">` +
        `<div class="tool-header"><span class="tool-title">Run Command</span></div>` +
        `<div class="tool-denial" data-vk-chrome>` +
        `<div class="tool-denial-row"><span>Resource</span><code>rm -rf /config</code></div>` +
        `</div>` +
        `<button class="tool-output-reveal" data-vk-chrome>Show 3 more lines</button>` +
        `</div>`,
    );
    expect(new FindEngine(host).search("rm -rf")).toBe(1);
    expect(marks(host)[0]?.closest(".tool-denial")).not.toBeNull();
    expect(new FindEngine(host).search("more lines")).toBe(0);
  });

  it("marks the MCP server name in the badge", () => {
    const host = mount(
      `<div class="tool-header"><span class="tool-title">create issue</span>` +
        `<span class="tool-mcp-badge" data-vk-chrome>github</span></div>`,
    );
    expect(new FindEngine(host).search("github")).toBe(1);
    expect(marks(host)[0]?.className).toBe("find-hit find-hit-current");
  });
});

describe("the fold", () => {
  it("marks the right characters after U+0130, whose lowercase grows", () => {
    const host = mount(`<p>\u0130\u{1F600}needle</p>`);
    expect(new FindEngine(host).search("needle")).toBe(1);
    expect(marks(host)[0]?.textContent).toBe("needle");
    expect(host.textContent).toBe("\u0130\u{1F600}needle");
  });

  it("folds U+0130 to a plain i, as the server does", () => {
    const host = mount(`<p>\u0130stanbul</p>`);
    expect(new FindEngine(host).search("istanbul")).toBe(1);
    expect(marks(host)[0]?.textContent).toBe("\u0130stanbul");
  });
});

function twoFrames(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

function currentHolder(eng: FindEngine): string | null {
  return eng.currentMark()?.closest("p")?.id ?? null;
}

/** A hidden early match ahead of three, so that un-hiding it renumbers every later hit. */
function threeParagraphs(): string {
  return (
    `<div hidden><p id="early">an early needle</p></div><p id="first">the first needle</p>` +
    `<p id="second">the second needle</p><p id="third">the third needle</p>`
  );
}

function paragraph(id: string, text: string): HTMLParagraphElement {
  const p = document.createElement("p");
  p.id = id;
  p.textContent = text;
  return p;
}

function currentOn(host: HTMLElement, index: number): FindEngine {
  const eng = new FindEngine(host);
  eng.search("needle");
  eng.setCurrent(index);
  return eng;
}

const AUTO_BOX = "content-visibility:auto;contain-intrinsic-size:auto 40px";

/** The 4000px gaps keep at most one of `rows` rendered in the 300px viewport. */
function tallScroller(...rows: string[]): string {
  return `<div style="height:300px;overflow:auto">${rows.join(`<div style="height:4000px"></div>`)}</div>`;
}

async function awayFromTop(host: HTMLElement): Promise<FindEngine> {
  const scroller = host.firstElementChild as HTMLElement;
  await twoFrames();
  const eng = new FindEngine(host);
  expect(eng.search("needle")).toBe(1);
  expect(currentHolder(eng)).toBe("top");
  scroller.scrollTop = scroller.scrollHeight;
  await twoFrames();
  expect(host.querySelector("#top")?.checkVisibility({ contentVisibilityAuto: true })).toBe(false);
  return eng;
}

describe("refresh", () => {
  it("keeps the current hit when an earlier match becomes searchable", () => {
    const host = mount(
      `<div hidden><p id="early">an early needle</p></div>` +
        `<p id="first">the first needle</p><p id="second">the second needle</p>`,
    );
    const eng = new FindEngine(host);
    eng.search("needle");
    eng.setCurrent(1);
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(currentHolder(eng)).toBe("second");
  });

  it("keeps the current hit among several in its paragraph", () => {
    const host = mount(
      `<div hidden><p id="early">an early needle</p></div>` +
        `<p id="both">one needle, then a second needle</p>`,
    );
    const eng = new FindEngine(host);
    eng.search("needle");
    eng.setCurrent(1);
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(eng.currentIndex).toBe(2);
  });

  it("keeps the current hit when an earlier match in its own paragraph becomes searchable", () => {
    const host = mount(
      `<p id="both"><span hidden>an early needle, </span>one needle, then a second needle</p>`,
    );
    const eng = new FindEngine(host);
    eng.search("needle");
    eng.setCurrent(1);
    host.querySelector("span")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(eng.currentMark()?.previousSibling?.textContent).toBe(", then a second ");
  });

  it("keeps the current hit once text before it merges into one node", () => {
    const host = mount(threeParagraphs());
    const eng = currentOn(host, 1);
    const second = host.querySelector("#second");
    second?.insertBefore(document.createTextNode("old "), eng.currentMark());
    second?.normalize();
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(4);
    expect(currentHolder(eng)).toBe("second");
  });

  it("makes the copy's hit current when the current hit's paragraph is replaced by a copy", () => {
    const host = mount(threeParagraphs());
    const eng = currentOn(host, 1);
    const copy = paragraph("second", "the second needle");
    host.querySelector("#second")?.replaceWith(copy);
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(4);
    expect(eng.currentMark()?.parentElement).toBe(copy);
  });

  it("makes the next hit current once the current hit's paragraph is replaced without it", () => {
    const host = mount(threeParagraphs());
    const eng = currentOn(host, 1);
    host.querySelector("#second")?.replaceWith(paragraph("second", "no match left"));
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(currentHolder(eng)).toBe("third");
  });

  it("makes the next hit current once the current hit's box folds", () => {
    const host = mount(
      `<div hidden><p id="early">an early needle</p></div><p id="first">the first needle</p>` +
        `<div id="fold"><p id="second">the second needle</p></div><p id="third">the third needle</p>`,
    );
    const eng = currentOn(host, 1);
    host.querySelector<HTMLElement>("#fold")?.style.setProperty("content-visibility", "hidden");
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(currentHolder(eng)).toBe("third");
  });

  it("wraps to the first hit once the vanished current hit was the last", () => {
    const host = mount(threeParagraphs());
    const eng = currentOn(host, 2);
    host.querySelector("#third")?.remove();
    host.querySelector("div")?.removeAttribute("hidden");
    expect(eng.refresh("needle")).toBe(3);
    expect(currentHolder(eng)).toBe("early");
  });

  it("has no current hit once no hits remain", () => {
    const host = mount(`<p id="only">the only needle</p>`);
    const eng = currentOn(host, 0);
    host.querySelector("#only")?.replaceWith(paragraph("only", "no match left"));
    expect(eng.refresh("needle")).toBe(0);
    expect({ index: eng.currentIndex, mark: eng.currentMark() }).toEqual({ index: -1, mark: null });
  });

  it.each(["clearCurrent", "clear"] as const)(
    "starts from the first hit when refreshed after %s",
    (drop) => {
      const host = mount(threeParagraphs());
      const eng = currentOn(host, 1);
      eng[drop]();
      expect(eng.refresh("needle")).toBe(3);
      expect(currentHolder(eng)).toBe("first");
    },
  );

  // A skipped `content-visibility: auto` box is pruned, so the reader scrolling away from the current hit would take it
  // out of the next walk; every other skipped box stays pruned.
  it("walks the current hit's skipped box and no other", async () => {
    const host = mount(
      tallScroller(
        `<div style="${AUTO_BOX}"><p id="top">a needle at the top</p></div>`,
        `<div style="${AUTO_BOX}"><p id="middle">a needle in the middle</p></div>`,
        `<div style="${AUTO_BOX}"><p id="bottom">a needle at the bottom</p></div>`,
      ),
    );
    const eng = await awayFromTop(host);
    expect({ total: eng.refresh("needle"), current: currentHolder(eng) }).toEqual({
      total: 2,
      current: "top",
    });
  });

  // A transcript row nests automatic boxes (a card inside a `.msg-row`'s bubble); the outer one's skip hides
  // everything between the two.
  it("walks the outermost skipped box around the current hit", async () => {
    const host = mount(
      tallScroller(
        `<div style="${AUTO_BOX}"><div><div style="${AUTO_BOX}"><p id="top">a needle at the top</p></div></div></div>`,
        `<div style="${AUTO_BOX}"><p id="bottom">a needle at the bottom</p></div>`,
      ),
    );
    const eng = await awayFromTop(host);
    expect({ total: eng.refresh("needle"), current: currentHolder(eng) }).toEqual({
      total: 2,
      current: "top",
    });
  });

  it("leaves content hidden inside the current hit's skipped box unsearched", async () => {
    const host = mount(
      tallScroller(
        `<div style="${AUTO_BOX}">` +
          `<p id="top">a needle at the top<span style="display:none"> a needle</span>` +
          `<span style="visibility:hidden"> a needle</span></p>` +
          `<div style="content-visibility:hidden"><p>a folded needle</p></div>` +
          `</div>`,
        `<div style="${AUTO_BOX}"><p id="bottom">a needle at the bottom</p></div>`,
      ),
    );
    const eng = await awayFromTop(host);
    expect({ total: eng.refresh("needle"), current: currentHolder(eng) }).toEqual({
      total: 2,
      current: "top",
    });
  });
});
