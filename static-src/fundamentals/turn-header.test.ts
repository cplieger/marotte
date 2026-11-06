// The turn card's header band. The CLAMP is CSS and fold-conditional, so
// `disclosure-row-css.test.ts` owns it against real layout; the cut fade is measured here.
import { describe, it, expect, afterEach } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { glyphMarkup, registryMarkup } from "../__test-helpers__/glyph.js";
import { workflowIcon } from "../icons.js";
import {
  buildRequestHeader,
  buildTurnHeader,
  updateTurnHeader,
  type TurnHeaderData,
} from "./turn-header.js";
import { initAttachmentPillCallbacks } from "../attachment-pill.js";
import { wakeLine } from "../turn-wake.js";
import { namedWake } from "../turns.js";
import type { EntryTurnWake } from "../wire/types.gen.js";
import { QUOTE_ATTRIBUTION, appendQuote, codeSpan, quoteSelection } from "../message-quote.js";

function data(over: Partial<TurnHeaderData> = {}): TurnHeaderData {
  return {
    n: 1,
    outcome: "completed",
    ts: 0,
    request: "short request",
    attachments: [],
    ...over,
  };
}

function attachmentRow(h: HTMLElement): HTMLElement {
  const r = h.querySelector<HTMLElement>(".turn-req-attachments");
  if (r === null) {
    throw new Error("no .turn-req-attachments");
  }
  return r;
}

function attachmentPaths(h: HTMLElement): (string | null)[] {
  return [...attachmentRow(h).querySelectorAll(".attachment-pill")].map((p) =>
    p.getAttribute("title"),
  );
}

function text(h: HTMLElement): HTMLElement {
  const t = h.querySelector<HTMLElement>(".turn-req-text");
  if (t === null) {
    throw new Error("no .turn-req-text");
  }
  return t;
}

const LONG = "x".repeat(400);

describe("buildTurnHeader", () => {
  it("renders the turn number, the request and an outcome dot", () => {
    const h = buildTurnHeader(data({ n: 14, request: "add the pause state" }));
    expect(h.querySelector(".turn-n")?.textContent).toBe("#14");
    expect(text(h).textContent).toBe("add the pause state");
    expect(h.querySelector(".turn-dot")?.getAttribute("aria-label")).toBe("Completed");
  });

  // The dot is colour-only in CSS, so the accessible name is the non-colour
  // channel that makes outcome readable at all.
  it("names every outcome on the dot rather than relying on colour", () => {
    for (const [outcome, label] of [
      ["running", "Running"],
      ["completed", "Completed"],
      ["interrupted", "Interrupted"],
      ["failed", "Failed"],
    ] as const) {
      const h = buildTurnHeader(data({ outcome }));
      expect(h.dataset["outcome"], outcome).toBe(outcome);
      expect(h.querySelector(".turn-dot")?.getAttribute("aria-label"), outcome).toBe(label);
    }
  });

  it("renders a typed trigger line instead of inventing a user message", () => {
    const h = buildTurnHeader(data({ request: undefined }));
    expect(h.dataset["trigger"]).toBe("system");
    expect(text(h).textContent).toBe("Agent-initiated turn");
  });

  it("treats a whitespace-only request as no request", () => {
    const h = buildTurnHeader(data({ request: "   \n  " }));
    // updateTurnHeader is what branches; a blank string still reaches it as a
    // defined value, so the trim has to happen where the text is written.
    expect(text(h).textContent).toBe("");
  });

  it("puts the fold toggle and every readout in the meta row", () => {
    // Every one of them is a child of the row rather than of the band, which is
    // the depth `setHitCount`, `wireRowToggle` and `setCardFolded` (messages.ts)
    // reach them at.
    const h = buildTurnHeader(data({ n: 14, ts: 1_700_000_000_000 }));
    for (const sel of [
      ".turn-fold-toggle",
      ".turn-n",
      ".turn-dot",
      ".turn-ts",
      ".turn-hit-count",
    ]) {
      expect(h.querySelector(`:scope > .turn-head-row > ${sel}`), sel).not.toBeNull();
    }
  });

  it("keeps the meta row out of the request's text", () => {
    // An inline readout as the text's first child would contaminate the `textContent` the copy
    // button reads (and `linkifyPaths` rewrites text nodes there).
    const h = buildTurnHeader(data({ n: 14, ts: 1_700_000_000_000, request: "  fix the test  " }));
    expect(h.querySelector(".turn-ts")?.textContent, "the time really is stamped").not.toBe("");
    expect(text(h).textContent).toBe("fix the test");
    expect(text(h).querySelector(".turn-head-row")).toBeNull();
    expect(text(h).contains(h.querySelector(".turn-head-row"))).toBe(false);
  });

  it("offers no show-more: the fold chevron opens the prompt", () => {
    const h = buildTurnHeader(data({ request: LONG }));
    expect(h.querySelector("button:not(.turn-fold-toggle)")).toBeNull();
    expect(text(h).hasAttribute("data-clamped")).toBe(false);
  });
});

