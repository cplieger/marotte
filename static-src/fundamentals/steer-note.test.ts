// The transcript row a steer becomes once it leaves the dock: its READ state, the agent's account,
// and WHOSE words it holds (a workflow's report arrives on KAS's steering buffer beside the user's
// corrections). The clamp is measured against real layout at the bottom.

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { buildSteerNote, type SteerNoteData } from "./steer-note.js";
import type { SteerReason } from "../types.js";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";

function note(over: Partial<SteerNoteData> = {}): HTMLElement {
  return buildSteerNote({ text: "actually target main", origin: "user", dropped: false, ...over });
}

function textOf(root: HTMLElement, sel: string): string | null {
  return root.querySelector(sel)?.textContent ?? null;
}

/** Any control at all. The note carries none in either state: an undelivered message is
 *  the server's to resend, and the CLAMP's opener is the one button the note may hold. */
function controls(root: HTMLElement): HTMLButtonElement[] {
  return Array.from(root.querySelectorAll<HTMLButtonElement>("button")).filter(
    (b) => !b.classList.contains("steer-note-more"),
  );
}

describe("the read state", () => {
  it("names the reader's own mid-turn message and carries the whole of it", () => {
    const n = note();

    expect(n.dataset["state"]).toBe("read");
    expect(n.dataset["origin"]).toBe("user");
    expect(textOf(n, ".steer-note-label")).toBe("Mid-turn message");
    expect(textOf(n, ".steer-note-text")).toBe("actually target main");
    // The label and the glyph are both visual, so the state has to be in the
    // accessible name too.
    expect(n.getAttribute("aria-label")).toBe("Mid-turn message: actually target main");
  });

  // THAT the agent acknowledged it, which is the whole of what the note records
  // now: the acknowledgement is its own `steer_ack` entry, so its words are not
  // this note's to carry and there is no account line to draw.
  it("records that the agent acknowledged it, and draws no account line", () => {
    const n = note({ acknowledged: true });

    expect(n.dataset["acknowledged"]).toBe("true");
    expect(n.querySelector(".steer-note-ack")).toBeNull();
    // The steer's own text stays the message: the note has to remain
    // identifiable as the thing that was sent.
    expect(textOf(n, ".steer-note-text")).toBe("actually target main");
    expect(n.getAttribute("aria-label")).toBe(
      "Mid-turn message \u00b7 acknowledged: actually target main",
    );
  });

  // A steer with no `steer_ack` behind it: the note says nothing about an
  // acknowledgement rather than claiming one with an empty clause.
  it("claims no acknowledgement when none was recorded", () => {
    const n = note({ text: "one" });
    expect(n.dataset["acknowledged"]).toBeUndefined();
    expect(n.querySelector(".steer-note-ack")).toBeNull();
    expect(n.getAttribute("aria-label")).toBe("Mid-turn message: one");
  });

  it("carries no control", () => {
    expect(controls(note({ text: "one" }))).toEqual([]);
  });
});

describe("the dropped state", () => {
  // The label states only what is known: the agent never read it.
  it("says it was not read, and keeps the text", () => {
    const n = note({ text: "never read this", dropped: true });

    expect(n.dataset["state"]).toBe("dropped");
    expect(textOf(n, ".steer-note-label")).toBe("Not read");
    expect(textOf(n, ".steer-note-text")).toBe("never read this");
    expect(n.getAttribute("aria-label")).toBe("Not read: never read this");
  });

  // A record, not an offer.
  it("carries no control, so the mark is a record rather than an offer", () => {
    const n = note({ text: "never read this", dropped: true });
    expect(controls(n)).toEqual([]);
    expect(n.textContent).not.toContain("message box");
  });

  // The dropped label states what is known and nothing more: no account line, and
  // no clause about an acknowledgement a dropped steer cannot have.
  it("says only that it was not read", () => {
    const n = note({ text: "never read this", dropped: true });
    expect(n.querySelector(".steer-note-ack")).toBeNull();
    expect(n.dataset["acknowledged"]).toBeUndefined();
    expect(n.getAttribute("aria-label")).toBe("Not read: never read this");
  });
});

