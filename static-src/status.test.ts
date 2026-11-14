import { describe, it, expect } from "vitest";
import { formatTokens, formatMetering } from "./status-format.js";

describe("formatTokens", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.0M"],
    [1_500_000, "1.5M"],
    [10_000_000, "10.0M"],
  ];

  it.each(cases)("formatTokens(%d) === %s", (input, expected) => {
    expect(formatTokens(input)).toBe(expected);
  });
});

describe("formatMetering", () => {
  const cases: [number, string][] = [
    [0, "0"],
    [1, "1"],
    [42, "42"],
    [999, "999"],
    [1000, "1.0K"],
    [1500, "1.5K"],
    [999_999, "1000.0K"],
    [1_000_000, "1.00M"],
    [1_500_000, "1.50M"],
    [0.5, "0.50"],
    [3.14, "3.14"],
    [99.9, "99.90"],
    // A positive sub-cent amount is spend, so it never reads as 0.00.
    [0.004, "<0.01"],
  ];

  it.each(cases)("formatMetering(%d) === %s", (input, expected) => {
    expect(formatMetering(input)).toBe(expected);
  });
});

// The pill names the model, and beside it whatever reasoning tier it is handed. status.ts is the
// single writer of both spans plus the button's aria-label; which tier to hand it is effort.ts's
// answer (see effort.test.ts) and this module renders the string and decides nothing about it.

/** Every element updateContextBar writes, so the registry's throwing getters resolve. The two
 *  under test are `ctx-model-pill` and `ctx-effort-pill`. */
function mountContextBar(): void {
  document.body.replaceChildren();
  const btn = document.createElement("button");
  btn.id = "switch-model-btn";
  for (const id of ["ctx-model-pill", "ctx-effort-pill"]) {
    const span = document.createElement("span");
    span.id = id;
    btn.appendChild(span);
  }
  document.body.appendChild(btn);
  for (const id of [
    "context-indicator",
    "context-label",
    "ctx-tokens",
    "ctx-credits",
    "ctx-turns",
    "ctx-last-turn",
    "ctx-entries",
    "ctx-tools",
    "ctx-compact-row",
    "ctx-compact-at",
    "ctx-metering",
  ]) {
    const span = document.createElement("span");
    span.id = id;
    document.body.appendChild(span);
  }
}

/** updateContextBar coalesces into a rAF, so a read has to wait one frame. */
async function paint(model: string, effort: string): Promise<void> {
  const { updateContextBar } = await import("./status.js");
  updateContextBar({
    pct: 0,
    contextSize: 0,
    compaction: { at: 80, t: 80 },
    credits: 0,
    turnCount: 0,
    lastTurnMs: 0,
    model,
    effort,
  });
  await new Promise((r) => {
    requestAnimationFrame(() => {
      r(undefined);
    });
  });
}

describe("the model pill", () => {
  it("names the model alone when it is handed no tier", async () => {
    mountContextBar();

    await paint("model-a-5", "");

    const { $ } = await import("./dom.js");
    expect($.ctxModelPill.textContent).toBe("model a 5");
    // Hidden rather than emptied: `.pill` is a flex row with a gap, so an empty span would still
    // pad the pill.
    expect($.ctxEffortPill.textContent).toBe("");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(true);
    expect($.switchModelBtn.getAttribute("aria-label")).toBe("Switch model, currently model a 5");
  });

  it("names the tier beside the model when it is handed one", async () => {
    mountContextBar();

    await paint("model-a-5", "max");

    const { $ } = await import("./dom.js");
    // Its OWN span, not appended to the model label: the label is capped at 10rem with an ellipsis,
    // so a concatenated tier is the half that gets clipped.
    expect($.ctxModelPill.textContent).toBe("model a 5");
    expect($.ctxEffortPill.textContent).toBe("· max");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(false);
  });

  it("names the current selection in the button's aria-label", async () => {
    mountContextBar();

    await paint("model-a-5", "max");

    // The button's aria-label wins over its own text, so this is the only path the selection
    // reaches assistive tech by. Spelled in words: the separator above is read out.
    const { $ } = await import("./dom.js");
    expect($.switchModelBtn.getAttribute("aria-label")).toBe(
      "Switch model, currently model a 5 at max reasoning effort",
    );
  });

  it("clears the tier when a later paint carries none", async () => {
    mountContextBar();

    await paint("model-a-5", "max");
    await paint("model-b-5", "");

    // A model switch can leave the new model with no tier to name, so the span has to empty again
    // rather than keep the previous model's.
    const { $ } = await import("./dom.js");
    expect($.ctxModelPill.textContent).toBe("model b 5");
    expect($.ctxEffortPill.textContent).toBe("");
    expect($.ctxEffortPill.classList.contains("hidden")).toBe(true);
  });

  it("labels an empty model auto and still names the tier", async () => {
    mountContextBar();

    await paint("", "high");

    const { $ } = await import("./dom.js");
    // Empty model = server-side default; "auto" rather than blank.
    expect($.ctxModelPill.textContent).toBe("auto");
    expect($.ctxEffortPill.textContent).toBe("· high");
  });
});