describe("the header band", () => {
  it("carries no copy control; the footer's copies include the prompt", () => {
    const h = buildTurnHeader(data());
    expect(h.querySelector("button:not(.turn-fold-toggle)")).toBeNull();
  });
});

describe("updateTurnHeader", () => {
  it("is idempotent", () => {
    const h = buildTurnHeader(data({ n: 3, request: "hello" }));
    const before = h.outerHTML;
    updateTurnHeader(h, data({ n: 3, request: "hello" }));
    expect(h.outerHTML).toBe(before);
  });

  it("renumbers and re-tints in place", () => {
    const h = buildTurnHeader(data({ n: 3 }));
    updateTurnHeader(h, data({ n: 4, outcome: "failed" }));
    expect(h.querySelector(".turn-n")?.textContent).toBe("#4");
    expect(h.dataset["outcome"]).toBe("failed");
  });

  it("stamps a machine-readable timestamp when one is known", () => {
    const h = buildTurnHeader(data({ ts: 1_700_000_000_000 }));
    const t = h.querySelector<HTMLTimeElement>(".turn-ts");
    expect(t?.getAttribute("datetime")).toBe(new Date(1_700_000_000_000).toISOString());
    expect(t?.textContent).not.toBe("");
  });

  it("leaves the time empty rather than rendering the epoch", () => {
    const h = buildTurnHeader(data({ ts: 0 }));
    expect(h.querySelector(".turn-ts")?.textContent).toBe("");
  });
});