// A user steer's `boundary` is the close's note: the tab closed while it was unread.
describe("the reason clause", () => {
  it("words the boundary drop, so the label says what ended the turn", () => {
    const n = note({ text: "use tabs", dropped: true, reason: "boundary" });

    expect(textOf(n, ".steer-note-label")).toBe("Not read \u00b7 the turn ended first");
    expect(n.getAttribute("aria-label")).toBe("Not read \u00b7 the turn ended first: use tabs");
  });

  it("words a deleted row as the reader's own removal", () => {
    const n = note({ text: "use tabs", dropped: true, reason: "deleted" });

    expect(textOf(n, ".steer-note-label")).toBe("Not read \u00b7 you deleted it");
    expect(n.getAttribute("aria-label")).toBe("Not read \u00b7 you deleted it: use tabs");
  });

  // TOTAL over the enum: a new SteerReason fails the client's type check until it is worded.
  // Asserted as a TYPE because the table is not public.
  it("cannot hold a reason it has no wording for", () => {
    // @ts-expect-error - a table missing `boundary` is not total over SteerReason.
    const partial: Record<SteerReason, string> = { restart: "the session restarted" };

    expect(Object.keys(partial)).toHaveLength(1);
  });
});

// A workflow injects its result through the same steering tools the user's own
// message rides, so the note must say whose words it holds (marotte.SteerOrigin).
describe("whose words the note holds", () => {
  it("names a workflow's report as one, and never as the reader's message", () => {
    const n = note({ text: "The review finished with 3 findings.", origin: "agent" });

    expect(n.dataset["origin"]).toBe("agent");
    expect(textOf(n, ".steer-note-label")).toBe("Workflow result");
    expect(n.getAttribute("aria-label")).toBe(
      "Workflow result: The review finished with 3 findings.",
    );
  });

  it("says a workflow's report was not delivered, in its own words", () => {
    const n = note({ text: "step failed", origin: "agent", dropped: true });
    expect(textOf(n, ".steer-note-label")).toBe("Workflow result not delivered");
  });

  it("gives the two origins different titles and different glyphs", () => {
    const mine = note();
    const theirs = note({ origin: "agent" });

    expect(textOf(mine, ".steer-note-label")).not.toBe(textOf(theirs, ".steer-note-label"));
    // The glyph is the second channel, so a reader who cannot tell the labels
    // apart at a glance still has a mark — and neither is colour alone.
    const glyph = (n: HTMLElement): string | undefined =>
      n.querySelector(".tool-icon svg path")?.getAttribute("d") ?? undefined;
    expect(glyph(mine)).toBeDefined();
    expect(glyph(theirs)).toBeDefined();
    expect(glyph(mine)).not.toBe(glyph(theirs));
  });

  // A step transcript sourced from KAS's replay holds the launching chat's send_message, which
  // reports no result: the note names whose words they are.
  it("names the main agent's message to a step as the main agent's, never a workflow result", () => {
    const n = note({ text: "Weigh this design input.", origin: "parent" });

    expect(n.dataset["origin"]).toBe("parent");
    expect(textOf(n, ".steer-note-label")).toBe("Message from the main agent");
    expect(n.getAttribute("aria-label")).toBe(
      "Message from the main agent: Weigh this design input.",
    );
  });

  it("says the main agent's message was not delivered, in its own words", () => {
    const n = note({ text: "weigh this", origin: "parent", dropped: true });
    expect(textOf(n, ".steer-note-label")).toBe("Message from the main agent not delivered");
  });

  it("gives the main agent's message a glyph of its own", () => {
    const glyph = (n: HTMLElement): string | undefined =>
      n.querySelector(".tool-icon svg path")?.getAttribute("d") ?? undefined;
    const parent = glyph(note({ origin: "parent" }));

    expect(parent).toBeDefined();
    expect(parent).not.toBe(glyph(note()));
    expect(parent).not.toBe(glyph(note({ origin: "agent" })));
  });
});