// context-indicator.ts owns both computations and this module is their one writer, so what is under test
// here is that each computed value reaches its own element.

async function paintContext(
  pct: number,
  contextSize: number,
  at: number | null,
  t: number = at ?? 100,
): Promise<void> {
  const { updateContextBar } = await import("./status.js");
  updateContextBar({
    pct,
    contextSize,
    compaction: { at, t },
    credits: 0,
    turnCount: 0,
    lastTurnMs: 0,
    model: "model-a-5",
  });
  await new Promise((r) => {
    requestAnimationFrame(() => {
      r(undefined);
    });
  });
}

describe("the credits line", () => {
  async function paintCredits(credits: number): Promise<string> {
    mountContextBar();
    const { updateContextBar } = await import("./status.js");
    updateContextBar({
      pct: 0,
      contextSize: 0,
      compaction: { at: 80, t: 80 },
      credits,
      turnCount: 0,
      lastTurnMs: 0,
      model: "model-a-5",
      effort: "",
    });
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(undefined);
      });
    });
    return document.getElementById("ctx-credits")?.textContent ?? "";
  }

  it("states a sub-cent metered chat as under a cent, never as 0.00", async () => {
    expect(await paintCredits(0.004)).toBe("<0.01 cr");
  });

  it("states a metered chat to the cent, and an unmetered one as 0.00", async () => {
    expect(await paintCredits(1.234)).toBe("1.23 cr");
    expect(await paintCredits(0)).toBe("0.00 cr");
  });
});

describe("the context pill", () => {
  it("inks the share along the ramp, through the pill's own shades", async () => {
    mountContextBar();

    // 65% sits inside the ramp's second segment, so the ink is a mix rather than a saturated
    // token — a constant would satisfy a green or a red arm.
    await paintContext(65, 200_000, 80);

    const { $ } = await import("./dom.js");
    expect($.contextLabel.textContent).toBe("65%");
    expect($.contextLabel.style.getPropertyValue("--ctx-ink")).toBe(
      "color-mix(in oklch, var(--ctx-ink-red) 50.0%, var(--ctx-ink-yellow))",
    );
  });

  it("names where the chat compacts in the tooltip and the card", async () => {
    mountContextBar();

    await paintContext(10, 200_000, 95);

    const { $ } = await import("./dom.js");
    expect($.contextIndicator.getAttribute("data-tooltip")).toBe(
      "Context usage: tokens, credits and turns for this chat. Compacts at 95%.",
    );
    expect($.ctxCompactAt.textContent).toBe("95%");
    expect($.ctxCompactRow.classList.contains("hidden")).toBe(false);
  });

  it("says nothing compacts, and drops the card's row, when automatic compaction is off", async () => {
    mountContextBar();

    await paintContext(85, 200_000, null, 100);

    const { $ } = await import("./dom.js");
    // Off keys the ramp on 100: 85 is past 70 and short of 90, so still a mix.
    expect($.contextLabel.style.getPropertyValue("--ctx-ink")).toBe(
      "color-mix(in oklch, var(--ctx-ink-red) 50.0%, var(--ctx-ink-yellow))",
    );
    expect($.contextIndicator.getAttribute("data-tooltip")).toContain(
      "Automatic compaction is off",
    );
    expect($.contextIndicator.getAttribute("data-tooltip")).not.toContain("Compacts at");
    expect($.ctxCompactRow.classList.contains("hidden")).toBe(true);
  });

  it("keys the ramp on the compaction point it names", async () => {
    mountContextBar();

    await paintContext(65, 200_000, 90);

    const { $ } = await import("./dom.js");
    expect($.ctxCompactAt.textContent).toBe("90%");
    expect($.contextLabel.style.getPropertyValue("--ctx-ink")).toBe(
      "color-mix(in oklch, var(--ctx-ink-yellow) 50.0%, var(--ctx-ink-green))",
    );
  });

  it("reads the token count out of the ramp's own derivation", async () => {
    mountContextBar();

    await paintContext(25, 200_000, 80);

    // The readout and the ramp are one derivation: 25% of 200K is the 50K that put the ink half
    // way through the first segment above.
    const { $ } = await import("./dom.js");
    expect($.ctxTokens.textContent).toBe("50.0K / 200.0K");
  });

  it("reads a percentage when the window is unknown", async () => {
    mountContextBar();

    await paintContext(25, 0, 80);

    const { $ } = await import("./dom.js");
    expect($.ctxTokens.textContent).toBe("25.0%");
  });
});