// Attachments are stamped on the user message because BuildPromptBlocks consumes them: they never
// appear in the request text.
describe("the request's attachments", () => {
  const shot = { path: "out/shot.png", name: "shot.png" };
  const spec = { path: "docs/spec.md", name: "spec.md" };

  it("renders one pill per attachment", () => {
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    expect(attachmentPaths(h)).toEqual(["out/shot.png", "docs/spec.md"]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(false);
  });

  it("hides the row when the request carried none", () => {
    const h = buildTurnHeader(data());
    expect(attachmentPaths(h)).toEqual([]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(true);
  });

  // The clamp is scoped to `.turn-req-text`. A pill inside it would vanish
  // whenever a folded turn's prompt clipped to four lines — and the attachments
  // are part of how a reader identifies which request this was.
  it("sits inside .turn-req but OUTSIDE the clamped text", () => {
    const h = buildTurnHeader(data({ request: LONG, attachments: [shot] }));
    expect(h.querySelector(".turn-req > .turn-req-attachments")).not.toBeNull();
    expect(text(h).querySelector(".attachment-pill")).toBeNull();
    expect(attachmentPaths(h)).toEqual(["out/shot.png"]);
  });

  it("stays clickable, opening the attachment it names", () => {
    const opened: string[] = [];
    initAttachmentPillCallbacks({
      open: (p) => {
        opened.push(p);
      },
    });
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    const bodies = [...attachmentRow(h).querySelectorAll<HTMLButtonElement>(".attachment-open")];
    expect(bodies).toHaveLength(2);
    for (const b of bodies) {
      b.click();
    }
    expect(opened).toEqual(["out/shot.png", "docs/spec.md"]);
  });

  // A sent attachment cannot be un-sent, so the header's pill carries no `×` —
  // unlike the composer's, which is the same component with a remover passed in.
  it("offers no remove control", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    expect(attachmentRow(h).querySelector(".attachment-close")).toBeNull();
  });

  // updateTurnHeader runs on every repaint, streaming chunks included. Rebuilding
  // the row each time would destroy a pill the user is tabbed onto.
  it("keeps the same pill elements across a repaint with the same list", () => {
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    const before = [...attachmentRow(h).children];
    updateTurnHeader(h, data({ attachments: [shot, spec], outcome: "failed" }));
    expect([...attachmentRow(h).children]).toEqual(before);
  });

  it("rebuilds when the list actually changes", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    updateTurnHeader(h, data({ attachments: [shot, spec] }));
    expect(attachmentPaths(h)).toEqual(["out/shot.png", "docs/spec.md"]);
    updateTurnHeader(h, data({ attachments: [] }));
    expect(attachmentPaths(h)).toEqual([]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(true);
  });

  // The row is synced ahead of the request-text branch, which returns early for an
  // agent-initiated turn — otherwise a repaint could leave pills describing a
  // request that is no longer there.
  it("clears the row even when the trigger becomes a system one", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    updateTurnHeader(h, data({ request: undefined, attachments: [] }));
    expect(attachmentPaths(h)).toEqual([]);
  });
});

describe("a labelled request", () => {
  function full(h: HTMLElement): HTMLElement {
    const f = h.querySelector<HTMLElement>(".turn-req-full");
    if (f === null) {
      throw new Error("no .turn-req-full");
    }
    return f;
  }

  it("shows the label, its full text part of the prompt the fold opens", () => {
    const h = buildTurnHeader(data({ request: "the whole instruction", label: "Run task 2" }));
    expect(text(h).textContent).toBe("Run task 2");
    expect(full(h).textContent).toBe("the whole instruction");
    expect(full(h).previousElementSibling).toBe(text(h));
    expect(h.querySelector(".turn-req-more")).toBeNull();
  });

  it("hides the full text while the turn is folded, and shows it open", () => {
    const style = mountAppCSS();
    const card = document.createElement("div");
    card.className = "turn";
    try {
      const h = buildTurnHeader(data({ request: "the whole instruction", label: "Run task 2" }));
      card.append(h);
      document.body.append(card);
      expect(getComputedStyle(full(h)).display).not.toBe("none");
      card.toggleAttribute("data-folded", true);
      expect(getComputedStyle(full(h)).display).toBe("none");
    } finally {
      card.remove();
      style.remove();
    }
  });

  it("never relabels a typed request", () => {
    const h = buildTurnHeader(data({ request: "what I typed" }));
    expect(text(h).textContent).toBe("what I typed");
    expect(h.querySelector(".turn-req-full")).toBeNull();
  });
});