describe("the message keeps its shape", () => {
  // Nothing is cut in the DOM: the clamp is an OPENER, so every character stays
  // where find-in-chat can match it.
  it("puts the whole message in the DOM whatever its length", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter " +
      "struct with the missing field, then re-run the census against both bundles";
    const n = note({ text: long });
    expect(textOf(n, ".steer-note-text")).toBe(long);
    expect(textOf(n, ".steer-note-text")).not.toContain("\u2026");
  });

  // The text is fully openable, so collapsing whitespace would only destroy its shape.
  it("keeps the newlines the reader typed", () => {
    const n = note({ text: "first line\n\nsecond line" });
    expect(textOf(n, ".steer-note-text")).toBe("first line\n\nsecond line");
    // The accessible name still collapses, because it is announced as one string.
    expect(n.getAttribute("aria-label")).toBe("Mid-turn message: first line second line");
  });
});

// Real layout: a detached note reads scrollHeight and clientHeight 0.
describe("the clamp measured on the page", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  afterAll(() => {
    styleEl.remove();
    host.remove();
  });

  afterEach(() => {
    host.replaceChildren();
  });

  function mount(d: Partial<SteerNoteData>, width = 700): HTMLElement {
    host.style.inlineSize = `${String(width)}px`;
    const n = note(d);
    host.replaceChildren(n);
    return n;
  }

  function textEl(n: HTMLElement): HTMLElement {
    const t = n.querySelector<HTMLElement>(".steer-note-text");
    if (t === null) {
      throw new Error("no .steer-note-text");
    }
    return t;
  }

  function moreEl(n: HTMLElement): HTMLButtonElement {
    const b = n.querySelector<HTMLButtonElement>(".steer-note-more");
    if (b === null) {
      throw new Error("no .steer-note-more");
    }
    return b;
  }

  /** Wait for the opener to reach `hidden`, for a verdict that must CHANGE. */
  async function settles(n: HTMLElement, hidden: boolean, why: string): Promise<void> {
    await vi.waitFor(() => {
      expect(moreEl(n).hidden, why).toBe(hidden);
    });
  }

  /** Two frames span one full resize delivery, for a verdict that must NOT
   *  change. */
  async function observerRuns(): Promise<void> {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  const FORTY_LINES = Array.from({ length: 40 }, (_, i) => `line ${String(i + 1)}`).join("\n");

  it("offers no opener for a one-line message", async () => {
    const n = mount({ text: "use tabs" });
    await observerRuns();
    expect(moreEl(n).hidden).toBe(true);
  });

  it("offers one for a forty-line message, with every line still in the DOM", async () => {
    const n = mount({ text: FORTY_LINES });
    await settles(n, false, "offered for a message past four lines");
    // The whole point of `data-clamped` over an ellipsis: the text is clipped by
    // overflow, not truncated, so find-in-chat can still match the last line.
    expect(textEl(n).textContent).toContain("line 40");
    expect(textEl(n).scrollHeight).toBeGreaterThan(textEl(n).clientHeight);
  });

  it("opens on the click and closes again", async () => {
    const n = mount({ text: FORTY_LINES });
    await settles(n, false, "offered");
    const btn = moreEl(n);
    expect(btn.getAttribute("aria-expanded")).toBe("false");

    btn.click();
    expect(textEl(n).hasAttribute("data-clamped")).toBe(false);
    expect(btn.textContent).toBe("Show less");
    expect(btn.getAttribute("aria-expanded")).toBe("true");

    btn.click();
    expect(textEl(n).hasAttribute("data-clamped")).toBe(true);
    expect(btn.textContent).toBe("Show more");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  // The other half of measuring once: four lines of a wide card become more of a
  // narrow one, and a measure-once clamp cut the difference away silently.
  it("re-decides at every width", async () => {
    const body =
      "widen the existing front-matter struct with the missing field instead of " +
      "rewriting the parser, and re-run the census against both bundles first";
    const n = mount({ text: body }, 1100);
    await settles(n, true, "fits wide");

    host.style.inlineSize = "150px";
    await settles(n, false, "offered once it no longer fits");
  });
});
