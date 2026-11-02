// A markdown table inside an assistant reply is sized by its content, and a split word is never the
// price of fitting: `.message`'s `overflow-wrap: anywhere` is reset on the table, because `anywhere`
// counts every character boundary toward a cell's min-content and auto layout would crush short
// columns to a letter. Measured on real layout over the shipped stylesheet and the production renderer.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";
import { userEvent } from "vitest/browser";
import { releaseScrollableTablesIn, tableObservationCount } from "./table-scroll.js";

// A model reply as stored: three columns, a long prose cell and three empty third cells.
const LANES = `| Lane | Findings | Main points |
|---|---|---|
| A1 OIDC | 1 high, 4 medium, 8 low | **High:** an anonymous visitor can exhaust memory through the OIDC state store and an unbounded redirect. **Medium:** the account-link takeover is confirmed with two new facets; sessions are capped at the access token's lifetime; new users are provisioned with no gate. It proposes a sealed cookie to replace the state store. |
| A5 WebAuthn | 2 high, 1 medium, 3 low | |
| A3 passwords | 1 high, 8 low | The hashing amplification is confirmed with measurements, and it applies to login too, not only setup. |
| A4 rate limiting | 5 medium, 3 low | 4 new issues, each reproduced. |
| A6 API keys | 3 medium, 4 low | |
| A2 sessions | 2 medium, 5 low | |
| A7 contracts | 2 medium, 5 low | The library's tests pass, and govulncheck found nothing reachable. |
`;

const WIDE = `| Service | Region | Status | Owner | Last deploy | Latency | Notes | Runbook |
|---|---|---|---|---|---|---|---|
| authentication | eu-central-1 | healthy | platform-team | 2026-10-12 | 42 ms | Rolling restart scheduled | runbooks/authentication |
`;

const ALIGNED = `| Name | Count | Note |
|:---|---:|:---:|
| alpha | 12 | ok |
`;

let style: HTMLStyleElement;
let row: HTMLElement;
let bubble: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  row = document.createElement("div");
  row.className = "msg-row";
  bubble = document.createElement("div");
  bubble.className = "message assistant";
  row.append(bubble);
  document.body.append(row);
});

afterAll(() => {
  row.remove();
  style.remove();
});

/** Render `md` into an assistant reply `width` px wide and return its table. */
function renderTable(md: string, width: number): HTMLTableElement {
  row.style.inlineSize = `${width}px`;
  bubble.replaceChildren();
  renderMarkdownInto(bubble, md);
  return bubble.querySelector("table")!;
}

/** Every run of letters and digits in `root` that sits on more than one line; a hyphen or slash is a
 *  break opportunity the browser takes on its own, so it ends a run. */
function brokenWords(root: Element): string[] {
  const broken: string[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    const text = node.textContent ?? "";
    for (const match of text.matchAll(/[^\s\-/]+/g)) {
      const range = document.createRange();
      range.setStart(node, match.index);
      range.setEnd(node, match.index + match[0].length);
      const lines = new Set([...range.getClientRects()].map((r) => Math.round(r.top)));
      if (lines.size > 1) {
        broken.push(match[0]);
      }
    }
  }
  return broken;
}

describe.each([
  ["phone", 366],
  ["desktop", 936],
] as const)("a markdown table at %s width", (_name, width) => {
  it("never splits a word inside a cell", () => {
    const table = renderTable(LANES, width);

    expect(brokenWords(table)).toEqual([]);
  });

  it("keeps every cell as wide as its longest word", () => {
    const table = renderTable(LANES, width);
    const longest = (cell: HTMLTableCellElement): number => {
      const probe = document.createElement("span");
      probe.textContent =
        cell.textContent
          .trim()
          .split(/\s+/)
          .sort((a, b) => b.length - a.length)[0] ?? "";
      probe.style.cssText = "position:absolute;visibility:hidden;white-space:nowrap";
      cell.append(probe);
      const w = probe.getBoundingClientRect().width;
      probe.remove();
      return w;
    };
    const padding = (cell: HTMLTableCellElement): number => {
      const cs = getComputedStyle(cell);
      return parseFloat(cs.paddingInlineStart) + parseFloat(cs.paddingInlineEnd);
    };

    const short = [...table.querySelectorAll<HTMLTableCellElement>("th, td")].filter(
      (c) => c.cellIndex < 2,
    );
    expect(short).toHaveLength(16);
    for (const cell of short) {
      expect(cell.getBoundingClientRect().width, cell.textContent).toBeGreaterThanOrEqual(
        longest(cell) + padding(cell) - 0.5,
      );
    }
  });
});

describe("a markdown table too wide for the reply", () => {
  it("scrolls inside its own box instead of crushing the columns", () => {
    const table = renderTable(WIDE, 366);

    expect(table.getBoundingClientRect().width).toBeLessThanOrEqual(366);
    expect(table.scrollWidth).toBeGreaterThan(table.clientWidth);
    expect(getComputedStyle(table).overflowX).toBe("auto");
    expect(bubble.scrollWidth).toBeLessThanOrEqual(bubble.clientWidth);
    expect(brokenWords(table)).toEqual([]);
  });

  it("is only as wide as its content when the reply has room", () => {
    const table = renderTable(ALIGNED, 936);

    expect(table.getBoundingClientRect().width).toBeLessThan(400);
  });
});