describe("a folded turn whose prompt is cut", () => {
  let style: HTMLStyleElement | undefined;
  let card: HTMLElement | undefined;
  afterEach(() => {
    card?.remove();
    style?.remove();
  });
  // Three frames: the observer delivers after the first layout and the review runs a frame later.
  const settle = (): Promise<void> =>
    new Promise((r) =>
      requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(() => r()))),
    );
  async function folded(request: string): Promise<HTMLElement> {
    style = mountAppCSS();
    card = document.createElement("div");
    card.className = "turn";
    card.toggleAttribute("data-folded", true);
    card.style.inlineSize = "30rem";
    const h = buildTurnHeader(data({ request }));
    card.append(h);
    document.body.append(card);
    await settle();
    return h;
  }
  const toggleName = (h: HTMLElement): string | null =>
    h.querySelector(".turn-fold-toggle")?.getAttribute("aria-label") ?? null;

  it("fades its last line and says so in the chevron's name", async () => {
    const h = await folded("a long line of words ".repeat(60));
    expect(text(h).hasAttribute("data-cut")).toBe(true);
    expect(getComputedStyle(text(h)).maskImage).toContain("linear-gradient");
    expect(toggleName(h)).toBe("Expand or collapse this turn, message truncated");
  });

  it("neither fades nor flags a prompt that fits", async () => {
    const h = await folded("short");
    expect(text(h).hasAttribute("data-cut")).toBe(false);
    expect(getComputedStyle(text(h)).maskImage).toBe("none");
    expect(toggleName(h)).toBe("Expand or collapse this turn");
  });

  it("drops the fade once the turn opens", async () => {
    const h = await folded("a long line of words ".repeat(60));
    card?.toggleAttribute("data-folded", false);
    await settle();
    await settle();
    expect(text(h).hasAttribute("data-cut")).toBe(false);
    expect(getComputedStyle(text(h)).maskImage).toBe("none");
    expect(toggleName(h)).toBe("Expand or collapse this turn");
  });
});

// The words per kind are wake_line.json's (turn-wake.node.test.ts); this pins where they land.
describe("a woken turn's header", () => {
  const RUN = "wf_5e1f0c2a9b7d4e36";
  const OTHER = "wf_7c3a9e1f0b2d4a58";
  const ERROR =
    "The site build failed before any edit: the install guide has no front matter, so the " +
    "link check cannot run on the built pages.";
  const stepWake = (severity: string, text = ERROR): EntryTurnWake => ({
    reason: "step_message",
    steers: [{ id: "wfmsg-1", run: RUN, step: "check-links", severity, text }],
  });
  const names = new Map([
    [RUN, "publish-docs"],
    [OTHER, "nightly-report"],
  ]);
  const wakeData = (w: EntryTurnWake, open?: (id: string) => void): TurnHeaderData =>
    data({
      request: undefined,
      wake: wakeLine(namedWake(w, names)),
      onOpenRun: open === undefined ? undefined : (run) => open(run.id),
    });
  const label = (h: HTMLElement): HTMLElement | null => h.querySelector(".turn-req > .turn-wake");

  it("puts the label on its own line above the message, the message where a prompt goes", () => {
    const h = buildTurnHeader(wakeData(stepWake("error")));
    expect(h.dataset["trigger"]).toBe("wake");
    expect(label(h)?.textContent).toBe("publish-docs \u00b7 check-links reports an error");
    expect(h.querySelector(".turn-req")?.firstElementChild).toBe(label(h));
    expect(text(h).textContent).toBe(ERROR);
    expect(text(h).hidden).toBe(false);
    expect(text(h).contains(label(h))).toBe(false);
  });

  it("leads a step message's line with the workflow message glyph, and a finished run's with none", () => {
    const icon = label(buildTurnHeader(wakeData(stepWake("error"))))?.querySelector(
      ".turn-wake-icon",
    );
    expect(icon?.getAttribute("aria-hidden")).toBe("true");
    expect(glyphMarkup(icon?.firstElementChild)).toBe(registryMarkup(workflowIcon("message")));

    const finished = buildTurnHeader(
      wakeData({
        reason: "run_finished",
        run: RUN,
        run_name: "publish-docs",
        run_status: "completed",
      }),
    );
    expect(label(finished)).not.toBeNull();
    expect(label(finished)?.querySelector(".turn-wake-icon")).toBeNull();
  });

  it("opens the run from its name, and the name is plain text with no opener", () => {
    const opened: string[] = [];
    const h = buildTurnHeader(wakeData(stepWake("warning"), (id) => opened.push(id)));
    const btn = h.querySelector<HTMLElement>(".turn-wake > .turn-wake-run[role=button]");
    expect(btn?.textContent).toBe("publish-docs");
    expect(btn?.dataset["runId"]).toBe(RUN);
    btn?.click();
    expect(opened).toEqual([RUN]);

    const plain = buildTurnHeader(wakeData(stepWake("warning")));
    expect(plain.querySelector(".turn-wake-run")?.hasAttribute("role")).toBe(false);
  });

  // `role="button"` names, so their words stay selectable; the keys a `<button>` takes still work.
  it("opens the run from a wake's or a re-delivery row's name with Enter or Space", () => {
    const opened: string[] = [];
    const step = buildTurnHeader(wakeData(stepWake("warning"), (id) => opened.push(id)));
    const rows = buildTurnHeader(
      wakeData(
        { reason: "rewake", steers: [{ id: "q", run: OTHER, step: "draft", text: "Which?" }] },
        (id) => opened.push(id),
      ),
    );
    const names = [
      step.querySelector<HTMLElement>(".turn-wake-run")!,
      rows.querySelector<HTMLElement>(".turn-wake-row-run")!,
    ];
    for (const name of names) {
      expect(name.tagName).not.toBe("BUTTON");
      expect(name.tabIndex).toBe(0);
      for (const key of ["Enter", " "]) {
        const press = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
        name.dispatchEvent(press);
        expect(press.defaultPrevented, `${key} does not also scroll or type`).toBe(true);
      }
      name.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
    }
    expect(opened).toEqual([RUN, RUN, OTHER, OTHER]);
  });

  it("does not open the run on the click that ends a selection in its name", () => {
    const opened: string[] = [];
    const h = buildTurnHeader(wakeData(stepWake("warning"), (id) => opened.push(id)));
    document.body.append(h);
    const name = h.querySelector<HTMLElement>(".turn-wake-run")!;
    const range = document.createRange();
    range.selectNodeContents(name);
    document.getSelection()?.removeAllRanges();
    document.getSelection()?.addRange(range);
    name.click();
    expect(opened).toEqual([]);
    document.getSelection()?.removeAllRanges();
    name.click();
    expect(opened).toEqual([RUN]);
    h.remove();
  });

  it("draws a finished run's label alone, with no empty message under it", () => {
    const h = buildTurnHeader(
      wakeData({
        reason: "run_finished",
        run: "wf_unknown01234",
        run_name: "publish-docs-held",
        run_status: "completed",
      }),
    );
    expect(label(h)?.textContent).toBe("publish-docs-held completed");
    expect(text(h).hidden).toBe(true);
  });

  it("renames the run on a repaint once its name arrives", () => {
    const unnamed = (): TurnHeaderData =>
      data({ request: undefined, wake: wakeLine(stepWake("warning")), onOpenRun: () => undefined });
    const h = buildTurnHeader(unnamed());
    expect(label(h)?.querySelector(".turn-wake-run")?.textContent).toBe("A workflow");
    updateTurnHeader(
      h,
      wakeData(stepWake("warning"), () => undefined),
    );
    expect(label(h)?.querySelector(".turn-wake-run")?.textContent).toBe("publish-docs");
    expect(label(h)?.textContent).toBe("publish-docs \u00b7 check-links asks you");
  });

  it("repaints rows whose joined words read alike but split differently", () => {
    const twoRows: EntryTurnWake = {
      reason: "rewake",
      steers: [
        { id: "q", run: RUN, step: "draft", text: "a" },
        { id: "r", run: OTHER, step: "collect", text: "b" },
      ],
    };
    const oneRow: EntryTurnWake = {
      reason: "rewake",
      steers: [
        {
          id: "q",
          run: RUN,
          step: "draft",
          text: `a\n${OTHER}nightly-report \u00b7 collect: b`,
        },
      ],
    };
    const h = buildTurnHeader(wakeData(oneRow));
    updateTurnHeader(h, wakeData(twoRows));
    expect(text(h).querySelectorAll(".turn-wake-row-run").length).toBe(2);
  });

  it("lists a re-delivery's messages as its message, each opening its own run from its name", () => {
    const opened: string[] = [];
    const h = buildTurnHeader(
      wakeData(
        {
          reason: "rewake",
          steers: [
            { id: "q", run: RUN, step: "draft", text: "Which branch?" },
            { id: "r", run: OTHER, step: "collect", text: "Leg 1 is done." },
          ],
        },
        (id) => opened.push(id),
      ),
    );
    expect(label(h)?.textContent).toBe(
      "Resumed with 2 workflow messages you stopped before they were read",
    );
    expect(label(h)?.querySelector(".turn-wake-run")).toBeNull();
    expect(text(h).textContent).toBe(
      "publish-docs \u00b7 draft: Which branch?\nnightly-report \u00b7 collect: Leg 1 is done.",
    );
    const buttons = [...text(h).querySelectorAll<HTMLElement>(".turn-wake-row-run[role=button]")];
    expect(buttons.map((b) => b.textContent)).toEqual(["publish-docs", "nightly-report"]);
    for (const b of buttons) {
      b.click();
    }
    expect(opened).toEqual([RUN, OTHER]);
  });

  it("gives a re-delivery's text back to a step message on a repaint", () => {
    const h = buildTurnHeader(
      wakeData({
        reason: "rewake",
        steers: [{ id: "q", run: RUN, step: "draft", text: "Which branch?" }],
      }),
    );
    updateTurnHeader(h, wakeData(stepWake("error")));
    expect(text(h).textContent).toBe(ERROR);
    expect(text(h).querySelector(".turn-wake-row-run")).toBeNull();
  });

  it("keeps the typed system line for a wake that names nothing", () => {
    const h = buildTurnHeader(wakeData({ reason: "unknown" }));
    expect(h.dataset["trigger"]).toBe("system");
    expect(text(h).textContent).toBe("Agent-initiated turn");
    expect(label(h)).toBeNull();
  });

  it("drops the label when a repaint has no wake, and keeps a focused run button across one that does", () => {
    const h = buildTurnHeader(wakeData(stepWake("info"), () => undefined));
    const btn = h.querySelector(".turn-wake-run");
    updateTurnHeader(
      h,
      wakeData(stepWake("info"), () => undefined),
    );
    expect(h.querySelector(".turn-wake-run")).toBe(btn);
    updateTurnHeader(h, data({ request: undefined }));
    expect(label(h)).toBeNull();
    expect(text(h).hidden).toBe(false);
    expect(text(h).textContent).toBe("Agent-initiated turn");
  });

  describe("clamps its message exactly as a prompt's", () => {
    let style: HTMLStyleElement | undefined;
    let cards: HTMLElement[] = [];
    afterEach(() => {
      for (const c of cards) {
        c.remove();
      }
      cards = [];
      style?.remove();
      style = undefined;
    });
    const settle = (): Promise<void> =>
      new Promise((r) =>
        requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(() => r()))),
      );
    function mount(d: TurnHeaderData, folded: boolean): HTMLElement {
      style ??= mountAppCSS();
      const card = document.createElement("div");
      card.className = "turn";
      card.toggleAttribute("data-folded", folded);
      card.style.inlineSize = "30rem";
      const h = buildTurnHeader(d);
      card.append(h);
      document.body.append(card);
      cards.push(card);
      return h;
    }
    const LONG_WAKE = "a long line of words ".repeat(60);

    it("in full on an open turn", async () => {
      const h = mount(wakeData(stepWake("warning", LONG_WAKE)), false);
      await settle();
      expect(getComputedStyle(text(h)).maxBlockSize).toBe("none");
      expect(text(h).hasAttribute("data-cut")).toBe(false);
      expect(text(h).scrollHeight).toBe(text(h).clientHeight);
    });

    it("cut to a folded prompt's box with its fade on a folded turn, the label left whole", async () => {
      const woken = mount(wakeData(stepWake("warning", LONG_WAKE)), true);
      const prompted = mount(data({ request: LONG_WAKE }), true);
      await settle();
      expect(text(woken).hasAttribute("data-cut")).toBe(true);
      expect(text(woken).clientHeight).toBe(text(prompted).clientHeight);
      expect(getComputedStyle(text(woken)).maskImage).toBe(
        getComputedStyle(text(prompted)).maskImage,
      );
      expect(woken.querySelector(".turn-fold-toggle")?.getAttribute("aria-label")).toBe(
        "Expand or collapse this turn, message truncated",
      );
      expect(label(woken)?.getBoundingClientRect().height).toBeGreaterThan(0);
    });
  });
});