describe("a markdown table's cell alignment", () => {
  it("starts at the top, left-aligned in header and body when no alignment is asked for", () => {
    const table = renderTable(LANES, 936);

    for (const cell of table.querySelectorAll("th, td")) {
      const cs = getComputedStyle(cell);
      expect(cs.textAlign, cell.textContent).toBe("start");
      expect(cs.verticalAlign, cell.textContent).toBe("top");
    }
  });

  it("gives a column's header and body the alignment its delimiter cell names", () => {
    const table = renderTable(ALIGNED, 936);
    const head = [...table.querySelectorAll("th")].map((c) => getComputedStyle(c).textAlign);
    const body = [...table.querySelectorAll("td")].map((c) => getComputedStyle(c).textAlign);

    expect(head).toEqual(["left", "right", "center"]);
    expect(body).toEqual(head);
  });
});

describe("a markdown table's empty cells", () => {
  it("keeps a row's empty cell as a cell, so the row stays three wide", () => {
    const table = renderTable(LANES, 936);
    const rows = [...table.tBodies[0]!.rows];

    expect(rows.map((r) => r.cells.length)).toEqual([3, 3, 3, 3, 3, 3, 3]);
    expect(rows.map((r) => r.cells[2]!.textContent.trim() === "")).toEqual([
      false,
      true,
      false,
      false,
      true,
      true,
      false,
    ]);
  });
});

/** Two frames: the resize observer's verdict lands after the layout that produced it. */
async function settled(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
}

describe("a markdown table's keyboard reach", () => {
  it("is a tab stop that scrolls with the arrow keys when it overflows", async () => {
    const before = document.createElement("button");
    row.before(before);
    try {
      const table = renderTable(WIDE, 366);
      await settled();
      expect(table.getAttribute("tabindex")).toBe("0");
      expect(table.getAttribute("aria-label")).toBe("Scrollable table");

      before.focus();
      await userEvent.tab();
      expect(document.activeElement).toBe(table);
      await userEvent.keyboard("{ArrowRight}");
      // Chromium animates a keyboard scroll, so the offset is polled, not read once.
      await expect.poll(() => table.scrollLeft).toBeGreaterThan(0);
    } finally {
      before.remove();
    }
  });

  it("is left out of the tab order when it fits", async () => {
    const table = renderTable(ALIGNED, 936);
    await settled();

    expect(table.hasAttribute("tabindex")).toBe(false);
    expect(table.hasAttribute("aria-label")).toBe(false);
  });

  it("follows the reply's width as it changes", async () => {
    const table = renderTable(WIDE, 366);
    await settled();
    expect(table.getAttribute("tabindex")).toBe("0");

    row.style.inlineSize = "1400px";
    await settled();
    expect(table.hasAttribute("tabindex")).toBe(false);

    row.style.inlineSize = "366px";
    await settled();
    expect(table.getAttribute("tabindex")).toBe("0");
  });

  it("is settled when a streamed reply ends", () => {
    row.style.inlineSize = "366px";
    bubble.replaceChildren();
    const stream = createMarkdownStream(bubble, { flushIntervalMs: 0 });
    stream.writeDelta(WIDE);
    stream.end();

    expect(bubble.querySelector("table")?.getAttribute("tabindex")).toBe("0");
  });
});

describe("a markdown table's observation", () => {
  it("is released with the root that held it, attached or detached", () => {
    const base = tableObservationCount();

    // A reply with two tables, left to the page: a nested root.
    bubble.replaceChildren();
    renderMarkdownInto(bubble, `${ALIGNED}\n${WIDE}`);
    expect(tableObservationCount() - base, "both tables watched").toBe(2);
    releaseScrollableTablesIn(bubble);
    expect(tableObservationCount() - base, "none after the sweep").toBe(0);

    // A reply rendered detached, never laid out: no resize entry will ever arrive for it.
    const detached = document.createElement("div");
    renderMarkdownInto(detached, ALIGNED);
    expect(tableObservationCount() - base, "detached table watched").toBe(1);
    releaseScrollableTablesIn(detached);
    expect(tableObservationCount() - base, "none after the detached sweep").toBe(0);

    // The table itself as the root.
    const alone = document.createElement("div");
    renderMarkdownInto(alone, ALIGNED);
    releaseScrollableTablesIn(alone.querySelector("table")!);
    expect(tableObservationCount() - base, "none after releasing the table itself").toBe(0);
  });

  it("leaves a table outside the swept root watched", () => {
    const base = tableObservationCount();
    const kept = document.createElement("div");
    const swept = document.createElement("div");
    renderMarkdownInto(kept, ALIGNED);
    renderMarkdownInto(swept, ALIGNED);

    releaseScrollableTablesIn(swept);

    expect(tableObservationCount() - base).toBe(1);
    releaseScrollableTablesIn(kept);
  });

  it("is let go of when a laid-out table leaves the page without a sweep", async () => {
    // Tables an earlier test left in the reply are dropped, and let go of, first.
    bubble.replaceChildren();
    await settled();
    const base = tableObservationCount();
    renderTable(ALIGNED, 936);
    await settled();
    expect(tableObservationCount() - base).toBe(1);

    bubble.replaceChildren();
    await settled();

    expect(tableObservationCount() - base).toBe(0);
  });
});