describe("a request that opens with a quoted reply passage", () => {
  const quoted = `${appendQuote("", quoteSelection("the passage\nover two lines", "From Kiro's reply"))}why that?`;
  const band = (h: HTMLElement): HTMLElement | null =>
    h.querySelector(".turn-req > .turn-req-quote");

  it("draws the passage as a band before the question, led by its source, without the attribution", () => {
    const h = buildTurnHeader(data({ request: quoted }));
    expect(band(h)?.textContent).toBe("From Kiro's reply: the passage\nover two lines");
    expect(band(h)?.querySelector(".turn-req-quote-from")?.textContent).toBe("From Kiro's reply:");
    expect(band(h)?.nextElementSibling).toBe(text(h));
    expect(text(h).textContent).toBe("why that?");
  });

  it("draws a source's command as code", () => {
    const source = `From the output of ${codeSpan("a`b")}`;
    const h = buildTurnHeader(
      data({ request: `${appendQuote("", quoteSelection("FAIL", source))}why?` }),
    );
    const from = band(h)?.querySelector(".turn-req-quote-from");
    expect(from?.textContent).toBe("From the output of a`b:");
    expect(from?.querySelector("code")?.textContent).toBe("a`b");
  });

  it("draws a quote sent before quotes named their source as the passage alone", () => {
    const h = buildTurnHeader(data({ request: `> old words\n>\n> ${QUOTE_ATTRIBUTION}\n\nwhy?` }));
    expect(band(h)?.textContent).toBe("old words");
    expect(band(h)?.querySelector(".turn-req-quote-from")).toBeNull();
  });

  it("redraws the band when only the source changes", () => {
    const h = buildTurnHeader(data({ request: quoted }));
    const other = `${appendQuote("", quoteSelection("the passage\nover two lines", "From Kiro's thinking"))}why that?`;
    updateTurnHeader(h, data({ request: other }));
    expect(band(h)?.querySelector(".turn-req-quote-from")?.textContent).toBe(
      "From Kiro's thinking:",
    );
  });

  it("leaves a hand-typed blockquote as the prompt's own text", () => {
    const h = buildTurnHeader(data({ request: "> something I typed\n\nmy question" }));
    expect(band(h)).toBeNull();
    expect(text(h).textContent).toBe("> something I typed\n\nmy question");
  });

  it("never restyles a labelled request", () => {
    const h = buildTurnHeader(data({ request: quoted, label: "Run task 2" }));
    expect(band(h)).toBeNull();
    expect(text(h).textContent).toBe("Run task 2");
  });

  it("keeps the band across a repaint and removes it when the request no longer quotes", () => {
    const h = buildTurnHeader(data({ request: quoted }));
    const first = band(h);
    updateTurnHeader(h, data({ request: quoted }));
    expect(band(h)).toBe(first);
    updateTurnHeader(h, data({ request: "plain" }));
    expect(band(h)).toBeNull();
    expect(text(h).textContent).toBe("plain");
    updateTurnHeader(h, data({ request: quoted }));
    updateTurnHeader(h, data({ request: undefined }));
    expect(band(h)).toBeNull();
  });

  it("draws the same band over a run step's request, below its Sent-by line", () => {
    const h = buildRequestHeader(quoted, { label: "Agent Mail" });
    expect(band(h)?.innerHTML).toBe(band(buildTurnHeader(data({ request: quoted })))?.innerHTML);
    expect(h.querySelector(".turn-req")?.firstElementChild?.className).toBe("turn-sent-by");
    expect(band(h)?.nextElementSibling).toBe(text(h));
    expect(text(h).textContent).toBe("why that?");
    expect(band(buildRequestHeader("> something I typed\n\nmy question"))).toBeNull();
  });

  it("holds the band to one line, open or folded", () => {
    const style = mountAppCSS();
    const card = document.createElement("div");
    card.className = "turn";
    card.style.inlineSize = "160px";
    try {
      const passage = `${"word ".repeat(40)}\n  second line`;
      const h = buildTurnHeader(
        data({ request: `${appendQuote("", quoteSelection(passage, "From Kiro's reply"))}why?` }),
      );
      card.append(h);
      document.body.append(card);
      const b = band(h)!;
      const line = parseFloat(getComputedStyle(b).lineHeight);
      for (const folded of [false, true]) {
        card.toggleAttribute("data-folded", folded);
        expect(b.getBoundingClientRect().height).toBeCloseTo(line, 0);
        expect(b.scrollWidth).toBeGreaterThan(b.clientWidth);
        expect(card.scrollWidth).toBeLessThanOrEqual(card.clientWidth);
        expect(getComputedStyle(b).textOverflow).toBe("ellipsis");
      }
    } finally {
      card.remove();
      style.remove();
    }
  });
});

describe("a request an API caller sent", () => {
  const sentByLine = (h: HTMLElement): HTMLElement | null => h.querySelector(".turn-sent-by");

  it("says who sent it over the words, and reads as that program's request", () => {
    const h = buildTurnHeader(
      data({ request: "Agent Mail: 1 new message", caller: { label: "Agent Mail" } }),
    );
    expect(h.dataset["trigger"]).toBe("caller");
    expect(sentByLine(h)?.textContent).toBe("Sent by Agent Mail");
    expect(text(h).textContent).toBe("Agent Mail: 1 new message");
    // Outside the clamped text, so a folded turn still names the sender.
    expect(text(h).contains(sentByLine(h))).toBe(false);
  });

  it("names a program that gave no label as an API caller", () => {
    const h = buildTurnHeader(data({ caller: {} }));
    expect(sentByLine(h)?.textContent).toBe("Sent by an API caller");
  });

  it("drops the line when the request turns out to be the user's", () => {
    const h = buildTurnHeader(data({ caller: { label: "Agent Mail" } }));
    updateTurnHeader(h, data());
    expect(h.dataset["trigger"]).toBe("user");
    expect(sentByLine(h)).toBeNull();
  });

  it("draws the words in another ink than the user's own prompt", () => {
    const style = mountAppCSS();
    try {
      const mine = buildTurnHeader(data());
      const theirs = buildTurnHeader(data({ caller: { label: "Agent Mail" } }));
      document.body.append(mine, theirs);
      expect(getComputedStyle(text(theirs)).color).not.toBe(getComputedStyle(text(mine)).color);
      expect(getComputedStyle(sentByLine(theirs)!).whiteSpace).toBe("nowrap");
      mine.remove();
      theirs.remove();
    } finally {
      style.remove();
    }
  });

  it("puts the Sent-by line above a quoted reply passage", () => {
    const quoted = `${appendQuote("", quoteSelection("the passage", "From Kiro's reply"))}why that?`;
    const h = buildTurnHeader(data({ request: quoted, caller: { label: "Agent Mail" } }));
    const req = h.querySelector(".turn-req")!;
    expect(req.firstElementChild).toBe(sentByLine(h));
    expect(sentByLine(h)?.nextElementSibling?.className).toBe("turn-req-quote");
    expect(text(h).textContent).toBe("why that?");
  });
});

describe("search unit", () => {
  it("stamps the request band, prompt and attachments, as the turn's header unit", () => {
    const h = buildTurnHeader(data({ request: "fix the deadset failure" }));
    const unit = h.querySelector<HTMLElement>("[data-find-unit]");
    expect(unit?.dataset["findUnit"]).toBe("header");
    expect(unit?.querySelector(".turn-req-text")?.textContent).toBe("fix the deadset failure");
    expect(unit?.querySelector(".turn-req-attachments")).not.toBeNull();
    expect(h.querySelector(".turn-head-row")?.closest("[data-find-unit]")).toBeNull();
  });
});
